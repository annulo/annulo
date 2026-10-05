// Package connect 是「连接」：用户在设置页授权一次外部账号（比如 Google），本机函数用 await ctx.oauth('google')
// 拿到 access token，自己调对方的 REST 接口（Search Console、GA4…）。
//
// 为什么是 Shuttle 的原语：OAuth 要一个客户端（client id）、一个本机回调地址、token 的保存和刷新，
// 这些和具体业务无关；本机函数里又没法自己做（不能起回调服务、不能存文件）。
// 哪些接口、怎么读数据是业务，写在工作空间的本机函数里，Shuttle 不认识 GSC。
//
// 流程：设置页点「连接」→ Start 生成授权地址（PKCE）→ 用户在浏览器里同意 → 跳回 /_shuttle/oauth/callback
// → Callback 用 code 换 token，存到 ~/.shuttle/connections/<provider>/<账号>.json（0600）。之后 Token 自动刷新。
//
// 一种连接可以连多个账号（比如两个项目的网站在两个 Google 账号下）：再点一次连接、选另一个账号就加一个，
// 同一个账号再连是覆盖（补权限）。本机函数 ctx.oauth('google', { account }) 指定用哪个，不指定用最早连的那个。
package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"

	"golang.org/x/oauth2"
)

// Google 的 OAuth 客户端（「桌面应用」类型：回调是本机回环地址，client secret 按 Google 的说法不算机密）。
// 打包时用 -ldflags "-X github.com/annulo/annulo/internal/connect.GoogleClientID=…" 注入；
// 自己编译的可以用环境变量 SHUTTLE_GOOGLE_CLIENT_ID / SHUTTLE_GOOGLE_CLIENT_SECRET。
var (
	GoogleClientID     string
	GoogleClientSecret string
)

// Provider 是一种可以连接的外部账号。
type Provider struct {
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Desc   string   `json:"desc"` // 设置页上的说明：连上以后能做什么
	descEn string   // Desc 的英文，列表输出时按界面语言取
	Scopes []string `json:"scopes"` // 申请的权限：只读为主，写权限要单独想清楚再加
	oauth  oauth2.Config
	// account 用 token 查账号名（邮箱），设置页显示「已连接 xxx」
	account func(ctx context.Context, c *http.Client) (string, error)
}

func providers() []*Provider {
	gid, gsecret := GoogleClientID, GoogleClientSecret
	if v := brand.Env("GOOGLE_CLIENT_ID"); v != "" {
		gid, gsecret = v, brand.Env("GOOGLE_CLIENT_SECRET")
	}
	google := &Provider{
		Key:    "google",
		Name:   "Google",
		Desc:   "读 Search Console 的搜索数据和 Google Analytics（GA4）的访问数据（只读）。",
		descEn: "Reads Search Console search data and Google Analytics (GA4) traffic data (read-only).",
		Scopes: []string{
			"openid", "email",
			"https://www.googleapis.com/auth/webmasters.readonly",
			"https://www.googleapis.com/auth/analytics.readonly",
		},
		oauth: oauth2.Config{
			ClientID: gid, ClientSecret: gsecret,
			Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
		},
		account: func(ctx context.Context, c *http.Client) (string, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
			resp, err := c.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			var u struct{ Email string }
			json.NewDecoder(resp.Body).Decode(&u)
			return u.Email, nil
		},
	}
	return []*Provider{google}
}

// Info 是给设置页看的一种连接的状态，不含 token。
type Info struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Desc      string    `json:"desc"`
	Scopes    []string  `json:"scopes"`
	Available bool      `json:"available"` // 这个版本带了客户端，能连
	Connected bool      `json:"connected"` // 至少连了一个账号
	Accounts  []Account `json:"accounts"`  // 连上的账号，最早连的在前（不指定账号时用它）
	Pending   bool      `json:"pending"`   // 已经打开授权页，等用户同意
}

