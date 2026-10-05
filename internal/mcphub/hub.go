// Package mcphub 连接用户配置的 MCP server，把它们的工具包成 pi 的工具交给本地 agent。
//
// 配置在 ~/.shuttle/mcp.json，格式和 Claude Code / Claude Desktop / Cursor 通用：
//
//	{"mcpServers": {
//	  "playwright": {"command": "npx", "args": ["@playwright/mcp@latest"]},
//	  "remote":     {"type": "http", "url": "https://…/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}}
//	}}
//
// command / args / env / url / headers 里的 ${VAR} 按 Shuttle 进程的环境变量展开。
// 工具名是 mcp__<server>__<tool>，和 Claude Code 的约定一致。
// excludeTools 列出不交给 agent 的工具（字段名和 Gemini CLI 一致），工具定义很占上下文，用不到的可以关掉。
package mcphub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	piagent "github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"

	"github.com/annulo/annulo/internal/version"
)

type ServerConfig struct {
	Type     string            `json:"type,omitempty"` // stdio（默认，有 command 时）/ http / sse
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	// ExcludeTools 是关掉的工具（MCP 里的原名），不交给 agent
	ExcludeTools []string `json:"excludeTools,omitempty"`
}

func (c ServerConfig) excluded(tool string) bool { return slices.Contains(c.ExcludeTools, tool) }

func (c ServerConfig) transport() string {
	switch {
	case c.Type != "":
		return c.Type
	case c.Command != "":
		return "stdio"
	default:
		return "http"
	}
}

type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ServerStatus 给界面显示每个 server 的连接情况。
type ServerStatus struct {
	Name      string       `json:"name"`
	Transport string       `json:"transport"`
	Status    string       `json:"status"` // connecting / needs_auth / connected / failed / disabled
	Error     string       `json:"error,omitempty"`
	AuthURL   string       `json:"auth_url,omitempty"` // needs_auth 时让用户打开的授权地址
	Authed    bool         `json:"authed"`             // 本机存有 OAuth 授权
	Tools     []ToolStatus `json:"tools"`
}

type ToolStatus struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Tokens  int    `json:"tokens"` // 工具定义（描述 + 参数 schema）大约占多少 token，每次请求都要带上
}

type server struct {
	name    string
	cfg     ServerConfig
	status  string
	err     error
	session *mcp.ClientSession
	tools   []*mcp.Tool
	pending *pendingAuth // 等用户授权时非空
}

type Hub struct {
	dir string // ~/.shuttle
	// CallbackURL 是 OAuth 授权完成后跳回的地址（Shuttle 的 /_shuttle/oauth/callback）
	CallbackURL string

	mu       sync.Mutex
	servers  map[string]*server
	onChange func() // 工具集变了（连上 / 断开 / 重载）时回调
}

func New(dir string) *Hub { return &Hub{dir: dir, servers: map[string]*server{}} }

func (h *Hub) File() string { return filepath.Join(h.dir, "mcp.json") }

// OnChange 设置工具集变化时的回调。
func (h *Hub) OnChange(fn func()) { h.onChange = fn }

// ReadConfig 读配置原文；文件不存在时返回一份空配置。
func (h *Hub) ReadConfig() ([]byte, error) {
	b, err := os.ReadFile(h.File())
	if os.IsNotExist(err) {
		return []byte("{\n  \"mcpServers\": {}\n}\n"), nil
	}
	return b, err
}

func parseConfig(b []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, i18n.Errorf("mcp.json 不是合法 JSON：%w", "mcp.json isn't valid JSON: %w", err)
	}
	for name, s := range c.MCPServers {
		if !nameRe.MatchString(name) {
			return c, i18n.Errorf("server 名 %q 只能用字母、数字、_ 和 -", "Server name %q may only use letters, digits, _ and -", name)
		}
		switch s.transport() {
		case "stdio":
			if s.Command == "" {
				return c, i18n.Errorf("%s：stdio 类型要填 command", "%s: the stdio type needs a command", name)
			}
		case "http", "sse":
			if s.URL == "" {
				return c, i18n.Errorf("%s：%s 类型要填 url", "%s: the %s type needs a url", name, s.transport())
			}
		default:
			return c, i18n.Errorf("%s：type 只能是 stdio / http / sse", "%s: type must be stdio / http / sse", name)
		}
	}
	return c, nil
}

// SaveConfig 校验并保存配置，然后按新配置重连。
func (h *Hub) SaveConfig(b []byte) error {
	if _, err := parseConfig(b); err != nil {
		return err
	}
	if err := h.writeConfig(b); err != nil {
		return err
	}
	return h.Reload()
}

func (h *Hub) writeConfig(b []byte) error {
	tmp := h.File() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.File())
}

