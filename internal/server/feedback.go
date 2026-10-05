package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/version"
)

// 对话反馈：用户给一条 AI 回复点赞 / 点踩。评价记在对话文件里（刷新后还在），同时把「这段对话的精简摘要」发给 creght 平台的
// 公共反馈接口 POST /api/p/ai/feedback（source: shuttle，creght 编辑器、folia 也用这个接口）。
// 摘要按规则截，不调模型：用户的话保留原文（超过 4000 字才截），助手的回复精简到 400 字（被评价的那条 1000 字），工具只列名字和一句参数。
// 发送失败（没联网、平台接口还没上线）存进本机队列 ~/.shuttle/feedback-queue.jsonl，之后定时重试；用户那边不受影响。
// 没连 creght 时只记在对话文件里，什么都不发、也不进队列：对话内容不能发给用户没连的平台（docs/annulo-plan.md 第 2 步）。
// 不实时发：可能是手误。点击时马上记进对话文件（界面状态），发给平台要等这条回复 fbSettle 内没再被点，发最后的状态；
// 这期间取消了、之前也没发过，就什么都不发。

const (
	fbMaxMessages = 40
	fbUserMax     = 4000
	fbReplyMax    = 400
	fbRatedMax    = 1000
	fbSettle      = 5 * time.Second
)

type fbMessage struct {
	Role  string   `json:"role"`
	Text  string   `json:"text"`
	Tools []string `json:"tools,omitempty"`
}

type fbPayload struct {
	Source         string         `json:"source"`
	Rating         string         `json:"rating"`
	ConversationID string         `json:"conversation_id"`
	MessageID      string         `json:"message_id"`
	ProjectID      string         `json:"project_id,omitempty"`
	Model          string         `json:"model,omitempty"`
	Context        map[string]any `json:"context,omitempty"`
	Messages       []fbMessage    `json:"messages"`
	Comment        string         `json:"comment"`
}

// 存下来的界面消息里用得到的部分
type fbUIPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ToolName string          `json:"toolName"`
	Input    json.RawMessage `json:"input"`
}
type fbUIMessage struct {
	ID       string     `json:"id"`
	Role     string     `json:"role"`
	Parts    []fbUIPart `json:"parts"`
	Metadata struct {
		Stats struct {
			Model string `json:"model"`
		} `json:"stats"`
	} `json:"metadata"`
}

var (
	codeBlockRe = regexp.MustCompile("(?s)```.*?```")
	spacesRe    = regexp.MustCompile(`\s+`)
)

