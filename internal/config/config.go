package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 配置分两处：
//   - 环境变量 SHUTTLE_*：监听地址、目录这类「怎么跑」的参数；
//   - ~/.shuttle/config.json：服务商、模型、当前模型、思考强度。API key 建议写成 api_key_env
//     （引用环境变量名），不把 key 本身落盘。
type Config struct {
	Host string
	Port int
	Dir  string // ~/.shuttle

	// Providers 是用户自己加的服务商（协议 + 地址 + key）。内置的 creght 服务商不在这里：
	// 它用登录的 creght 账号，地址和凭据运行时才取（见 agent.creghtEndpoint）。
	Providers []Provider `json:"providers"`
	// Models 是用户配的模型（每个指定一个服务商），Active 是当前用哪个：Models 里的 ID，
	// 或者 creght 平台的模型 "creght:<模型 id>"。空表示没选过，默认用 creght 平台的第一个模型。
	Models []LLM  `json:"models"`
	Active string `json:"active_model"`
	// Thinking 是思考强度：off / low / medium / high，所有模型共用；模型不支持思考时不生效。
	Thinking string `json:"thinking"`
	// AutoTitle：新对话发出第一句话后让模型起个短标题。没设过（nil）算开。
	AutoTitle *bool `json:"auto_title,omitempty"`
	// TitleModel 是起标题用的模型（Models 里的 ID 或 "creght:<模型 id>"）；空或者这个模型不在了，用当前对话的模型。
	TitleModel string `json:"title_model,omitempty"`
	// DisabledProviders 是关掉的服务商（creght 平台、本机 agent claude / codex…）：它的模型不出现在列表和选择菜单里，
	// 当前模型在里面就回到默认。只关模型，creght 账号、MCP 这些照常用。
	DisabledProviders []string `json:"disabled_providers,omitempty"`
	// LLM 是早期只有一个模型时的配置，读到就迁移进 Models，不再写回。
	LLM LLM `json:"llm"`
	// Backend 是当前使用的运营后台（creght 项目 id）。一个账号可以有多个，在初始化页 / 设置里切换。
	Backend string `json:"backend,omitempty"`
	// Language 是界面语言：空 / auto 跟随系统，zh，en（见 locale.go）。
	Language string `json:"language,omitempty"`
	// Creght 是用哪个 creght 集群（API 地址，如 https://creght.com）：登录、项目列表、新建项目都在这个集群。空是 creght.cn。
	Creght string `json:"creght,omitempty"`
	// RemoteCalls：允许远程调用这台电脑（设置 → 远程访问）。打开后 Shuttle 连平台，在手机、别的电脑上点发布、采集转到这里跑（internal/relay）。默认关。
	RemoteCalls bool `json:"remote_calls,omitempty"`
	// Offline：用户选了不登录、离线使用（登录页上点的）。项目可以是离线的（数据在本机），以后随时能登录 creght。
	Offline bool `json:"offline_mode,omitempty"`
	// TemplateSources：自己加的 git 模板，每项是 <仓库地址>[#<子目录>]，版本是仓库里的 semver tag（docs/annulo-plan.md 第 3 步）。
	TemplateSources []string `json:"template_sources,omitempty"`
	// RenderCDN：不连 creght 时页面的包（react、talizen…）从哪个 CDN 加载，要和 esm.sh 同一套 URL 写法。空是 https://esm.sh。
	RenderCDN string `json:"render_cdn,omitempty"`
}

// CreghtProvider 是内置服务商的 id：creght 平台的模型，登录了就能用，按平台积分计费。
const CreghtProvider = "creght"

// Provider 是一个模型服务商：协议、地址、key。一个服务商下可以挂多个模型。
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Api 是 pi 的协议名：openai-completions / anthropic-messages / openai-responses
	Api       string `json:"api"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

func (p Provider) Key() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return Lookup(p.APIKeyEnv)
	}
	return ""
}

// LLM 是一个模型。配置里只存 Provider + Model 和模型自己的属性；Api / BaseURL / key 是
// Resolve 之后从服务商填进来的（早期配置直接写在模型上，Load 时迁移成服务商）。
type LLM struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"` // 显示名，不填就用模型 id
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`

	Api       string `json:"api,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// ContextWindow 是模型的上下文窗口（token）。不填按 512k 算；它决定何时压缩上下文和占用条的分母。
	ContextWindow int `json:"context_window,omitempty"`
	// Images：能不能看图。不填当作能（现在主流模型都能），不能看图的模型关掉，图片就只把本机路径告诉它。
	Images *bool `json:"images,omitempty"`
	// Reasoning：支不支持思考（推理强度）。打开后思考强度才会发给模型。
	Reasoning bool `json:"reasoning,omitempty"`
	// Pricing：creght 平台模型的价格（平台 /models 给的），自己加的模型没有。不存盘。
	Pricing *Pricing `json:"-"`
	// WebSearch：creght 平台标了能用 /responses 联网搜索（web_search）的模型（平台 /models 给的）；自己加的模型不知道，是 false。不存盘。
	WebSearch bool `json:"-"`
}

