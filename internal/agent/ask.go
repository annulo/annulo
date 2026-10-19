package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localcmd"
)

// ctx.llm 指定模型、联网搜索（本机函数，docs 见 internal/localfn LLMAsk）。
// 接口服务商的联网搜索走 OpenAI Responses，在 internal/server/llmask.go；这里是选模型和本机 agent 的那一半。

// ModelFor 找 ctx.llm 要用的模型：
// provider 空时 model 是模型 id（ctx.llm.providers() 里的 id），两个都空是助手当前的模型；
// 给了 provider 时 model 是这个服务商下的模型名，没登记的也照样用（服务商认不认由它自己说），空 = 这个服务商的第一个模型。
func (a *Agent) ModelFor(ctx context.Context, provider, model string) (config.LLM, error) {
	a.RefreshCreghtModels(ctx, false)
	a.mu.Lock()
	defer a.mu.Unlock()
	if provider == "" {
		if model == "" {
			return a.current(), nil
		}
		if m := a.byID(model, config.LLM{}); m.ID != "" {
			return m, nil
		}
		return config.LLM{}, i18n.Errorf("没有这个模型：%s（ctx.llm.providers() 里模型的 id）", "No such model: %s (use a model id from ctx.llm.providers())", model)
	}
	if a.disabled(provider) {
		return config.LLM{}, i18n.Errorf("服务商「%s」在 设置 → 模型 里关掉了", "Provider \"%s\" is turned off in Settings → Models", provider)
	}
	for _, m := range a.models() {
		if m.Provider == provider && (model == "" || m.Model == model) {
			return m, nil
		}
	}
	if IsCLIProvider(provider) {
		if findCLIEngine(provider) == "" {
			return config.LLM{}, i18n.Errorf("本机没找到 %s", "%s wasn't found on this computer", CLIName(provider))
		}
		id := cliPrefix + provider
		if model == "" || model == provider {
			model = provider
		} else {
			id += "/" + model
		}
		return config.LLM{ID: id, Name: CLIName(provider), Provider: provider, Model: model}, nil
	}
	if model == "" {
		return config.LLM{}, i18n.Errorf("服务商「%s」没有登记模型，要写明用哪个模型", "Provider \"%s\" has no models registered; say which model to use", provider)
	}
	if _, ok := a.cfg.ProviderByID(provider); !ok && provider != config.CreghtProvider {
		return config.LLM{}, i18n.Errorf("没有这个服务商：%s（到 设置 → 模型 里添加）", "No such provider: %s (add it in Settings → Models)", provider)
	}
	return config.LLM{ID: provider + ":" + model, Name: model, Provider: provider, Model: model}, nil
}

// CompleteWith 用指定的模型答一次（ctx.llm 带了 provider / model）。
func (a *Agent) CompleteWith(ctx context.Context, m config.LLM, system, prompt string) (string, error) {
	return a.completeLLM(ctx, m, system, prompt, "")
}

// cliModel 是传给本机 agent 的 --model：模型名就是 agent 自己（默认）时不传，用它自己的设置。
func cliModel(l config.LLM) string {
	if l.Model == "" || l.Model == l.Provider {
		return ""
	}
	return l.Model
}

func findCLIEngine(engine string) string {
	for _, e := range cliEngines {
		if e.id == engine {
			return findCLI(e.bin)
		}
	}
	return ""
}

// WebSource、WebSearch、WebAnswer 是联网搜索的结果，字段和 localfn.LLMAnswer 一一对应。
type WebSource struct {
	URL   string
	Title string
}

type WebSearch struct {
	Type    string
	Queries []string
	URL     string
	Sources []string
}

type WebAnswer struct {
	Text     string
	Sources  []WebSource
	Searches []WebSearch
	Tokens   int
}

