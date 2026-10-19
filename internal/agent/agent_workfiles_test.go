package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

func TestWorkDirName(t *testing.T) {
	for in, want := range map[string]string{"我的店": "我的店", "a/b:c": "a-b-c", "  ": "p1", "..": "p1", "": "p1"} {
		if got := workDirName(in, "p1"); got != want {
			t.Errorf("%q → %q，要 %q", in, got, want)
		}
	}
}

// 本机放主目录下的 Annulo/<项目名>，服务端部署放数据目录的 work/<项目名>
func TestWorkFilesPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	a := &Agent{cfg: &config.Config{Dir: "/data"}, ws: "p1", cwd: "/data/backends/p1", ProjectName: func() string { return "外贸/站" }}
	if p := a.workFilesPrompt(); !strings.Contains(p, filepath.Join(home, "Annulo", "外贸-站")) || !strings.Contains(p, "/data/backends/p1") {
		t.Fatal(p)
	}
	a.cfg.Server = true
	if p := a.workFilesPrompt(); !strings.Contains(p, filepath.Join("/data", "work", "外贸-站")) {
		t.Fatal(p)
	}
	if (&Agent{cfg: a.cfg}).workFilesPrompt() != "" {
		t.Fatal("没有项目时不加")
	}
}
