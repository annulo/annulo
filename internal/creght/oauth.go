package creght

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// creght 连接（docs/annulo-plan.md 第 5 步）：creght 是 设置 → 连接 里的一项，走平台的 OAuth（原生客户端：PKCE 公开客户端、回环地址回调）。
// 不再用 creght CLI 的设备码登录，也不读写 CLI 的 config.json：token 存在 App 自己的数据目录 connections/creght/ 下，一个集群一个文件。
//
//   - 客户端按集群动态注册一次（POST /api/oauth/register），client id 记在同一个文件里；
//   - 授权时必须带 resource={api_host}/api，拿到的 token 才能打平台接口；
//   - access token 1 小时，refresh token 30 天、每次刷新都换新的、旧的立刻作废，同一个旧 refresh token 用两次整条授权作废。
//     所以刷新要跨进程串行（App 和命令行 annulo push 是两个进程）：文件锁里先重读一遍，别人刚刷过就直接用。

// OAuthScopes 是向平台要的权限：模板、项目和站点、业务表、平台模型、远程访问、MCP 读写。
const OAuthScopes = "profile templates projects tables llm remote site:read site:write"

// ErrReconnect：授权失效（refresh token 过期、被作废），要用户在 设置 → 连接 里重新连接 creght。
var ErrReconnect = errors.New("creght: reconnect required")

type oauthFile struct {
	Host         string    `json:"host"`
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}

// tokenDir 是 creght 连接的目录（数据目录下，和 config.Load 一样，brand.DataDir）。
func tokenDir() string {
	return filepath.Join(brand.DataDir(), "connections", "creght")
}

func oauthPath(host string) string {
	sum := sha256.Sum256([]byte(normHost(host)))
	return filepath.Join(tokenDir(), hex.EncodeToString(sum[:6])+".json")
}

func readOAuth(host string) (*oauthFile, error) {
	var b []byte
	err := retryShared(func() (err error) { b, err = os.ReadFile(oauthPath(host)); return })
	if err != nil {
		return nil, err
	}
	var f oauthFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func writeOAuth(f *oauthFile) error {
	p := oauthPath(f.Host)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return retryShared(func() error { return os.Rename(tmp, p) })
}

// retryShared 在 Windows 上重试一会儿：别的进程正在读写 token 文件时，读和 rename 会撞上共享冲突（ERROR_SHARING_VIOLATION /
// ERROR_ACCESS_DENIED），等它放手就好；文件不在不重试。
func retryShared(op func() error) error {
	err := op()
	for i := 0; err != nil && runtime.GOOS == "windows" && !errors.Is(err, os.ErrNotExist) && i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		err = op()
	}
	return err
}

// oauthToken 是 host 的 access token，快过期就刷新；没连过返回 ErrNotLoggedIn，授权失效返回 ErrReconnect。
func oauthToken(ctx context.Context, host string) (string, error) {
	f, err := readOAuth(host)
	if err != nil || f.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	if f.AccessToken != "" && time.Until(f.Expiry) > time.Minute {
		return f.AccessToken, nil
	}
	unlock, err := lockFile(oauthPath(host) + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if f, err = readOAuth(host); err != nil || f.RefreshToken == "" { // 锁里重读：别的进程可能刚刷过
		return "", ErrNotLoggedIn
	}
	if f.AccessToken != "" && time.Until(f.Expiry) > time.Minute {
		return f.AccessToken, nil
	}
	tok, err := tokenRequest(ctx, f.Host, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {f.RefreshToken}, "client_id": {f.ClientID}, "resource": {f.Host + "/api"}})
	if err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && oe.Code == "invalid_grant" {
			f.AccessToken, f.RefreshToken = "", ""
			writeOAuth(f)
			return "", ErrReconnect
		}
		return "", err
	}
	tok.apply(f)
	if err := writeOAuth(f); err != nil {
		return "", err
	}
	return f.AccessToken, nil
}

type oauthError struct {
	Code, Desc string
}

func (e *oauthError) Error() string {
	if e.Desc != "" {
		return "creght OAuth: " + e.Code + ": " + e.Desc
	}
	return "creght OAuth: " + e.Code
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (t *tokenResp) apply(f *oauthFile) {
	f.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		f.RefreshToken = t.RefreshToken
	}
	f.Expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	if t.Scope != "" {
		f.Scope = t.Scope
	}
}

var oauthHTTP = &http.Client{Timeout: 30 * time.Second}

func tokenRequest(ctx context.Context, host string, form url.Values) (*tokenResp, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, &oauthError{Code: e.Error, Desc: e.Desc}
		}
		return nil, fmt.Errorf("creght OAuth: HTTP %d: %.200s", resp.StatusCode, b)
	}
	var t tokenResp
	if err := json.Unmarshal(b, &t); err != nil || t.AccessToken == "" {
		return nil, fmt.Errorf("creght OAuth: bad token response: %.200s", b)
	}
	return &t, nil
}

