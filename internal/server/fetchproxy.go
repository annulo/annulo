package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 页面请求外部接口的代理：浏览器直接 fetch 外站会被跨域拦下来，运营后台的页面把请求交给本机 Shuttle，
// 由 Shuttle 用用户自己的网络发出去，再把结果原样带回。
//
// 只有同源、带 X-Shuttle 头的请求能进来（guard 保证），也就是只有从 Shuttle 打开的后台页面能用。
// 默认不许访问本机和内网：页面代码是 agent 写的，不能让它顺手去探测用户本机的其他服务。
// 确实要访问内网接口时，启动时设 SHUTTLE_FETCH_ALLOW_PRIVATE=1。

const (
	fetchTimeout  = 30 * time.Second
	fetchMaxBody  = 5 << 20
	fetchMaxInput = 2 << 20
)

type fetchReq struct {
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodyBase64 string            `json:"body_base64"`
}

type fetchResp struct {
	OK         bool              `json:"ok"`
	Status     int               `json:"status"`
	StatusText string            `json:"status_text"`
	URL        string            `json:"url"` // 跟随跳转后的最终地址
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body,omitempty"`        // 文本内容
	BodyBase64 string            `json:"body_base64,omitempty"` // 二进制内容
	Truncated  bool              `json:"truncated,omitempty"`   // 超过 5MB 被截断
}

var errPrivateAddr = i18n.New("不允许访问本机或内网地址（启动时设 SHUTTLE_FETCH_ALLOW_PRIVATE=1 可以放开）", "Access to localhost or private network addresses isn't allowed (set SHUTTLE_FETCH_ALLOW_PRIVATE=1 at startup to allow it)")

func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func allowPrivate() bool { return brand.Env("FETCH_ALLOW_PRIVATE") == "1" }

// proxyAddrs 是环境变量里配置的 HTTP 代理地址。国内常见 127.0.0.1:7890 这类本机代理，
// 连它们要放行，否则「禁止访问本机」会把所有请求都挡掉。
func proxyAddrs() map[string]bool {
	out := map[string]bool{}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if v := os.Getenv(k); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				host, port := u.Hostname(), u.Port()
				if port == "" {
					port = map[string]string{"https": "443", "socks5": "1080"}[u.Scheme]
					if port == "" {
						port = "80"
					}
				}
				for _, ip := range resolve(host) {
					out[net.JoinHostPort(ip.String(), port)] = true
				}
			}
		}
	}
	return out
}

func resolve(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	ips, _ := net.LookupIP(host)
	return ips
}

// checkTarget 在发请求前解析目标域名：走代理时建连接的是代理，只能在这里挡内网目标。
func checkTarget(host string) error {
	if allowPrivate() {
		return nil
	}
	for _, ip := range resolve(host) {
		if privateIP(ip) {
			return errPrivateAddr
		}
	}
	return nil
}

// fetchClient 在真正建连接时再检查一次目标 IP（直连时有效），挡住解析结果和实际连接不一致的绕法；
// 每次跳转都重新检查目标。
func fetchClient() *http.Client { return newFetchClient(fetchTimeout) }

// newFetchClient：timeout 是整个请求（含读完响应体）的时限，0 = 不限，由调用方自己管（流式读，见 localFetch）
func newFetchClient(timeout time.Duration) *http.Client {
	proxies := proxyAddrs()
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if proxies[address] || allowPrivate() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && privateIP(ip) {
				return errPrivateAddr
			}
			return nil
		},
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = http.ProxyFromEnvironment
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return i18n.New("跳转次数太多", "Too many redirects")
			}
			return checkTarget(req.URL.Hostname())
		},
	}
}

var hopHeaders = map[string]bool{"connection": true, "keep-alive": true, "proxy-connection": true, "transfer-encoding": true, "upgrade": true, "te": true, "trailer": true, "host": true, "content-length": true}

func (s *Server) apiFetch(w http.ResponseWriter, r *http.Request) {
	var in fetchReq
	b, _ := io.ReadAll(io.LimitReader(r.Body, fetchMaxInput))
	if err := json.Unmarshal(b, &in); err != nil {
		fail(w, http.StatusBadRequest, i18n.New("请求体要是 JSON：{url, method, headers, body}", "The request body must be JSON: {url, method, headers, body}"))
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail(w, http.StatusBadRequest, i18n.Errorf("url 要是完整的 http(s) 地址，收到的是 %q", "url must be a full http(s) URL; got %q", in.URL))
		return
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	switch {
	case in.BodyBase64 != "":
		raw, err := base64.StdEncoding.DecodeString(in.BodyBase64)
		if err != nil {
			fail(w, http.StatusBadRequest, i18n.New("body_base64 不是合法的 base64", "body_base64 isn't valid base64"))
			return
		}
		body = strings.NewReader(string(raw))
	case in.Body != "":
		body = strings.NewReader(in.Body)
	}
	if err := checkTarget(u.Hostname()); err != nil {
		fail(w, http.StatusBadGateway, i18n.Errorf("请求 %s 失败：%s", "Request to %s failed: %s", u.Host, err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	for k, v := range in.Headers {
		if !hopHeaders[strings.ToLower(k)] {
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Annulo)")
	}
	resp, err := fetchClient().Do(req)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, errPrivateAddr) || strings.Contains(msg, errPrivateAddr.Error()) {
			msg = errPrivateAddr.Error()
		}
		fail(w, http.StatusBadGateway, i18n.Errorf("请求 %s 失败：%s", "Request to %s failed: %s", u.Host, msg))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody+1))
	out := fetchResp{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		URL:        resp.Request.URL.String(),
		Headers:    map[string]string{},
	}
	if len(raw) > fetchMaxBody {
		raw, out.Truncated = raw[:fetchMaxBody], true
	}
	for k, v := range resp.Header {
		if k == "Set-Cookie" {
			continue // 外站的 cookie 不带回页面
		}
		out.Headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	if isText(resp.Header.Get("Content-Type"), raw) {
		out.Body = string(raw)
	} else {
		out.BodyBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	writeJSON(w, out)
}

func isText(ct string, b []byte) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mt, "text/"), strings.Contains(mt, "json"), strings.Contains(mt, "xml"), strings.Contains(mt, "javascript"), mt == "application/x-www-form-urlencoded":
		return true
	case mt == "":
		return utf8.Valid(b)
	}
	return false
}
