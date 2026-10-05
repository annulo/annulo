package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineIDStablePerDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	id := machine(a)["id"].(string)
	if len(id) != 24 {
		t.Fatalf("id = %q", id)
	}
	// 进程重启（缓存清掉）后从 machine-id 文件读回同一个
	machines.Delete(a)
	if again := machine(a)["id"]; again != id {
		t.Fatalf("id changed: %v → %v", id, again)
	}
	if raw, _ := os.ReadFile(filepath.Join(a, "machine-id")); strings.TrimSpace(string(raw)) != id {
		t.Fatalf("machine-id file = %q", raw)
	}
	// 另一个数据目录（另起的 Shuttle）是另一台「电脑」
	if other := machine(b)["id"]; other == id {
		t.Fatal("different data dirs share an id")
	}
	if machine(a)["name"] == "" {
		t.Fatal("empty name")
	}
}