// registerClient 在集群上注册一次原生客户端（回环地址的端口不参与匹配），client id 记下来以后接着用。
func registerClient(ctx context.Context, host string) (string, error) {
	if f, err := readOAuth(host); err == nil && f.ClientID != "" {
		return f.ClientID, nil
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                brand.Name,
		"redirect_uris":              []string{"http://127.0.0.1" + brand.URLPrefix + "oauth/callback"},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/oauth/register", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r struct {
		ClientID string `json:"client_id"`
	}
	if resp.StatusCode/100 != 2 || json.Unmarshal(b, &r) != nil || r.ClientID == "" {
		return "", i18n.Errorf("在 %s 注册客户端失败：HTTP %d %.200s", "Registering the client on %s failed: HTTP %d %.200s", host, resp.StatusCode, b)
	}
	f, _ := readOAuth(host)
	if f == nil {
		f = &oauthFile{Host: host}
	}
	f.ClientID = r.ClientID
	return r.ClientID, writeOAuth(f)
}

// OAuthPending 是一次进行中的授权：浏览器跳回本机时用 state 找到它。
type OAuthPending struct {
	Host, ClientID, Verifier, Redirect string
	At                                 time.Time
}

var pendingOAuth = struct {
	sync.Mutex
	m map[string]*OAuthPending
}{m: map[string]*OAuthPending{}}

// OAuthStatePrefix 标出回调是 creght 连接的（回调地址和别的连接、MCP 共用）。
const OAuthStatePrefix = "creght."

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// StartOAuth 生成授权页地址（用户在浏览器里打开），redirect 是本机的 /_shuttle/oauth/callback（带端口）。
func StartOAuth(ctx context.Context, apiHost, redirect string) (authURL, state string, err error) {
	host := normHost(apiHost)
	cid, err := registerClient(ctx, host)
	if err != nil {
		return "", "", err
	}
	verifier := randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	state = OAuthStatePrefix + randToken(16)
	pendingOAuth.Lock()
	for k, p := range pendingOAuth.m {
		if time.Since(p.At) > 15*time.Minute {
			delete(pendingOAuth.m, k)
		}
	}
	pendingOAuth.m[state] = &OAuthPending{Host: host, ClientID: cid, Verifier: verifier, Redirect: redirect, At: time.Now()}
	pendingOAuth.Unlock()
	q := url.Values{
		"response_type": {"code"}, "client_id": {cid}, "redirect_uri": {redirect}, "scope": {OAuthScopes},
		"resource": {host + "/api"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	return host + "/api/oauth/authorize?" + q.Encode(), state, nil
}

// FinishOAuth 处理授权页跳回来的 code：换 token、存下来。返回连上的集群。
func FinishOAuth(ctx context.Context, state, code, errCode string) (string, error) {
	pendingOAuth.Lock()
	p := pendingOAuth.m[state]
	delete(pendingOAuth.m, state)
	pendingOAuth.Unlock()
	if p == nil {
		return "", i18n.New("授权链接已经用过或者过期了：回到设置里重新连接 creght", "This authorization link was already used or has expired: connect creght again from Settings")
	}
	if errCode != "" {
		return p.Host, i18n.Errorf("creght 没有授权：%s", "creght didn't authorize: %s", errCode)
	}
	tok, err := tokenRequest(ctx, p.Host, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.Redirect},
		"client_id": {p.ClientID}, "code_verifier": {p.Verifier}, "resource": {p.Host + "/api"}})
	if err != nil {
		return p.Host, err
	}
	f := &oauthFile{Host: p.Host, ClientID: p.ClientID}
	tok.apply(f)
	return p.Host, writeOAuth(f)
}

// Disconnect 断开 host 上的 creght 连接：删掉本机的 token（client id 留着，下次连接不用再注册）。
func Disconnect(apiHost string) error {
	host := normHost(apiHost)
	f, err := readOAuth(host)
	if err != nil {
		return nil
	}
	f.AccessToken, f.RefreshToken, f.Expiry, f.Scope = "", "", time.Time{}, ""
	return writeOAuth(f)
}

// CLIEnv 是调 creght 命令行时给的环境变量：集群，和 creght 连接的 token（creght-cli 0.22 起认 CREGHT_TOKEN，
// 用户不用在命令行里再登录一次）；不让它在后台自动升级。没连就不给 token，命令行用它自己的登录。
func CLIEnv(apiHost string) []string {
	env := []string{"CREGHT_API_HOST=" + normHost(apiHost), "CREGHT_NO_AUTO_UPDATE=1"}
	if tok, err := ReadToken(apiHost); err == nil {
		env = append(env, "CREGHT_TOKEN="+tok)
	}
	return env
}

// StoreToken 直接存一份 host 的 access token（refresh token 填个占位）：测试里装作已经连上 creght 用。
func StoreToken(apiHost, accessToken string, expiry time.Time) error {
	host := normHost(apiHost)
	return writeOAuth(&oauthFile{Host: host, ClientID: "test", AccessToken: accessToken, RefreshToken: "test", Expiry: expiry})
}
