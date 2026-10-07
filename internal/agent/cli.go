package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sky-valley/pi/ai"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localcmd"
)

// 外部 agent：本机装了 Claude Code / Codex 的，可以直接让它们当 Shuttle 的助手（设置 → 模型里和模型一起选）。
//
// 每一轮起一个进程（claude -p / codex exec，工作目录是项目），读它的 JSON 事件流，转成和 pi 一样的 Event 推给界面。
// 上下文在它们自己的会话里：第一轮记下会话 id（存进对话的 meta），之后每轮 --resume / exec resume 接着聊。
//   - 系统提示：Claude 用 --append-system-prompt（每轮都带）；Codex 没有对应参数，新会话的第一条消息前面带上。
//   - Shuttle 的工具（db_query、request_user_input、page_errors、已连接的 MCP）经本机的 MCP 接口给它们（climcp.go）。
//   - skill：把 Shuttle 管理的 skill 软链进项目的 .claude/skills、.agents/skills，它们按自己的方式发现、读取（不进 git、不推 creght）。
//   - 用它们自己的登录和模型（订阅或它们自己的 key），不经过 Shuttle 的服务商。
// 不支持中途插话：插的话留在队列里，这一轮结束后当普通消息发（界面已经这样处理 409）。

const cliPrefix = "cli:"

// CLI 引擎：服务商 id 就是引擎名
const (
	EngineClaude = "claude-code"
	EngineCodex  = "codex"
)

var cliEngines = []struct {
	id, bin, name string
	window        int
}{
	{EngineClaude, "claude", "Claude Code", 200000},
	{EngineCodex, "codex", "Codex", 272000},
}

// IsCLIProvider：这个服务商是本机的外部 agent（Claude Code / Codex）。
func IsCLIProvider(p string) bool { return p == EngineClaude || p == EngineCodex }

// CLIName 是外部 agent 的显示名（Claude Code / Codex）。
func CLIName(p string) string {
	for _, e := range cliEngines {
		if e.id == p {
			return e.name
		}
	}
	return p
}

// cliModels 是本机装了的外部 agent，排在模型列表最后。每个引擎一个「默认」（用它自己配置的模型，id 是 cli:<引擎>），
// 再加上能选的模型（cli:<引擎>/<模型>，跑的时候带 --model / -m）：Claude 用它认的别名，Codex 读它自己的模型目录（codex debug models）。
func cliModels() []config.LLM {
	var out []config.LLM
	yes := true
	for _, e := range cliEngines {
		if findCLI(e.bin) == "" {
			continue
		}
		list := engineModels(e.id)
		name := e.name
		if d := engineDefault(e.id); d != "" {
			name += " · " + i18n.T("默认", "Default") + paren(d)
		}
		out = append(out, config.LLM{ID: cliPrefix + e.id, Name: name, Provider: e.id, Model: e.id, ContextWindow: e.window, Images: &yes, Reasoning: true})
		for _, m := range list {
			w := m.window
			if w == 0 {
				w = e.window
			}
			out = append(out, config.LLM{ID: cliPrefix + e.id + "/" + m.id, Name: e.name + " · " + m.name, Provider: e.id, Model: m.id, ContextWindow: w, Images: &yes, Reasoning: true})
		}
	}
	return out
}

type engineModel struct {
	id, name string
	window   int
}

// 外部 agent 自己报的模型目录（每个账号能用的不一样，不写死）：
//   - Claude Code：控制协议的 initialize 回包里的 models（和 Agent SDK 的 supportedModels 一样，不调模型、不花 token）；
//   - Codex：codex debug models。
//
// 都要起一次进程：缓存 10 分钟，在后台刷新（列模型时持着锁，不能等它）；设置页可以手动刷新（RefreshCLIModels）。
type engineCatalog struct {
	list    []engineModel
	deflt   string // 「默认」现在是哪个模型（Claude 报的）
	at      time.Time
	loading bool
}

var catalogs = struct {
	sync.Mutex
	m map[string]*engineCatalog
}{m: map[string]*engineCatalog{}}

func engineModels(engine string) []engineModel {
	catalogs.Lock()
	defer catalogs.Unlock()
	c := catalogs.m[engine]
	if c == nil {
		c = &engineCatalog{}
		catalogs.m[engine] = c
	}
	if !c.loading && time.Since(c.at) > 10*time.Minute {
		c.loading = true
		go loadCatalog(engine)
	}
	return c.list
}

