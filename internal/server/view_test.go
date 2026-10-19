package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/mcphub"
)

func TestHostProject(t *testing.T) {
	for host, want := range map[string]string{
		"p9ok3myl0ne6.localhost:7799":  "p9ok3myl0ne6",
		"local-0c69304433ec.localhost": "local-0c69304433ec",
		"P1.LOCALHOST:7799":            "p1",
		"localhost:7799":               "",
		"127.0.0.1:7799":               "",
		"a.b.localhost:7799":           "",
		"evil.com:7799":                "",
		"-x.localhost:7799":            "",
		"p1.localhost.evil.com:7799":   "",
		"p_1.localhost:7799":           "",
	} {
		if got := hostProject(host); got != want {
			t.Errorf("%s → %q，要 %q", host, got, want)
		}
	}
}

// 两个项目同时开着：按 Host（<项目>.localhost）或 X-Annulo-Project 找到各自的视图，各有各的 Agent；没带项目的是默认项目
func TestRequestView(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"local-a", "local-b"} {
		creght.WriteOffline(filepath.Join(root, "backends", id), creght.OfflineProject{ProjectID: id, Name: id})
	}
	cfg := &config.Config{Dir: root, Port: 7799, Backend: "local-a", Offline: true}
	ag := agent.New(cfg)
	s := newTestServer(cfg, nil, ag)
	s.mcp = mcphub.New(root)
	s.relay = s.newRelay() // 不连
	t.Cleanup(func() {
		for _, v := range s.views() {
			v.Detach()
		}
		localdb.Close(s.offlineDBPath("local-a"))
		localdb.Close(s.offlineDBPath("local-b"))
	})

	req := func(host, header string) *Server {
		r := httptest.NewRequest("GET", "http://"+host+"/_shuttle/api/status", nil)
		if header != "" {
			r.Header.Set(ProjectHeader, header)
		}
		return s.requestView(r)
	}
	a, b := req("local-a.localhost:7799", ""), req("local-b.localhost:7799", "")
	if !a.ready.Load() || a.ws.ProjectID != "local-a" || !b.ready.Load() || b.ws.ProjectID != "local-b" {
		t.Fatalf("按 Host 找项目不对")
	}
	if a.agent == b.agent || a.agent.Workspace() != "local-a" || b.agent.Workspace() != "local-b" {
		t.Fatalf("每个项目要有自己的 Agent：%s %s", a.agent.Workspace(), b.agent.Workspace())
	}
	if v := req("127.0.0.1:7799", "local-b"); v.project != b.project {
		t.Fatal("X-Annulo-Project 要找到 local-b")
	}
	if v := req("127.0.0.1:7799", ""); v.project != a.project {
		t.Fatal("没带项目的是默认项目")
	}
	if v := req("local-zz.localhost:7799", ""); v.ready.Load() {
		t.Fatal("本机没有的项目给空视图")
	}
	if n := len(s.views()); n != 2 {
		t.Fatalf("打开着的项目要是 2 个：%d", n)
	}
	if got := s.projectURL("local-b"); got != "http://local-b.localhost:7799/_shuttle/" {
		t.Fatal(got)
	}
	// 关掉一个，另一个照常开着
	b.Detach()
	if s.openView("local-b") != nil || s.openView("local-a") == nil {
		t.Fatal("关掉 local-b 不能影响 local-a")
	}
}
