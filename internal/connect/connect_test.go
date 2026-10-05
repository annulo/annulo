package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	s := New(t.TempDir())
	p := &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth: oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	s.providers["demo"] = p
	ctx := context.Background()

	if _, err := s.Token(ctx, "demo", ""); !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "设置 → 连接") {
		t.Fatalf("没连接时的错误：%v", err)
	}
	u, err := s.Start("demo", "http://127.0.0.1:1/_shuttle/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	state := q.Query().Get("state")
	if !strings.HasPrefix(state, StatePrefix) || q.Query().Get("code_challenge") == "" || q.Query().Get("access_type") != "offline" {
		t.Fatalf("授权地址：%s", u)
	}
	if !s.List()[0].Pending {
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
	tok, err := s.Token(ctx, "demo", "")
	if err != nil || tok != "a1" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	// 刷新后落盘，重开 Store 还能用
	s2 := New(s.dir[:len(s.dir)-len("/connections")])
	s2.providers["demo"] = p
	if v := s2.load("demo", ""); v == nil || v.Token.AccessToken != "a1" || v.Token.RefreshToken != "r1" {
		t.Fatalf("落盘的 token：%+v", v)
	}
	if in := s2.List(); !in[0].Connected {
		t.Error("应该显示已连接")
	}
	if err := s2.Disconnect("demo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Token(ctx, "demo", ""); !errors.Is(err, ErrNotConnected) {
		t.Errorf("断开后：%v", err)
	}
}

func TestConnectDenied(t *testing.T) {
	s := New(t.TempDir())
	s.providers["demo"] = &Provider{Key: "demo", Name: "Demo", oauth: oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: "http://x/auth", TokenURL: "http://x/token"}}}
	u, _ := s.Start("demo", "http://127.0.0.1:1/cb")
	q, _ := url.Parse(u)
	if _, err := s.Callback(context.Background(), q.Query().Get("state"), "", "access_denied"); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Errorf("拒绝授权：%v", err)
	}
	if _, err := s.Start("nope", "x"); err == nil {
		t.Error("不存在的连接")
	}
	s.providers["demo"].oauth.ClientID = ""
	if _, err := s.Start("demo", "x"); err == nil {
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
	s := New(t.TempDir())
	who := ""
	p := &Provider{Key: "demo", Name: "Demo", Scopes: []string{"x"},
		oauth:   oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}},
		account: func(context.Context, *http.Client) (string, error) { return who, nil }}
	s.providers["demo"] = p
	ctx := context.Background()
	connect := func(account string) {
		t.Helper()
		who = account
		u, _ := s.Start("demo", "http://127.0.0.1:1/cb")
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
	if got, _ := s.Accounts("demo"); len(got) != 2 || got[0] != "a@x.com" || got[1] != "B@x.com" {
		t.Fatalf("accounts = %v", got)
	}
	if in := s.List()[0]; !in.Connected || len(in.Accounts) != 2 {
		t.Fatalf("list = %+v", in)
	}
	for _, acc := range []string{"", "a@x.com", "b@x.com"} {
		if tok, err := s.Token(ctx, "demo", acc); err != nil || tok == "" {
			t.Fatalf("%q: token=%q err=%v", acc, tok, err)
		}
	}
	if _, err := s.Token(ctx, "demo", "c@x.com"); !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "a@x.com") {
		t.Fatalf("没连的账号：%v", err)
	}
	if err := s.Disconnect("demo", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Accounts("demo"); len(got) != 1 || got[0] != "B@x.com" {
		t.Fatalf("断开一个后：%v", got)
	}
	if err := s.Disconnect("demo", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Accounts("demo"); len(got) != 0 {
		t.Fatalf("全部断开后：%v", got)
	}
}

// 老版本存在 connections/<provider>.json：读的时候挪进 <provider>/<账号>.json
func TestMigrateSingleFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.providers["demo"] = &Provider{Key: "demo", Name: "Demo"}
	os.MkdirAll(filepath.Join(dir, "connections"), 0o700)
	b, _ := json.Marshal(saved{Token: &oauth2.Token{AccessToken: "old", RefreshToken: "r"}, Account: "me@x.com", ConnectedAt: time.Now()})
	os.WriteFile(filepath.Join(dir, "connections", "demo.json"), b, 0o600)
	if got, _ := s.Accounts("demo"); len(got) != 1 || got[0] != "me@x.com" {
		t.Fatalf("accounts = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "connections", "demo.json")); !os.IsNotExist(err) {
		t.Error("旧文件应该挪走")
	}
	if _, err := os.Stat(filepath.Join(dir, "connections", "demo", "me@x.com.json")); err != nil {
		t.Error(err)
	}
}