// CLIWebSearch 让本机的 Claude Code / Codex 联网搜索后回答一次：
// Claude Code 只放行 WebSearch、WebFetch；Codex 开 --search（OpenAI 的 web_search），只读沙箱。
// 搜索过程从它们的 JSON 事件里取，引用的网址从回答正文的链接里取（两边都不给引用注释）。
func (a *Agent) CLIWebSearch(ctx context.Context, m config.LLM, system, prompt string) (*WebAnswer, error) {
	bin := findCLIEngine(m.Provider)
	if bin == "" {
		return nil, i18n.Errorf("本机没找到 %s", "%s wasn't found on this computer", CLIName(m.Provider))
	}
	model := cliModel(m)
	var args []string
	switch m.Provider {
	case EngineClaude:
		args = []string{"-p", "--output-format", "stream-json", "--verbose", "--tools", "WebSearch,WebFetch", "--allowedTools", "WebSearch,WebFetch",
			"--strict-mcp-config", "--no-session-persistence", "--effort", "low"}
		if system != "" {
			args = append(args, "--system-prompt", system)
		}
		if model != "" {
			args = append(args, "--model", model)
		}
	case EngineCodex:
		args = []string{"--search", "exec", "--json", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-c", "model_reasoning_effort=\"low\""}
		if model != "" {
			args = append(args, "-m", model)
		}
		args = append(args, "-")
		if system != "" {
			prompt = system + "\n\n" + prompt
		}
	default:
		return nil, fmt.Errorf("%s 不是本机 agent", m.Provider)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = cliEnv()
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = 3 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	localcmd.SetProcGroup(cmd)
	out, err := cmd.Output()
	var ans *WebAnswer
	var perr error
	if m.Provider == EngineClaude {
		ans, perr = parseClaudeSearch(out)
	} else {
		ans, perr = parseCodexSearch(out)
	}
	if err != nil && (ans == nil || ans.Text == "") {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s：%s", CLIName(m.Provider), lastLines(msg, 3))
		}
		if perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("%s：%w", CLIName(m.Provider), err)
	}
	if perr != nil {
		return nil, perr
	}
	ans.Sources = SourcesInText(ans.Text, nil)
	return ans, nil
}

func lastLines(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func eachLine(out []byte, f func(line []byte)) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			f(l)
		}
	}
}