// Account 是连上的一个账号。
type Account struct {
	Account     string    `json:"account"` // 邮箱；对方没给账号名时是空的
	ConnectedAt time.Time `json:"connected_at"`
	// 连接时还没有、后来新加的权限（比如加了 GA4）：要重新连接一次才有
	MissingScopes []string `json:"missing_scopes,omitempty"`
}

type saved struct {
	Token       *oauth2.Token `json:"token"`
	Scopes      []string      `json:"scopes"`
	Account     string        `json:"account,omitempty"`
	ConnectedAt time.Time     `json:"connected_at"`
}

type pending struct {
	provider *Provider
	verifier string
	redirect string
	at       time.Time
}

// Store 管所有连接：授权、保存、刷新。
type Store struct {
	dir       string // ~/.shuttle/connections
	providers map[string]*Provider

	mu      sync.Mutex
	pending map[string]*pending           // 按 state
	sources map[string]oauth2.TokenSource // 按 provider + 账号
}

func New(shuttleDir string) *Store {
	s := &Store{dir: filepath.Join(shuttleDir, "connections"), providers: map[string]*Provider{}, pending: map[string]*pending{}, sources: map[string]oauth2.TokenSource{}}
	for _, p := range providers() {
		s.providers[p.Key] = p
	}
	return s
}

// StatePrefix 标出回调是连接的，不是 MCP 的：两边共用 /_shuttle/oauth/callback。
const StatePrefix = "conn_"

// file 是一个账号的授权文件。账号名当文件名，不能用的字符换成 _；对方没给账号名时叫 default
func (s *Store) file(key, account string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@._-+", r) {
			return r
		}
		return '_'
	}, strings.ToLower(account))
	if name == "" || strings.Trim(name, ".") == "" {
		name = "default"
	}
	return filepath.Join(s.dir, key, name+".json")
}

func readSaved(path string) *saved {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v saved
	if json.Unmarshal(b, &v) != nil || v.Token == nil {
		return nil
	}
	return &v
}

// migrate：以前一种连接只有一个账号，存在 <provider>.json，挪进 <provider>/<账号>.json
func (s *Store) migrate(key string) {
	old := filepath.Join(s.dir, key+".json")
	v := readSaved(old)
	if v == nil {
		return
	}
	if s.save(key, v) == nil {
		os.Remove(old)
	}
}

