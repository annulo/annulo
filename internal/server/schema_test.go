package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseTables(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, WorkspaceTablesDir)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "leads.json"), []byte(`{"name":"询盘","desc":"x","json_schema":{"type":"object"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "articles.json"), []byte(`{"name":"文章","json_schema":{"type":"object"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte(`不是表`), 0o644)

	var wt workspaceTables
	defs, changed, err := wt.load(root)
	if err != nil || !changed || len(defs) != 2 || defs[0].Key != "articles" || defs[1].Key != "leads" || defs[1].Name != "询盘" {
		t.Fatalf("defs=%+v changed=%v err=%v", defs, changed, err)
	}
	if _, changed, _ := wt.load(root); changed {
		t.Error("没改文件不应该算变了")
	}
	// 改了其中一个文件（目录本身的修改时间不变）也要认出来
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "leads.json"), []byte(`{"name":"询盘线索","json_schema":{"type":"object"}}`), 0o644)
	if defs, changed, _ := wt.load(root); !changed || defs[1].Name != "询盘线索" {
		t.Errorf("改了文件应该重读：%+v %v", defs, changed)
	}

	for name, bad := range map[string]string{"Bad-Key": `{"json_schema":{}}`, "no_schema": `{"name":"x"}`, "broken": `{`} {
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, name+".json"), []byte(bad), 0o644)
		if _, err := parseTables(root); err == nil {
			t.Errorf("%s.json 应该报错", name)
		}
	}
}

func TestTablesLegacyAndMigrate(t *testing.T) {
	// 模板还没升级：只有老的 shuttle.tables.json，不读、提示升级，也不按早期表结构乱写 tables/
	old := t.TempDir()
	os.WriteFile(filepath.Join(old, legacyTablesFile), []byte(`[]`), 0o644)
	migrateTablesFile(old)
	if _, err := os.Stat(filepath.Join(old, WorkspaceTablesDir)); err == nil {
		t.Error("有老文件时不应该写 tables/")
	}
	if _, err := parseTables(old); err == nil {
		t.Error("只有老的 shuttle.tables.json 时应该提示升级模板")
	}
	// 最早的运营后台什么都没有：按早期表结构写出 tables/
	empty := t.TempDir()
	migrateTablesFile(empty)
	defs, err := parseTables(empty)
	if err != nil || len(defs) == 0 {
		t.Fatalf("应该写出早期的表：%v %v", defs, err)
	}
}