// parseClaudeSearch 读 claude -p --output-format stream-json：
// WebSearch 的结果在 user 消息的 tool_use_result（query、results[].content[].url），WebFetch 打开的网页在 assistant 的 tool_use，
// 回答和用量在最后的 result。
func parseClaudeSearch(out []byte) (*WebAnswer, error) {
	ans := &WebAnswer{}
	done := false
	var failure string
	eachLine(out, func(l []byte) {
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string `json:"type"`
					Name  string `json:"name"`
					Input struct {
						URL string `json:"url"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"tool_use_result"`
			Result        string          `json:"result"`
			IsError       bool            `json:"is_error"`
			Usage         struct {
				InputTokens  int `json:"input_tokens"`
				CacheCreate  int `json:"cache_creation_input_tokens"`
				CacheRead    int `json:"cache_read_input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(l, &ev) != nil {
			return
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" && c.Name == "WebFetch" && c.Input.URL != "" {
					ans.Searches = append(ans.Searches, WebSearch{Type: "open_page", URL: c.Input.URL, Queries: []string{}, Sources: []string{}})
				}
			}
		case "user":
			var r struct {
				Query   string `json:"query"`
				Results []struct {
					Content []struct {
						URL string `json:"url"`
					} `json:"content"`
				} `json:"results"`
			}
			if len(ev.ToolUseResult) == 0 || json.Unmarshal(ev.ToolUseResult, &r) != nil || r.Query == "" {
				return
			}
			s := WebSearch{Type: "search", Queries: []string{r.Query}, Sources: []string{}}
			for _, x := range r.Results {
				for _, c := range x.Content {
					if c.URL != "" && len(s.Sources) < 15 {
						s.Sources = append(s.Sources, c.URL)
					}
				}
			}
			ans.Searches = append(ans.Searches, s)
		case "result":
			done = true
			if ev.IsError {
				failure = ev.Result
				return
			}
			ans.Text = strings.TrimSpace(ev.Result)
			u := ev.Usage
			ans.Tokens = u.InputTokens + u.CacheCreate + u.CacheRead + u.OutputTokens
		}
	})
	if failure != "" {
		return nil, fmt.Errorf("Claude Code：%s", failure)
	}
	if !done {
		return nil, i18n.Errorf("Claude Code 没有答完：%.300s", "Claude Code didn't finish answering: %.300s", lastLines(string(out), 3))
	}
	return ans, nil
}

// parseCodexSearch 读 codex exec --json：web_search 项是一次搜索（action.queries、results[].url），
// 回答是最后一条 agent_message（前面的是「我去查一下」这类过程话），用量在 turn.completed。
func parseCodexSearch(out []byte) (*WebAnswer, error) {
	ans := &WebAnswer{}
	done := false
	var failure string
	eachLine(out, func(l []byte) {
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Query  string `json:"query"`
				Action struct {
					Type    string   `json:"type"`
					Query   string   `json:"query"`
					Queries []string `json:"queries"`
					URL     string   `json:"url"`
				} `json:"action"`
				Results []struct {
					URL string `json:"url"`
				} `json:"results"`
			} `json:"item"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(l, &ev) != nil {
			return
		}
		switch ev.Type {
		case "item.completed":
			switch ev.Item.Type {
			case "agent_message":
				ans.Text = strings.TrimSpace(ev.Item.Text)
			case "web_search":
				a := ev.Item.Action
				s := WebSearch{Type: a.Type, URL: a.URL, Queries: []string{}, Sources: []string{}}
				if s.Type == "" || s.Type == "other" {
					s.Type = "search"
				}
				seen := map[string]bool{}
				for _, q := range append(append([]string{}, a.Queries...), a.Query) {
					if q = strings.TrimSpace(q); q != "" && !seen[q] {
						seen[q] = true
						s.Queries = append(s.Queries, q)
					}
				}
				for _, r := range ev.Item.Results {
					if r.URL != "" && len(s.Sources) < 15 {
						s.Sources = append(s.Sources, r.URL)
					}
				}
				ans.Searches = append(ans.Searches, s)
			}
		case "turn.completed":
			done = true
			ans.Tokens = ev.Usage.InputTokens + ev.Usage.OutputTokens
		case "turn.failed":
			failure = ev.Error.Message
		case "error":
			if failure == "" {
				failure = ev.Message
			}
		}
	})
	if failure != "" && ans.Text == "" {
		return nil, fmt.Errorf("Codex：%s", failure)
	}
	if !done && ans.Text == "" {
		return nil, i18n.Errorf("Codex 没有答完：%.300s", "Codex didn't finish answering: %.300s", lastLines(string(out), 3))
	}
	return ans, nil
}

var (
	mdLink  = regexp.MustCompile(`\[([^\]\n]*)\]\((https?://[^\s)]+)\)`)
	bareURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `|)\]]+`)
)

// SourcesInText 合并引用：先放 known（接口给的引用注释），再加回答正文里写出来的链接（Markdown 链接带标题），按网址去重。
func SourcesInText(text string, known []WebSource) []WebSource {
	out := []WebSource{}
	seen := map[string]int{}
	add := func(u, title string) {
		u = strings.TrimRight(u, ".,;:!?。，；：！？*_")
		if u == "" {
			return
		}
		if i, ok := seen[u]; ok {
			if out[i].Title == "" {
				out[i].Title = title
			}
			return
		}
		seen[u] = len(out)
		out = append(out, WebSource{URL: u, Title: title})
	}
	for _, s := range known {
		add(s.URL, s.Title)
	}
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		add(m[2], strings.TrimSpace(m[1]))
	}
	for _, u := range bareURL.FindAllString(text, -1) {
		add(u, "")
	}
	return out
}
