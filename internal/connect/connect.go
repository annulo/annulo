// Package connect 是「连接」：用户在设置页授权一次外部账号（比如 Google），本机函数用 await ctx.oauth('google')
// 拿到 access token，自己调对方的 REST 接口（Search Console、GA4…）。
//
// 为什么是 Shuttle 的原语：OAuth 要一个客户端（client id）、一个本机回调地址、token 的保存和刷新，
// 这些和具体业务无关；本机函数里又没法自己做（不能起回调服务、不能存文件）。
// 哪些接口、怎么读数据是业务，写在工作空间的本机函数里，Shuttle 不认识 GSC。
//
// 流程：设置页点「连接」→ Start 生成授权地址（Google 用 PKCE，Facebook 经固定 HTTPS 中转）→ 用户在浏览器里同意
// → 跳回 /_shuttle/oauth/callback → Callback 用 code 换 token，存到 ~/.annulo/connections/projects/<项目>/<provider>/<账号>.json（0600）。
// 支持 refresh token 的 provider 自动刷新；否则到期后重连。
//
// 连接按项目分开：一个项目一个业务，A 项目连 A 的 Google、B 项目连 B 的，本机函数只拿得到自己项目连的账号。
// 同一个账号两个项目都要用，就在两个项目里各授权一次，互不影响（断开一边另一边照常）。creght 账号不在这里，是整台电脑共用的（internal/creght/oauth.go）。
//
// 一个项目里一种连接也可以连多个账号（比如几个网站在不同的 Google 账号下）：再点一次连接、选另一个账号就加一个，
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
	// account 用 token 查稳定的账号标识（Google 邮箱、Facebook 用户 ID 等）
	account func(ctx context.Context, c *http.Client) (string, error)
	// 特殊 provider 可以自行生成授权地址、换 token 和核对实际获授予的权限。
	authorize func(state, verifier, redirect string) (string, error)
	exchange  func(ctx context.Context, code, verifier, redirect string) (*oauth2.Token, error)
	inspect   func(ctx context.Context, token *oauth2.Token) (string, []string, error)
	noRefresh bool // Meta 的长期 User token 到期后重新授权，不走 Google 的 refresh_token 流程
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
	out := []*Provider{google}
	if fb := facebookProvider(); fb != nil {
		out = append(out, fb)
	}
	return out
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
	Account        string    `json:"account"` // 稳定账号标识；对方没给时是空的
	ConnectedAt    time.Time `json:"connected_at"`
	NeedsReconnect bool      `json:"needs_reconnect,omitempty"`
	// 连接时还没有、后来新加的权限（比如加了 GA4）：要重新连接一次才有
	MissingScopes []string `json:"missing_scopes,omitempty"`
}

type saved struct {
	// Grant 是这份授权（一次在对方那里同意拿到的 refresh token）的 id。几个项目的文件 Grant 一样，说明共用同一个 refresh token
	// （从以前整台电脑共用的连接拷过来的）：刷新时按 Grant 加锁，刷完写回所有共用它的项目，refresh token 轮换了也不会把别的项目顶掉。
	// 各项目自己连的（哪怕是同一个 Google 账号）是各自的 Grant、各自的 refresh token，互不相干。
	Grant       string        `json:"grant,omitempty"`
	Token       *oauth2.Token `json:"token"`
	Scopes      []string      `json:"scopes"`
	Account     string        `json:"account,omitempty"`
	ConnectedAt time.Time     `json:"connected_at"`
}

type pending struct {
	project   string // 在哪个项目里点的连接，授权存到这个项目
	provider  *Provider
	verifier  string
	redirect  string
	at        time.Time
	finishing bool // state 已消费，换取和保存凭据还在进行；List 仍显示 pending
}

// Store 管所有项目的连接：授权、保存、刷新。每个方法都带项目 id。
type Store struct {
	dir       string // ~/.annulo/connections
	providers map[string]*Provider

	mu      sync.Mutex
	pending map[string]*pending // 按 state
}

// New 打开连接。projects 是本机已有的项目：以前的连接整台电脑共用，第一次启动时拷给每个项目（见 migrateGlobal）。
func New(shuttleDir string, projects []string) *Store {
	s := &Store{dir: filepath.Join(shuttleDir, "connections"), providers: map[string]*Provider{}, pending: map[string]*pending{}}
	for _, p := range providers() {
		s.providers[p.Key] = p
	}
	s.loadClients()
	s.migrateGlobal(projects)
	return s
}

