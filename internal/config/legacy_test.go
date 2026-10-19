package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 早期的 <数据目录>/backend 启动时挪进 backends/<项目 id>，当前项目跟着记下来
func TestMigrateLegacyBackend(t *testing.T) {
	d := t.TempDir()
	c := &Config{Dir: d}
	os.MkdirAll(filepath.Join(d, "backend", ".creght"), 0o755)
	os.WriteFile(filepath.Join(d, "backend", ".creght", "state.json"), []byte(`{"site_id":"p1/s1"}`), 0o644)
	if err := c.MigrateLegacyBackend(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "backends", "p1", ".creght", "state.json")); err != nil {
		t.Fatal("没挪过去")
	}
	if c.Backend != "p1" || c.BackendDir() != filepath.Join(d, "backends", "p1") {
		t.Fatalf("%q %q", c.Backend, c.BackendDir())
	}
}
