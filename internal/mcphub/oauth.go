package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// 远程 MCP（比如 Notion 的 https://mcp.notion.com/mcp）用 OAuth 授权：
//
//  1. 连接时服务端回 401，SDK 发现授权服务器、动态注册客户端，生成授权地址；
//  2. 这个 server 进入 needs_auth 状态，设置页显示「授权」按钮，用户在浏览器里同意；
//  3. 授权服务器跳回 Shuttle 的 /_shuttle/oauth/callback，code 交还给等待中的连接，连接继续。
//
// 换到的 token 连同刷新要用的客户端信息存在 ~/.shuttle/mcp-auth/<server>.json（0600），
// 重启后直接用；过期自动刷新，刷新失败会重新走一遍授权。

const authWait = 10 * time.Minute

type pendingAuth struct {
	url   string
	state string
	done  chan *auth.AuthorizationResult
}

type savedAuth struct {
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret,omitempty"`
	AuthURL      string        `json:"auth_url"`
	TokenURL     string        `json:"token_url"`
	AuthStyle    int           `json:"auth_style,omitempty"`
	Token        *oauth2.Token `json:"token"`
}

func (h *Hub) authFile(name string) string { return filepath.Join(h.dir, "mcp-auth", name+".json") }

func (h *Hub) loadAuth(name string) *savedAuth {
	b, err := os.ReadFile(h.authFile(name))
	if err != nil {
		return nil
	}
	var a savedAuth
	if json.Unmarshal(b, &a) != nil || a.Token == nil {
		return nil
	}
	return &a
}

func (h *Hub) saveAuth(name string, a *savedAuth) {
	if err := os.MkdirAll(filepath.Dir(h.authFile(name)), 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(a)
	os.WriteFile(h.authFile(name), b, 0o600)
}

// Logout 删掉保存的授权，下次连接重新授权。
func (h *Hub) Logout(name string) error {
	err := os.Remove(h.authFile(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// savingSource 包一层 TokenSource：每次拿到新 token（刷新后）就落盘。
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
	save func(*oauth2.Token)
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		s.save(t)
	}
	s.mu.Unlock()
	return t, nil
}

func (h *Hub) oauthHandler(s *server) (auth.OAuthHandler, error) {
	redirect := h.CallbackURL
	persist := func(cfg *oauth2.Config, tok *oauth2.Token) {
		h.saveAuth(s.name, &savedAuth{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			AuthURL: cfg.Endpoint.AuthURL, TokenURL: cfg.Endpoint.TokenURL, AuthStyle: int(cfg.Endpoint.AuthStyle),
			Token: tok,
		})
	}
	wrap := func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) oauth2.TokenSource {
		c := *cfg
		return &savingSource{src: c.TokenSource(context.WithoutCancel(ctx), tok), last: tok.AccessToken, save: func(t *oauth2.Token) { persist(&c, t) }}
	}
	conf := &auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:              "Annulo",
				RedirectURIs:            []string{redirect},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "none",
			},
		},
		RedirectURL:         redirect,
		RequestRefreshToken: true,
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			persist(cfg, tok)
			return wrap(ctx, cfg, tok), nil
		},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return h.waitForUser(ctx, s, args.URL)
		},
	}
	if a := h.loadAuth(s.name); a != nil {
		cfg := &oauth2.Config{ClientID: a.ClientID, ClientSecret: a.ClientSecret,
			Endpoint: oauth2.Endpoint{AuthURL: a.AuthURL, TokenURL: a.TokenURL, AuthStyle: oauth2.AuthStyle(a.AuthStyle)}}
		conf.InitialTokenSource = wrap(context.Background(), cfg, a.Token)
	}
	return auth.NewAuthorizationCodeHandler(conf)
}

// waitForUser 把授权地址挂到 server 上等用户去点，直到回调带着 code 回来或超时。
func (h *Hub) waitForUser(ctx context.Context, s *server, authURL string) (*auth.AuthorizationResult, error) {
	p := &pendingAuth{url: authURL, state: stateOf(authURL), done: make(chan *auth.AuthorizationResult, 1)}
	h.mu.Lock()
	s.status, s.pending = "needs_auth", p
	h.mu.Unlock()
	h.changed()
	defer func() {
		h.mu.Lock()
		if s.pending == p {
			s.pending = nil
			if s.status == "needs_auth" {
				s.status = "connecting"
			}
		}
		h.mu.Unlock()
	}()
	select {
	case r := <-p.done:
		return r, nil
	case <-ctx.Done():
		return nil, errors.New("等待授权超时")
	}
}

// Callback 处理授权服务器的跳转，按 state 找到等待中的连接。
func (h *Hub) Callback(state, code, iss, errMsg string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.servers {
		if s.pending != nil && s.pending.state == state {
			if errMsg != "" {
				return errors.New(errMsg)
			}
			s.pending.done <- &auth.AuthorizationResult{Code: code, State: state, Iss: iss}
			return nil
		}
	}
	return i18n.New("这个授权链接已经用过或者过期了（比如刷新了授权完成的页面）。设置 → MCP 里已经连上的话就不用管；没连上就在那里重新授权", "This authorization link was already used or has expired (e.g. you refreshed the completion page). If it's connected in Settings → MCP, you're fine; otherwise authorize again there")
}