// defaultModelName 从 Claude 对「default」的描述里取出它现在对应的模型：
// 老版本是「Opus 5.5 · Best for everyday…」（点号前面），新版本是「Use the default model (currently Opus 5.5)」。
func defaultModelName(desc string) string {
	if m := currentModelRe.FindStringSubmatch(desc); m != nil {
		return strings.TrimSpace(m[1])
	}
	name, _, _ := strings.Cut(desc, " · ")
	return name
}

var currentModelRe = regexp.MustCompile(`\(currently ([^)]+)\)`)

// paren 按界面语言加括号：中文用全角，英文用半角加空格
func paren(s string) string { return i18n.T("（", " (") + s + i18n.T("）", ")") }

func engineDefault(engine string) string {
	catalogs.Lock()
	defer catalogs.Unlock()
	if c := catalogs.m[engine]; c != nil {
		return c.deflt
	}
	return ""
}

// RefreshCLIModels 马上重新读一遍外部 agent 的模型目录（设置页的刷新按钮），读完才返回。
func RefreshCLIModels() {
	var wg sync.WaitGroup
	for _, e := range cliEngines {
		if findCLI(e.bin) == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			loadCatalog(e.id)
		}()
	}
	wg.Wait()
}

func loadCatalog(engine string) {
	var list []engineModel
	var deflt string
	defer func() {
		catalogs.Lock()
		c := catalogs.m[engine]
		if c == nil {
			c = &engineCatalog{}
			catalogs.m[engine] = c
		}
		if list != nil {
			c.list, c.deflt = list, deflt
		}
		c.at, c.loading = time.Now(), false
		catalogs.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch engine {
	case EngineClaude:
		list, deflt = claudeCatalog(ctx)
	case EngineCodex:
		list = codexCatalog(ctx)
	}
}

// claudeCatalog：给 claude 发一个 initialize 控制请求，读回包里的 models。
func claudeCatalog(ctx context.Context) ([]engineModel, string) {
	bin := findCLI("claude")
	if bin == "" {
		return nil, ""
	}
	cmd := exec.CommandContext(ctx, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--strict-mcp-config", "--tools", "", "--no-session-persistence")
	cmd.Dir = os.TempDir()
	cmd.Env = cliEnv()
	cmd.Stdin = strings.NewReader(`{"type":"control_request","request_id":"shuttle-models","request":{"subtype":"initialize"}}` + "\n")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				Response struct {
					Models []struct {
						Value         string `json:"value"`
						ResolvedModel string `json:"resolvedModel"`
						DisplayName   string `json:"displayName"`
						Description   string `json:"description"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(line), &m) != nil || m.Type != "control_response" {
			continue
		}
		list := []engineModel{}
		deflt := ""
		for _, x := range m.Response.Response.Models {
			if x.Value == "" {
				continue
			}
			if x.Value == "default" {
				deflt = defaultModelName(x.Description)
				continue
			}
			name := x.DisplayName
			if name == "" {
				name = x.Value
			}
			w := 0
			if strings.Contains(x.Value, "[1m]") || strings.Contains(x.ResolvedModel, "[1m]") {
				w = 1000000
			}
			list = append(list, engineModel{x.Value, name, w})
		}
		return list, strings.TrimSpace(deflt)
	}
	return nil, ""
}

func codexCatalog(ctx context.Context) []engineModel {
	bin := findCLI("codex")
	if bin == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, bin, "debug", "models")
	cmd.Env = cliEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ms []struct {
		Slug          string `json:"slug"`
		DisplayName   string `json:"display_name"`
		Visibility    string `json:"visibility"`
		ContextWindow int    `json:"context_window"`
		Priority      int    `json:"priority"`
	}
	if json.Unmarshal(out, &ms) != nil {
		var wrapped struct {
			Models json.RawMessage `json:"models"`
		}
		if json.Unmarshal(out, &wrapped) != nil || json.Unmarshal(wrapped.Models, &ms) != nil {
			return nil
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Priority < ms[j].Priority })
	list := []engineModel{}
	for _, m := range ms {
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		list = append(list, engineModel{m.Slug, name, m.ContextWindow})
	}
	return list
}

// 命令在哪：按登录 shell 的 PATH 找（internal/localcmd）。
func findCLI(bin string) string { return localcmd.Find(bin) }

func cliEnv(extra ...string) []string { return localcmd.Env(extra...) }

// cliRun 是一轮外部 agent 的输入。
type cliRun struct {
	engine    string
	bin       string
	cwd       string
	system    string
	prompt    string
	images    []string
	session   string // 接着哪个会话；空是新会话
	model     string // 用哪个模型（--model / -m）；空用它自己配置的
	thinking  string
	chatID    string // 这段对话的 id：给它的 bash 带上 ANNULO_CHAT_ID，annulo run 传给本机函数（ctx.chat_id）
	mcpURL    string
	mcpToken  string
	extraDirs []string
}

// cliResult 是一轮跑完的结果。
type cliResult struct {
	session string
	usage   ai.Usage
	model   string
	errMsg  string
}

// command 拼出这一轮的命令。提示词从 stdin 给，不受命令行长度限制。
func (r cliRun) command(ctx context.Context) *exec.Cmd {
	var args []string
	switch r.engine {
	case EngineClaude:
		args = []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--permission-mode", "acceptEdits",
			"--allowedTools", "Bash", "Read", "Edit", "Write", "MultiEdit", "Glob", "Grep", "LS", "WebFetch", "WebSearch", "TodoWrite", "Skill", "mcp__shuttle",
			"--append-system-prompt", r.system}
		if r.mcpURL != "" {
			cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"shuttle": map[string]any{"type": "http", "url": r.mcpURL, "headers": map[string]string{"Authorization": "Bearer " + r.mcpToken}}}})
			// 只用 Shuttle 的 MCP：用户在 Claude 里配的连接器（没授权的会在回复后面附一段提示）不带进来
			args = append(args, "--mcp-config", string(cfg), "--strict-mcp-config")
		}
		for _, d := range r.extraDirs {
			args = append(args, "--add-dir", d)
		}
		switch r.thinking {
		case "off", "low":
			args = append(args, "--effort", "low")
		case "high":
			args = append(args, "--effort", "high")
		}
		if r.model != "" {
			args = append(args, "--model", r.model)
		}
		if r.session != "" {
			args = append(args, "--resume", r.session)
		}
	case EngineCodex:
		args = []string{"exec"}
		if r.session != "" {
			args = append(args, "resume")
		}
		args = append(args, "--json", "--skip-git-repo-check")
		if r.session == "" {
			// 在项目目录里能写、能联网（creght / shuttle 命令要连平台和本机）；resume 沿用会话的设置
			args = append(args, "-s", "workspace-write", "-C", r.cwd)
			for _, d := range r.extraDirs {
				args = append(args, "--add-dir", d)
			}
		}
		args = append(args, "-c", "sandbox_workspace_write.network_access=true", "-c", "approval_policy=\"never\"")
		if r.mcpURL != "" {
			args = append(args, "-c", "mcp_servers.shuttle.url="+tomlString(r.mcpURL), "-c", "mcp_servers.shuttle.bearer_token_env_var=\"SHUTTLE_MCP_TOKEN\"",
				"-c", "mcp_servers.shuttle.default_tools_approval_mode=\"approve\"") // Shuttle 自己的工具不用逐个批准（exec 里也没人能批）
		}
		switch r.thinking {
		case "off", "low":
			args = append(args, "-c", "model_reasoning_effort=\"low\"")
		case "medium":
			args = append(args, "-c", "model_reasoning_effort=\"medium\"")
		case "high":
			args = append(args, "-c", "model_reasoning_effort=\"high\"")
		}
		if r.model != "" {
			args = append(args, "-m", r.model)
		}
		for _, img := range r.images {
			args = append(args, "-i", img)
		}
		if r.session != "" {
			args = append(args, r.session)
		}
		args = append(args, "-")
	}
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = r.cwd
	env := append(brand.ChildEnv("MCP_TOKEN", r.mcpToken), brand.ChildEnv("AGENT", r.engine)...)
	cmd.Env = cliEnv(append(env, brand.ChildEnv("CHAT_ID", r.chatID)...)...)
	cmd.Stdin = strings.NewReader(r.prompt)
	cmd.WaitDelay = 3 * time.Second
	localcmd.SetProcGroup(cmd)
	return cmd
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // JSON 字符串也是合法的 TOML 基本字符串
	return string(b)
}

// exec 跑一轮，把事件转给 emit。ctx 取消就杀掉进程（停止）。
func (r cliRun) exec(ctx context.Context, emit func(Event)) (cliResult, error) {
	cmd := r.command(ctx)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return cliResult{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Start(); err != nil {
		return cliResult{}, err
	}
	var res cliResult
	p := newCLIParser(r.engine, emit, &res)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 256<<10), 64<<20)
	for sc.Scan() {
		p.line(sc.Bytes())
	}
	p.finish()
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if res.errMsg != "" {
		return res, errors.New(res.errMsg)
	}
	if werr != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		if msg == "" {
			msg = werr.Error()
		}
		return res, i18n.Errorf("%s 退出了：%s", "%s exited: %s", r.bin, msg)
	}
	return res, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		q := p
		if len(q) > l.n {
			q = q[:l.n]
		}
		l.w.Write(q)
		l.n -= len(q)
	}
	return len(p), nil
}

// cliParser 把两种 JSON 事件流整理成 Event：每次模型请求之间的工具调用前后切步骤（turn_start / turn_end），
// 界面按步骤折叠过程、取最后一步的文字当回答。
type cliParser struct {
	engine string
	emit   func(Event)
	res    *cliResult
	inStep bool
	// Claude：正在流式输出的消息 id（流式的文字已经推过，整条消息到了就不再推）和还没出结果的工具
	streamed map[string]bool
	pending  map[string]bool
}

func newCLIParser(engine string, emit func(Event), res *cliResult) *cliParser {
	return &cliParser{engine: engine, emit: emit, res: res, streamed: map[string]bool{}, pending: map[string]bool{}}
}

func (p *cliParser) step() {
	if !p.inStep {
		p.emit(Event{Type: "turn_start"})
		p.inStep = true
	}
}

func (p *cliParser) endStep() {
	if p.inStep {
		p.emit(Event{Type: "turn_end"})
		p.inStep = false
	}
}

func (p *cliParser) finish() { p.endStep() }

func (p *cliParser) line(b []byte) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return
	}
	if p.engine == EngineCodex {
		p.codex(m)
	} else {
		p.claude(m)
	}
}

// claudeTool 把 Claude 的工具名换成界面认得的（和 pi 的一致），Shuttle 自己的 MCP 工具去掉前缀。
func claudeTool(name string) string {
	if n, ok := strings.CutPrefix(name, "mcp__shuttle__"); ok {
		return n
	}
	switch name {
	case "Bash":
		return "bash"
	case "Read":
		return "read"
	case "Write":
		return "write"
	case "Edit", "MultiEdit":
		return "edit"
	case "Grep":
		return "grep"
	case "Glob":
		return "find"
	case "LS":
		return "ls"
	}
	return name
}

func (p *cliParser) claude(m map[string]any) {
	switch m["type"] {
	case "system":
		if m["subtype"] == "init" {
			p.res.session, _ = m["session_id"].(string)
			p.res.model, _ = m["model"].(string)
		}
	case "stream_event":
		ev, _ := m["event"].(map[string]any)
		switch ev["type"] {
		case "message_start":
			msg, _ := ev["message"].(map[string]any)
			if id, _ := msg["id"].(string); id != "" {
				p.streamed[id] = true
			}
		case "content_block_delta":
			d, _ := ev["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				if t, _ := d["text"].(string); t != "" {
					p.step()
					p.emit(Event{Type: "text", Text: t})
				}
			case "thinking_delta":
				if t, _ := d["thinking"].(string); t != "" {
					p.step()
					p.emit(Event{Type: "thinking", Text: t})
				}
			}
		}
	case "assistant":
		if parent, _ := m["parent_tool_use_id"].(string); parent != "" {
			return // 子 agent 的过程不展开
		}
		msg, _ := m["message"].(map[string]any)
		id, _ := msg["id"].(string)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			c, _ := c.(map[string]any)
			switch c["type"] {
			case "text":
				if !p.streamed[id] {
					if t, _ := c["text"].(string); t != "" {
						p.step()
						p.emit(Event{Type: "text", Text: t})
					}
				}
			case "thinking":
				if !p.streamed[id] {
					if t, _ := c["thinking"].(string); t != "" {
						p.step()
						p.emit(Event{Type: "thinking", Text: t})
					}
				}
			case "tool_use":
				tid, _ := c["id"].(string)
				name, _ := c["name"].(string)
				args, _ := c["input"].(map[string]any)
				p.step()
				p.pending[tid] = true
				p.emit(Event{Type: "tool_start", Tool: claudeTool(name), ToolID: tid, Args: args})
			}
		}
		if u, _ := msg["usage"].(map[string]any); u != nil {
			total := num(u["input_tokens"]) + num(u["output_tokens"]) + num(u["cache_read_input_tokens"]) + num(u["cache_creation_input_tokens"])
			if total > 0 {
				p.emit(Event{Type: "context", ContextTokens: total})
			}
		}
	case "user":
		if parent, _ := m["parent_tool_use_id"].(string); parent != "" {
			return
		}
		msg, _ := m["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			c, _ := c.(map[string]any)
			if c["type"] != "tool_result" {
				continue
			}
			tid, _ := c["tool_use_id"].(string)
			isErr, _ := c["is_error"].(bool)
			delete(p.pending, tid)
			p.emit(Event{Type: "tool_end", ToolID: tid, Text: clip(contentText(c["content"])), IsError: isErr})
		}
		if len(p.pending) == 0 {
			p.endStep()
		}
	case "result":
		if s, _ := m["session_id"].(string); s != "" {
			p.res.session = s
		}
		if u, _ := m["usage"].(map[string]any); u != nil {
			p.res.usage = ai.Usage{Input: num(u["input_tokens"]), Output: num(u["output_tokens"]), CacheRead: num(u["cache_read_input_tokens"]), CacheWrite: num(u["cache_creation_input_tokens"])}
		}
		if isErr, _ := m["is_error"].(bool); isErr {
			msg, _ := m["result"].(string)
			if msg == "" {
				msg, _ = m["subtype"].(string)
			}
			p.res.errMsg = msg
		}
	}
}

func (p *cliParser) codex(m map[string]any) {
	switch m["type"] {
	case "thread.started":
		p.res.session, _ = m["thread_id"].(string)
	case "turn.completed":
		if u, _ := m["usage"].(map[string]any); u != nil {
			cached := num(u["cached_input_tokens"])
			p.res.usage = ai.Usage{Input: num(u["input_tokens"]) - cached, Output: num(u["output_tokens"]), CacheRead: cached}
			p.emit(Event{Type: "context", ContextTokens: num(u["input_tokens"]) + num(u["output_tokens"])})
		}
	case "turn.failed":
		e, _ := m["error"].(map[string]any)
		p.res.errMsg, _ = e["message"].(string)
	case "error":
		if msg, _ := m["message"].(string); msg != "" && p.res.errMsg == "" {
			p.res.errMsg = msg
		}
	case "item.started", "item.completed":
		item, _ := m["item"].(map[string]any)
		id, _ := item["id"].(string)
		done := m["type"] == "item.completed"
		switch item["type"] {
		case "agent_message":
			if t, _ := item["text"].(string); done && t != "" {
				p.step()
				p.emit(Event{Type: "text", Text: t})
			}
		case "reasoning":
			if t, _ := item["text"].(string); done && t != "" {
				p.step()
				p.emit(Event{Type: "thinking", Text: t})
			}
		case "command_execution":
			cmd, _ := item["command"].(string)
			p.tool(id, done, "bash", map[string]any{"command": unwrapShell(cmd)}, func() (string, bool) {
				out, _ := item["aggregated_output"].(string)
				code, hasCode := item["exit_code"].(float64)
				return out, item["status"] == "failed" || (hasCode && code != 0)
			})
		case "mcp_tool_call":
			server, _ := item["server"].(string)
			tool, _ := item["tool"].(string)
			name := tool
			if server != "shuttle" {
				name = "mcp__" + server + "__" + tool
			}
			args, _ := item["arguments"].(map[string]any)
			p.tool(id, done, name, args, func() (string, bool) {
				if e, _ := item["error"].(map[string]any); e != nil {
					msg, _ := e["message"].(string)
					return msg, true
				}
				r, _ := item["result"].(map[string]any)
				return contentText(r["content"]), item["status"] == "failed"
			})
		case "file_change":
			var paths []string
			changes, _ := item["changes"].([]any)
			for _, c := range changes {
				c, _ := c.(map[string]any)
				if s, _ := c["path"].(string); s != "" {
					paths = append(paths, s)
				}
			}
			p.tool(id, done, "edit", map[string]any{"path": strings.Join(paths, ", ")}, func() (string, bool) {
				return strings.Join(paths, "\n"), item["status"] == "failed"
			})
		case "web_search":
			q, _ := item["query"].(string)
			p.tool(id, done, "web_search", map[string]any{"query": q}, func() (string, bool) { return q, false })
		}
	}
}

// tool：Codex 的一项工具调用。只给了完成事件的（file_change）补一个开始。
func (p *cliParser) tool(id string, done bool, name string, args map[string]any, result func() (string, bool)) {
	if !p.pending[id] {
		p.step()
		p.pending[id] = true
		p.emit(Event{Type: "tool_start", Tool: name, ToolID: id, Args: args})
	}
	if done {
		out, isErr := result()
		delete(p.pending, id)
		p.emit(Event{Type: "tool_end", Tool: name, ToolID: id, Text: clip(out), IsError: isErr})
		if len(p.pending) == 0 {
			p.endStep()
		}
	}
}

// unwrapShell：Codex 的命令是「/bin/zsh -lc '…'」，界面只显示里面那段。
func unwrapShell(cmd string) string {
	for _, pre := range []string{"/bin/zsh -lc ", "/bin/bash -lc ", "bash -lc ", "zsh -lc "} {
		if rest, ok := strings.CutPrefix(cmd, pre); ok {
			if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
				rest = strings.ReplaceAll(rest[1:len(rest)-1], `'\''`, `'`)
			}
			return rest
		}
	}
	return cmd
}

func contentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, x := range c {
			if m, ok := x.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func clip(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "\n…"
	}
	return s
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// cliNote 是给外部 agent 多加的几句：工具名不一样、skill 在哪。
func cliNote(engine string) string {
	tools := "Annulo 的工具（db_query、db_aggregate、request_user_input、page_errors 等）在名为 shuttle 的 MCP 服务器里"
	if engine == EngineClaude {
		tools += "（工具名形如 mcp__shuttle__db_query）"
	}
	return "\n\n## 运行环境\n\n你是通过 " + map[string]string{EngineClaude: "Claude Code", EngineCodex: "Codex"}[engine] + " 运行的 Annulo 助手。" + tools + "，上面提到这些工具时就用它们。" +
		"request_user_input 调用后这一轮就结束，不要再接着输出。Annulo 管理的 skill 已经链接进工作目录的 " +
		map[string]string{EngineClaude: ".claude/skills", EngineCodex: ".agents/skills"}[engine] + "（软链，不要改、不要提交），按你平时用 skill 的方式用它们。"
}

// linkSkills 把 skill 软链进项目的 .claude/skills 或 .agents/skills，并让 git、creght 都忽略这个目录。
// 每轮重做一遍：skill 装了、停用了，下一轮就跟着变。
func linkSkills(cwd, engine string, skills []skillLink) {
	rel := ".agents"
	if engine == EngineClaude {
		rel = ".claude"
	}
	dir := filepath.Join(cwd, rel, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	want := map[string]string{}
	for _, s := range skills {
		want[s.name] = s.dir
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if target, err := os.Readlink(p); err == nil && target == want[e.Name()] {
			delete(want, e.Name())
			continue
		}
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(p) // 只删自己建的软链，用户自己放的目录不动
		}
	}
	for name, target := range want {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil && runtime.GOOS == "windows" {
			copyDir(target, filepath.Join(dir, name)) // Windows 没开发者模式建不了软链，复制一份
		}
	}
	ignoreLine(filepath.Join(cwd, ".git", "info", "exclude"), "/"+rel+"/")
	ignoreLine(filepath.Join(cwd, ".creghtignore"), rel+"/")
}

type skillLink struct{ name, dir string }

func (a *Agent) skillLinks() []skillLink {
	var out []skillLink
	for _, s := range *a.sessionSkills() {
		out = append(out, skillLink{s.Name, s.BaseDir})
	}
	return out
}

func ignoreLine(file, line string) {
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return
		}
	}
	if _, err := os.Stat(filepath.Dir(file)); err != nil {
		return
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	os.WriteFile(file, []byte(s+line+"\n"), 0o644)
}

// transcript 把界面历史写成一段文字：对话中途换成外部 agent（或换了另一个），新会话不知道前面聊了什么，带上它。
func transcript(raw []json.RawMessage) string {
	msgs := historyFromUI(raw, &ai.Model{})
	var sb strings.Builder
	for _, m := range msgs {
		var role string
		var content ai.ContentList
		switch v := m.(type) {
		case ai.UserMessage:
			role, content = "用户", v.Content
		case ai.AssistantMessage:
			role, content = "助手", v.Content
		default:
			continue
		}
		for _, c := range content {
			if t, ok := c.(ai.TextContent); ok {
				fmt.Fprintf(&sb, "%s：%s\n\n", role, strings.TrimSpace(t.Text))
			}
		}
	}
	s := sb.String()
	if r := []rune(s); len(r) > 30000 {
		s = "…\n" + string(r[len(r)-30000:])
	}
	return s
}

// runCLI 是 Run 在外部 agent 上的实现。调用方没拿 a.mu。
func (a *Agent) runCLI(ctx context.Context, chatID, prompt string, images []string, l config.LLM, emit func(Event)) (RunStats, error) {
	start := time.Now()
	st := RunStats{Model: l.Label(), ContextWindow: l.Window()}
	bin := ""
	for _, e := range cliEngines {
		if e.id == l.Provider {
			bin = findCLI(e.bin)
			if bin == "" {
				return st, i18n.Errorf("本机没找到 %s 命令：装好 %s 并登录后再试，或者在模型菜单里换一个", "The %s command wasn't found on this computer: install and sign in to %s, or pick another model", e.bin, e.name)
			}
		}
	}
	a.mu.Lock()
	if c := a.chats[chatID]; c != nil && c.running {
		a.mu.Unlock()
		return st, ErrBusy
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &chatSession{cli: true, cancel: cancel, running: true}
	a.chats[chatID] = c
	delete(a.waiting, chatID)
	cwd := a.cwd
	system := a.systemPrompt() + cliNote(l.Provider)
	thinking := clampCLIThinking(a.thinkingFor(chatID)) // 本机 agent 只有 低 / 中 / 高
	links := a.skillLinks()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		c.running, c.used = false, time.Now()
		a.evictIdle()
		a.mu.Unlock()
	}()

	orig := prompt
	var session string
	if ch, err := a.LoadChat(chatID); err == nil {
		if ch.CLIEngine == l.Provider {
			session = ch.CLISession
		}
		if session == "" && len(ch.Messages) > 1 {
			if t := transcript(ch.Messages[:len(ch.Messages)-1]); t != "" {
				prompt = "（这段对话前面的内容，供你接着做）\n\n" + t + "---\n\n用户现在说：\n" + prompt
			}
		}
	}
	if session == "" && l.Provider == EngineCodex {
		prompt = "<shuttle_instructions>\n" + system + "\n</shuttle_instructions>\n\n" + prompt
	}
	if cwd != "" {
		linkSkills(cwd, l.Provider, links)
	} else {
		cwd = os.TempDir()
	}
	model := ""
	if l.Model != l.Provider {
		model = l.Model
	}
	r := cliRun{engine: l.Provider, bin: bin, cwd: cwd, system: system, prompt: prompt, images: images, session: session, model: model, thinking: thinking,
		chatID: chatID, extraDirs: []string{a.installedDir()}}
	if a.MCPURL != "" {
		r.mcpURL, r.mcpToken = a.MCPURL, a.mcpToken
	}
	window := l.Window()
	res, err := r.exec(runCtx, func(e Event) {
		if e.Type == "context" {
			e.ContextWindow = window
			st.ContextTokens = e.ContextTokens
		}
		if e.Type == "tool_end" {
			a.noteToolEnd(chatID, c, e)
		}
		emit(e)
	})
	// resume 的会话丢了（Codex / Claude 清理了旧会话）：重新开一个，带上前面的对话
	if err != nil && session != "" && res.session == "" && ctx.Err() == nil && strings.Contains(strings.ToLower(err.Error()), "session") {
		log.Printf("外部 agent 会话 %s 接不上，新开一个：%v", session, err)
		a.setCLISession(chatID, l.Provider, "")
		return a.runCLI(ctx, chatID, orig, images, l, emit)
	}
	if res.session != "" && res.session != session {
		a.setCLISession(chatID, l.Provider, res.session)
	}
	if res.model != "" {
		st.Model = l.Label() + " · " + res.model
		if strings.Contains(res.model, "[1m]") {
			st.ContextWindow = 1000000
		}
	}
	st.DurationMs = time.Since(start).Milliseconds()
	st.InputTokens, st.OutputTokens, st.CacheRead, st.CacheWrite = res.usage.Input, res.usage.Output, res.usage.CacheRead, res.usage.CacheWrite
	a.recordUsage(chatID, &ai.AssistantMessage{Model: l.Label(), Usage: res.usage})
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || c.aborted {
		return st, ErrAborted
	}
	return st, err
}

// noteToolEnd：request_user_input 显示了问卷，这段对话标成在等用户回答（和 pi 的路径一样）。
func (a *Agent) noteToolEnd(chatID string, c *chatSession, e Event) {
	if !strings.Contains(e.Text, `"waiting_for_user"`) || e.IsError {
		return
	}
	a.mu.Lock()
	if a.waiting == nil {
		a.waiting = map[string]bool{}
	}
	a.waiting[chatID] = true
	a.mu.Unlock()
}

// setCLISession 记下这段对话在外部 agent 那边的会话 id（追加一行 meta）。
func (a *Agent) setCLISession(chatID, engine, session string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.LoadChat(chatID)
	if err != nil {
		return
	}
	ts := c.UpdatedAt
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	if session == "" {
		session = "-" // 清掉
	}
	a.appendLines(chatID, chatLine{Type: "meta", Timestamp: ts, CLIEngine: engine, CLISession: session})
}

// cliComplete 用外部 agent 答一次（起标题、本机函数的 ctx.llm）：不带工具、不留会话。
func cliComplete(ctx context.Context, engine, system, prompt string) (string, error) {
	bin := ""
	for _, e := range cliEngines {
		if e.id == engine {
			bin = findCLI(e.bin)
		}
	}
	if bin == "" {
		return "", i18n.Errorf("本机没找到 %s", "%s wasn't found on this computer", engine)
	}
	var args []string
	switch engine {
	case EngineClaude:
		args = []string{"-p", "--output-format", "json", "--tools", "", "--strict-mcp-config", "--no-session-persistence", "--effort", "low"}
		if system != "" {
			args = append(args, "--system-prompt", system)
		}
	case EngineCodex:
		args = []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-c", "model_reasoning_effort=\"low\"", "-"}
		if system != "" {
			prompt = system + "\n\n" + prompt
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = cliEnv()
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = 3 * time.Second
	localcmd.SetProcGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s：%s", engine, strings.TrimSpace(string(ee.Stderr)))
		}
		if len(out) == 0 {
			return "", err
		}
	}
	if engine == EngineClaude {
		var r struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal(out, &r) != nil {
			return "", fmt.Errorf("claude 的输出看不懂：%.200s", out)
		}
		if r.IsError {
			return "", errors.New(r.Result)
		}
		return strings.TrimSpace(r.Result), nil
	}
	var last string
	for _, l := range strings.Split(string(out), "\n") {
		var m struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.Type == "item.completed" && m.Item.Type == "agent_message" {
			last = m.Item.Text
		}
	}
	return strings.TrimSpace(last), nil
}

