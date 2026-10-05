package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/sky-valley/pi/agent"
)

// 对话历史：每段对话一个 JSONL 文件 ~/.shuttle/workspaces/<项目>/chats/<id>.jsonl，只追加不改写，一行一条记录：
//
//	{"type":"meta","timestamp":…,"id":…,"title":…,"title_by":…,"pi_session":…}   对话元信息，后写的覆盖先写的
//	{"type":"message","timestamp":…,"message":<UIMessage>}           一条消息（Vercel AI SDK 的 UIMessage）
//
// 消息由服务端边推流边拼，浏览器中途关掉也不丢。pi_session 指向 pi 自己的 JSONL 会话文件，
// 切回这段对话或重启 Shuttle 时用它恢复模型上下文。坏行（比如写到一半断电）跳过。

type chatLine struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	ID        string `json:"id,omitempty"`
	Title     string `json:"title,omitempty"`
	// TitleBy：标题是谁起的。空 = 取的第一句话（还没起名），auto = 模型起的，user = 用户改的（之后不再自动改）
	TitleBy   string          `json:"title_by,omitempty"`
	PiSession string          `json:"pi_session,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	// type=feedback：用户给某条回复点的赞 / 踩（rating up / down / none=取消），后一行覆盖前一行
	MessageID string `json:"message_id,omitempty"`
	Rating    string `json:"rating,omitempty"`
	// 外部 agent（Claude Code / Codex）那边的会话：哪个引擎、会话 id（"-" 是清掉）
	CLIEngine  string `json:"cli_engine,omitempty"`
	CLISession string `json:"cli_session,omitempty"`
}

type Chat struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	TitleBy   string            `json:"title_by,omitempty"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
	PiSession string            `json:"pi_session,omitempty"`
	Messages  []json.RawMessage `json:"messages"`
	// Feedback：消息 id → up / down（用户点的赞 / 踩，取消了就没有）
	Feedback map[string]string `json:"feedback,omitempty"`
	// 外部 agent 的会话（cli.go）
	CLIEngine  string `json:"cli_engine,omitempty"`
	CLISession string `json:"cli_session,omitempty"`
}

type ChatSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updated_at"`
	Messages  int    `json:"messages"`
	// Status：running 正在执行 / asking 在等用户回答问卷 / 空 闲着
	Status string `json:"status,omitempty"`
}

var chatIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var ErrBadChatID = i18n.New("对话 id 不合法", "Invalid chat id")

// 对话历史按项目分开：~/.shuttle/workspaces/<项目 id>/chats。项目之间互相看不到。
func (a *Agent) chatsDir() string {
	if a.ws == "" {
		return filepath.Join(a.cfg.Dir, "chats")
	}
	return filepath.Join(a.cfg.Dir, "workspaces", a.ws, "chats")
}

// migrateLegacyChats：早期对话都在 ~/.shuttle/chats，那时只有一个运营后台（~/.shuttle/backend），
// 归到它的项目；没有它就归到当前项目。
func (a *Agent) migrateLegacyChats() {
	old := filepath.Join(a.cfg.Dir, "chats")
	ents, err := os.ReadDir(old)
	if err != nil || len(ents) == 0 {
		return
	}
	owner := a.cfg.LegacyProject()
	if owner == "" {
		owner = a.ws
	}
	if owner == "" {
		return
	}
	dst := filepath.Join(a.cfg.Dir, "workspaces", owner, "chats")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		os.Rename(filepath.Join(old, e.Name()), filepath.Join(dst, e.Name()))
	}
	os.Remove(old) // 空了才删得掉
}

func (a *Agent) chatPath(id string) (string, error) {
	if !chatIDRe.MatchString(id) {
		return "", ErrBadChatID
	}
	return filepath.Join(a.chatsDir(), id+".jsonl"), nil
}

func (a *Agent) LoadChat(id string) (*Chat, error) {
	p, err := a.chatPath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &Chat{ID: id, Messages: []json.RawMessage{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20) // 一条消息里可能有大段工具输出
	for sc.Scan() {
		var l chatLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if c.CreatedAt == 0 {
			c.CreatedAt = l.Timestamp
		}
		c.UpdatedAt = max(c.UpdatedAt, l.Timestamp)
		switch l.Type {
		case "meta":
			if l.Title != "" {
				c.Title = l.Title
				c.TitleBy = l.TitleBy
			}
			if l.PiSession != "" {
				c.PiSession = l.PiSession
			}
			if l.CLIEngine != "" {
				c.CLIEngine, c.CLISession = l.CLIEngine, l.CLISession
				if c.CLISession == "-" {
					c.CLISession = ""
				}
			}
		case "message":
			if len(l.Message) > 0 {
				c.Messages = append(c.Messages, l.Message)
			}
		case "feedback":
			if c.Feedback == nil {
				c.Feedback = map[string]string{}
			}
			if l.Rating == "up" || l.Rating == "down" {
				c.Feedback[l.MessageID] = l.Rating
			} else {
				delete(c.Feedback, l.MessageID)
			}
		}
	}
	return c, sc.Err()
}

// SetFeedback 记下用户给一条回复的评价（up / down / none=取消），追加一行，读对话时后一行覆盖前一行。
func (a *Agent) SetFeedback(id, messageID, rating string) error {
	if messageID == "" || (rating != "up" && rating != "down" && rating != "none") {
		return errors.New("feedback：要有 message_id，rating 是 up / down / none")
	}
	return a.appendLines(id, chatLine{Type: "feedback", Timestamp: time.Now().UnixMilli(), MessageID: messageID, Rating: rating})
}

// appendLines 把记录追加到对话文件末尾。
func (a *Agent) appendLines(id string, lines ...chatLine) error {
	p, err := a.chatPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.chatsDir(), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf.Bytes())
	return err
}