func cut(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// replySummary：助手回复去掉代码块、压掉多余空白后截到 n 字
func replySummary(s string, n int) string {
	s = codeBlockRe.ReplaceAllString(s, i18n.T("（代码）", "(code)"))
	return cut(spacesRe.ReplaceAllString(s, " "), n)
}

// toolLine：一次工具调用写成「名字: 一句参数」
func toolLine(p fbUIPart) string {
	arg := ""
	var in map[string]any
	if json.Unmarshal(p.Input, &in) == nil {
		for _, k := range []string{"command", "path", "file_path", "table", "query", "url", "fn"} {
			if v, ok := in[k].(string); ok && v != "" {
				arg = v
				break
			}
		}
		if arg == "" {
			for _, v := range in {
				if s, ok := v.(string); ok && s != "" {
					arg = s
					break
				}
			}
		}
	}
	if arg == "" {
		return p.ToolName
	}
	return p.ToolName + ": " + cut(spacesRe.ReplaceAllString(arg, " "), 80)
}

// buildFeedback 从对话里整理出发给平台的请求体：从被评价的回复往前最多 40 条消息，按时间顺序。
func buildFeedback(c *agent.Chat, messageID, rating string) (*fbPayload, error) {
	msgs := make([]fbUIMessage, 0, len(c.Messages))
	at := -1
	for _, raw := range c.Messages {
		var m fbUIMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		msgs = append(msgs, m)
		if m.ID == messageID {
			at = len(msgs) - 1
		}
	}
	if at < 0 || msgs[at].Role != "assistant" {
		return nil, i18n.Errorf("对话里没有这条回复：%s", "No such reply in this chat: %s", messageID)
	}
	start := max(0, at+1-fbMaxMessages)
	p := &fbPayload{Source: "shuttle", Rating: rating, ConversationID: c.ID, MessageID: messageID, Model: msgs[at].Metadata.Stats.Model}
	for i := start; i <= at; i++ {
		m := msgs[i]
		var text strings.Builder
		var tools []string
		for _, part := range m.Parts {
			switch part.Type {
			case "text":
				text.WriteString(part.Text)
				text.WriteString("\n")
			case "dynamic-tool":
				tools = append(tools, toolLine(part))
			}
		}
		switch m.Role {
		case "user":
			p.Messages = append(p.Messages, fbMessage{Role: "user", Text: cut(text.String(), fbUserMax)})
		case "assistant":
			n := fbReplyMax
			if i == at {
				n = fbRatedMax
			}
			if len(tools) > 30 {
				tools = append(tools[:30], fmt.Sprintf(i18n.T("…还有 %d 次", "…%d more"), len(tools)-30))
			}
			p.Messages = append(p.Messages, fbMessage{Role: "assistant", Text: replySummary(text.String(), n), Tools: tools})
		}
	}
	return p, nil
}

// POST agent/chats/<id>/feedback  { message_id, rating: up / down / none }
func (s *Server) apiChatFeedback(w http.ResponseWriter, r *http.Request, chatID string) {
	var in struct {
		MessageID string `json:"message_id"`
		Rating    string `json:"rating"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.agent.LoadChat(chatID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	p, err := buildFeedback(c, in.MessageID, in.Rating)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.SetFeedback(chatID, in.MessageID, in.Rating); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p.ProjectID = s.ws.ProjectID
	p.Context = map[string]any{"app_version": version.Version, "api": version.API, "os": runtime.GOOS, "locale": s.cfg.Locale()}
	s.settleFeedback(p, fbSettle)
	writeJSON(w, map[string]any{"ok": true})
}

// 等用户点定了再发：每条回复一个计时器，再点就重新计时
var fbPending struct {
	sync.Mutex
	timers map[string]*time.Timer
	sent   map[string]string // 已经发给平台（或进了队列）的评价：取消时只有发过才要告诉平台
}

func (s *Server) settleFeedback(p *fbPayload, wait time.Duration) {
	k := p.ConversationID + "\x00" + p.MessageID
	fbPending.Lock()
	defer fbPending.Unlock()
	if fbPending.timers == nil {
		fbPending.timers, fbPending.sent = map[string]*time.Timer{}, map[string]string{}
	}
	if t := fbPending.timers[k]; t != nil {
		t.Stop()
	}
	fbPending.timers[k] = time.AfterFunc(wait, func() {
		fbPending.Lock()
		delete(fbPending.timers, k)
		prev := fbPending.sent[k]
		// 最后的状态和发过的一样（比如点赞又取消、再点赞），或者从没发过就取消了：不用发
		skip := p.Rating == prev || (p.Rating == "none" && prev == "")
		if !skip {
			fbPending.sent[k] = p.Rating
		}
		fbPending.Unlock()
		if !skip {
			fbDeliver(s, p)
		}
	})
}

// fbDeliver 是真正发出去的那一步（测试里换掉）
var fbDeliver = (*Server).deliverFeedback

var fbQueueMu sync.Mutex

func (s *Server) feedbackQueueFile() string { return filepath.Join(s.cfg.Dir, "feedback-queue.jsonl") }

// deliverFeedback：补上模板信息后发给平台，失败就进队列；没连 creght 不发
func (s *Server) deliverFeedback(p *fbPayload) {
	if _, err := creght.ReadToken(s.loginHost()); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, def, err := s.projectTemplate(ctx); err == nil && def != nil {
		p.Context["template"] = def.Key
	}
	if err := creght.NewClient(s.loginHost()).SendAIFeedback(ctx, p); err != nil {
		log.Printf("反馈发送失败，先存进队列：%v", err)
		s.queueFeedback(p)
	}
}

func (s *Server) queueFeedback(p *fbPayload) {
	fbQueueMu.Lock()
	defer fbQueueMu.Unlock()
	b, _ := json.Marshal(p)
	f, err := os.OpenFile(s.feedbackQueueFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// retryFeedback：定时把队列里的反馈重发一遍；同一条回复只发最后一次评价（后面的覆盖前面的）
func (s *Server) retryFeedback(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute): // 启动后等一会儿再发第一轮
		case <-t.C:
		}
		s.flushFeedback(ctx)
	}
}

func (s *Server) flushFeedback(ctx context.Context) {
	if _, err := creght.ReadToken(s.loginHost()); err != nil {
		return // 断开了 creght：队列留着，连回来再发
	}
	fbQueueMu.Lock()
	defer fbQueueMu.Unlock()
	f, err := os.Open(s.feedbackQueueFile())
	if err != nil {
		return
	}
	var order []string
	last := map[string]*fbPayload{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var p fbPayload
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			continue
		}
		k := p.ConversationID + "\x00" + p.MessageID
		if _, ok := last[k]; !ok {
			order = append(order, k)
		}
		last[k] = &p
	}
	f.Close()
	var left []*fbPayload
	for _, k := range order {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := creght.NewClient(s.loginHost()).SendAIFeedback(c, last[k])
		cancel()
		if err != nil {
			left = append(left, last[k])
		}
	}
	var b strings.Builder
	for _, p := range left {
		j, _ := json.Marshal(p)
		b.Write(j)
		b.WriteByte('\n')
	}
	if len(left) == 0 {
		os.Remove(s.feedbackQueueFile())
		return
	}
	os.WriteFile(s.feedbackQueueFile(), []byte(b.String()), 0o600)
}
