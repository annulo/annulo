package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

// 模型设置。模型 = 服务商 + 模型 id + 模型自己的属性（看图、思考、上下文窗口）。
//
// 服务商有两种：
//   - 内置的 creght：用登录的 creght 账号，按平台积分计费，不用配 key。它的模型从平台的
//     /models 接口拉，界面上直接出现，id 是 "creght:<模型 id>"；没选过模型时默认用它的第一个。
//   - 用户自己加的（DeepSeek、豆包、各家网关……），存在 config.json。
//
// 改动都是下一条消息生效：正在跑的这一轮不受影响，对话上下文也不丢。

// creghtLLMPath 是平台 OpenAI 兼容接口的前缀：<api host>/api/p/ai/llm/v1/chat/completions、/models。
const creghtLLMPath = "/api/p/ai/llm/v1"

// llmMaxRetries 是模型请求的自动重试次数。pi 在拿到响应头之前重试网络错误（断网、DNS 解析失败）
// 和 408/409/429/5xx，指数退避 0.5s、1s、2s、4s、8s、8s（有 Retry-After 按它的），6 次大约撑 23 秒，
// 够盖住切网络、休眠唤醒这类短暂断网。
const llmMaxRetries = 6

const creghtPrefix = config.CreghtProvider + ":"

// creghtApis 是平台模型能用的协议（pi 的协议名）。
var creghtApis = []string{"openai-completions", "anthropic-messages"}

// creghtModels 缓存平台的模型列表：列表不常变，每轮对话都去拉太慢。
type creghtModels struct {
	mu   sync.Mutex
	host string
	list []config.LLM
	err  error
	at   time.Time
}

// SetCreghtHost 设置 creght 服务商用哪个集群（跟着运营后台 / 登录走）。
func (a *Agent) SetCreghtHost(host string) {
	a.creght.mu.Lock()
	defer a.creght.mu.Unlock()
	if a.creght.host != host {
		a.creght.host, a.creght.list, a.creght.err, a.creght.at = host, nil, nil, time.Time{}
	}
}

func (a *Agent) creghtHost() string {
	a.creght.mu.Lock()
	defer a.creght.mu.Unlock()
	return a.creght.host
}

// creghtEndpoint 是 creght 服务商此刻给模型 m 的协议、地址和凭据：凭据就是 creght CLI 登录的 token，每次现取，
// 重新登录、换账号都跟着变。协议按平台列表里这个模型的 api：OpenAI 兼容的走 <api host>/api/p/ai/llm/v1/chat/completions，
// Anthropic 的走 <api host>/api/p/ai/llm/v1/messages（pi 的 Anthropic 客户端自己拼 /v1/messages，所以地址不带 /v1）。
func (a *Agent) creghtEndpoint(m config.LLM) (config.Provider, error) {
	host := a.creghtHost()
	tok, err := creght.ReadToken(host)
	if err != nil {
		return config.Provider{}, i18n.New("还没登录 creght，平台的模型登录后才能用", "Not signed in to creght; the platform's models are available after signing in")
	}
	p := config.Provider{ID: config.CreghtProvider, Name: "creght 平台", Api: "openai-completions", BaseURL: host + creghtLLMPath, APIKey: tok}
	if a.creghtApi(m) == "anthropic-messages" {
		p.Api, p.BaseURL = "anthropic-messages", host+strings.TrimSuffix(creghtLLMPath, "/v1")
	}
	return p, nil
}

// creghtApi 是平台模型的协议：模型上带了就用（平台列表来的），没带（界面测试连接只传 id）按缓存的列表找。
func (a *Agent) creghtApi(m config.LLM) string {
	if m.Api != "" {
		return m.Api
	}
	cm, _ := a.cachedCreghtModels()
	for _, c := range cm {
		if c.ID == m.ID || c.Model == m.Model {
			return c.Api
		}
	}
	return ""
}

