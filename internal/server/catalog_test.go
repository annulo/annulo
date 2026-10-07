package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%s", args, out)
		}
	}
	add := func(dir, tag string) {
		os.MkdirAll(filepath.Join(repo, dir), 0o755)
		os.WriteFile(filepath.Join(repo, dir, "annulo.json"), []byte("{}"), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", dir)
		git("tag", "-a", tag, "-m", tag)
	}
	git("init", "-q")
	add("blank", "v1.0.0")

	data := t.TempDir()
	c := &catalog{kind: "templates", repo: repo, markers: []string{"annulo.json"}, fallback: []string{"fallback"}}
	if got := strings.Join(c.list(data), ","); got != "blank" {
		t.Fatalf("第一次读（等得到）：%s", got)
	}
	add("creator", "v1.1.0")
	if got := strings.Join(c.list(data), ","); got != "blank" {
		t.Fatalf("10 分钟内用缓存：%s", got)
	}
	// 过期了：先给旧的，后台读到新的
	c.mu.Lock()
	c.at, c.tried = time.Now().Add(-time.Hour), time.Time{}
	c.mu.Unlock()
	if got := strings.Join(c.list(data), ","); got != "blank" {
		t.Fatalf("过期先给旧的：%s", got)
	}
	for i := 0; i < 50 && strings.Join(c.list(data), ",") != "blank,creator"; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if got := strings.Join(c.list(data), ","); got != "blank,creator" {
		t.Fatalf("后台读到新加的模板：%s", got)
	}
	// 重启后从磁盘缓存读
	c2 := &catalog{kind: "templates", repo: repo, markers: []string{"annulo.json"}, fallback: []string{"fallback"}}
	c2.tried = time.Now() // 不去仓库读
	c2.at = time.Now()
	if got := strings.Join(c2.list(data), ","); got != "blank,creator" {
		t.Fatalf("磁盘缓存：%s", got)
	}
	// 仓库读不到、也没读过：用随安装包带的
	bad := &catalog{kind: "plugins", repo: filepath.Join(t.TempDir(), "nope"), markers: []string{"plugin.json"}, fallback: []string{"social"}}
	if got := strings.Join(bad.list(data), ","); got != "social" {
		t.Fatalf("兜底：%s", got)
	}
}