// Pricing 是每百万 token 的积分：未命中缓存的输入、命中缓存的输入、输出。
type Pricing struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	Output      float64 `json:"output"`
}

// TokenMix 是 token 的构成比例（三项加起来是 1），用来把三种单价折成一个数比较。
type TokenMix struct{ Cached, Input, Output float64 }

// DefaultTokenMix：agent 每次请求带着整段上下文，绝大部分输入命中缓存。
// 取自实测的 240 次请求：缓存 96.4%、新输入 3.1%、输出 0.5%。只比输出价会选错。
var DefaultTokenMix = TokenMix{Cached: 0.96, Input: 0.035, Output: 0.005}

// Cost 是按 token 构成折算的单价（每百万 token 积分）。没有缓存价的按输入价算，和平台计费一致。
func (p Pricing) Cost(m TokenMix) float64 {
	cached := p.CachedInput
	if cached <= 0 {
		cached = p.Input
	}
	return m.Cached*cached + m.Input*p.Input + m.Output*p.Output
}

func (l LLM) Vision() bool { return l.Images == nil || *l.Images }

func (l LLM) Label() string {
	if l.Name != "" {
		return l.Name
	}
	return l.Model
}

var ThinkingLevels = []string{"off", "low", "medium", "high"}

// ThinkingLevel 是生效的思考强度，没设过按 medium。
func (c *Config) ThinkingLevel() string {
	for _, l := range ThinkingLevels {
		if c.Thinking == l {
			return l
		}
	}
	return "medium"
}

// AutoTitleOn：自动起名开着没有（没设过算开）。
func (c *Config) AutoTitleOn() bool { return c.AutoTitle == nil || *c.AutoTitle }

// ProviderByID 找用户加的服务商。
func (c *Config) ProviderByID(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Resolve 把服务商的协议、地址、key 填到模型上，得到能直接发请求的配置。
// creght 服务商的协议、地址和 token 由调用方按模型给（跟着登录状态和平台的模型列表走，不在配置里）。
func (c *Config) Resolve(m LLM, creght func(LLM) (Provider, error)) (LLM, error) {
	var p Provider
	switch {
	case m.Provider == CreghtProvider:
		var err error
		if p, err = creght(m); err != nil {
			return m, err
		}
	case m.Provider == "claude-code" || m.Provider == "codex":
		return m, nil // 本机的外部 agent：用它自己的登录，没有地址和 key（internal/agent/cli.go）
	case m.Provider != "":
		var ok bool
		if p, ok = c.ProviderByID(m.Provider); !ok {
			return m, i18n.Errorf("模型「%s」的服务商已经删掉了，编辑模型换一个", "The provider of model \"%s\" was deleted — edit the model to pick another", m.Label())
		}
	default:
		return m, m.Ready() // 没迁移的旧配置：地址和 key 就在模型上
	}
	m.Api, m.BaseURL, m.APIKey, m.APIKeyEnv = p.Api, p.BaseURL, p.Key(), ""
	return m, m.Ready()
}

func (l LLM) Window() int {
	if l.ContextWindow > 0 {
		return l.ContextWindow
	}
	return 512000
}

func (l LLM) Key() string {
	if l.APIKey != "" {
		return l.APIKey
	}
	if l.APIKeyEnv != "" {
		return Lookup(l.APIKeyEnv)
	}
	return ""
}

func (l LLM) Ready() error {
	switch {
	case l.Model == "":
		return i18n.New("还没有能用的模型：在 设置 → 模型 里填自己的 API key，或者用这台电脑上的 Claude Code / Codex；连上 creght 也能用平台的模型", "No model to use yet: add your own API key in Settings → Models, or use Claude Code / Codex on this computer; connecting creght also gives you the platform's models")
	case l.Api == "" || l.BaseURL == "":
		return i18n.Errorf("模型「%s」没配置服务商地址", "Model \"%s\" has no provider URL", l.Label())
	case l.Key() == "":
		if l.APIKeyEnv != "" {
			return i18n.Errorf("环境变量 %s 是空的", "Environment variable %s is empty", l.APIKeyEnv)
		}
		return i18n.New("模型缺 API Key", "The model has no API key")
	}
	return nil
}

func Load() (*Config, error) {
	c := &Config{Host: "127.0.0.1", Port: 7799, Dir: brand.DataDir()} // ~/.annulo（老的 ~/.shuttle 第一次会迁过来）
	if v := brand.Env("HOST"); v != "" {
		c.Host = v
	}
	if v := brand.Env("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, i18n.Errorf("%s=%q 不是端口号", "%s=%q is not a port number", brand.EnvName("PORT"), v)
		}
		c.Port = p
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(c.File())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", c.File(), err)
		}
	}
	// 早期的单个 llm 配置迁移成模型列表里的第一个；它能输出思考过程，所以默认支持思考
	if len(c.Models) == 0 && c.LLM.Model != "" {
		m := c.LLM
		m.ID, m.Reasoning = "m1", true
		c.Models, c.Active = []LLM{m}, m.ID
	}
	c.LLM = LLM{}
	c.migrateProviders()
	// Go 这边给用户看的文字跟着界面语言：每次取文字时读 c.Locale()，设置里切换立刻生效
	i18n.SetLocale(c.Locale)
	return c, nil
}