// accounts 是一种连接下连上的账号，最早连的在前。
func (s *Store) accounts(key string) []*saved {
	s.migrate(key)
	ents, _ := os.ReadDir(filepath.Join(s.dir, key))
	var out []*saved
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			if v := readSaved(filepath.Join(s.dir, key, e.Name())); v != nil {
				out = append(out, v)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

// load 取一个账号的授权；account 空是最早连的那个。账号名不分大小写
func (s *Store) load(key, account string) *saved {
	list := s.accounts(key)
	for _, v := range list {
		if account == "" || strings.EqualFold(v.Account, account) {
			return v
		}
	}
	return nil
}

func (s *Store) save(key string, v *saved) error {
	f := s.file(key, v.Account)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(f, b, 0o600)
}

// Accounts 是一种连接下连上的账号名（ctx.oauth.accounts），最早连的在前。
func (s *Store) Accounts(key string) ([]string, error) {
	if _, err := s.provider(key); err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range s.accounts(key) {
		out = append(out, v.Account)
	}
	return out, nil
}

func (s *Store) List() []Info {
	s.mu.Lock()
	waiting := map[string]bool{}
	for _, p := range s.pending {
		if time.Since(p.at) < 15*time.Minute {
			waiting[p.provider.Key] = true
		}
	}
	s.mu.Unlock()
	var out []Info
	for _, p := range s.providers {
		in := Info{Key: p.Key, Name: p.Name, Desc: i18n.T(p.Desc, p.descEn), Scopes: p.Scopes, Available: p.oauth.ClientID != "", Pending: waiting[p.Key], Accounts: []Account{}}
		for _, v := range s.accounts(p.Key) {
			in.Accounts = append(in.Accounts, Account{Account: v.Account, ConnectedAt: v.ConnectedAt, MissingScopes: missing(p.Scopes, v.Scopes)})
		}
		in.Connected = len(in.Accounts) > 0
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// missing 是 want 里 have 没有的权限
func missing(want, have []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(have, w) {
			out = append(out, w)
		}
	}
	return out
}

func (s *Store) provider(key string) (*Provider, error) {
	p := s.providers[key]
	if p == nil {
		var keys []string
		for k := range s.providers {
			keys = append(keys, "'"+k+"'")
		}
		sort.Strings(keys)
		return nil, i18n.Errorf("没有叫 %q 的连接，现在能连的是 %s", "No connection named %q; available: %s", key, strings.Join(keys, i18n.T("、", ", ")))
	}
	return p, nil
}

// Start 生成授权地址。redirect 是本机的 /_shuttle/oauth/callback。
func (s *Store) Start(key, redirect string) (string, error) {
	p, err := s.provider(key)
	if err != nil {
		return "", err
	}
	if p.oauth.ClientID == "" {
		return "", i18n.Errorf("这个版本的 Annulo 没有带 %s 的 OAuth 客户端，暂时连不了", "This build of Annulo has no %s OAuth client, so it can't connect yet", p.Name)
	}
	state := StatePrefix + oauth2.GenerateVerifier()[:24]
	verifier := oauth2.GenerateVerifier()
	cfg := p.oauth
	cfg.RedirectURL, cfg.Scopes = redirect, p.Scopes
	s.mu.Lock()
	for st, x := range s.pending {
		if time.Since(x.at) > 15*time.Minute {
			delete(s.pending, st)
		}
	}
	s.pending[state] = &pending{provider: p, verifier: verifier, redirect: redirect, at: time.Now()}
	s.mu.Unlock()
	// access_type=offline + prompt=consent：Google 只有这样才每次都给 refresh token；select_account：每次都让用户选账号，才连得上第二个
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "select_account consent")), nil
}

// Callback 用授权码换 token 并保存。返回连上的是哪个连接的名字。
func (s *Store) Callback(ctx context.Context, state, code, errMsg string) (string, error) {
	s.mu.Lock()
	p := s.pending[state]
	delete(s.pending, state)
	s.mu.Unlock()
	if p == nil {
		return "", i18n.New("这个授权链接已经用过或者过期了（比如刷新了授权完成的页面）。设置 → 连接 里已经显示「已连接」的话就不用管；没连上就在那里重新连接", "This authorization link was already used or has expired (e.g. you refreshed the completion page). If Settings → Connections shows \"Connected\", you're fine; otherwise reconnect there")
	}
	if errMsg != "" {
		if errMsg == "access_denied" {
			errMsg = i18n.T("你在授权页上选了拒绝", "You chose Deny on the authorization page")
		}
		return p.provider.Name, errors.New(errMsg)
	}
	cfg := p.provider.oauth
	cfg.RedirectURL, cfg.Scopes = p.redirect, p.provider.Scopes
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(p.verifier))
	if err != nil {
		return p.provider.Name, i18n.Errorf("换取授权失败：%w", "Failed to exchange the authorization: %w", err)
	}
	v := &saved{Token: tok, Scopes: p.provider.Scopes, ConnectedAt: time.Now()}
	if p.provider.account != nil {
		v.Account, _ = p.provider.account(ctx, cfg.Client(ctx, tok))
	}
	// 同一个账号再连（补权限）：覆盖它，连接时间不变（不改变默认账号是哪个）
	if old := readSaved(s.file(p.provider.Key, v.Account)); old != nil {
		v.ConnectedAt = old.ConnectedAt
		if tok.RefreshToken == "" {
			// 没有 refresh token 一小时后就得重连；保留旧的（重新授权时 Google 有时不再给）
			tok.RefreshToken = old.Token.RefreshToken
		}
	}
	if err := s.save(p.provider.Key, v); err != nil {
		return p.provider.Name, err
	}
	s.mu.Lock()
	delete(s.sources, sourceKey(p.provider.Key, v.Account))
	s.mu.Unlock()
	return p.provider.Name, nil
}

