package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/auth"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestPublicPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/_shuttle/":                     true,
		"/_shuttle/assets/index-abc.js":  true,
		"/_shuttle/settings":             true, // 外壳的前端路由：静态文件，界面自己显示登录页
		"/_shuttle/api/auth/login":       true,
		"/_shuttle/api/auth/users":       false,
		"/_shuttle/api/status":           false,
		"/_shuttle/api/db/notes":         false,
		"/_shuttle/project-files/a.html": false,
		"/_shuttle/work-files/a.mp4":     false,
		"/_shuttle/port/8000/":           false,
		"/_shuttle/files/x":              false,
		"/_shuttle/uploaded/x":           false,
		"/":                              false, // 项目页面
		"/api/x":                         false,
	} {
		if got := publicPath(p); got != want {
			t.Errorf("%s → %v，要 %v", p, got, want)
		}
	}
}

func TestAuthorize(t *testing.T) {
	s := newTestServer(&config.Config{Server: true}, nil, nil)
	req := func(role auth.Role, method, p string) error {
		r := httptest.NewRequest(method, "/_shuttle/api/"+p, nil)
		if role != "" {
			r = r.WithContext(context.WithValue(r.Context(), userKey{}, &auth.User{ID: "u", Role: role}))
		}
		return s.authorize(r, p)
	}
	cases := []struct {
		role   auth.Role
		method string
		p      string
		ok     bool
	}{
		{auth.Admin, "PUT", "settings/llm", true},
		{auth.Member, "PUT", "settings/llm", false},
		{auth.Member, "GET", "settings/llm", true},
		{auth.Member, "POST", "agent/chat", true},
		{auth.Member, "POST", "setup/delete", false},
		{auth.Member, "GET", "auth/users", false},
		{auth.Member, "POST", "template/upgrade", false},
		{auth.Viewer, "GET", "db/notes", true},
		{auth.Viewer, "POST", "db/notes", false},
		{auth.Viewer, "POST", "agent/chat", false},
		{auth.Viewer, "GET", "agent/chats", false},
		{auth.Viewer, "POST", "local/run", true}, // 函数是不是 readonly 在 apiLocalRun 里再查
		{auth.Viewer, "GET", "settings/llm", false},
		{"", "POST", "auth/login", true},
		{"", "GET", "status", false},
	}
	for _, c := range cases {
		if err := req(c.role, c.method, c.p); (err == nil) != c.ok {
			t.Errorf("%s %s %s → %v，要 ok=%v", c.role, c.method, c.p, err, c.ok)
		}
	}
	// 本机版不限
	local := newTestServer(&config.Config{}, nil, nil)
	if err := local.authorize(httptest.NewRequest("PUT", "/_shuttle/api/settings/llm", nil), "settings/llm"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p1")
	os.MkdirAll(filepath.Join(dir, "out"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "out", "a.html"), []byte("<h1>hi</h1>"), 0o644)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(root, "outside.txt"), []byte("outside"), 0o644)
	os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(dir, "link.txt"))
	s := newTestServer(&config.Config{Dir: root}, &creght.Workspace{Dir: dir, ProjectID: "p1"}, nil)
	s.ready.Store(true)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleProjectFile(w, httptest.NewRequest("GET", projectFilesURL+p, nil))
		return w
	}
	if w := get("out/a.html"); w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != previewCSP {
		t.Fatalf("项目里的文件要能看、带 sandbox：%d %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	for _, p := range []string{".git/config", "../outside.txt", "out/../../outside.txt", "link.txt", "%2e%2e/outside.txt"} {
		if w := get(p); w.Code == http.StatusOK {
			t.Errorf("%s 不能给：%s", p, w.Body.String())
		}
	}
}
