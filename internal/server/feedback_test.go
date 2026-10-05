package server

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/agent"
)

func TestBuildFeedback(t *testing.T) {
	long := strings.Repeat("很长的回复。", 200)
	msgs := []any{
		map[string]any{"id": "u1", "role": "user", "parts": []any{map[string]any{"type": "text", "text": "帮我同步一下询盘"}}},
		map[string]any{"id": "a1", "role": "assistant", "parts": []any{
			map[string]any{"type": "step-start"},
			map[string]any{"type": "dynamic-tool", "toolName": "bash", "input": map[string]any{"command": "shuttle run leads.sync --input '{}'"}},
			map[string]any{"type": "text", "text": "同步好了：\n```json\n{\"n\":3}\n```\n新增 3 条。" + long},
		}, "metadata": map[string]any{"stats": map[string]any{"model": "gpt-6-luna"}}},
		map[string]any{"id": "u2", "role": "user", "parts": []any{map[string]any{"type": "text", "text": "再看看高意向的"}}},
		map[string]any{"id": "a2", "role": "assistant", "parts": []any{map[string]any{"type": "text", "text": long}}},
	}
	c := &agent.Chat{ID: "chat1"}
	for _, m := range msgs {
		b, _ := json.Marshal(m)
		c.Messages = append(c.Messages, b)
	}
	p, err := buildFeedback(c, "a1", "up")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "shuttle" || p.ConversationID != "chat1" || p.Model != "gpt-6-luna" || len(p.Messages) != 2 {
		t.Fatalf("payload: %+v", p)
	}
	if p.Messages[0].Text != "帮我同步一下询盘" {
		t.Errorf("用户原话要原样：%q", p.Messages[0].Text)
	}
	a := p.Messages[1]
	if strings.Contains(a.Text, "```") || !strings.Contains(a.Text, "新增 3 条") || len([]rune(a.Text)) > fbRatedMax+1 {
		t.Errorf("回复摘要：%q", a.Text)
	}
	if len(a.Tools) != 1 || a.Tools[0] != "bash: shuttle run leads.sync --input '{}'" {
		t.Errorf("tools: %v", a.Tools)
	}
	// 评价后面那条：前面的回复按 400 字截
	p2, _ := buildFeedback(c, "a2", "down")
	if len(p2.Messages) != 4 || len([]rune(p2.Messages[1].Text)) > fbReplyMax+1 {
		t.Errorf("前面的回复要截到 400：%d", len([]rune(p2.Messages[1].Text)))
	}
	if _, err := buildFeedback(c, "u1", "up"); err == nil {
		t.Error("只能评价助手的回复")
	}
}

func TestSettleFeedback(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	old := fbDeliver
	fbDeliver = func(_ *Server, p *fbPayload) {
		mu.Lock()
		sent = append(sent, p.MessageID+":"+p.Rating)
		mu.Unlock()
	}
	defer func() { fbDeliver = old }()
	s := &Server{}
	click := func(msg, r string) {
		s.settleFeedback(&fbPayload{ConversationID: "c", MessageID: msg, Rating: r}, 30*time.Millisecond)
	}
	wait := func() { time.Sleep(80 * time.Millisecond) }
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), sent...) }

	click("m1", "up") // 手误点了赞，马上取消：什么都不发
	click("m1", "none")
	wait()
	if len(got()) != 0 {
		t.Fatalf("手误取消不该发：%v", got())
	}
	click("m1", "up") // 先赞后改踩：只发最后的踩
	click("m1", "down")
	wait()
	if g := got(); len(g) != 1 || g[0] != "m1:down" {
		t.Fatalf("只发最后的状态：%v", g)
	}
	click("m1", "up") // 改成赞又改回踩：和发过的一样，不发
	click("m1", "down")
	wait()
	if len(got()) != 1 {
		t.Fatalf("和发过的一样不该再发：%v", got())
	}
	click("m1", "none") // 发过之后取消：要告诉平台
	wait()
	if g := got(); len(g) != 2 || g[1] != "m1:none" {
		t.Fatalf("发过之后取消要发 none：%v", g)
	}
	click("m2", "up") // 不同回复各自计时
	wait()
	if g := got(); len(g) != 3 || g[2] != "m2:up" {
		t.Fatalf("另一条回复：%v", g)
	}
}