func sourceKey(key, account string) string { return key + "\x00" + strings.ToLower(account) }

// Disconnect 删掉本机保存的一个账号的 token；account 空是这种连接的全部账号。
// 不去对方那里撤销授权：用户要撤销在对方的账号页里做。
func (s *Store) Disconnect(key, account string) error {
	if _, err := s.provider(key); err != nil {
		return err
	}
	s.mu.Lock()
	for k := range s.sources {
		if strings.HasPrefix(k, key+"\x00") && (account == "" || k == sourceKey(key, account)) {
			delete(s.sources, k)
		}
	}
	s.mu.Unlock()
	s.migrate(key)
	if account == "" {
		return os.RemoveAll(filepath.Join(s.dir, key))
	}
	for _, v := range s.accounts(key) {
		if strings.EqualFold(v.Account, account) {
			return os.Remove(s.file(key, v.Account))
		}
	}
	return nil
}

// ErrNotConnected：还没连接。本机函数里抛出去，页面据此提示去设置。
var ErrNotConnected = errors.New("not connected")

// Token 返回一个账号可用的 access token（快过期时自动刷新，刷新后落盘）。account 空是最早连的那个。
func (s *Store) Token(ctx context.Context, key, account string) (string, error) {
	p, err := s.provider(key)
	if err != nil {
		return "", err
	}
	v := s.load(key, account)
	if v == nil {
		if account == "" {
			return "", i18n.Errorf("还没连接 %s：到 Annulo 的 设置 → 连接 里连接 %s（%w）", "%s isn't connected: connect %s in Annulo → Settings → Connections (%w)", p.Name, p.Name, ErrNotConnected)
		}
		have, _ := s.Accounts(key)
		list := strings.Join(have, i18n.T("、", ", "))
		if list == "" {
			list = i18n.T("没有", "none")
		}
		return "", i18n.Errorf("%s 账号 %s 没连接（这台电脑连着的：%s）：到 Annulo 的 设置 → 连接 里连接这个账号（%w）", "%s account %s isn't connected (connected on this computer: %s): connect it in Annulo → Settings → Connections (%w)",
			p.Name, account, list, ErrNotConnected)
	}
	sk := sourceKey(key, v.Account)
	s.mu.Lock()
	src := s.sources[sk]
	s.mu.Unlock()
	if src == nil {
		cfg := p.oauth
		cfg.Scopes = p.Scopes
		base := cfg.TokenSource(context.WithoutCancel(ctx), v.Token)
		file := s.file(key, v.Account)
		src = &savingSource{src: base, last: v.Token.AccessToken, save: func(t *oauth2.Token) {
			if cur := readSaved(file); cur != nil {
				cur.Token = t
				s.save(key, cur)
			}
		}}
		s.mu.Lock()
		s.sources[sk] = src
		s.mu.Unlock()
	}
	t, err := src.Token()
	if err != nil {
		s.mu.Lock()
		delete(s.sources, sk)
		s.mu.Unlock()
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.Response != nil && re.Response.StatusCode == http.StatusBadRequest) {
			name := p.Name
			if v.Account != "" {
				name += " " + v.Account
			}
			return "", i18n.Errorf("%s 的授权失效了（可能在 Google 账号里撤销了，或者太久没用）：到 Annulo 的 设置 → 连接 里重新连接（%w）", "%s authorization has expired (revoked in the Google account, or unused for too long): reconnect in Annulo → Settings → Connections (%w)", name, ErrNotConnected)
		}
		return "", i18n.Errorf("刷新 %s 的授权失败：%w", "Failed to refresh %s authorization: %w", p.Name, err)
	}
	return t.AccessToken, nil
}

// savingSource 每次拿到新 token（刷新后）就落盘。
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
	save func(*oauth2.Token)
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		s.save(t)
	}
	return t, nil
}
