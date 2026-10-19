package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// llmAsk 是带 provider / model / web_search 的 ctx.llm（localfn.LLMAsk）：
// 不搜索时就是用指定的模型答一次；搜索时本机 agent 用它自己的搜索（agent.CLIWebSearch），
// 接口服务商走 OpenAI Responses 的 web_search 工具（OpenAI 官方、兼容的代理、creght 平台标了 web_search 的模型）。
func (s *Server) llmAsk(ctx context.Context, q localfn.LLMAsk) (*localfn.LLMAnswer, error) {
	m, err := s.agent.ModelFor(ctx, q.Provider, q.Model)
	if err != nil {
		return nil, err
	}
	if !q.WebSearch {
		text, err := s.agent.CompleteWith(ctx, m, q.System, q.Prompt)
		if err != nil {
			return nil, err
		}
		return &localfn.LLMAnswer{Text: text, Sources: []localfn.LLMSource{}, Searches: []localfn.LLMSearch{}, Model: m.ID}, nil
	}
	var a *agent.WebAnswer
	if agent.IsCLIProvider(m.Provider) {
		a, err = s.agent.CLIWebSearch(ctx, m, q.System, q.Prompt)
	} else {
		a, err = s.responsesSearch(ctx, m, q.System, q.Prompt)
	}
	if err != nil {
		return nil, err
	}
	out := &localfn.LLMAnswer{Text: a.Text, Tokens: a.Tokens, Model: m.ID, Sources: []localfn.LLMSource{}, Searches: []localfn.LLMSearch{}}
	for _, x := range a.Sources {
		out.Sources = append(out.Sources, localfn.LLMSource{URL: x.URL, Title: x.Title})
	}
	for _, x := range a.Searches {
		out.Searches = append(out.Searches, localfn.LLMSearch{Type: x.Type, Queries: x.Queries, URL: x.URL, Sources: x.Sources})
	}
	return out, nil
}

