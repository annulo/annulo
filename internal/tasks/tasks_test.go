package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndSaveBody(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	src := "---\nname: 写运营周报\ndescription: \"每周写一期\"\nextra: keep\n---\n\n# 周报\n\n先拿数字。\n"
	os.WriteFile(Path(root, "weekly-report"), []byte(src), 0o644)
	os.WriteFile(filepath.Join(root, Dir, "bad id.md"), []byte("x"), 0o644)

	list := List(root)
	if len(list) != 1 {
		t.Fatalf("List = %d, want 1", len(list))
	}
	tk := list[0]
	if tk.Name != "写运营周报" || tk.Description != "每周写一期" || tk.Body != "# 周报\n\n先拿数字。" || tk.File != "tasks/weekly-report.md" {
		t.Fatalf("got %+v", tk)
	}
	if err := tk.SaveBody("新的说明\n写完发邮件"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(tk.Path)
	if want := "---\nname: 写运营周报\ndescription: \"每周写一期\"\nextra: keep\n---\n\n新的说明\n写完发邮件\n"; string(b) != want {
		t.Fatalf("saved:\n%q\nwant\n%q", b, want)
	}
	if _, err := Load(root, "../x"); err == nil {
		t.Fatal("bad id should fail")
	}
	if _, err := Load(root, "missing"); err == nil {
		t.Fatal("missing should fail")
	}
}

func TestNoFrontmatter(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	os.WriteFile(Path(root, "a"), []byte("只有正文"), 0o644)
	tk, err := Load(root, "a")
	if err != nil || tk.Name != "a" || tk.Body != "只有正文" {
		t.Fatalf("got %+v %v", tk, err)
	}
}

func TestPrompt(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	os.WriteFile(Path(root, "write"), []byte("---\nname: 写文章\n---\n系统流程"), 0o644)
	tk, _ := Load(root, "write")
	if tk.Prompt != "" || tk.PromptFile != "" || tk.HasDefault {
		t.Fatalf("两份都没有时应该是空的：%+v", tk)
	}
	// 模板给的默认
	os.MkdirAll(filepath.Join(root, PromptDir), 0o755)
	os.WriteFile(PromptPath(root, "write"), []byte("- 默认写法\n"), 0o644)
	tk, _ = Load(root, "write")
	if tk.Prompt != "- 默认写法" || tk.PromptFile != "prompts/write.md" || tk.PromptCustom || !tk.HasDefault {
		t.Fatalf("默认 %+v", tk)
	}
	// 用户改了：存到 user/，之后用它
	if err := tk.SavePrompt("  - 每篇 1500 字\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(UserPromptPath(root, "write"))
	if string(b) != "- 每篇 1500 字\n" || tk.PromptFile != "user/prompts/write.md" || !tk.PromptCustom {
		t.Fatalf("saved %q, %+v", b, tk)
	}
	if d, _ := os.ReadFile(PromptPath(root, "write")); string(d) != "- 默认写法\n" {
		t.Fatal("模板的默认不能被改")
	}
	tk, _ = Load(root, "write")
	if tk.Prompt != "- 每篇 1500 字" || tk.Body != "系统流程" {
		t.Fatalf("reload %+v", tk)
	}
	// 恢复默认
	if err := tk.ResetPrompt(); err != nil || tk.Prompt != "- 默认写法" || tk.PromptCustom {
		t.Fatalf("reset %+v %v", tk, err)
	}
	if l := List(root); len(l) != 1 {
		t.Fatalf("List = %d", len(l))
	}
}