// LocalAgent 是设置页上的一个外部 agent：装没装、在哪、怎么装。
type LocalAgent struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Install string `json:"install"`
	// Models 是它支持的模型（第一个是「默认」，用它自己的设置）
	Models []LocalAgentModel `json:"models,omitempty"`
}

type LocalAgentModel struct {
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
}

// LocalAgents 列出支持的外部 agent（装了的带 Path）。
func LocalAgents() []LocalAgent {
	install := map[string]string{EngineClaude: "curl -fsSL https://claude.ai/install.sh | bash", EngineCodex: "npm install -g @openai/codex"}
	var out []LocalAgent
	for _, e := range cliEngines {
		a := LocalAgent{ID: e.id, ModelID: cliPrefix + e.id, Name: e.name, Path: findCLI(e.bin), Install: install[e.id]}
		if a.Path != "" {
			dn := i18n.T("默认", "Default")
			if d := engineDefault(e.id); d != "" {
				dn += paren(d)
			}
			a.Models = []LocalAgentModel{{ModelID: a.ModelID, Name: dn}}
			for _, m := range engineModels(e.id) {
				a.Models = append(a.Models, LocalAgentModel{ModelID: cliPrefix + e.id + "/" + m.id, Name: m.name})
			}
		}
		out = append(out, a)
	}
	return out
}
