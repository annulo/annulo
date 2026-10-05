package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
)

// SHUTTLE_LIVE_CLI=codex|claude-code go test ./internal/agent -run TestLiveCLI -v：真的跑一轮本机的外部 agent。
func TestLiveCLI(t *testing.T) {
	engine := os.Getenv("SHUTTLE_LIVE_CLI")
	if engine == "" {
		t.Skip("set SHUTTLE_LIVE_CLI")
	}
	dir := t.TempDir()
	cwd := filepath.Join(dir, "proj")
	os.MkdirAll(cwd, 0o755)
	os.WriteFile(filepath.Join(cwd, "hello.txt"), []byte("the magic number is 4242\n"), 0o644)
	active := cliPrefix + engine
	if m := os.Getenv("SHUTTLE_LIVE_MODEL"); m != "" {
		active += "/" + m // SHUTTLE_LIVE_MODEL=haiku：用指定的模型
	}
	cfg := &config.Config{Dir: dir, Active: active, Thinking: "low"}
	f := false
	cfg.AutoTitle = &f
	a := New(cfg)
	a.SetWorkspace("p1", cwd)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	for i, q := range []string{"读一下 hello.txt，告诉我 magic number 是多少。只回答数字。", "把刚才那个数字加 1，只回答数字。"} {
		msg, _ := json.Marshal(map[string]any{"id": "u" + string(rune('0'+i)), "role": "user", "parts": []any{map[string]any{"type": "text", "text": q}}})
		if err := a.SaveMessage("c1", msg, q, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		st, err := a.Run(ctx, "c1", q, nil, func(e Event) {
			t.Logf("event %s %s %s %v %.200s", e.Type, e.Tool, e.ToolID, e.Args, e.Text)
			if e.Type == "text" {
				text.WriteString(e.Text)
			}
		})
		t.Logf("stats %+v err %v", st, err)
		if err != nil {
			t.Fatal(err)
		}
		asst, _ := json.Marshal(map[string]any{"id": "a" + string(rune('0'+i)), "role": "assistant", "parts": []any{map[string]any{"type": "text", "text": text.String()}}})
		a.SaveMessage("c1", asst, q, time.Now().UnixMilli())
		want := []string{"4242", "4243"}[i]
		if !strings.Contains(text.String(), want) {
			t.Fatalf("turn %d: want %s in %q", i, want, text.String())
		}
	}
	ch, _ := a.LoadChat("c1")
	t.Logf("cli session %s %s", ch.CLIEngine, ch.CLISession)
}
