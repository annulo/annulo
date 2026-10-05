package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 项目总说明新旧名字都认（internal/brand）：ANNULO.md 优先，没有再读 SHUTTLE.md。
func TestWorkspaceNotesBothNames(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cwd: dir}
	if !strings.Contains(a.workspaceNotes(), "还没有 SHUTTLE.md") {
		t.Fatal("没有说明文件时提示补一份")
	}
	os.WriteFile(filepath.Join(dir, "SHUTTLE.md"), []byte("旧说明"), 0o644)
	if got := a.workspaceNotes(); !strings.Contains(got, "## 项目（SHUTTLE.md）") || !strings.Contains(got, "旧说明") {
		t.Fatal(got)
	}
	os.WriteFile(filepath.Join(dir, "ANNULO.md"), []byte("新说明"), 0o644)
	if got := a.workspaceNotes(); !strings.Contains(got, "## 项目（ANNULO.md）") || !strings.Contains(got, "新说明") || strings.Contains(got, "旧说明") {
		t.Fatal(got)
	}
}