// RefreshCreghtModels 拉平台的模型列表（有缓存：成功的留 5 分钟，失败的 30 秒后再试）。
// 不要在持有 a.mu 时调用：它会发网络请求。
func (a *Agent) RefreshCreghtModels(ctx context.Context, force bool) ([]config.LLM, error) {
	c := &a.creght
	c.mu.Lock()
	ttl := 5 * time.Minute
	if c.err != nil {
		ttl = 30 * time.Second
	}
	if !force && !c.at.IsZero() && time.Since(c.at) < ttl {
		defer c.mu.Unlock()
		return c.list, c.err
	}
	host := c.host
	c.mu.Unlock()

	list, err := fetchCreghtModels(ctx, host)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.host == host {
		c.list, c.err, c.at = list, err, time.Now()
	}
	return list, err
}

func (a *Agent) cachedCreghtModels() ([]config.LLM, error) {
	a.creght.mu.Lock()
	defer a.creght.mu.Unlock()
	return a.creght.list, a.creght.err
}

// fetchCreghtModels：GET /api/p/ai/llm/v1/models，OpenAI 的列表格式，平台在每个模型上多给了
// name / context_window / input / reasoning / api，缺了就按默认（能看图、支持思考、OpenAI 兼容协议）。
// apis 告诉平台这边认得哪些协议：不带它（老版本）平台只列 OpenAI 兼容的模型。
func fetchCreghtModels(ctx context.Context, host string) ([]config.LLM, error) {
	if _, err := creght.ReadToken(host); err != nil {
		return nil, i18n.New("还没登录 creght", "Not signed in to creght yet")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var resp struct {
		Data []struct {
			ID            string          `json:"id"`
			Name          string          `json:"name"`
			ContextWindow int             `json:"context_window"`
			Input         []string        `json:"input"`
			Reasoning     *bool           `json:"reasoning"`
			Pricing       *config.Pricing `json:"pricing"`
			Api           string          `json:"api"`
			WebSearch     bool            `json:"web_search"`
		} `json:"data"`
	}
	if err := creght.NewClient(host).Get(ctx, creghtLLMPath+"/models", url.Values{"apis": {strings.Join(creghtApis, ",")}}, &resp); err != nil {
		if msg := err.Error(); strings.Contains(msg, "route not found") || strings.Contains(msg, "HTTP 404") {
			return nil, i18n.New("creght 平台的模型接口还没上线，先用自己加的服务商", "The creght platform model API isn't live yet; use a provider you added yourself for now")
		}
		return nil, i18n.Errorf("拉取平台模型失败：%w", "Failed to fetch platform models: %w", err)
	}
	out := make([]config.LLM, 0, len(resp.Data))
	for _, d := range resp.Data {
		if d.ID == "" || (d.Api != "" && !slices.Contains(creghtApis, d.Api)) {
			continue
		}
		images := d.Input == nil || slices.Contains(d.Input, "image")
		m := config.LLM{ID: creghtPrefix + d.ID, Name: d.Name, Provider: config.CreghtProvider, Model: d.ID,
			Api: d.Api, ContextWindow: d.ContextWindow, Images: &images, Reasoning: d.Reasoning == nil || *d.Reasoning, Pricing: d.Pricing, WebSearch: d.WebSearch}
		out = append(out, m)
	}
	return out, nil
}

// models 是界面上能选的全部模型：平台的在前，用户自己加的在后；关掉的服务商的不算（调用方持有 a.mu）。
func (a *Agent) models() []config.LLM {
	cm, _ := a.cachedCreghtModels()
	all := append(append(append([]config.LLM(nil), cm...), a.cfg.Models...), cliModels()...)
	return slices.DeleteFunc(all, func(m config.LLM) bool { return a.disabled(m.Provider) })
}

// disabled：这个服务商被关掉了（调用方持有 a.mu）。
func (a *Agent) disabled(provider string) bool { return slices.Contains(a.cfg.DisabledProviders, provider) }

// ProviderEndpoint 是服务商 id 此刻的地址和凭据（本机函数的 ctx.llm.fetch 用）。creght 平台给 OpenAI 兼容的根地址
// <api host>/api/p/ai/llm/v1 和登录 token；本机 agent 没有地址；关掉的服务商不给用。
func (a *Agent) ProviderEndpoint(id string) (config.Provider, error) {
	a.mu.Lock()
	off := a.disabled(id)
	p, ok := a.cfg.ProviderByID(id)
	a.mu.Unlock()
	switch {
	case off:
		return config.Provider{}, i18n.Errorf("服务商「%s」在 设置 → 模型 里关掉了", "Provider \"%s\" is turned off in Settings → Models", id)
	case id == config.CreghtProvider:
		return a.creghtEndpoint(config.LLM{Api: "openai-completions"})
	case IsCLIProvider(id):
		return config.Provider{}, i18n.Errorf("「%s」是本机的 agent，没有接口地址", "\"%s\" is a local agent and has no API URL", id)
	case !ok:
		return config.Provider{}, i18n.Errorf("没有这个服务商：%s（到 设置 → 模型 里添加）", "No such provider: %s (add it in Settings → Models)", id)
	case p.BaseURL == "":
		return config.Provider{}, i18n.Errorf("服务商「%s」没填地址", "Provider \"%s\" has no URL", p.Name)
	}
	return p, nil
}

// DisabledProviders 是关掉的服务商（设置页显示开关）。
func (a *Agent) DisabledProviders() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.cfg.DisabledProviders...)
}