func (c *Config) File() string { return filepath.Join(c.Dir, "config.json") }

// BackendDir 是当前运营后台站点的本地副本（creght pull 出来的工作区），agent 在这里改代码。
// 还没选运营后台时返回空。
func (c *Config) BackendDir() string {
	if c.Backend == "" {
		// 早期版本只有一个 ~/.shuttle/backend，没记 Backend：有就接着用
		if legacyProject(c.legacyDir()) != "" {
			return c.legacyDir()
		}
		return ""
	}
	return c.DirFor(c.Backend)
}

// DirFor 是某个运营后台（creght 项目 id）的本地目录：~/.shuttle/backends/<id>；
// 早期的 ~/.shuttle/backend 如果就是这个项目，继续用它。
func (c *Config) DirFor(projectID string) string {
	if legacyProject(c.legacyDir()) == projectID {
		return c.legacyDir()
	}
	return filepath.Join(c.Dir, "backends", projectID)
}

// BackendID 是当前项目（运营后台 creght 项目）的 id；早期版本没记 Backend，就看 ~/.shuttle/backend 是哪个项目。
func (c *Config) BackendID() string {
	if c.Backend != "" {
		return c.Backend
	}
	return legacyProject(c.legacyDir())
}

// LegacyProject 是早期 ~/.shuttle/backend 对应的项目 id，没有就是空。
func (c *Config) LegacyProject() string { return legacyProject(c.legacyDir()) }

func (c *Config) legacyDir() string { return filepath.Join(c.Dir, "backend") }

func legacyProject(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".creght", "state.json"))
	if err != nil {
		return ""
	}
	var st struct {
		SiteID string `json:"site_id"`
	}
	json.Unmarshal(b, &st)
	pid, _, _ := strings.Cut(st.SiteID, "/")
	return pid
}

// migrateProviders：早期模型把协议、地址、key 直接写在自己身上。相同的（协议 + 地址 + key）
// 合成一个服务商，模型改成引用它。
func (c *Config) migrateProviders() {
	for i := range c.Models {
		m := &c.Models[i]
		if m.Provider != "" || m.BaseURL == "" {
			continue
		}
		var id string
		for _, p := range c.Providers {
			if p.Api == m.Api && p.BaseURL == m.BaseURL && p.APIKey == m.APIKey && p.APIKeyEnv == m.APIKeyEnv {
				id = p.ID
			}
		}
		if id == "" {
			id = fmt.Sprintf("p%d", len(c.Providers)+1)
			name := m.BaseURL
			if u, err := url.Parse(m.BaseURL); err == nil && u.Host != "" {
				name = u.Host
			}
			c.Providers = append(c.Providers, Provider{ID: id, Name: name, Api: m.Api, BaseURL: m.BaseURL, APIKey: m.APIKey, APIKeyEnv: m.APIKeyEnv})
		}
		m.Provider = id
		m.Api, m.BaseURL, m.APIKey, m.APIKeyEnv = "", "", "", ""
	}
}

func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Host, c.Port) }

// Save 把配置写回 config.json（0600：里面可能有直接填的 API key）。
// 只写文件里该有的字段，环境变量那部分不落盘。
func (c *Config) Save() error {
	b, err := json.MarshalIndent(struct {
		Providers   []Provider `json:"providers"`
		Models      []LLM      `json:"models"`
		Active      string     `json:"active_model,omitempty"`
		Thinking    string     `json:"thinking,omitempty"`
		AutoTitle   *bool      `json:"auto_title,omitempty"`
		TitleModel  string     `json:"title_model,omitempty"`
		Disabled    []string   `json:"disabled_providers,omitempty"`
		Backend     string     `json:"backend,omitempty"`
		Language    string     `json:"language,omitempty"`
		Creght      string     `json:"creght,omitempty"`
		RemoteCalls bool       `json:"remote_calls,omitempty"`
		Offline     bool       `json:"offline_mode,omitempty"`
		Templates   []string   `json:"template_sources,omitempty"`
		RenderCDN   string     `json:"render_cdn,omitempty"`
	}{c.Providers, c.Models, c.Active, c.Thinking, c.AutoTitle, c.TitleModel, c.DisabledProviders, c.Backend, c.Language, c.Creght, c.RemoteCalls, c.Offline, c.TemplateSources, c.RenderCDN}, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.File() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.File())
}
