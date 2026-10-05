package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
)

// newProxy 把请求转给运营后台的 creght 预览域名：页面在本机渲染（localsite），这里转的是 /api、/func 这类平台接口；
// SHUTTLE_REMOTE_PREVIEW=1 时页面也走这里。
func newProxy(target string) *httputil.ReverseProxy {
	u, _ := url.Parse(target)
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = u.Host
			// 本地凭据、Shuttle 自己的头都不往外带
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del(csrfHeader)
			pr.Out.Header.Del("Cookie")
			// 只放行界面语言：creght 平台按 CREGHT_LOCALE 给多语言后台选语言（没有时看 Accept-Language，照常转发）
			if c, err := pr.In.Cookie("CREGHT_LOCALE"); err == nil && (c.Value == "zh" || c.Value == "en") {
				pr.Out.Header.Set("Cookie", "CREGHT_LOCALE="+c.Value)
			}
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Referer")
		},
		ModifyResponse: func(resp *http.Response) error {
			h := resp.Header
			// 跳转到预览域名的改回相对地址，留在 localhost 上
			if loc := h.Get("Location"); strings.HasPrefix(loc, target) {
				h.Set("Location", strings.TrimPrefix(loc, target))
			}
			h.Del("Set-Cookie")
			h.Del("X-Frame-Options")
			h.Del("Strict-Transport-Security")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(i18n.Tf("连不上运营后台的预览地址 %s：%s", "Can't reach the back office preview at %s: %s", target, err.Error())))
		},
	}
}