// SetProviderEnabled 打开或关掉一个服务商（creght 平台、本机 agent、自己加的）。关掉以后它的模型不出现，
// 当前模型在里面就回到默认。全关了也行：不用 AI 是正常用法，助手和 ctx.agent.current() 会说「还没有能用的模型」。
func (a *Agent) SetProviderEnabled(id string, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	known := id == config.CreghtProvider || IsCLIProvider(id)
	if _, ok := a.cfg.ProviderByID(id); ok {
		known = true
	}
	if !known {
		return i18n.Errorf("没有这个服务商：%s", "No such provider: %s", id)
	}
	next := slices.DeleteFunc(append([]string{}, a.cfg.DisabledProviders...), func(x string) bool { return x == id })
	if !on {
		next = append(next, id)
	}
	a.cfg.DisabledProviders = next
	return a.cfg.Save()
}

// current 是当前模型（还没填服务商的地址和 key）：选过的就用选过的；没选过、或者选的不在了，
// 用平台最便宜的（平台没给价格就用它排的第一个），再没有就用自己加的第一个（调用方持有 a.mu）。
func (a *Agent) current() config.LLM {
	all := a.models()
	for _, m := range all {
		if m.ID == a.cfg.Active {
			return m
		}
	}
	// 选的是外部 agent，但这会儿找不到命令：照样用它，跑的时候报「没找到」，别悄悄换（关掉了的除外）
	if rest, ok := strings.CutPrefix(a.cfg.Active, cliPrefix); ok && !a.disabled(strings.SplitN(rest, "/", 2)[0]) {
		id, model, _ := strings.Cut(rest, "/")
		for _, e := range cliEngines {
			if e.id == id {
				name := e.name
				if model == "" {
					model = id
				} else {
					name += " · " + model
				}
				return config.LLM{ID: a.cfg.Active, Name: name, Provider: id, Model: model, ContextWindow: e.window, Reasoning: true}
			}
		}
	}
	// 选的是平台模型，但这会儿列表没拉到（断网、平台出错）：照样按 id 用，别悄悄换模型
	if id, ok := strings.CutPrefix(a.cfg.Active, creghtPrefix); ok && id != "" && !a.disabled(config.CreghtProvider) {
		return config.LLM{ID: a.cfg.Active, Provider: config.CreghtProvider, Model: id, Reasoning: true}
	}
	if m, ok := cheapestCreght(all, a.tokenMix()); ok {
		return m
	}
	if len(all) > 0 {
		return all[0]
	}
	if a.disabled(config.CreghtProvider) {
		return config.LLM{} // 全关了：Resolve 报没有模型
	}
	if _, err := creght.ReadToken(a.creghtHost()); err != nil {
		return config.LLM{} // 没连 creght、也没配别的：Resolve 报没有模型，引导去配自己的（docs/annulo-plan.md 第 2 步）
	}
	return config.LLM{Provider: config.CreghtProvider}
}

