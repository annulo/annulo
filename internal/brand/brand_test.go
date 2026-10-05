package brand

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestEnvPrefersNewName(t *testing.T) {
	t.Setenv("SHUTTLE_BRAND_TEST", "old")
	if got := Env("BRAND_TEST"); got != "old" {
		t.Fatalf("只有旧名字：%q", got)
	}
	if got := EnvName("BRAND_TEST"); got != "SHUTTLE_BRAND_TEST" {
		t.Fatalf("报错里的名字：%q", got)
	}
	t.Setenv("ANNULO_BRAND_TEST", "new")
	if got := Env("BRAND_TEST"); got != "new" {
		t.Fatalf("新名字优先：%q", got)
	}
	if got := EnvName("BRAND_TEST"); got != "ANNULO_BRAND_TEST" {
		t.Fatalf("报错里的名字：%q", got)
	}
}

func TestChildEnvSetsBoth(t *testing.T) {
	got := ChildEnv("MCP_TOKEN", "x")
	if !slices.Equal(got, []string{"SHUTTLE_MCP_TOKEN=x", "ANNULO_MCP_TOKEN=x"}) {
		t.Fatal(got)
	}
}

func TestCanonicalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/_annulo/api/status":     "/_shuttle/api/status",
		"/_annulo/":               "/_shuttle/",
		"/_annulo":                "/_shuttle",
		"/_shuttle/api/status":    "/_shuttle/api/status",
		"/_annulox/a":             "/_annulox/a",
		"/pages/_annulo/":         "/pages/_annulo/",
		"/_annulo/uploaded/a.png": "/_shuttle/uploaded/a.png",
		"/_annulo/oauth/callback": "/_shuttle/oauth/callback",
		"/":                       "/",
	} {
		if got := CanonicalPath(in); got != want {
			t.Errorf("%s → %s，想要 %s", in, got, want)
		}
	}
}

func TestRemoteName(t *testing.T) {
	if RemoteName("_annulo.send") != "_shuttle.send" || RemoteName("_shuttle.send") != "_shuttle.send" || RemoteName("leads.list") != "leads.list" {
		t.Fatal("远程函数名换错了")
	}
}

func TestHasCloudMarker(t *testing.T) {
	if !HasCloudMarker([]byte("// shuttle:cloud local/a.ts\n")) || !HasCloudMarker([]byte("// annulo:cloud local/a.ts\n")) || HasCloudMarker([]byte("export function x() {}")) {
		t.Fatal("生成文件标记认错了")
	}
}

func TestMigrateDataDir(t *testing.T) {
	// 全新安装：用 ~/.annulo
	home := t.TempDir()
	if got := MigrateDataDir(home); got != filepath.Join(home, ".annulo") {
		t.Fatal(got)
	}
	// 老用户：~/.shuttle 改名成 ~/.annulo，原地留链接，里面的东西从老路径也读得到
	home = t.TempDir()
	os.MkdirAll(filepath.Join(home, ".shuttle", "backends", "p1"), 0o755)
	os.WriteFile(filepath.Join(home, ".shuttle", "config.json"), []byte("{}"), 0o600)
	if got := MigrateDataDir(home); got != filepath.Join(home, ".annulo") {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(home, ".annulo", "config.json")); err != nil {
		t.Fatal("内容要搬过去")
	}
	if _, err := os.Stat(filepath.Join(home, ".shuttle", "backends", "p1")); err != nil {
		t.Fatal("老路径要还能用")
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".shuttle")); fi.Mode()&os.ModeSymlink == 0 && runtime.GOOS != "windows" {
		t.Fatal("老路径是一个链接")
	}
	// 再启动一次：什么都不动
	if got := MigrateDataDir(home); got != filepath.Join(home, ".annulo") {
		t.Fatal(got)
	}
	// 两个都有（以前手动建过 ~/.annulo）：用新的，不碰老的
	home = t.TempDir()
	os.MkdirAll(filepath.Join(home, ".shuttle"), 0o755)
	os.MkdirAll(filepath.Join(home, ".annulo"), 0o755)
	if got := MigrateDataDir(home); got != filepath.Join(home, ".annulo") {
		t.Fatal(got)
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".shuttle")); !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("老目录不动")
	}
}
