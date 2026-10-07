package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// 本机函数和页面用用户在 设置 → 模型 里配好的服务商：ctx.llm.providers() 列出服务商和模型（不含 key），
// ctx.llm.fetch(服务商, 路径, init) 像 fetch 一样发请求，Shuttle 补上地址和鉴权。
// Shuttle 不认识具体接口（Responses、联网搜索…），请求体、怎么解析由函数自己写。

// llmProviders：设置 → 模型 里的服务商（creght 平台 + 自己加的 + 本机装了的 Claude Code / Codex），每个带它的模型。
// kind 是 api（能用 ctx.llm.fetch 发请求）或 cli（本机 agent：助手能用，没有接口地址，ctx.llm.fetch 不能调）。
func (s *Server) llmProviders(ctx context.Context) []map[string]any {
	_, cmErr := s.agent.RefreshCreghtModels(ctx, false)
	st := s.agent.ModelSettings() // 只有打开的服务商的模型
	byProvider := map[string][]map[string]any{}
	cliNames := map[string]string{} // 本机装了的 agent：服务商 id → 名字
	for _, m := range st.Models {
		if agent.IsCLIProvider(m.Provider) {
			cliNames[m.Provider] = agent.CLIName(m.Provider)
		}
		// web_search：creght 平台标了能联网搜索的模型；自己加的服务商不知道，都是 false
		byProvider[m.Provider] = append(byProvider[m.Provider], map[string]any{"id": m.ID, "model": m.Model, "name": m.Label(), "api": m.Api, "web_search": m.WebSearch})
	}
	disabled := s.agent.DisabledProviders()
	off := func(id string) bool {
		for _, d := range disabled {
			if d == id {
				return true
			}
		}
		return false
	}
	view := func(id, name, api, base string, builtin bool, err error) map[string]any {
		models := byProvider[id]
		if models == nil {
			models = []map[string]any{}
		}
		v := map[string]any{"id": id, "name": name, "kind": "api", "api": api, "base_url": base, "builtin": builtin, "models": models, "enabled": !off(id), "ready": err == nil && !off(id), "error": ""}
		if err != nil {
			v["error"] = err.Error()
		}
		return v
	}
	out := []map[string]any{}
	cp, err := s.agent.ProviderEndpoint(config.CreghtProvider)
	if err == nil {
		err = cmErr
	}
	out = append(out, view(config.CreghtProvider, i18n.T("creght 平台", "creght platform"), "openai-completions", cp.BaseURL, true, err))
	for _, p := range st.Providers {
		_, err := s.agent.ProviderEndpoint(p.ID)
		if err == nil && p.Key() == "" {
			err = i18n.New("缺 API Key", "No API key")
		}
		out = append(out, view(p.ID, p.Name, p.Api, p.BaseURL, false, err))
	}
	for _, id := range []string{agent.EngineClaude, agent.EngineCodex} {
		if name, ok := cliNames[id]; ok {
			v := view(id, name, "", "", false, nil)
			v["kind"] = "cli"
			out = append(out, v)
		}
	}
	return out
}

// llmFetch：path 接在服务商的地址后面（和助手调这个服务商时一样：OpenAI 兼容的地址一般到 /v1，接 /responses、/chat/completions；
// Anthropic 的地址是域名，接 /v1/messages）。鉴权由 Shuttle 加，函数传的同名头会被覆盖；只能发到这个服务商的地址下。
func (s *Server) llmFetch(ctx context.Context, provider string, r localfn.FetchRequest) (*localfn.FetchResponse, error) {
	if agent.IsCLIProvider(provider) {
		return nil, i18n.Errorf("「%s」是本机的 agent，没有接口地址，ctx.llm.fetch 用不了：换一个 kind 是 api 的服务商", "\"%s\" is a local agent with no API endpoint, so ctx.llm.fetch can't use it: pick a provider whose kind is api", agent.CLIName(provider))
	}
	p, err := s.agent.ProviderEndpoint(provider)
	if err != nil {
		return nil, err
	}
	path := r.URL
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "://") || strings.Contains(path, "..") || strings.Contains(path, "\\") {
		return nil, i18n.Errorf("ctx.llm.fetch 的路径要以 / 开头，接在服务商地址后面（比如 '/responses'）：%q", "ctx.llm.fetch's path must start with / and is appended to the provider's URL (e.g. '/responses'): %q", path)
	}
	base := strings.TrimRight(p.BaseURL, "/")
	r.URL = base + path
	key := p.Key()
	if key == "" {
		return nil, i18n.Errorf("服务商「%s」缺 API Key：到 设置 → 模型 里填", "Provider \"%s\" has no API key: add it in Settings → Models", p.Name)
	}
	h := map[string]string{}
	for k, v := range r.Headers {
		switch strings.ToLower(k) {
		case "authorization", "x-api-key":
		default:
			h[k] = v
		}
	}
	if p.Api == "anthropic-messages" && provider != config.CreghtProvider {
		h["x-api-key"] = key
	} else {
		h["authorization"] = "Bearer " + key
	}
	r.Headers = h
	return localFetch(ctx, r)
}

// apiLLMProviders：GET local/llm/providers，页面选服务商和模型用（不含 key）
func (s *Server) apiLLMProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"providers": s.llmProviders(r.Context())})
}