// byID 按 id 找模型，找不到（删了、平台下架了）用 fallback（调用方持有 a.mu）。
func (a *Agent) byID(id string, fallback config.LLM) config.LLM {
	for _, m := range a.models() {
		if m.ID == id {
			return m
		}
	}
	return fallback
}

// TitleSettings：自动起名开没开、用哪个模型（空 = 跟对话用同一个）。
func (a *Agent) TitleSettings() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.AutoTitleOn(), a.cfg.TitleModel
}

// SetTitleSettings 改自动起名：on 为 nil 不改开关；model 为 nil 不改模型，"" 表示跟对话用同一个。
func (a *Agent) SetTitleSettings(on *bool, model *string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if model != nil && *model != "" {
		if a.byID(*model, config.LLM{}).ID == "" {
			return i18n.Errorf("没有这个模型：%s", "No such model: %s", *model)
		}
	}
	if on != nil {
		v := *on
		a.cfg.AutoTitle = &v
	}
	if model != nil {
		a.cfg.TitleModel = *model
	}
	return a.cfg.Save()
}

// cheapestCreght 是平台模型里按 token 构成折算最便宜的；一样便宜的保持平台的顺序。
// 平台没给价格时退回平台排的第一个。
func cheapestCreght(all []config.LLM, mix config.TokenMix) (config.LLM, bool) {
	var best config.LLM
	found := false
	for _, m := range all {
		if m.Provider != config.CreghtProvider {
			continue
		}
		if !found {
			best, found = m, true
			continue
		}
		if m.Pricing != nil && (best.Pricing == nil || m.Pricing.Cost(mix) < best.Pricing.Cost(mix)) {
			best = m
		}
	}
	return best, found
}

// resolved 是当前模型填好服务商之后的配置，可以直接发请求（调用方持有 a.mu）。
func (a *Agent) resolved() (config.LLM, error) {
	return a.cfg.Resolve(a.current(), a.creghtEndpoint)
}

// Resolve 给界面用：把某个模型填好服务商（测试连接前）。
func (a *Agent) Resolve(m config.LLM) (config.LLM, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Resolve(m, a.creghtEndpoint)
}

// LLM 是当前使用的模型，以及它能不能用（不能用时 error 说明原因）。
func (a *Agent) LLM() (config.LLM, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.current()
	_, err := a.cfg.Resolve(cur, a.creghtEndpoint)
	return cur, err
}

// ModelSettings 给设置页：全部模型、服务商、当前模型 id、思考强度。
type ModelSettings struct {
	Models    []config.LLM
	Providers []config.Provider
	Active    string
	Thinking  string
}

func (a *Agent) ModelSettings() ModelSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ModelSettings{Models: a.models(), Providers: append([]config.Provider(nil), a.cfg.Providers...), Active: a.current().ID, Thinking: a.cfg.ThinkingLevel()}
}

// Model 按 id 找模型（平台的和自己加的都算）。
func (a *Agent) Model(id string) (config.LLM, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.models() {
		if m.ID == id {
			return m, true
		}
	}
	return config.LLM{}, false
}

