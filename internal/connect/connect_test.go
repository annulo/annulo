package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// 假的授权服务器：authorization_code 换 token，refresh_token 刷新（每次给新的 access token）
func fakeOAuth(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("content-type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if !strings.HasPrefix(r.Form.Get("code"), "good") || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "a0", "refresh_token": "r1", "expires_in": -1, "token_type": "Bearer"})
		case "refresh_token":
			if !strings.HasPrefix(r.Form.Get("refresh_token"), "r1") {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			k := n.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "a" + string(rune('0'+k)), "expires_in": 3600, "token_type": "Bearer"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestConnectFlow(t *testing.T) {
	srv, _ := fakeOAuth(t)
	s := New(t.TempDir(), nil)
	p := &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth: oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	s.providers["demo"] = p
	ctx := context.Background()

	if _, err := s.Token(ctx, "p1", "demo", ""); !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "设置 → 连接") {
		t.Fatalf("没连接时的错误：%v", err)
	}
	u, err := s.Start("p1", "demo", "http://127.0.0.1:1/_shuttle/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	state := q.Query().Get("state")
	if !strings.HasPrefix(state, StatePrefix) || q.Query().Get("code_challenge") == "" || q.Query().Get("access_type") != "offline" {
		t.Fatalf("授权地址：%s", u)
	}
	if !s.List("p1")[0].Pending {
		t.Error("应该显示等待授权")
	}
	if _, err := s.Callback(ctx, "conn_nope", "good", ""); err == nil {
		t.Error("不认识的 state 应该报错")
	}
	if _, err := s.Callback(ctx, state, "good", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Callback(ctx, state, "good", ""); err == nil {
		t.Error("state 只能用一次")
	}
	// 换到的 access token 已过期（expires_in -1），第一次取就刷新
	tok, err := s.Token(ctx, "p1", "demo", "")
	if err != nil || tok != "a1" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	// 刷新后落盘，重开 Store 还能用
	s2 := New(s.dir[:len(s.dir)-len("/connections")], nil)
	s2.providers["demo"] = p
	if v := s2.load("p1", "demo", ""); v == nil || v.Token.AccessToken != "a1" || v.Token.RefreshToken != "r1" {
		t.Fatalf("落盘的 token：%+v", v)
	}
	if in := s2.List("p1"); !in[0].Connected {
		t.Error("应该显示已连接")
	}
	if err := s2.Disconnect("p1", "demo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Token(ctx, "p1", "demo", ""); !errors.Is(err, ErrNotConnected) {
		t.Errorf("断开后：%v", err)
	}
}

func TestConnectDenied(t *testing.T) {
	s := New(t.TempDir(), nil)
	s.providers["demo"] = &Provider{Key: "demo", Name: "Demo", oauth: oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: "http://x/auth", TokenURL: "http://x/token"}}}
	u, _ := s.Start("p1", "demo", "http://127.0.0.1:1/cb")
	q, _ := url.Parse(u)
	if _, err := s.Callback(context.Background(), q.Query().Get("state"), "", "access_denied"); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Errorf("拒绝授权：%v", err)
	}
	u, _ = s.Start("p1", "demo", "http://127.0.0.1:1/cb")
	q, _ = url.Parse(u)
	s.pending[q.Query().Get("state")].at = time.Now().Add(-16 * time.Minute)
	if _, err := s.Callback(context.Background(), q.Query().Get("state"), "good", ""); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Errorf("过期的 state 不应换 token：%v", err)
	}
	if _, err := s.Start("p1", "nope", "x"); err == nil {
		t.Error("不存在的连接")
	}
	s.providers["demo"].oauth.ClientID = ""
	if _, err := s.Start("p1", "demo", "x"); err == nil {
		t.Error("没有客户端时应该报错")
	}
}

func TestMissingScopes(t *testing.T) {
	got := missing([]string{"a", "b", "c"}, []string{"a", "c"})
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("missing = %v", got)
	}
	if missing([]string{"a"}, []string{"a", "z"}) != nil {
		t.Fatal("全有时应该是空")
	}
}

