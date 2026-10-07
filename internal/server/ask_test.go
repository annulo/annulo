package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestLocalAskValidates(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{"text":"   "}`, `not json`} {
		w := httptest.NewRecorder()
		s.apiLocalAsk(w, httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/ask", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code %d", body, w.Code)
		}
	}
}

// 页面拿到 chat_id 马上就去打开那段对话：接口返回时对话必须已经存好、登记成正在跑（以前在 goroutine 里存，偶尔读到「对话不存在」）
func TestLocalAskChatExistsOnReturn(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backends", "pa")
	writeProject(t, dir, "pa/sa", "https://creght.cn", nil)
	f := false
	// 没有能用的模型：这一轮马上失败结束（本机装了 Claude Code / Codex 也不用，不然测试会真的去跑它）
	cfg := &config.Config{Dir: root, AutoTitle: &f, DisabledProviders: []string{config.CreghtProvider, agent.EngineClaude, agent.EngineCodex}}
	ag := agent.New(cfg)
	ag.SetWorkspace("pa", dir)
	s := &Server{cfg: cfg, agent: ag, runs: map[string]*agentRun{}, ws: &creght.Workspace{Dir: dir, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}}
	s.ready.Store(true)

	w := httptest.NewRecorder()
	s.apiLocalAsk(w, httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/ask", strings.NewReader(`{"text":"解决冲突","title":"解决模板合并的冲突"}`)))
	var out struct {
		ChatID string `json:"chat_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.ChatID == "" {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	c, err := ag.LoadChat(out.ChatID)
	if err != nil {
		t.Fatalf("返回 chat_id 时对话还没存好：%v", err)
	}
	if c.Title != "解决模板合并的冲突" || len(c.Messages) == 0 {
		t.Fatalf("对话不对：%+v", c)
	}
	// 后台那一轮（没有模型，会失败）跑完再结束，免得它在临时目录删掉后还在写
	for i := 0; i < 100; i++ {
		s.runsMu.Lock()
		_, running := s.runs[out.ChatID]
		s.runsMu.Unlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("后台那一轮 5 秒还没跑完")
}