// Reconnect 重连单个 server。
func (h *Hub) Reconnect(name string) error {
	h.mu.Lock()
	old := h.servers[name]
	if old == nil {
		h.mu.Unlock()
		return i18n.Errorf("没有叫 %s 的 server", "No server named %s", name)
	}
	if old.session != nil {
		old.session.Close()
	}
	s := &server{name: name, cfg: old.cfg, status: "connecting"}
	if s.cfg.Disabled {
		s.status = "disabled"
	}
	h.servers[name] = s
	h.mu.Unlock()
	if s.status == "connecting" {
		go h.connect(s)
	}
	h.changed()
	return nil
}

// SetToolsEnabled 开关这个 server 的一批工具：改 mcp.json 里这个 server 的 excludeTools，不用重连。
// 按原始 JSON 改，保留用户写的其他字段。
func (h *Hub) SetToolsEnabled(name string, tools []string, enabled bool) error {
	b, err := h.ReadConfig()
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return i18n.Errorf("mcp.json 不是合法 JSON：%w", "mcp.json isn't valid JSON: %w", err)
	}
	servers, _ := raw["mcpServers"].(map[string]any)
	sc, _ := servers[name].(map[string]any)
	if sc == nil {
		return i18n.Errorf("没有叫 %s 的 server", "No server named %s", name)
	}
	var ex []string
	if list, ok := sc["excludeTools"].([]any); ok {
		for _, v := range list {
			if t, ok := v.(string); ok && !slices.Contains(tools, t) {
				ex = append(ex, t)
			}
		}
	}
	if !enabled {
		ex = append(ex, tools...)
	}
	if len(ex) == 0 {
		delete(sc, "excludeTools")
	} else {
		sc["excludeTools"] = ex
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := h.writeConfig(append(out, '\n')); err != nil {
		return err
	}
	h.mu.Lock()
	if s := h.servers[name]; s != nil {
		s.cfg.ExcludeTools = ex
	}
	h.mu.Unlock()
	h.changed()
	return nil
}

// EnsureServer 在 mcp.json 里没有 name 时加上它并连接；已经有（包括用户改过或关掉的）就不动。
// 用户不想要可以在设置里关掉（disabled），删掉的话下次还会被加回来。
func (h *Hub) EnsureServer(name string, sc ServerConfig) error {
	b, err := h.ReadConfig()
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return i18n.Errorf("mcp.json 不是合法 JSON：%w", "mcp.json isn't valid JSON: %w", err)
	}
	servers, _ := raw["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		raw["mcpServers"] = servers
	}
	if _, ok := servers[name]; ok {
		return nil
	}
	servers[name] = sc
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return h.SaveConfig(append(out, '\n'))
}

// estimateTokens 粗估工具定义的 token 数：JSON 字节数 / 4，给界面提示哪些工具占得多。
func estimateTokens(t *mcp.Tool) int {
	b, _ := json.Marshal(struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      any    `json:"input_schema"`
	}{t.Name, t.Description, t.InputSchema})
	return len(b) / 4
}

// Reload 断开所有 server，按当前配置文件重新连接。连接在后台进行，不阻塞调用方。
func (h *Hub) Reload() error {
	b, err := h.ReadConfig()
	if err != nil {
		return err
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return err
	}
	h.Close()
	h.mu.Lock()
	h.servers = map[string]*server{}
	for name, sc := range cfg.MCPServers {
		st := "connecting"
		if sc.Disabled {
			st = "disabled"
		}
		h.servers[name] = &server{name: name, cfg: sc, status: st}
	}
	servers := h.servers
	h.mu.Unlock()
	for _, s := range servers {
		if s.status == "connecting" {
			go h.connect(s)
		}
	}
	h.changed()
	return nil
}

func (h *Hub) changed() {
	if h.onChange != nil {
		h.onChange()
	}
}

func (h *Hub) connect(s *server) {
	// 远程 server 可能要等用户去浏览器里授权，给足时间
	ctx, cancel := context.WithTimeout(context.Background(), authWait+time.Minute)
	defer cancel()
	sess, tools, err := h.dial(ctx, s)
	h.mu.Lock()
	if h.servers[s.name] != s { // 期间被重载掉了
		h.mu.Unlock()
		if sess != nil {
			sess.Close()
		}
		return
	}
	if err != nil {
		s.status, s.err = "failed", err
		log.Printf("mcp %s 连接失败：%v", s.name, err)
	} else {
		s.status, s.session, s.tools = "connected", sess, tools
		log.Printf("mcp %s 已连接，%d 个工具", s.name, len(tools))
	}
	h.mu.Unlock()
	h.changed()
}