// 连两个账号：各存各的、各自刷新，不指定账号用最早连的；同一个账号再连是覆盖；可以只断开一个
func TestMultipleAccounts(t *testing.T) {
	srv, _ := fakeOAuth(t)
	s := New(t.TempDir(), nil)
	who := ""
	p := &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth:   oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}},
		account: func(context.Context, *http.Client) (string, error) { return who, nil }}
	s.providers["demo"] = p
	ctx := context.Background()
	connect := func(account string) {
		t.Helper()
		who = account
		u, _ := s.Start("p1", "demo", "http://127.0.0.1:1/cb")
		q, _ := url.Parse(u)
		if q.Query().Get("prompt") != "select_account consent" {
			t.Fatalf("要让用户选账号：%s", u)
		}
		if _, err := s.Callback(ctx, q.Query().Get("state"), "good", ""); err != nil {
			t.Fatal(err)
		}
	}
	connect("a@x.com")
	time.Sleep(10 * time.Millisecond)
	connect("B@x.com")
	connect("a@x.com") // 补权限：覆盖，不变成新的默认
	if got, _ := s.Accounts("p1", "demo"); len(got) != 2 || got[0] != "a@x.com" || got[1] != "B@x.com" {
		t.Fatalf("accounts = %v", got)
	}
	if in := s.List("p1")[0]; !in.Connected || len(in.Accounts) != 2 {
		t.Fatalf("list = %+v", in)
	}
	for _, acc := range []string{"", "a@x.com", "b@x.com"} {
		if tok, err := s.Token(ctx, "p1", "demo", acc); err != nil || tok == "" {
			t.Fatalf("%q: token=%q err=%v", acc, tok, err)
		}
	}
	if _, err := s.Token(ctx, "p1", "demo", "c@x.com"); !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "a@x.com") {
		t.Fatalf("没连的账号：%v", err)
	}
	if err := s.Disconnect("p1", "demo", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Accounts("p1", "demo"); len(got) != 1 || got[0] != "B@x.com" {
		t.Fatalf("断开一个后：%v", got)
	}
	if err := s.Disconnect("p1", "demo", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Accounts("p1", "demo"); len(got) != 0 {
		t.Fatalf("全部断开后：%v", got)
	}
}

// 以前连接整台电脑共用（connections/<provider>/、更早的 connections/<provider>.json）：拷给每个项目，删掉老的；creght 的不动
func TestMigrateGlobal(t *testing.T) {
	dir := t.TempDir()
	conns := filepath.Join(dir, "connections")
	write := func(path, account string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		b, _ := json.Marshal(saved{Token: &oauth2.Token{AccessToken: "old", RefreshToken: "r"}, Account: account, ConnectedAt: time.Now()})
		os.WriteFile(path, b, 0o600)
	}
	write(filepath.Join(conns, "google.json"), "me@x.com")
	write(filepath.Join(conns, "google", "b@x.com.json"), "b@x.com")
	write(filepath.Join(conns, "creght", "token.json"), "")
	s := New(dir, []string{"p1", "p2"})
	for _, pid := range []string{"p1", "p2"} {
		if got, _ := s.Accounts(pid, "google"); len(got) != 2 {
			t.Fatalf("%s accounts = %v", pid, got)
		}
	}
	if got, _ := s.Accounts("p3", "google"); len(got) != 0 {
		t.Fatalf("新项目不该有老连接：%v", got)
	}
	for _, f := range []string{"google.json", "google"} {
		if _, err := os.Stat(filepath.Join(conns, f)); !os.IsNotExist(err) {
			t.Errorf("老的 %s 应该删掉", f)
		}
	}
	if _, err := os.Stat(filepath.Join(conns, "creght", "token.json")); err != nil {
		t.Error("creght 的授权不该动：", err)
	}
}

// 连接按项目分开：A 项目连的账号 B 项目看不到、用不了，断开 A 不影响 B
func TestPerProject(t *testing.T) {
	srv, _ := fakeOAuth(t)
	s := New(t.TempDir(), nil)
	n := 0
	s.providers["demo"] = &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth: oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}},
		account: func(context.Context, *http.Client) (string, error) {
			n++
			return []string{"a@x.com", "b@x.com", "a@x.com"}[n-1], nil
		}}
	ctx := context.Background()
	connect := func(pid string) {
		u, err := s.Start(pid, "demo", "http://127.0.0.1:1/cb")
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		if _, err := s.Callback(ctx, pu.Query().Get("state"), "good", ""); err != nil {
			t.Fatal(err)
		}
	}
	connect("A")
	connect("B")
	connect("B") // 同一个 Google 账号 a@x.com 两个项目都连
	if got, _ := s.Accounts("A", "demo"); len(got) != 1 || got[0] != "a@x.com" {
		t.Fatalf("A = %v", got)
	}
	if got, _ := s.Accounts("B", "demo"); len(got) != 2 {
		t.Fatalf("B = %v", got)
	}
	if _, err := s.Token(ctx, "A", "demo", "b@x.com"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("A 不该拿到 B 的账号：%v", err)
	}
	if err := s.Disconnect("A", "demo", ""); err != nil {
		t.Fatal(err)
	}
	if tok, err := s.Token(ctx, "B", "demo", "a@x.com"); err != nil || tok == "" {
		t.Fatalf("断开 A 不该影响 B：%q %v", tok, err)
	}
	if _, err := s.Start("", "demo", "x"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("没项目时：%v", err)
	}
}