// SaveModel 新增（ID 为空）或修改一个自己加的模型。
func (a *Agent) SaveModel(m config.LLM) (config.LLM, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if m.Provider != config.CreghtProvider {
		if _, ok := a.cfg.ProviderByID(m.Provider); !ok {
			return m, i18n.New("先选一个服务商", "Pick a provider first")
		}
	}
	if strings.HasPrefix(m.ID, creghtPrefix) {
		return m, i18n.New("平台的模型不能修改", "Platform models can't be edited")
	}
	m.Api, m.BaseURL, m.APIKey, m.APIKeyEnv = "", "", "", ""
	// 同一个服务商下不能有两个一样的模型 id
	if slices.ContainsFunc(a.cfg.Models, func(x config.LLM) bool { return x.ID != m.ID && x.Provider == m.Provider && x.Model == m.Model }) {
		name := m.Provider
		if p, ok := a.cfg.ProviderByID(m.Provider); ok {
			name = p.Name
		}
		return m, i18n.Errorf("「%s」下已经有 %s 了", "\"%s\" already has %s", name, m.Model)
	}
	if m.ID == "" {
		m.ID = newID("m", func(id string) bool {
			return slices.ContainsFunc(a.cfg.Models, func(x config.LLM) bool { return x.ID == id })
		})
		a.cfg.Models = append(a.cfg.Models, m)
		return m, a.cfg.Save()
	}
	for i := range a.cfg.Models {
		if a.cfg.Models[i].ID == m.ID {
			a.cfg.Models[i] = m
			return m, a.cfg.Save()
		}
	}
	return m, i18n.Errorf("没有这个模型：%s", "No such model: %s", m.ID)
}

// DeleteModel 删掉一个自己加的模型；删的是当前模型就回到默认（平台的第一个）。
func (a *Agent) DeleteModel(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.cfg.Models)
	a.cfg.Models = slices.DeleteFunc(a.cfg.Models, func(m config.LLM) bool { return m.ID == id })
	if len(a.cfg.Models) == n {
		return i18n.Errorf("没有这个模型：%s", "No such model: %s", id)
	}
	if a.cfg.Active == id {
		a.cfg.Active = ""
	}
	return a.cfg.Save()
}

// SetActiveModel 切换当前模型。
func (a *Agent) SetActiveModel(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.ContainsFunc(a.models(), func(m config.LLM) bool { return m.ID == id }) {
		return i18n.Errorf("没有这个模型：%s", "No such model: %s", id)
	}
	a.cfg.Active = id
	return a.cfg.Save()
}

// Provider 按 id 找用户加的服务商。
func (a *Agent) Provider(id string) (config.Provider, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.ProviderByID(id)
}

// SaveProvider 新增（ID 为空）或修改一个服务商。key 留空表示沿用原来的。
func (a *Agent) SaveProvider(p config.Provider) (config.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.ID == config.CreghtProvider {
		return p, i18n.New("creght 是内置服务商，不能修改", "creght is a built-in provider and can't be edited")
	}
	if p.ID == "" {
		p.ID = newID("p", func(id string) bool { _, ok := a.cfg.ProviderByID(id); return ok || id == config.CreghtProvider })
		a.cfg.Providers = append(a.cfg.Providers, p)
		return p, a.cfg.Save()
	}
	for i := range a.cfg.Providers {
		if a.cfg.Providers[i].ID == p.ID {
			a.cfg.Providers[i] = p
			return p, a.cfg.Save()
		}
	}
	return p, i18n.Errorf("没有这个服务商：%s", "No such provider: %s", p.ID)
}

// DeleteProvider 删掉一个服务商；还有模型在用它时不让删。
func (a *Agent) DeleteProvider(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var users []string
	for _, m := range a.cfg.Models {
		if m.Provider == id {
			users = append(users, m.Label())
		}
	}
	if len(users) > 0 {
		return i18n.Errorf("还有模型在用这个服务商：%s。先删掉或改掉它们", "These models still use this provider: %s. Delete or change them first", strings.Join(users, i18n.T("、", ", ")))
	}
	n := len(a.cfg.Providers)
	a.cfg.Providers = slices.DeleteFunc(a.cfg.Providers, func(p config.Provider) bool { return p.ID == id })
	if len(a.cfg.Providers) == n {
		return i18n.Errorf("没有这个服务商：%s", "No such provider: %s", id)
	}
	return a.cfg.Save()
}