func (h *Hub) dial(ctx context.Context, s *server) (*mcp.ClientSession, []*mcp.Tool, error) {
	c := s.cfg
	var t mcp.Transport
	switch c.transport() {
	case "stdio":
		args := make([]string, len(c.Args))
		for i, a := range c.Args {
			args[i] = expand(a)
		}
		cmd := exec.Command(expand(c.Command), args...)
		// 本地 MCP 是用户自己配的工具：带上设置里的密钥（助手的命令行里没有）
		cmd.Env = append(os.Environ(), config.All()...)
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+expand(v))
		}
		// server 的 stderr 写到日志文件，排查启动失败用
		if err := os.MkdirAll(filepath.Join(h.dir, "logs"), 0o700); err == nil {
			if f, err := os.OpenFile(filepath.Join(h.dir, "logs", "mcp-"+s.name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				cmd.Stderr = f
			}
		}
		t = &mcp.CommandTransport{Command: cmd}
	case "http":
		st := &mcp.StreamableClientTransport{Endpoint: expand(c.URL), HTTPClient: headerClient(c.Headers)}
		// 自己配了 Authorization 头的不走 OAuth；其余遇到 401 时发起授权
		if _, ok := c.Headers["Authorization"]; !ok && h.CallbackURL != "" {
			oh, err := h.oauthHandler(s)
			if err != nil {
				return nil, nil, err
			}
			st.OAuthHandler = oh
		}
		t = st
	case "sse":
		t = &mcp.SSEClientTransport{Endpoint: expand(c.URL), HTTPClient: headerClient(c.Headers)}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "shuttle", Version: version.Version}, nil)
	sess, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, nil, err
	}
	var tools []*mcp.Tool
	for tool, err := range sess.Tools(ctx, nil) {
		if err != nil {
			sess.Close()
			return nil, nil, i18n.Errorf("列出工具失败：%w", "Failed to list tools: %w", err)
		}
		tools = append(tools, tool)
	}
	return sess, tools, nil
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, expand(v))
	}
	return t.base.RoundTrip(r)
}

func headerClient(h map[string]string) *http.Client {
	if len(h) == 0 {
		return nil
	}
	return &http.Client{Transport: headerTransport{headers: h, base: http.DefaultTransport}}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.servers {
		if s.session != nil {
			s.session.Close()
			s.session = nil
		}
	}
}

func (h *Hub) Status() []ServerStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ServerStatus, 0, len(h.servers))
	for _, s := range h.servers {
		st := ServerStatus{Name: s.name, Transport: s.cfg.transport(), Status: s.status, Tools: []ToolStatus{}}
		if s.err != nil {
			st.Error = s.err.Error()
		}
		if s.pending != nil {
			st.AuthURL = s.pending.url
		}
		st.Authed = h.loadAuth(s.name) != nil
		for _, t := range s.tools {
			st.Tools = append(st.Tools, ToolStatus{Name: t.Name, Enabled: !s.cfg.excluded(t.Name), Tokens: estimateTokens(t)})
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ToolName 是 MCP 工具在 agent 里的名字：mcp__<server>__<tool>，模型接口要求 ≤64 个字符。
func ToolName(server, tool string) string {
	n := "mcp__" + server + "__" + unsafeRe.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// Tools 返回所有已连接 server 的工具。
func (h *Hub) Tools() []piagent.AgentTool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []piagent.AgentTool
	names := make([]string, 0, len(h.servers))
	for n := range h.servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := h.servers[n]
		if s.status != "connected" {
			continue
		}
		for _, t := range s.tools {
			if !s.cfg.excluded(t.Name) {
				out = append(out, wrap(s, t))
			}
		}
	}
	return out
}

func wrap(s *server, t *mcp.Tool) piagent.AgentTool {
	params := ai.Object()
	if t.InputSchema != nil {
		if b, err := json.Marshal(t.InputSchema); err == nil {
			var sc ai.Schema
			if json.Unmarshal(b, &sc) == nil && sc.Type == "object" {
				params = &sc
			}
		}
	}
	desc := t.Description
	if desc == "" {
		desc = t.Name
	}
	sess := s.session
	return piagent.AgentTool{
		Name:        ToolName(s.name, t.Name),
		Label:       s.name + " · " + t.Name,
		Description: "[MCP " + s.name + "] " + desc,
		Parameters:  params,
		Execute: func(ctx context.Context, _ string, p map[string]any, _ piagent.ToolUpdateFunc) (piagent.AgentToolResult, error) {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: p})
			if err != nil {
				return piagent.AgentToolResult{}, err
			}
			content := convert(res)
			if res.IsError {
				var sb strings.Builder
				for _, c := range content {
					if tc, ok := c.(ai.TextContent); ok {
						sb.WriteString(tc.Text)
					}
				}
				msg := sb.String()
				if msg == "" {
					msg = i18n.T("工具返回了错误", "The tool returned an error")
				}
				return piagent.AgentToolResult{}, errors.New(msg)
			}
			return piagent.AgentToolResult{Content: content}, nil
		},
	}
}

