package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestWorkFiles(t *testing.T) {
	data, proj := t.TempDir(), t.TempDir()
	if err := creght.WriteOffline(proj, creght.OfflineProject{ProjectID: "local-1", Name: "自媒体"}); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(&config.Config{Dir: data}, &creght.Workspace{ProjectID: "local-1", Dir: proj, Offline: true}, nil)
	s.ready.Store(true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := s.workFilesDir(); got != filepath.Join(home, "Annulo", "自媒体") {
		t.Fatalf("本机版的工作目录是 ~/Annulo/<项目名>：%s", got)
	}
	s.cfg.Server = true
	if got := s.workFilesDir(); got != filepath.Join(data, "work", "自媒体") {
		t.Fatalf("服务端部署的工作目录是 <数据目录>/work/<项目名>：%s", got)
	}
	s.cfg.Server = false // 下面按本机版看（不用登录）
	work := filepath.Join(home, "Annulo", "自媒体")
	os.MkdirAll(filepath.Join(work, "内容", "一篇"), 0o755)
	os.WriteFile(filepath.Join(work, "内容", "一篇", "a b.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(work, ".secret"), []byte("x"), 0o644)
	os.Symlink(proj, filepath.Join(work, "proj"))
	for p, want := range map[string]int{
		"/_shuttle/work-files/内容/一篇/a%20b.txt":            200,
		"/_shuttle/work-files/内容/一篇":                      404,
		"/_shuttle/work-files/.secret":                    404,
		"/_shuttle/work-files/../x":                       404,
		"/_shuttle/work-files/proj/.shuttle/project.json": 404,
	} {
		w := httptest.NewRecorder()
		s.handleWorkFile(w, httptest.NewRequest("GET", p, nil))
		if w.Code != want {
			t.Errorf("%s → %d，要 %d", p, w.Code, want)
		}
	}
}