func (a *Agent) ThinkingLevel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.ThinkingLevel()
}

// SetThinking 设置思考强度（config.ThinkingLevels 里的一档；当前模型没有这档时按最接近的发）。
func (a *Agent) SetThinking(level string) error {
	if !slices.Contains(config.ThinkingLevels, level) {
		return i18n.Errorf("思考强度只能是 %s", "Thinking level must be %s", strings.Join(config.ThinkingLevels, " / "))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.Thinking = level
	return a.cfg.Save()
}

// LanguagePref / Locale：设置里的语言选择（auto / zh / en）和生效的界面语言（zh / en）。
func (a *Agent) LanguagePref() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.LanguagePref()
}

func (a *Agent) Locale() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Locale()
}

// SetLanguage 设置界面语言。已经开着的对话下一段新对话才换系统提示。
func (a *Agent) SetLanguage(lang string) error {
	if !slices.Contains(config.Languages, lang) {
		return i18n.Errorf("语言只能是 %s", "Language must be %s", strings.Join(config.Languages, " / "))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if lang == "auto" {
		lang = ""
	}
	a.cfg.Language = lang
	return a.cfg.Save()
}

func newID(prefix string, taken func(string) bool) string {
	for i := 1; ; i++ {
		if id := fmt.Sprintf("%s%d", prefix, i); !taken(id) {
			return id
		}
	}
}

// modelSig 是模型配置（已填好服务商）的指纹，变了就要给会话换模型。key 也在里面：
// creght 的 token 换了（重新登录）要跟着换。
func modelSig(l config.LLM) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%v|%v|%s", l.ID, l.Api, l.BaseURL, l.Model, l.Window(), l.Reasoning, l.Vision(), l.Key())
}

// SetChatThinking 让这段对话固定用某个思考档位（空 = 跟全局设置走）。任务文件写了 thinking 时用：
// 抽资料这类不用深想的任务用低档，快得多。
func (a *Agent) SetChatThinking(chatID, level string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if level == "" {
		delete(a.chatThinking, chatID)
		return
	}
	if a.chatThinking == nil {
		a.chatThinking = map[string]string{}
	}
	a.chatThinking[chatID] = level
}

// thinkingFor 是这段对话用的思考档位：固定了就用固定的，否则是全局设置（调用方持有 a.mu）
func (a *Agent) thinkingFor(chatID string) string {
	if l := a.chatThinking[chatID]; l != "" {
		return l
	}
	return a.cfg.ThinkingLevel()
}

// applySettings 在每轮开始前把当前的模型和思考强度同步到会话上（调用方持有 a.mu）。
func (a *Agent) applySettings(c *chatSession, chatID string) error {
	l, err := a.resolved()
	if err != nil {
		return err
	}
	if sig := modelSig(l); sig != c.model {
		m, key, err := modelFor(l)
		if err != nil {
			return err
		}
		c.sess.SetModel(m, key)
		c.model = sig
	}
	if t := a.thinkingFor(chatID); t != c.thinking {
		c.sess.SetThinkingLevel(agent.ThinkingLevel(t))
		c.thinking = t
	}
	return nil
}

// TestLLM 用和正式对话完全相同的路径（pi session，不带工具）发一句话，验证配置能用。l 要先 Resolve。
func TestLLM(ctx context.Context, l config.LLM) (string, error) {
	m, key, err := modelFor(l)
	if err != nil {
		return "", err
	}
	empty := []coding.Skill{}
	sess := coding.NewSession(coding.SessionOptions{
		Model:        m,
		Cwd:          os.TempDir(),
		NoTools:      coding.NoToolsAll,
		Skills:       &empty,
		SystemPrompt: "只回复两个字：好的",
		APIKey:       key,
		MaxRetries:   0,
	})
	res, err := sess.Run(ctx, "测试连接")
	if err != nil {
		return "", err
	}
	if res.ErrorMessage != "" {
		return "", errors.New(res.ErrorMessage)
	}
	return res.Text, nil
}