// responsesSearch：POST <服务商>/responses，带 web_search 工具，流式读。
// 写法照 ChatGPT 账号登录的 Codex 接口也认的来：input 是消息列表、必须有 instructions、stream；官方 API 也认。
// 有的接口不给引用注释，所以默认的 instructions 让它把来源写成 Markdown 链接，正文里的链接也算引用。
func (s *Server) responsesSearch(ctx context.Context, m config.LLM, system, prompt string) (*agent.WebAnswer, error) {
	if m.Provider != config.CreghtProvider && m.Api == "anthropic-messages" {
		return nil, i18n.Errorf("服务商「%s」走 Anthropic 接口，联网搜索要用支持 OpenAI Responses 的服务商，或者本机的 Claude Code / Codex", "Provider \"%s\" uses the Anthropic API; web search needs a provider with the OpenAI Responses API, or the local Claude Code / Codex", m.Provider)
	}
	if system == "" {
		system = "Answer the user's question. When you use web search, cite each source inline as a Markdown link to the page you used."
	}
	body, _ := json.Marshal(map[string]any{
		"model":        m.Model,
		"instructions": system,
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}},
		"tools":        []any{map[string]any{"type": "web_search"}},
		"include":      []string{"web_search_call.action.sources"},
		"store":        false,
		"stream":       true,
	})
	var resp *localfn.FetchResponse
	for attempt := 0; ; attempt++ {
		r, err := s.llmFetch(ctx, m.Provider, localfn.FetchRequest{URL: "/responses", Method: "POST", Headers: map[string]string{"content-type": "application/json"}, Body: string(body), Stream: true})
		if err != nil {
			return nil, err
		}
		if r.Status >= 200 && r.Status < 300 {
			resp = r
			break
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Stream, 64*1024))
		r.Stream.Close()
		if attempt == 0 && (r.Status == 429 || r.Status >= 500) {
			select {
			case <-time.After(4 * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		msg := strings.TrimSpace(string(raw))
		var j struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Detail  string `json:"detail"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &j) == nil {
			for _, x := range []string{j.Error.Message, j.Detail, j.Message} {
				if x != "" {
					msg = x
					break
				}
			}
		}
		switch r.Status {
		case 401:
			return nil, i18n.Errorf("服务商「%s」的 key 无效（401），到 设置 → 模型 里改", "Provider \"%s\" rejected the key (401); fix it in Settings → Models", m.Provider)
		case 404:
			return nil, i18n.Errorf("服务商「%s」没有 /responses 接口（不支持 OpenAI Responses）：%.300s", "Provider \"%s\" has no /responses endpoint (no OpenAI Responses support): %.300s", m.Provider, msg)
		}
		return nil, fmt.Errorf("%s %d：%.300s", m.Provider, r.Status, msg)
	}
	defer resp.Stream.Close()

	type content struct {
		Text        string `json:"text"`
		Annotations []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"annotations"`
	}
	type item struct {
		Type    string    `json:"type"`
		Content []content `json:"content"`
		Action  struct {
			Type    string   `json:"type"`
			Query   string   `json:"query"`
			Queries []string `json:"queries"`
			URL     string   `json:"url"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
		} `json:"action"`
	}
	type response struct {
		Output []item `json:"output"`
		Usage  struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var items []item
	var final *response
	var failure string
	sc := bufio.NewScanner(resp.Stream)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		d, ok := strings.CutPrefix(l, "data:")
		if !ok {
			continue
		}
		if d = strings.TrimSpace(d); d == "" || d == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string    `json:"type"`
			Item     *item     `json:"item"`
			Response *response `json:"response"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(d), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.done":
			if ev.Item != nil {
				items = append(items, *ev.Item)
			}
		case "response.completed", "response.incomplete":
			final = ev.Response
		case "response.failed":
			failure = "response.failed"
			if ev.Response != nil && ev.Response.Error.Message != "" {
				failure = ev.Response.Error.Message
			}
		case "error":
			failure = ev.Error.Message
			if failure == "" {
				failure = ev.Message
			}
		}
	}
	if failure != "" {
		return nil, fmt.Errorf("%s：%s", m.Provider, failure)
	}
	output := items
	if final != nil && len(final.Output) > 0 {
		output = final.Output
	}
	ans := &agent.WebAnswer{}
	var texts []string
	var cited []agent.WebSource
	for _, it := range output {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				if c.Text != "" {
					texts = append(texts, c.Text)
				}
				for _, a := range c.Annotations {
					if a.URL != "" {
						cited = append(cited, agent.WebSource{URL: a.URL, Title: a.Title})
					}
				}
			}
		case "web_search_call":
			a := it.Action
			w := agent.WebSearch{Type: a.Type, URL: a.URL, Queries: []string{}, Sources: []string{}}
			if w.Type == "" {
				w.Type = "search"
			}
			seen := map[string]bool{}
			for _, q := range append(append([]string{}, a.Queries...), a.Query) {
				if q = strings.TrimSpace(q); q != "" && !seen[q] {
					seen[q] = true
					w.Queries = append(w.Queries, q)
				}
			}
			for _, x := range a.Sources {
				if x.URL != "" && len(w.Sources) < 15 {
					w.Sources = append(w.Sources, x.URL)
				}
			}
			ans.Searches = append(ans.Searches, w)
		}
	}
	ans.Text = strings.Join(texts, "\n")
	if ans.Text == "" && final == nil {
		if err := sc.Err(); err != nil {
			return nil, i18n.Errorf("回答没有读完（连接中途断了）：%v", "The answer didn't finish (the connection dropped): %v", err)
		}
		return nil, i18n.New("回答没有读完（连接中途断了）", "The answer didn't finish (the connection dropped)")
	}
	if final != nil {
		ans.Tokens = final.Usage.TotalTokens
	}
	ans.Sources = agent.SourcesInText(ans.Text, cited)
	return ans, nil
}