// ErrNoProject：还没打开项目。连接是项目的，没有项目就没处存。
var ErrNoProject = i18n.New("先打开一个项目：连接是按项目分开的", "Open a project first: connections are per project")

// root 是一个项目的连接目录。项目 id 当目录名，不能用的字符换成 _
func (s *Store) root(project string) string {
	return filepath.Join(s.dir, "projects", safeName(project))
}

func safeName(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@._-+", r) {
			return r
		}
		return '_'
	}, v)
}

// migrateGlobal：以前连接整台电脑共用，存在 connections/<provider>/（更早是 connections/<provider>.json）。
// 拷给本机每个项目（升级后照常能用，不用的在那个项目里断开），然后删掉老的。
// 只动这里认识的 provider：connections/creght/ 是 creght 账号，仍然整台电脑共用。
func (s *Store) migrateGlobal(projects []string) {
	for key := range s.providers {
		var list []*saved
		if v := readSaved(filepath.Join(s.dir, key+".json")); v != nil {
			list = append(list, v)
		}
		ents, _ := os.ReadDir(filepath.Join(s.dir, key))
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				if v := readSaved(filepath.Join(s.dir, key, e.Name())); v != nil {
					list = append(list, v)
				}
			}
		}
		if len(list) == 0 {
			continue
		}
		for _, v := range list {
			if v.Grant == "" {
				v.Grant = newGrant() // 拷给几个项目的是同一份授权：同一个 Grant，刷新时一起更新
			}
		}
		ok := true
		for _, pid := range projects {
			if pid == "" {
				continue
			}
			for _, v := range list {
				if readSaved(s.file(pid, key, v.Account)) != nil {
					continue // 这个项目已经有这个账号（拷过一次没删干净）：用项目自己的
				}
				if s.save(pid, key, v) != nil {
					ok = false
				}
			}
		}
		if ok {
			os.Remove(filepath.Join(s.dir, key+".json"))
			os.RemoveAll(filepath.Join(s.dir, key))
		}
	}
}

// RemoveProject 删掉一个项目的全部连接（项目从本机删除时）。
func (s *Store) RemoveProject(project string) error {
	if project == "" {
		return nil
	}
	return os.RemoveAll(s.root(project))
}

// StatePrefix 标出回调是连接的，不是 MCP 的：两边共用 /_shuttle/oauth/callback。
const StatePrefix = "conn_"

