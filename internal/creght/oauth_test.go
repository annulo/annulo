package creght

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 假的平台 OAuth：动态注册、授权码 + PKCE 换 token、refresh token 每次换新、旧的再用就 invalid_grant。
func fakePlatform(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var refreshes atomic.Int32
	var mu sync.Mutex
	valid := map[string]bool{}
	n := 0
	issue := func(w http.ResponseWriter) {
		n++
		rt := fmt.Sprintf("r%d", n)
		valid[rt] = true
		fmt.Fprintf(w, `{"access_token":"a%d","refresh_token":"%s","expires_in":3600,"token_type":"Bearer"}`, n, rt)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/oauth/register":
			w.WriteHeader(201)
			w.Write([]byte(`{"client_id":"dcr_1"}`))
		case "/api/oauth/token":
			r.ParseForm()
			mu.Lock()
			defer mu.Unlock()
			if r.Form.Get("resource") == "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_target"}`))
				return
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				if r.Form.Get("code") != "good" || r.Form.Get("code_verifier") == "" || r.Form.Get("client_id") != "dcr_1" {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				issue(w)
			case "refresh_token":
				refreshes.Add(1)
				rt := r.Form.Get("refresh_token")
				if !valid[rt] {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				delete(valid, rt) // 用过就作废
				issue(w)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &refreshes
}

func TestOAuthFlow(t *testing.T) {
	t.Setenv("ANNULO_DIR", t.TempDir())
	srv, refreshes := fakePlatform(t)
	ctx := context.Background()
	if _, err := ReadToken(srv.URL); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("没连过：%v", err)
	}
	authURL, state, err := StartOAuth(ctx, srv.URL, "http://127.0.0.1:7799/_shuttle/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if u.Path != "/api/oauth/authorize" || q.Get("client_id") != "dcr_1" || q.Get("resource") != srv.URL+"/api" || q.Get("code_challenge_method") != "S256" || !strings.HasPrefix(state, OAuthStatePrefix) || q.Get("scope") != OAuthScopes {
		t.Fatalf("授权地址：%s", authURL)
	}
	if _, err := FinishOAuth(ctx, state, "good", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishOAuth(ctx, state, "good", ""); err == nil {
		t.Fatal("同一个 state 只能用一次")
	}
	if tok, err := ReadToken(srv.URL); err != nil || tok != "a1" {
		t.Fatalf("连上了：%q %v", tok, err)
	}

	// 快过期：几个人同时要 token，只刷新一次（旧 refresh token 用两次整条授权就作废了）
	f, _ := readOAuth(normHost(srv.URL))
	f.Expiry = time.Now().Add(10 * time.Second)
	writeOAuth(f)
	var wg sync.WaitGroup
	toks := make([]string, 8)
	for i := range toks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			toks[i], _ = ReadToken(srv.URL)
		}(i)
	}
	wg.Wait()
	for _, tk := range toks {
		if tk != "a2" {
			t.Fatalf("都拿到刷新后的那个：%v", toks)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("只刷新一次：%d", refreshes.Load())
	}

	// refresh token 失效：要重新连接
	f, _ = readOAuth(normHost(srv.URL))
	f.Expiry, f.RefreshToken = time.Now(), "stale"
	writeOAuth(f)
	if _, err := ReadToken(srv.URL); !errors.Is(err, ErrReconnect) {
		t.Fatalf("授权失效：%v", err)
	}
	if _, err := ReadToken(srv.URL); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("失效以后当没连：%v", err)
	}

	// 断开：token 删掉，client id 留着
	FinishOAuth(ctx, "x", "", "")
	_, st2, _ := StartOAuth(ctx, srv.URL, "http://127.0.0.1:7799/_shuttle/oauth/callback")
	FinishOAuth(ctx, st2, "good", "")
	if err := Disconnect(srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(srv.URL); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("断开了：%v", err)
	}
	if f, _ := readOAuth(normHost(srv.URL)); f.ClientID != "dcr_1" {
		t.Fatal("client id 留着")
	}
}

func TestCLIEnv(t *testing.T) {
	t.Setenv("ANNULO_DIR", t.TempDir())
	env := strings.Join(CLIEnv("https://creght.cn"), "\n")
	if !strings.Contains(env, "CREGHT_API_HOST=https://creght.cn") || strings.Contains(env, "CREGHT_TOKEN=") {
		t.Fatalf("没连：不给 token：%s", env)
	}
	StoreToken("https://creght.cn", "tok", time.Now().Add(time.Hour))
	if env := strings.Join(CLIEnv("https://creght.cn"), "\n"); !strings.Contains(env, "CREGHT_TOKEN=tok") {
		t.Fatalf("连上了：给 token：%s", env)
	}
}