// Call 给本机函数（ctx.mcp）用：调一个已连接 server 的工具，和 agent 走同一个连接、同一套开关（关掉的工具调不了）。
// 返回值：有 structuredContent 就是它；否则把文本拼起来，是 JSON 就解析成对象，不是就原样返回字符串。
// 工具报错（isError）返回 error，内容是工具给的文字。
func (h *Hub) Call(ctx context.Context, name, tool string, args map[string]any) (any, error) {
	// 刚启动或正在重连时是 connecting：等它连完（最多 30 秒），别让定时任务一开机就报没连上
	deadline := time.Now().Add(30 * time.Second)
	for {
		h.mu.Lock()
		connecting := h.servers[name] != nil && h.servers[name].status == "connecting"
		h.mu.Unlock()
		if !connecting || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	// 平台新加的工具：连上时拿的列表里没有，先重新拉一次列表再判断（不用用户去设置里重连）
	h.mu.Lock()
	if s := h.servers[name]; s != nil && s.status == "connected" && !slices.ContainsFunc(s.tools, func(t *mcp.Tool) bool { return t.Name == tool }) {
		sess := s.session
		h.mu.Unlock()
		h.refreshTools(ctx, s, sess)
		h.mu.Lock()
	}
	s := h.servers[name]
	var sess *mcp.ClientSession
	var err error
	switch {
	case s == nil:
		err = i18n.Errorf("没有叫 %s 的 MCP：到 Annulo 的 设置 → MCP 里添加", "No MCP named %s: add it in Annulo → Settings → MCP", name)
	case s.status == "needs_auth":
		err = i18n.Errorf("MCP %s 还没授权：到 Annulo 的 设置 → MCP 里完成授权", "MCP %s isn't authorized yet: finish authorizing in Annulo → Settings → MCP", name)
	case s.status != "connected":
		err = i18n.Errorf("MCP %s 没连上（%s）：到 Annulo 的 设置 → MCP 里看看", "MCP %s isn't connected (%s): check Annulo → Settings → MCP", name, s.status)
	case s.cfg.excluded(tool):
		err = i18n.Errorf("MCP %s 的工具 %s 在设置里关掉了", "Tool %[2]s of MCP %[1]s is turned off in Settings", name, tool)
	case !slices.ContainsFunc(s.tools, func(t *mcp.Tool) bool { return t.Name == tool }):
		err = i18n.Errorf("MCP %s 没有工具 %s", "MCP %s has no such tool: %s", name, tool)
	default:
		sess = s.session
	}
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, i18n.Errorf("调用 MCP %s 的 %s 失败：%w", "Calling %[2]s on MCP %[1]s failed: %[3]w", name, tool, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	if res.IsError {
		if text == "" {
			text = i18n.T("工具返回了错误", "The tool returned an error")
		}
		return nil, i18n.Errorf("MCP %s 的 %s 报错：%s", "%[2]s on MCP %[1]s returned an error: %[3]s", name, tool, text)
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		var v any
		if json.Unmarshal(b, &v) == nil {
			return v, nil
		}
	}
	var v any
	if json.Unmarshal([]byte(text), &v) == nil {
		return v, nil
	}
	return text, nil
}

// refreshTools 重新拉 server 的工具列表；拉不到就保持原样。
func (h *Hub) refreshTools(ctx context.Context, s *server, sess *mcp.ClientSession) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var tools []*mcp.Tool
	for tool, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return
		}
		tools = append(tools, tool)
	}
	h.mu.Lock()
	if h.servers[s.name] != s || s.session != sess {
		h.mu.Unlock()
		return
	}
	s.tools = tools
	h.mu.Unlock()
	h.changed()
}

// convert 把 MCP 的结果内容转成 pi 的：文本、图片原样，其余（资源等）转成 JSON 文本。
func convert(res *mcp.CallToolResult) ai.ContentList {
	var out ai.ContentList
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			out = append(out, ai.TextContent{Text: v.Text})
		case *mcp.ImageContent:
			out = append(out, ai.ImageContent{Data: base64.StdEncoding.EncodeToString(v.Data), MimeType: v.MIMEType})
		default:
			b, _ := json.Marshal(c)
			out = append(out, ai.TextContent{Text: string(b)})
		}
	}
	if len(out) == 0 && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		out = append(out, ai.TextContent{Text: string(b)})
	}
	if len(out) == 0 {
		out = append(out, ai.TextContent{Text: "(无输出)"})
	}
	return out
}

// stateOf 取授权地址里的 state 参数，回调时靠它找回是哪个 server 在等。
func stateOf(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("state")
}

// expand 展开配置里的 ${NAME}：先查设置里的密钥，再查环境变量
func expand(s string) string { return os.Expand(s, config.Lookup) }
