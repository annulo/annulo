package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"「对比 X 和小红书浏览量」":     "对比 X 和小红书浏览量",
		"标题：修复站点体检报错。\n解释一下": "修复站点体检报错",
		`"Fix audit errors"`: "Fix audit errors",
		"**画浏览量趋势图**":        "画浏览量趋势图",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestSetTitle(t *testing.T) {
	a := &Agent{cfg: &config.Config{Dir: t.TempDir()}, chats: map[string]*chatSession{}}
	// 对话还没存：起名不新建文件（可能已经切到别的项目了）
	if err := a.SetTitle("c1", "标题", "auto"); err == nil {
		t.Fatal("对话不存在时应该报错")
	}
	msg, _ := json.Marshal(map[string]any{"id": "u1", "role": "user"})
	if err := a.SaveMessage("c1", msg, "画个 x 和 小红书的对比图吧", 1); err != nil {
		t.Fatal(err)
	}
	if !a.NeedsTitle("c1") {
		t.Fatal("只有第一句话的对话应该要起名")
	}
	a.SetTitle("c1", "对比 X 和小红书", "auto")
	if c, _ := a.LoadChat("c1"); c.Title != "对比 X 和小红书" || a.NeedsTitle("c1") {
		t.Fatalf("自动起名没生效：%+v", c)
	}
	a.SetTitle("c1", "我的名字", "user")
	a.SetTitle("c1", "又一个自动的", "auto")
	if c, _ := a.LoadChat("c1"); c.Title != "我的名字" {
		t.Fatalf("用户改的名被自动起名盖掉了：%q", c.Title)
	}
}

func TestAutoTitleOff(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cfg: &config.Config{Dir: dir}, chats: map[string]*chatSession{}}
	msg, _ := json.Marshal(map[string]any{"id": "u1", "role": "user"})
	a.SaveMessage("c1", msg, "你好", 1)
	off := false
	if err := a.SetTitleSettings(&off, nil); err != nil {
		t.Fatal(err)
	}
	if a.NeedsTitle("c1") {
		t.Fatal("关了自动起名还要起名")
	}
	if on, _ := a.TitleSettings(); on {
		t.Fatal("开关没存上")
	}
	bad := "不存在的模型"
	if err := a.SetTitleSettings(nil, &bad); err == nil {
		t.Fatal("选了不存在的模型应该报错")
	}
}

func TestInstructions(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cwd: dir}
	if a.instructions() != "" {
		t.Fatal("没有 INSTRUCTIONS.md 时不该有这一节")
	}
	os.WriteFile(filepath.Join(dir, InstructionsFile), []byte("  回答先给结论\n"), 0o644)
	if got := a.instructions(); !strings.Contains(got, "## 用户的要求") || !strings.Contains(got, "回答先给结论") {
		t.Fatalf("系统提示里没有用户的要求：%q", got)
	}
}