func TestFacebookClientCodeConnection(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/access_token":
			if r.URL.Query().Get("code") != "one-use-client-code" || r.URL.Query().Get("client_id") != "app-id" || r.URL.Query().Get("redirect_uri") != "http://127.0.0.1:9999/facebook/callback" {
				http.Error(w, `{"error":{"code":190}}`, http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"access_token":"long-user-token","expires_in":5184000}`))
		case "/me":
			if r.Header.Get("Authorization") != "Bearer long-user-token" {
				http.Error(w, `{"error":{"code":190}}`, http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"id":"12345"}`))
		case "/me/permissions":
			w.Write([]byte(`{"data":[{"permission":"pages_show_list","status":"granted"},{"permission":"pages_read_engagement","status":"granted"},{"permission":"pages_manage_posts","status":"declined"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer graph.Close()
	p, err := newFacebookProvider("http://127.0.0.1:9999", graph.URL, "app-id")
	if err != nil {
		t.Fatal(err)
	}
	s := New(t.TempDir(), nil)
	s.providers["facebook"] = p
	u, err := s.Start("p1", "facebook", "http://127.0.0.1:8000/_shuttle/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	q := parsed.Query()
	if parsed.Path != "/facebook/start" || q.Get("state") == "" || q.Get("return_to") != "http://127.0.0.1:8000/_shuttle/oauth/callback" || q.Get("access_type") != "" || q.Get("prompt") != "" {
		t.Fatalf("Facebook authorization URL: %s", u)
	}
	if _, err := s.Callback(context.Background(), q.Get("state"), "one-use-client-code", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Accounts("p1", "facebook"); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("accounts=%v", got)
	}
	if tok, err := s.Token(context.Background(), "p1", "facebook", "12345"); err != nil || tok != "long-user-token" {
		t.Fatalf("token=%q error=%v", tok, err)
	}
	in := s.List("p1")
	var fb Info
	for _, v := range in {
		if v.Key == "facebook" {
			fb = v
		}
	}
	if len(fb.Accounts) != 1 || len(fb.Accounts[0].MissingScopes) != 1 || fb.Accounts[0].MissingScopes[0] != "pages_manage_posts" || fb.Accounts[0].NeedsReconnect {
		t.Fatalf("Facebook connection status: %+v", fb)
	}
	v := s.load("p1", "facebook", "12345")
	v.Token.Expiry = time.Now().Add(4 * time.Minute)
	if err := s.save("p1", "facebook", v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token(context.Background(), "p1", "facebook", "12345"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("expired Facebook token should require reconnect: %v", err)
	}
	for _, v := range s.List("p1") {
		if v.Key == "facebook" && !v.Accounts[0].NeedsReconnect {
			t.Fatal("expired account should show reconnect")
		}
	}
}

func TestFacebookBrokerURLValidation(t *testing.T) {
	for _, raw := range []string{"", "http://example.com", "https://user@example.com", "https://example.com?x=1", "https://example.com#x"} {
		if _, err := newFacebookProvider(raw, "https://graph.facebook.com/v26.0", "app-id"); err == nil {
			t.Errorf("accepted invalid broker URL %q", raw)
		}
	}
}

func TestFacebookBrokerPathPrefix(t *testing.T) {
	p, err := newFacebookProvider("https://api.example.com/api/annulo/oauth/", "https://graph.facebook.com/v26.0", "app-id")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := p.authorize("conn_x", "", "http://127.0.0.1:8000/_shuttle/oauth/callback")
	if !strings.HasPrefix(u, "https://api.example.com/api/annulo/oauth/facebook/start?") {
		t.Fatalf("start URL = %s", u)
	}
}

func TestFacebookCodeExchangeRejectsRedirect(t *testing.T) {
	var followed atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer sink.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer graph.Close()
	p, err := newFacebookProvider("http://127.0.0.1:9999", graph.URL, "app-id")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.exchange(context.Background(), "sensitive-client-code", "", "")
	if err == nil || followed.Load() || strings.Contains(err.Error(), "sensitive-client-code") {
		t.Fatalf("unsafe redirect or error: followed=%v err=%v", followed.Load(), err)
	}
}

func TestFacebookExchangeWithoutExpiresIn(t *testing.T) {
	future := time.Now().Add(60 * 24 * time.Hour).Unix()
	accessExpiry := time.Now().Add(30 * 24 * time.Hour).Unix()
	base := map[string]any{"is_valid": true, "app_id": "app-id", "type": "USER", "user_id": "12345", "expires_at": future}
	for _, tc := range []struct {
		name  string
		patch map[string]any
		want  int64
		bad   bool
		omit  bool
	}{
		{name: "expiry recovered", want: future},
		{name: "data access limits expiry", patch: map[string]any{"data_access_expires_at": accessExpiry}, want: accessExpiry},
		{name: "nonexpiring with data limit", patch: map[string]any{"expires_at": 0, "data_access_expires_at": accessExpiry}, want: accessExpiry},
		{name: "explicit nonexpiring", patch: map[string]any{"expires_at": 0}},
		{name: "unknown expiry", omit: true, bad: true},
		{name: "invalid", patch: map[string]any{"is_valid": false}, bad: true},
		{name: "wrong app", patch: map[string]any{"app_id": "other-app"}, bad: true},
		{name: "Page token", patch: map[string]any{"type": "PAGE"}, bad: true},
		{name: "missing user", patch: map[string]any{"user_id": ""}, bad: true},
		{name: "expired", patch: map[string]any{"expires_at": time.Now().Add(-time.Hour).Unix()}, bad: true},
		{name: "data access expired", patch: map[string]any{"data_access_expires_at": 1}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{}
			for k, v := range base {
				data[k] = v
			}
			for k, v := range tc.patch {
				data[k] = v
			}
			if tc.omit {
				delete(data, "expires_at")
			}
			graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/oauth/access_token":
					w.Write([]byte(`{"access_token":"user-token"}`))
				case "/debug_token":
					if r.URL.Query().Get("input_token") != "user-token" || r.Header.Get("Authorization") != "Bearer user-token" {
						t.Error("token inspection must use the exchanged token")
					}
					json.NewEncoder(w).Encode(map[string]any{"data": data})
				default:
					t.Error("unexpected Graph request")
				}
			}))
			defer graph.Close()
			p, err := newFacebookProvider("http://127.0.0.1:9999", graph.URL, "app-id")
			if err != nil {
				t.Fatal(err)
			}
			token, err := p.exchange(context.Background(), "client-code", "", "")
			if tc.bad {
				if err == nil || token != nil || strings.Contains(err.Error(), "user-token") {
					t.Fatalf("invalid metadata accepted or credential exposed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == 0 && !token.Expiry.IsZero() || tc.want != 0 && token.Expiry.Unix() != tc.want {
				t.Fatalf("expiry=%v want=%d", token.Expiry, tc.want)
			}
		})
	}
}

func TestConnectionRemainsPendingUntilCallbackCompletes(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	p, _ := newFacebookProvider("http://127.0.0.1:9999", "https://graph.facebook.com", "app-id")
	p.exchange = func(context.Context, string, string, string) (*oauth2.Token, error) {
		close(entered)
		<-release
		return nil, errors.New("exchange failed")
	}
	s := New(t.TempDir(), nil)
	s.providers["facebook"] = p
	u, err := s.Start("p1", "facebook", "http://127.0.0.1:8000/_shuttle/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	state := parsed.Query().Get("state")
	done := make(chan struct{})
	go func() { defer close(done); s.Callback(context.Background(), state, "code", "") }()
	<-entered
	for _, info := range s.List("p1") {
		if info.Key == "facebook" && !info.Pending {
			t.Fatal("connection stopped being pending before exchange finished")
		}
	}
	if _, err := s.Callback(context.Background(), state, "replay", ""); err == nil {
		t.Fatal("in-progress state must not be reusable")
	}
	release <- struct{}{}
	<-done
	for _, info := range s.List("p1") {
		if info.Key == "facebook" && info.Pending {
			t.Fatal("failed callback should clear pending")
		}
	}
}

// 平台明确说不按时间过期的 token（Facebook expires_at=0）到期时间是零值：不能当成已过期，否则连上就提示重连。
func TestTokenExpiredZeroExpiry(t *testing.T) {
	if tokenExpired(&oauth2.Token{AccessToken: "x"}) {
		t.Fatal("zero expiry means no time-based expiry")
	}
	if !tokenExpired(&oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Minute)}) {
		t.Fatal("expiring within five minutes counts as expired")
	}
	if !tokenExpired(&oauth2.Token{}) {
		t.Fatal("empty token is expired")
	}
}

// 会轮换 refresh token 的授权服务器：每次刷新发新的 refresh token，旧的立刻作废（最严格的情况）
func rotatingOAuth(t *testing.T, expires int) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	var mu sync.Mutex
	valid := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("content-type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			rt := "rt-code-" + r.Form.Get("code")
			valid[rt] = true
			json.NewEncoder(w).Encode(map[string]any{"access_token": "a0", "refresh_token": rt, "expires_in": -1, "token_type": "Bearer"})
		case "refresh_token":
			rt := r.Form.Get("refresh_token")
			if !valid[rt] {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			delete(valid, rt)
			k := n.Add(1)
			next := fmt.Sprintf("rt-%d", k)
			valid[next] = true
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("a%d", k), "refresh_token": next, "expires_in": expires, "token_type": "Bearer"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func demoProvider(srv *httptest.Server, account string) *Provider {
	return &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth:   oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}},
		account: func(context.Context, *http.Client) (string, error) { return account, nil }}
}

func connectWith(t *testing.T, s *Store, project, code string) {
	t.Helper()
	u, err := s.Start(project, "demo", "http://127.0.0.1:1/cb")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	if _, err := s.Callback(context.Background(), pu.Query().Get("state"), code, ""); err != nil {
		t.Fatal(err)
	}
}

// 从以前全局连接拷给 A、B 的是同一份授权：A 刷新换了 refresh token，B 不能被顶掉
func TestSharedGrantRotation(t *testing.T) {
	srv, _ := rotatingOAuth(t, 3600)
	dir := t.TempDir()
	s := New(dir, nil)
	s.providers["demo"] = demoProvider(srv, "a@x.com")
	connectWith(t, s, "legacy", "c1")
	// 模拟升级前的全局连接：挪到 connections/demo/，再让 New 拷给 A、B
	os.Rename(filepath.Join(dir, "connections", "projects", "legacy", "demo"), filepath.Join(dir, "connections", "demo"))
	s = New(dir, []string{"A", "B"})
	s.providers["demo"] = demoProvider(srv, "a@x.com")
	s.migrateGlobal([]string{"A", "B"}) // New 时还没有 demo 这个 provider，这里再迁一次
	ctx := context.Background()
	for i, pid := range []string{"A", "B", "A", "B", "B", "A"} {
		if tok, err := s.Token(ctx, pid, "demo", ""); err != nil || tok == "" {
			t.Fatalf("第 %d 次 %s：%q %v", i, pid, tok, err)
		}
		// 让 token 过期，逼下一次刷新
		for _, p := range []string{"A", "B"} {
			v := s.load(p, "demo", "")
			v.Token.Expiry = time.Now().Add(-time.Hour)
			s.save(p, "demo", v)
		}
	}
}

// 同一个 Google 账号，A、B 各自授权：各是各的 refresh token，A 刷新、断开都不影响 B
func TestSameAccountIndependentGrants(t *testing.T) {
	srv, _ := rotatingOAuth(t, 3600)
	s := New(t.TempDir(), nil)
	s.providers["demo"] = demoProvider(srv, "a@x.com")
	connectWith(t, s, "A", "c1")
	connectWith(t, s, "B", "c2")
	ctx := context.Background()
	if s.load("A", "demo", "").Grant == s.load("B", "demo", "").Grant {
		t.Fatal("各自授权应该是不同的 Grant")
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Token(ctx, "A", "demo", ""); err != nil {
			t.Fatal(err)
		}
		v := s.load("A", "demo", "")
		v.Token.Expiry = time.Now().Add(-time.Hour)
		s.save("A", "demo", v)
	}
	if tok, err := s.Token(ctx, "B", "demo", ""); err != nil || tok == "" {
		t.Fatalf("A 刷新了几次后 B：%q %v", tok, err)
	}
}

// 并发取 token：同一份授权只刷新一次，大家拿到同一个
func TestConcurrentRefreshOnce(t *testing.T) {
	srv, n := rotatingOAuth(t, 3600)
	s := New(t.TempDir(), nil)
	s.providers["demo"] = demoProvider(srv, "a@x.com")
	connectWith(t, s, "A", "c1")
	v := s.load("A", "demo", "")
	v.Token.Expiry = time.Now().Add(-time.Hour)
	s.save("A", "demo", v)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	before := n.Load()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Token(context.Background(), "A", "demo", ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal("并发刷新被顶掉：", err)
	}
	if got := n.Load() - before; got != 1 {
		t.Fatalf("刷新了 %d 次，应该只刷一次", got)
	}
}