// file 是一个项目里一个账号的授权文件。账号名当文件名，不能用的字符换成 _；对方没给账号名时叫 default
func (s *Store) file(project, key, account string) string {
	name := safeName(strings.ToLower(account))
	if name == "" || strings.Trim(name, ".") == "" {
		name = "default"
	}
	return filepath.Join(s.root(project), key, name+".json")
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

// accounts 是一个项目里一种连接下连上的账号，最早连的在前。
func (s *Store) accounts(project, key string) []*saved {
	if project == "" {
		return nil
	}
	dir := filepath.Join(s.root(project), key)
	ents, _ := os.ReadDir(dir)
	var out []*saved
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			if v := readSaved(filepath.Join(dir, e.Name())); v != nil {
				out = append(out, v)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

// load 取一个账号的授权；account 空是最早连的那个。账号名不分大小写
func (s *Store) load(project, key, account string) *saved {
	list := s.accounts(project, key)
	for _, v := range list {
		if account == "" || strings.EqualFold(v.Account, account) {
			return v
		}
	}
	return nil
}

func (s *Store) save(project, key string, v *saved) error {
	f := s.file(project, key, v.Account)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return writeFile(f, b)
}

// Accounts 是一个项目里一种连接下连上的账号名（ctx.oauth.accounts），最早连的在前。
func (s *Store) Accounts(project, key string) ([]string, error) {
	if _, err := s.provider(key); err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range s.accounts(project, key) {
		out = append(out, v.Account)
	}
	return out, nil
}

// List 是一个项目的连接状态。
func (s *Store) List(project string) []Info {
	s.mu.Lock()
	waiting := map[string]bool{}
	for _, p := range s.pending {
		if p.project == project && time.Since(p.at) < 15*time.Minute {
			waiting[p.provider.Key] = true
		}
	}
	s.mu.Unlock()
	var out []Info
	for _, p := range s.providers {
		in := Info{Key: p.Key, Name: p.Name, Desc: i18n.T(p.Desc, p.descEn), Scopes: p.Scopes, Available: p.oauth.ClientID != "", Pending: waiting[p.Key], Accounts: []Account{}}
		for _, v := range s.accounts(project, p.Key) {
			in.Accounts = append(in.Accounts, Account{Account: v.Account, ConnectedAt: v.ConnectedAt, MissingScopes: missing(p.Scopes, v.Scopes), NeedsReconnect: p.noRefresh && tokenExpired(v.Token)})
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

// Start 生成授权地址，授权完存到 project。redirect 是本机的 /_shuttle/oauth/callback。
func (s *Store) Start(project, key, redirect string) (string, error) {
	if project == "" {
		return "", ErrNoProject
	}
	p, err := s.provider(key)
	if err != nil {
		return "", err
	}
	if p.oauth.ClientID == "" {
		return "", i18n.Errorf("这个版本的 Annulo 尚未配置 %s 授权，暂时连不了", "This build of Annulo has no %s authorization configured yet", p.Name)
	}
	state := StatePrefix + oauth2.GenerateVerifier()[:24]
	verifier := oauth2.GenerateVerifier()
	cfg := p.oauth
	cfg.RedirectURL, cfg.Scopes = redirect, p.Scopes
	var authURL string
	if p.authorize != nil {
		authURL, err = p.authorize(state, verifier, redirect)
	} else {
		// access_type=offline + prompt=consent：Google 只有这样才每次都给 refresh token；select_account：每次都让用户选账号。
		authURL = cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "select_account consent"))
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	for st, x := range s.pending {
		if time.Since(x.at) > 15*time.Minute {
			delete(s.pending, st)
		}
	}
	s.pending[state] = &pending{project: project, provider: p, verifier: verifier, redirect: redirect, at: time.Now()}
	s.mu.Unlock()
	return authURL, nil
}

// Callback 用授权码换 token 并保存。返回连上的是哪个连接的名字。
func (s *Store) Callback(ctx context.Context, state, code, errMsg string) (string, error) {
	s.mu.Lock()
	p := s.pending[state]
	if p == nil || p.finishing || time.Since(p.at) > 15*time.Minute {
		p = nil
	} else {
		p.finishing = true
	}
	s.mu.Unlock()
	if p == nil {
		return "", i18n.New("这个授权链接已经用过或者过期了（比如刷新了授权完成的页面）。设置 → 连接 里已经显示「已连接」的话就不用管；没连上就在那里重新连接", "This authorization link was already used or has expired (e.g. you refreshed the completion page). If Settings → Connections shows \"Connected\", you're fine; otherwise reconnect there")
	}
	defer func() {
		s.mu.Lock()
		delete(s.pending, state)
		s.mu.Unlock()
	}()
	if errMsg != "" {
		if errMsg == "access_denied" {
			errMsg = i18n.T("你在授权页上选了拒绝", "You chose Deny on the authorization page")
		}
		return p.provider.Name, errors.New(errMsg)
	}
	cfg := p.provider.oauth
	cfg.RedirectURL, cfg.Scopes = p.redirect, p.provider.Scopes
	var tok *oauth2.Token
	var err error
	if p.provider.exchange != nil {
		tok, err = p.provider.exchange(ctx, code, p.verifier, p.redirect)
	} else {
		tok, err = cfg.Exchange(ctx, code, oauth2.VerifierOption(p.verifier))
	}
	if err != nil {
		return p.provider.Name, i18n.Errorf("换取授权失败：%w", "Failed to exchange the authorization: %w", err)
	}
	v := &saved{Grant: newGrant(), Token: tok, Scopes: p.provider.Scopes, ConnectedAt: time.Now()}
	if p.provider.inspect != nil {
		v.Account, v.Scopes, err = p.provider.inspect(ctx, tok)
		if err != nil {
			return p.provider.Name, err
		}
	} else if p.provider.account != nil {
		v.Account, _ = p.provider.account(ctx, cfg.Client(ctx, tok))
	}
	// 同一个账号再连（补权限）：覆盖它，连接时间不变（不改变默认账号是哪个）
	if old := readSaved(s.file(p.project, p.provider.Key, v.Account)); old != nil {
		v.ConnectedAt = old.ConnectedAt
		if tok.RefreshToken == "" && !p.provider.noRefresh {
			// 没有 refresh token 一小时后就得重连；保留旧的（重新授权时 Google 有时不再给），还是旧的那份授权
			tok.RefreshToken = old.Token.RefreshToken
			v.Grant = old.Grant
		}
	}
	if err := s.save(p.project, p.provider.Key, v); err != nil {
		return p.provider.Name, err
	}
	return p.provider.Name, nil
}

func newGrant() string { return oauth2.GenerateVerifier()[:16] }

// Disconnect 删掉一个项目里保存的一个账号的 token；account 空是这种连接的全部账号。别的项目连的同一个账号不受影响。
// 不去对方那里撤销授权：用户要撤销在对方的账号页里做（撤销了所有项目都用不了）。
func (s *Store) Disconnect(project, key, account string) error {
	if _, err := s.provider(key); err != nil {
		return err
	}
	if project == "" {
		return ErrNoProject
	}
	if account == "" {
		return os.RemoveAll(filepath.Join(s.root(project), key))
	}
	for _, v := range s.accounts(project, key) {
		if strings.EqualFold(v.Account, account) {
			return os.Remove(s.file(project, key, v.Account))
		}
	}
	return nil
}

// ErrNotConnected：还没连接。本机函数里抛出去，页面据此提示去设置。
var ErrNotConnected = errors.New("not connected")

// Token 返回一个项目里一个账号可用的 access token（快过期时自动刷新，刷新后落盘）。account 空是最早连的那个。
func (s *Store) Token(ctx context.Context, project, key, account string) (string, error) {
	p, err := s.provider(key)
	if err != nil {
		return "", err
	}
	v := s.load(project, key, account)
	if v == nil {
		if account == "" {
			return "", i18n.Errorf("这个项目还没连接 %s：到 Annulo 的 设置 → 连接 里连接 %s（%w）", "%s isn't connected in this project: connect %s in Annulo → Settings → Connections (%w)", p.Name, p.Name, ErrNotConnected)
		}
		have, _ := s.Accounts(project, key)
		list := strings.Join(have, i18n.T("、", ", "))
		if list == "" {
			list = i18n.T("没有", "none")
		}
		return "", i18n.Errorf("%s 账号 %s 没连接（这个项目连着的：%s）：到 Annulo 的 设置 → 连接 里连接这个账号（%w）", "%s account %s isn't connected (connected in this project: %s): connect it in Annulo → Settings → Connections (%w)",
			p.Name, account, list, ErrNotConnected)
	}
	if p.noRefresh {
		if tokenExpired(v.Token) {
			return "", i18n.Errorf("%s 的授权已过期：到 Annulo 的 设置 → 连接 里重新连接（%w）", "%s authorization has expired: reconnect in Annulo → Settings → Connections (%w)", p.Name, ErrNotConnected)
		}
		return v.Token.AccessToken, nil
	}
	if fresh(v.Token) {
		return v.Token.AccessToken, nil
	}
	t, err := s.refresh(ctx, p, project, v)
	if err != nil {
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

// fresh：access token 还能用一分钟以上，不用刷新
func fresh(t *oauth2.Token) bool {
	return t != nil && t.AccessToken != "" && time.Until(t.Expiry) > time.Minute
}

// refresh 用 refresh token 换新的 access token。每次都从文件读，不在内存里留 token：
// 共用一份授权的几个项目、几个进程（App、annulo 命令）谁刷新了，别的马上读到新的。
// 按授权加跨进程文件锁：锁里重读，别人刚刷过就直接用；刷完写回所有共用这份授权的项目（refresh token 轮换时也不会顶掉别的项目）。
func (s *Store) refresh(ctx context.Context, p *Provider, project string, v *saved) (*oauth2.Token, error) {
	lock := v.Grant
	if lock == "" {
		lock = safeName(project) + "-" + p.Key + "-" + safeName(strings.ToLower(v.Account))
	}
	unlock, err := lockFile(filepath.Join(s.dir, "locks", lock+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	cur := readSaved(s.file(project, p.Key, v.Account))
	if cur == nil {
		return nil, ErrNotConnected // 刚被断开
	}
	if fresh(cur.Token) {
		return cur.Token, nil
	}
	cfg := p.oauth
	cfg.Scopes = p.Scopes
	t, err := cfg.TokenSource(context.WithoutCancel(ctx), &oauth2.Token{RefreshToken: cur.Token.RefreshToken}).Token()
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = cur.Token.RefreshToken
	}
	if cur.Grant == "" {
		cur.Token = t
		return t, s.save(project, p.Key, cur)
	}
	// 共用这份授权的项目（包括自己）都写上新 token
	files, _ := filepath.Glob(filepath.Join(s.dir, "projects", "*", p.Key, "*.json"))
	for _, f := range files {
		if x := readSaved(f); x != nil && x.Grant == cur.Grant {
			x.Token = t
			b, _ := json.MarshalIndent(x, "", "  ")
			writeFile(f, b)
		}
	}
	return t, nil
}

// writeFile 先写临时文件再改名：别的进程读的时候不会读到写了一半的
func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// 非刷新型 token 的到期时间由 provider 换 token 时核实过（Facebook 见 facebookTokenExpiry）：
// 零值是平台明确说了不按时间过期，不算过期；有到期时间的预留五分钟，避免拿到后在 API 调用中途过期。
func tokenExpired(t *oauth2.Token) bool {
	return t == nil || t.AccessToken == "" || !t.Expiry.IsZero() && time.Until(t.Expiry) <= 5*time.Minute
}

// OAuth 客户端也能在页面上配（服务端部署：用部署者自己在 Google Cloud 建的「Web 应用」客户端，回调是服务器的 https 域名；
// AGENTS.md「服务端部署：配置都在页面上」）。存在 connections/clients.json，覆盖打包注入的和环境变量的。

type clientConf struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (s *Store) clientsFile() string { return filepath.Join(s.dir, "clients.json") }

// loadClients 启动时把页面上配过的客户端套上。
func (s *Store) loadClients() {
	b, err := os.ReadFile(s.clientsFile())
	if err != nil {
		return
	}
	var m map[string]clientConf
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for key, c := range m {
		if p := s.providers[key]; p != nil && c.ClientID != "" {
			p.oauth.ClientID, p.oauth.ClientSecret = c.ClientID, c.ClientSecret
		}
	}
}

// Client 是这种连接现在用的客户端 id，以及是不是页面上配的（custom）。不返回密钥。
func (s *Store) Client(key string) (id string, custom bool, ok bool) {
	p := s.providers[key]
	if p == nil {
		return "", false, false
	}
	var m map[string]clientConf
	if b, err := os.ReadFile(s.clientsFile()); err == nil {
		json.Unmarshal(b, &m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, custom = m[key]
	return p.oauth.ClientID, custom, true
}

// SetClient 在页面上配一种连接的 OAuth 客户端（只有 google 这类自带客户端的才行）。secret 为空表示不改原来的；id 为空表示删掉、回到打包带的。
func (s *Store) SetClient(key, id, secret string) error {
	p := s.providers[key]
	if p == nil || p.authorize != nil {
		return i18n.Errorf("%s 不能在这里配客户端", "%s's client can't be configured here", key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]clientConf
	if b, err := os.ReadFile(s.clientsFile()); err == nil {
		json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]clientConf{}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		delete(m, key)
		for _, def := range providers() { // 回到打包带的 / 环境变量的
			if def.Key == key {
				p.oauth.ClientID, p.oauth.ClientSecret = def.oauth.ClientID, def.oauth.ClientSecret
			}
		}
	} else {
		c := m[key]
		c.ClientID = id
		if secret = strings.TrimSpace(secret); secret != "" {
			c.ClientSecret = secret
		}
		if c.ClientSecret == "" {
			return i18n.New("还要填客户端密钥（client secret）", "The client secret is required too")
		}
		m[key] = c
		p.oauth.ClientID, p.oauth.ClientSecret = c.ClientID, c.ClientSecret
	}
	os.MkdirAll(s.dir, 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(s.clientsFile(), b, 0o600)
}
