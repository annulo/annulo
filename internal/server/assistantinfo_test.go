package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAssistant(t *testing.T) {
	dir := t.TempDir()
	if a := projectAssistant(dir); a.Name != "" || len(a.Suggestions) == 0 {
		t.Fatalf("没写用通用的：%+v", a)
	}
	os.WriteFile(filepath.Join(dir, "shuttle.json"), []byte(`{"min_shuttle_api": 24, "assistant": {"name": {"zh": "运营助手", "en": "Ops"}, "suggestions": {"zh": ["甲", "乙"]}}}`), 0o644)
	a := projectAssistant(dir)
	if a.Name != "运营助手" && a.Name != "Ops" || len(a.Suggestions) != 2 || a.Intro == "" {
		t.Fatalf("项目写的优先，没写的项用通用的：%+v", a)
	}
	os.WriteFile(filepath.Join(dir, "annulo.json"), []byte(`{"assistant": {"name": "记账助手"}}`), 0o644)
	if a := projectAssistant(dir); a.Name != "记账助手" {
		t.Fatalf("annulo.json 优先，字符串不分语言：%+v", a)
	}
}
