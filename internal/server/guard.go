package server

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 门卫：本地端口背后是 creght 登录态和一个能跑命令的 agent，任何网页都能尝试请求它。
//
//   - Host 必须是 IP 字面量或 localhost，挡 DNS rebinding（把 evil.com 解析到 127.0.0.1，
//     浏览器就当它和本服务同源）；
//   - /_shuttle/api 带 Origin 就必须同源，并且要有自定义头 X-Shuttle（或新名字 X-Annulo）—— 跨站请求设不了
//     自定义头（会触发 preflight，本服务不答 preflight）。
//
// 反代出去的站点页面本来就是公开的预览内容，不额外设防。
func (s *Server) hostOK(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "localhost" || net.ParseIP(host) != nil
}

func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

const csrfHeader = brand.Header

func hasCSRFHeader(r *http.Request) bool {
	return r.Header.Get(brand.Header) != "" || r.Header.Get(brand.HeaderNew) != ""
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r) {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusMisdirectedRequest)
			fmt.Fprintf(w, i18n.T("Host %q 不在白名单里。请用 http://127.0.0.1:%d 访问。\n", "Host %q isn't allowed. Open http://127.0.0.1:%d instead.\n"), r.Host, s.cfg.Port)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_shuttle/api/") {
			if r.Method == http.MethodOptions {
				fail(w, http.StatusForbidden, i18n.New("不接受跨站预检请求", "Cross-site preflight requests aren't accepted"))
				return
			}
			if !originOK(r) {
				log.Printf("api %s %s 被拒：Origin %q 和 Host %q 不一致", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host)
				fail(w, http.StatusForbidden, i18n.Errorf("跨站请求被拒：Origin %s 和当前地址 %s 不一致", "Cross-site request rejected: Origin %s doesn't match the current host %s", r.Header.Get("Origin"), r.Host))
				return
			}
			if !hasCSRFHeader(r) {
				fail(w, http.StatusForbidden, i18n.Errorf("缺少 %s 请求头", "Missing %s request header", csrfHeader))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
