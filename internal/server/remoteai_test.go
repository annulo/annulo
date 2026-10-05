package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestRemoteAIListed(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "backends", "pa")
	writeProject(t, a, "pa/sa", "https://creght.cn", nil)
	s := &Server{cfg: &config.Config{Dir: root}, ws: &creght.Workspace{Dir: a, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}}
	s.ready.Store(true)
	if fns := s.relayRemote()["pa"]; !slices.Contains(fns, "_shuttle.send") || !slices.Contains(fns, "_shuttle.chat") {
		t.Fatalf("当前项目要带上助手函数：%v", fns)
	}
	if _, err := s.relayAI(t.Context(), "pb", "_shuttle.chats", nil); err == nil {
		t.Fatal("不是打开着的项目应该拒绝")
	}
}

// SHUTTLE_LIVE_CLI=codex go test ./internal/server -run TestRemoteAILive -v：手机发一句话、轮询到回答
func TestRemoteAILive(t *testing.T) {
	engine := os.Getenv("SHUTTLE_LIVE_CLI")
	if engine == "" {
		t.Skip("set SHUTTLE_LIVE_CLI")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "backends", "pa")
	writeProject(t, dir, "pa/sa", "https://creght.cn", map[string]string{"notes.txt": "secret word: ZEBRA"})
	f := false
	cfg := &config.Config{Dir: root, Active: "cli:" + engine, Thinking: "low", AutoTitle: &f}
	ag := agent.New(cfg)
	ag.SetWorkspace("pa", dir)
	s := &Server{cfg: cfg, agent: ag, runs: map[string]*agentRun{}, ws: &creght.Workspace{Dir: dir, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}}
	s.ready.Store(true)
	r, err := s.relayAI(t.Context(), "pa", "_shuttle.send", map[string]any{"text": "读 notes.txt，只回答里面的 secret word。"})
	if err != nil {
		t.Fatal(err)
	}
	id := r.(map[string]any)["chat_id"].(string)
	sawLive := false
	for i := 0; i < 120; i++ {
		time.Sleep(time.Second)
		c, err := s.relayAI(t.Context(), "pa", "_shuttle.chat", map[string]any{"chat_id": id})
		if err != nil {
			t.Fatal(err)
		}
		m := c.(map[string]any)
		if m["live"] != nil {
			sawLive = true
		}
		if m["status"] == "" && m["total"].(int) >= 2 {
			msgs := m["messages"].([]any)
			t.Logf("last: %v live seen: %v", msgs[len(msgs)-1], sawLive)
			list, _ := s.relayAI(t.Context(), "pa", "_shuttle.chats", nil)
			t.Logf("chats: %v", list)
			return
		}
	}
	t.Fatal("没跑完")
}