// Complete 用当前模型答一次（本机函数的 ctx.llm）：不带工具、不进对话历史、不算进哪段对话。
func (a *Agent) Complete(ctx context.Context, system, prompt string) (string, error) {
	return a.complete(ctx, system, prompt, "", "")
}

// complete：model 为空用当前模型；thinking 为空时用模型默认的思考强度，起标题这类小事传 off，快几秒。
func (a *Agent) complete(ctx context.Context, system, prompt, model string, thinking agent.ThinkingLevel) (string, error) {
	a.RefreshCreghtModels(ctx, false)
	a.mu.Lock()
	cur := a.current()
	if model != "" {
		cur = a.byID(model, cur)
	}
	l, err := a.cfg.Resolve(cur, a.creghtEndpoint)
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	if system == "" {
		system = "你是一个严谨的助手，直接给出答案，不要寒暄。"
	}
	if IsCLIProvider(l.Provider) {
		return cliComplete(ctx, l.Provider, system, prompt)
	}
	m, key, err := modelFor(l)
	if err != nil {
		return "", err
	}
	empty := []coding.Skill{}
	sess := coding.NewSession(coding.SessionOptions{Model: m, Cwd: os.TempDir(), NoTools: coding.NoToolsAll, Skills: &empty, SystemPrompt: system, APIKey: key, MaxRetries: llmMaxRetries, ThinkingLevel: thinking})
	res, err := sess.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	if res.ErrorMessage != "" {
		return "", errors.New(res.ErrorMessage)
	}
	// 用量照样记本机（对话 id 记成 local，用量页能看到本机函数花了多少）
	a.recordUsage("local", &ai.AssistantMessage{Model: m.ID, Usage: res.Usage})
	return res.Text, nil
}

// model 是当前模型的 pi 配置（调用方持有 a.mu）。
func (a *Agent) model() (*ai.Model, string, error) {
	l, err := a.resolved()
	if err != nil {
		return nil, "", err
	}
	return modelFor(l)
}

// modelFor 把填好服务商的模型配置转成 pi 的模型。
func modelFor(l config.LLM) (*ai.Model, string, error) {
	if err := l.Ready(); err != nil {
		return nil, "", err
	}
	input := []string{"text"}
	if l.Vision() {
		input = append(input, "image")
	}
	m := &ai.Model{
		ID:            l.Model,
		Name:          l.Label(),
		Api:           ai.Api(l.Api),
		Provider:      ai.ProviderId("shuttle"),
		BaseURL:       l.BaseURL,
		Reasoning:     l.Reasoning,
		Input:         input,
		ContextWindow: l.Window(),
		MaxTokens:     32000,
		Compat:        compatFor(l),
	}
	m.ThinkingLevelMap, _ = thinkingMap(l) // 每档发什么、哪些档不支持（thinking.go）
	return m, l.Key(), nil
}

// compatFor 补 pi 按地址认不出来的模型差异。pi 只在地址是 deepseek.com 时才按 DeepSeek 处理，
// 走网关（creght 平台、subhub……）时认不出来：DeepSeek 的思考模式要求把上一轮的 reasoning_content
// 原样带回去，否则第二次请求就 400（「The reasoning_content in the thinking mode must be passed back」）。
// 所以按模型名认。
func compatFor(l config.LLM) json.RawMessage {
	if l.Api != "openai-completions" || !strings.Contains(strings.ToLower(l.Model), "deepseek") {
		return nil
	}
	return json.RawMessage(`{"thinkingFormat":"deepseek","requiresReasoningContentOnAssistantMessages":true,"maxTokensField":"max_tokens"}`)
}
