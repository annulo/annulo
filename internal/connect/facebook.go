package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"golang.org/x/oauth2"
)

// FacebookAppID 是公开的应用 ID。App Secret 只放在 creght 服务端的 OAuth 中转，绝不能打进桌面客户端。
const FacebookAppID = "1602666168024506"

// FacebookConfigID 是 Meta Business Login 的 User access token 配置。
const FacebookConfigID = "1448473380737254"

const facebookGraphURL = "https://graph.facebook.com/v26.0"

var facebookID = regexp.MustCompile(`^[0-9]+$`)

var facebookHTTPClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// FacebookBrokerURL 是 creght 服务端的 OAuth 中转（weave）：它持有 App Secret，完成 Meta 的服务端 code exchange，
// 再把 Meta 一次性 client code 交给本机。接口是 <URL>/facebook/start 和 <URL>/facebook/callback，见 docs/facebook-oauth-broker.md。
// 空着时设置 → 连接 里不出现 Facebook：Meta 应用过审前只有应用角色账号能授权，联调时用环境变量 ANNULO_FACEBOOK_OAUTH_BROKER_URL 打开。
var FacebookBrokerURL string

// facebookProvider 没配中转地址（或地址不对）时返回 nil，不列出 Facebook。
func facebookProvider() *Provider {
	base := FacebookBrokerURL
	if v := brand.Env("FACEBOOK_OAUTH_BROKER_URL"); v != "" {
		base = v
	}
	if base == "" {
		return nil
	}
	p, err := newFacebookProvider(base, facebookGraphURL, FacebookAppID)
	if err != nil {
		return nil
	}
	return p
}

func newFacebookProvider(broker, graph, appID string) (*Provider, error) {
	p := &Provider{
		Key: "facebook", Name: "Facebook",
		Desc:      "授权 Facebook 账号后，由项目插件选择公共主页并管理内容。",
		descEn:    "Connect a Facebook account so project plugins can select and manage Pages.",
		Scopes:    []string{"pages_show_list", "pages_read_engagement", "pages_manage_posts"},
		noRefresh: true,
	}
	base, err := brokerBase(broker)
	if err != nil {
		return nil, err
	}
	p.oauth.ClientID = appID // List.Available；Meta 的 App Secret 不在这里
	callback := base + "/facebook/callback"
	p.authorize = func(state, _ string, redirect string) (string, error) {
		u, _ := url.Parse(base + "/facebook/start")
		q := u.Query()
		q.Set("state", state)
		q.Set("return_to", redirect)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	p.exchange = func(ctx context.Context, code, _, _ string) (*oauth2.Token, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if code == "" || len(code) > 4096 || strings.ContainsAny(code, "\r\n") {
			return nil, fmt.Errorf("Facebook authorization code is missing or invalid")
		}
		u, _ := url.Parse(graph + "/oauth/access_token")
		q := u.Query()
		q.Set("client_id", appID)
		q.Set("redirect_uri", callback)
		q.Set("code", code)
		u.RawQuery = q.Encode()
		var body struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := facebookJSON(ctx, facebookHTTPClient, u.String(), "", &body); err != nil {
			return nil, err
		}
		if body.AccessToken == "" {
			return nil, fmt.Errorf("Facebook did not return an access token")
		}
		expiry, err := facebookTokenExpiry(ctx, graph, appID, body.AccessToken, body.ExpiresIn)
		if err != nil {
			return nil, err
		}
		return &oauth2.Token{AccessToken: body.AccessToken, TokenType: "Bearer", Expiry: expiry}, nil
	}
	p.inspect = func(ctx context.Context, tok *oauth2.Token) (string, []string, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var me struct {
			ID string `json:"id"`
		}
		if err := facebookJSON(ctx, facebookHTTPClient, graph+"/me?fields=id", tok.AccessToken, &me); err != nil {
			return "", nil, err
		}
		if !facebookID.MatchString(me.ID) {
			return "", nil, fmt.Errorf("Facebook did not return an account ID")
		}
		var perms struct {
			Data []struct {
				Permission string `json:"permission"`
				Status     string `json:"status"`
			} `json:"data"`
		}
		if err := facebookJSON(ctx, facebookHTTPClient, graph+"/me/permissions?limit=100", tok.AccessToken, &perms); err != nil {
			return "", nil, err
		}
		granted := []string{}
		for _, v := range perms.Data {
			if v.Status == "granted" {
				granted = append(granted, v.Permission)
			}
		}
		return me.ID, granted, nil
	}
	return p, nil
}

// Meta 再次授权时可能不返回 expires_in。不能把缺字段当作无限期：
// 用同一 User token 检查自身的 metadata，核对应用、类型、有效性和到期时间。
// expires_at 明确为 0 才表示不按时间过期；data_access_expires_at 有值时仍以它为上限。
func facebookTokenExpiry(ctx context.Context, graph, appID, token string, expiresIn int64) (time.Time, error) {
	if expiresIn != 0 {
		if expiresIn <= 300 || expiresIn > 365*24*60*60 {
			return time.Time{}, fmt.Errorf("Facebook did not return a usable long-lived token")
		}
		return time.Now().Add(time.Duration(expiresIn) * time.Second), nil
	}
	u, _ := url.Parse(graph + "/debug_token")
	q := u.Query()
	q.Set("input_token", token)
	u.RawQuery = q.Encode()
	var body struct {
		Data struct {
			Valid             bool   `json:"is_valid"`
			AppID             string `json:"app_id"`
			Type              string `json:"type"`
			UserID            string `json:"user_id"`
			ExpiresAt         *int64 `json:"expires_at"`
			DataAccessExpires *int64 `json:"data_access_expires_at"`
		} `json:"data"`
	}
	if err := facebookJSON(ctx, facebookHTTPClient, u.String(), token, &body); err != nil {
		return time.Time{}, err
	}
	d := body.Data
	if !d.Valid || d.AppID != appID || d.Type != "USER" || !facebookID.MatchString(d.UserID) || d.ExpiresAt == nil || *d.ExpiresAt < 0 || (d.DataAccessExpires != nil && *d.DataAccessExpires < 0) {
		return time.Time{}, fmt.Errorf("Facebook token metadata is invalid")
	}
	var expiry time.Time
	if *d.ExpiresAt > 0 {
		expiry = time.Unix(*d.ExpiresAt, 0)
	}
	if d.DataAccessExpires != nil && *d.DataAccessExpires > 0 {
		accessExpiry := time.Unix(*d.DataAccessExpires, 0)
		if expiry.IsZero() || accessExpiry.Before(expiry) {
			expiry = accessExpiry
		}
	}
	if !expiry.IsZero() && (time.Until(expiry) <= 5*time.Minute || time.Until(expiry) > 365*24*time.Hour) {
		return time.Time{}, fmt.Errorf("Facebook token metadata has an unusable expiry")
	}
	return expiry, nil
}

// 中转可以挂在路径前缀下（比如 https://api.example.com/api/annulo/oauth），不能带 query、fragment、userinfo。
func brokerBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("invalid Facebook OAuth broker URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("Facebook OAuth broker must use HTTPS")
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// 仅报告状态码和 Meta 错误码；请求 URL 可能带一次性 code，错误正文也可能带凭据。
func facebookJSON(ctx context.Context, client *http.Client, endpoint, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid Facebook request")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Facebook request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Facebook response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &failure)
		return fmt.Errorf("Facebook returned HTTP %d (error %d)", resp.StatusCode, failure.Error.Code)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("invalid Facebook response")
	}
	return nil
}
