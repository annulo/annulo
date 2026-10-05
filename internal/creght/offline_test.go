package creght

import (
	"os"
	"path/filepath"
	"testing"
)

// 离线项目的标记新旧目录都认（internal/brand）：.annulo/project.json 优先，没有再读 .shuttle/project.json。
func TestReadOfflineBothMarkers(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadOffline(dir); err == nil {
		t.Fatal("没有标记不是离线项目")
	}
	if err := WriteOffline(dir, OfflineProject{ProjectID: "local-old"}); err != nil {
		t.Fatal(err)
	}
	if p, err := ReadOffline(dir); err != nil || p.ProjectID != "local-old" {
		t.Fatalf("读旧目录：%+v %v", p, err)
	}
	os.MkdirAll(filepath.Join(dir, ".annulo"), 0o755)
	os.WriteFile(filepath.Join(dir, ".annulo", "project.json"), []byte(`{"project_id":"local-new"}`), 0o644)
	if p, err := ReadOffline(dir); err != nil || p.ProjectID != "local-new" {
		t.Fatalf("新目录优先：%+v %v", p, err)
	}
	if ws, err := OpenWorkspace(dir); err != nil || !ws.Offline {
		t.Fatalf("打开成离线项目：%+v %v", ws, err)
	}
}