func (a *Agent) ListChats() ([]ChatSummary, error) {
	ents, err := os.ReadDir(a.chatsDir())
	if os.IsNotExist(err) {
		return []ChatSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []ChatSummary{}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		c, err := a.LoadChat(id)
		if err != nil {
			continue
		}
		out = append(out, ChatSummary{ID: c.ID, Title: c.Title, UpdatedAt: c.UpdatedAt, Messages: len(c.Messages), Status: a.chatStatus(c.ID)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// ChatStatuses 是正在执行、或者在等用户回答问卷的对话和它们的状态（running / asking），界面轮询它来标历史列表。
func (a *Agent) ChatStatuses() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for id := range a.waiting {
		out[id] = "asking"
	}
	for id, c := range a.chats {
		if c.running {
			out[id] = "running"
		}
	}
	return out
}

func (a *Agent) chatStatus(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch c := a.chats[id]; {
	case c != nil && c.running:
		return "running"
	case a.waiting[id]:
		return "asking"
	}
	return ""
}

func (a *Agent) DeleteChat(id string) error {
	a.mu.Lock()
	if c := a.chats[id]; c != nil {
		if c.running {
			a.mu.Unlock()
			return ErrBusy
		}
		delete(a.chats, id)
	}
	delete(a.waiting, id)
	a.mu.Unlock()
	p, err := a.chatPath(id)
	if err != nil {
		return err
	}
	c, _ := a.LoadChat(id)
	if c != nil && c.PiSession != "" {
		os.Remove(c.PiSession)
	}
	return os.Remove(p)
}

// SaveMessage 往对话历史追加一条消息。对话文件不存在时先写元信息（标题取 title 的开头）；
// pi 会话文件第一次落盘后补一行元信息记下它的路径。
//
// 用户消息在发出时就写，助手消息在这一轮结束时写：中途刷新页面，历史里已经有这条提问，
// 正在进行的回答由断线续传补上。
func (a *Agent) SaveMessage(id string, msg json.RawMessage, title string, ts int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var lines []chatLine
	prev, err := a.LoadChat(id)
	if err != nil {
		prev = nil
		lines = append(lines, chatLine{Type: "meta", Timestamp: ts, ID: id, Title: titleOf(title)})
	}
	if c := a.chats[id]; c != nil && c.rec != nil {
		if p := c.rec.Path(); p != "" && (prev == nil || prev.PiSession != p) {
			if _, err := os.Stat(p); err == nil {
				lines = append(lines, chatLine{Type: "meta", Timestamp: ts, PiSession: p})
			}
		}
	}
	lines = append(lines, chatLine{Type: "message", Timestamp: ts, Message: msg})
	return a.appendLines(id, lines...)
}

func titleOf(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 30 {
		return string(r[:30]) + "…"
	}
	return s
}

// NeedsTitle：这段对话已经存下来了，标题还是第一句话（模型没起过名、用户也没改过）。
func (a *Agent) NeedsTitle(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.AutoTitleOn() {
		return false
	}
	c, err := a.LoadChat(id)
	return err == nil && c.TitleBy == ""
}

// SetTitle 改对话的标题（by：auto 模型起的 / user 用户改的）。对话不存在时报错，不新建文件：
// 起名是异步的，这时可能已经切到了别的项目，不能写到那个项目的目录里。
func (a *Agent) SetTitle(id, title, by string) error {
	title = titleOf(title)
	if title == "" {
		return i18n.New("标题是空的", "The title is empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.LoadChat(id)
	if err != nil {
		return err
	}
	if by == "auto" && c.TitleBy == "user" {
		return nil
	}
	return a.appendLines(id, chatLine{Type: "meta", Timestamp: c.UpdatedAt, Title: title, TitleBy: by})
}

const titleSystem = `你给一段对话起标题。只输出标题本身：不超过 14 个字，说清楚用户要做的事（比如「对比 X 和小红书浏览量」「修复站点体检报错」），
不要引号、不要句末标点、不要「关于」「请求」这类虚词。用户用什么语言就用什么语言。`

// GenerateTitle 按用户的第一句话起一个短标题：用设置里选的起名模型，没选用当前模型。
func (a *Agent) GenerateTitle(ctx context.Context, first string) (string, error) {
	if r := []rune(strings.TrimSpace(first)); len(r) > 800 {
		first = string(r[:800])
	}
	_, model := a.TitleSettings()
	out, err := a.complete(ctx, titleSystem, "用户的第一句话：\n"+first, model, agent.ThinkOff)
	if err != nil {
		return "", err
	}
	return cleanTitle(out), nil
}

// cleanTitle 去掉模型常带的引号、「标题：」前缀、句末标点，只留第一行。
func cleanTitle(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	for _, p := range []string{"标题：", "标题:", "Title:", "title:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, p))
	}
	s = strings.Trim(s, "\"'“”‘’「」『』《》*# ")
	s = strings.TrimRight(s, "。.!！?？,，;；:：")
	if r := []rune(s); len(r) > 24 {
		s = string(r[:24])
	}
	return s
}
