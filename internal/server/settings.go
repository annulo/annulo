package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

// 模型设置：服务商（内置 creght + 用户自己加的）和模型（每个指定一个服务商），选一个当前用；
// 思考强度所有模型共用。API key 只进不出：GET 只告诉前端「有没有、尾号是什么」，
// 保存时 key 留空表示沿用原来的，改地址不用重新粘 key。

type providerView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Builtin   bool   `json:"builtin"`
	Api       string `json:"api"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	HasKey    bool   `json:"has_key"`  // 配置里直接存了 key
	KeyHint   string `json:"key_hint"` // key 的尾号
	Ready     bool   `json:"ready"`
	Error     string `json:"error"`
	Agent     bool   `json:"agent,omitempty"` // 本机的外部 agent（Claude Code / Codex），不能编辑
	// NeedsConnect：creght 平台没连上账号，界面给「连接」入口（设置 → 连接）
	NeedsConnect bool `json:"needs_connect,omitempty"`
	// Disabled：用户关掉了这个服务商，它的模型不出现在列表和选择菜单里（PUT settings/llm {provider, enabled}）
	Disabled bool `json:"disabled,omitempty"`
}

func keyHint(k string) string {
	if len(k) >= 8 {
		return "…" + k[len(k)-4:]
	}
	return ""
}

func viewProvider(p config.Provider) providerView {
	v := providerView{ID: p.ID, Name: p.Name, Api: p.Api, BaseURL: p.BaseURL, APIKeyEnv: p.APIKeyEnv, HasKey: p.APIKey != "", KeyHint: keyHint(p.Key())}
	switch {
	case p.BaseURL == "":
		v.Error = i18n.T("没填地址", "No URL")
	case p.Key() == "" && p.APIKeyEnv != "":
		v.Error = i18n.Tf("环境变量 %s 是空的", "Environment variable %s is empty", p.APIKeyEnv)
	case p.Key() == "":
		v.Error = i18n.T("缺 API Key", "No API key")
	}
	v.Ready = v.Error == ""
	return v
}

type modelView struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Label     string          `json:"label"`
	Provider  string          `json:"provider"`
	Builtin   bool            `json:"builtin"` // creght 平台的模型：列表来自平台，不能改
	Model     string          `json:"model"`
	Window    int             `json:"context_window"`
	Images    bool            `json:"images"`
	Reasoning bool            `json:"reasoning"`
	Pricing   *config.Pricing `json:"pricing,omitempty"` // 平台模型：每百万 token 积分
	Ready     bool            `json:"ready"`
	Error     string          `json:"error"`
	Agent     bool            `json:"agent,omitempty"` // 本机的外部 agent
}

func (s *Server) viewModel(m config.LLM) modelView {
	v := modelView{ID: m.ID, Name: m.Name, Label: m.Label(), Provider: m.Provider, Builtin: strings.HasPrefix(m.ID, config.CreghtProvider+":"),
		Model: m.Model, Window: m.ContextWindow, Images: m.Vision(), Reasoning: m.Reasoning, Pricing: m.Pricing, Agent: agent.IsCLIProvider(m.Provider)}
	_, err := s.agent.Resolve(m)
	v.Ready, v.Error = err == nil, errString(err)
	return v
}

func (s *Server) apiLLMGet(w http.ResponseWriter, r *http.Request) {
	// 平台模型列表：带 ?refresh=1 强制重拉，否则用缓存
	_, cmErr := s.agent.RefreshCreghtModels(r.Context(), r.URL.Query().Get("refresh") == "1")
	if r.URL.Query().Get("refresh") == "agents" {
		agent.RefreshCLIModels() // 设置页「本机 Agent」的刷新：重新问一遍 Claude Code / Codex 能用哪些模型
	}
	st := s.agent.ModelSettings()

	host := s.loginHost()
	_, tokErr := creght.ReadToken(host)
	builtin := providerView{ID: config.CreghtProvider, Name: i18n.T("creght 平台", "creght platform"), Builtin: true, Api: "openai-completions", Ready: tokErr == nil && cmErr == nil}
	switch {
	case tokErr != nil:
		builtin.Error = i18n.T("还没连接 creght", "creght isn't connected yet")
		builtin.NeedsConnect = true
	case cmErr != nil:
		builtin.Error = cmErr.Error()
	}
	var providers []providerView
	if tokErr == nil {
		providers = append(providers, builtin) // 连了 creght：平台模型排第一
	}
	for _, p := range st.Providers {
		providers = append(providers, viewProvider(p))
	}
	agents := agent.LocalAgents()
	for _, a := range agents {
		if a.Path != "" {
			providers = append(providers, providerView{ID: a.ID, Name: a.Name + i18n.T("（本机）", " (this computer)"), Agent: true, Ready: true})
		}
	}
	if tokErr != nil {
		providers = append(providers, builtin) // 没连：排在自己的和本机 agent 后面，点了引导去连接
	}
	disabled := s.agent.DisabledProviders()
	for i := range providers {
		providers[i].Disabled = slices.Contains(disabled, providers[i].ID)
	}
	models := make([]modelView, 0, len(st.Models))
	for _, m := range st.Models {
		models = append(models, s.viewModel(m))
	}
	autoTitle, titleModel := s.agent.TitleSettings()
	writeJSON(w, map[string]any{"providers": providers, "models": models, "agents": agents, "active": st.Active, "thinking": st.Thinking, "thinking_levels": config.ThinkingLevels,
		"auto_title": autoTitle, "title_model": titleModel})
}

// apiLLMUpdate：PUT settings/llm {active?, thinking?, auto_title?, title_model?, provider? + enabled?}，切换当前模型、改思考强度、开关服务商、
// 改自动起名（title_model 传 "" 表示跟对话用同一个模型）。下一条消息生效。
func (s *Server) apiLLMUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Active     string  `json:"active"`
		Thinking   string  `json:"thinking"`
		AutoTitle  *bool   `json:"auto_title"`
		TitleModel *string `json:"title_model"`
		// provider + enabled：打开或关掉一个服务商（creght 平台、本机 agent、自己加的）
		Provider string `json:"provider"`
		Enabled  *bool  `json:"enabled"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	if in.Active != "" {
		if err := s.agent.SetActiveModel(in.Active); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Thinking != "" {
		if err := s.agent.SetThinking(in.Thinking); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Provider != "" && in.Enabled != nil {
		if err := s.agent.SetProviderEnabled(in.Provider, *in.Enabled); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.AutoTitle != nil || in.TitleModel != nil {
		if err := s.agent.SetTitleSettings(in.AutoTitle, in.TitleModel); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	s.apiLLMGet(w, r)
}

type modelInput struct {
	ID        string `json:"id"` // 空：新增
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Window    int    `json:"context_window"`
	Images    bool   `json:"images"`
	Reasoning bool   `json:"reasoning"`
}

func (in modelInput) llm() (config.LLM, error) {
	images := in.Images
	m := config.LLM{ID: strings.TrimSpace(in.ID), Name: strings.TrimSpace(in.Name), Provider: strings.TrimSpace(in.Provider),
		Model: strings.TrimSpace(in.Model), ContextWindow: max(in.Window, 0), Images: &images, Reasoning: in.Reasoning}
	if m.Model == "" {
		return m, i18n.New("填一下模型 id", "Enter a model id")
	}
	return m, nil
}

func readJSON(r *http.Request, v any) error {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err := json.Unmarshal(b, v); err != nil {
		return i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON")
	}
	return nil
}

// apiLLMSaveModel：POST settings/llm/models，新增（不带 id）或修改一个自己加的模型。
func (s *Server) apiLLMSaveModel(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	m, err := in.llm()
	if err == nil {
		m, err = s.agent.SaveModel(m)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, s.viewModel(m))
}

func (s *Server) apiLLMDeleteModel(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.agent.DeleteModel(id); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiLLMGet(w, r)
}

type providerInput struct {
	ID        string `json:"id"` // 空：新增
	Name      string `json:"name"`
	Api       string `json:"api"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`     // 留空：沿用原来的 key
	APIKeyEnv string `json:"api_key_env"` // 填了就改用环境变量，并清掉存着的 key
}

var validApis = map[string]bool{"openai-completions": true, "openai-responses": true, "anthropic-messages": true}

// provider 把输入转成服务商配置；key 留空时沿用已保存的那个服务商的 key。
func (s *Server) provider(in providerInput) (config.Provider, error) {
	p := config.Provider{ID: strings.TrimSpace(in.ID), Name: strings.TrimSpace(in.Name), Api: strings.TrimSpace(in.Api),
		BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")}
	if !validApis[p.Api] {
		return p, i18n.New("协议只支持 openai-completions / openai-responses / anthropic-messages", "Only openai-completions / openai-responses / anthropic-messages are supported")
	}
	if p.BaseURL == "" {
		return p, i18n.New("填一下 Base URL", "Enter a Base URL")
	}
	if p.Name == "" {
		p.Name = p.BaseURL
	}
	cur, _ := s.agent.Provider(p.ID)
	switch {
	case strings.TrimSpace(in.APIKey) != "":
		p.APIKey = strings.TrimSpace(in.APIKey)
	case strings.TrimSpace(in.APIKeyEnv) != "":
		p.APIKeyEnv = strings.TrimSpace(in.APIKeyEnv)
	default:
		p.APIKey, p.APIKeyEnv = cur.APIKey, cur.APIKeyEnv
	}
	if p.Key() == "" {
		if p.APIKeyEnv != "" {
			return p, i18n.Errorf("环境变量 %s 是空的：Annulo 读的是启动它的终端（或登录 shell）的环境变量", "Environment variable %s is empty: Annulo reads the environment of the terminal (or login shell) that started it", p.APIKeyEnv)
		}
		return p, i18n.New("填一下 API Key", "Enter an API key")
	}
	return p, nil
}

func (s *Server) apiLLMSaveProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.provider(in)
	if err == nil {
		p, err = s.agent.SaveProvider(p)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, viewProvider(p))
}

func (s *Server) apiLLMDeleteProvider(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.agent.DeleteProvider(id); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiLLMGet(w, r)
}

// apiLLMTest：POST settings/llm/test {model: {...}, provider?: {...}}。
// 带 provider 就用这份（还没保存的服务商），否则用模型指定的服务商。
func (s *Server) apiLLMTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model    modelInput     `json:"model"`
		Provider *providerInput `json:"provider"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	m, err := in.Model.llm()
	if err == nil {
		if in.Provider != nil {
			var p config.Provider
			if p, err = s.provider(*in.Provider); err == nil {
				m.Api, m.BaseURL, m.APIKey = p.Api, p.BaseURL, p.Key()
				m.Provider = ""
				err = m.Ready()
			}
		} else {
			m, err = s.agent.Resolve(m)
		}
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	text, err := agent.TestLLM(ctx, m)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "reply": text, "ms": time.Since(start).Milliseconds()})
}
