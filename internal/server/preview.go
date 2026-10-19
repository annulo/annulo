package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/auth"
	"github.com/annulo/annulo/internal/i18n"
)

// 预览：助手写的文件、起的服务，用户在浏览器里看（docs/server-and-multi-project.md「预览」）。
// 服务器上用户打不开 localhost、看不到文件路径；本机版也能用，地址一样。
//
//	/_shuttle/project-files/<项目里的路径>   项目目录里的文件（.git 和隐藏目录不给）
//	/_shuttle/port/<端口>/…                  反代到这台机器上的 127.0.0.1:<端口>（助手起的服务）
//
// 两种响应都带 Content-Security-Policy: sandbox：页面里的脚本跑在「不透明源」里，读不到 Annulo 的登录 cookie、
// 调不了 /_shuttle/api（跨站、带不了自定义头），一段用户的 HTML 拿不到能执行命令的会话。
// 要登录且不是只读角色（只读角色看不到项目文件和服务）。

const (
	projectFilesURL = "/_shuttle/project-files/"
	portURL         = "/_shuttle/port/"
	// 页面能跑脚本、提交表单、弹窗，但没有 allow-same-origin：拿不到 Annulo 的源
	previewCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"
)

func (s *Server) previewAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Server {
		if u := userOf(r.Context()); u == nil || u.Role == auth.Viewer {
			http.Error(w, errForbidden.Error(), http.StatusForbidden)
			return false
		}
	}
	if !s.ready.Load() {
		http.Error(w, i18n.T("还没有打开项目", "No project is open"), http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) handleProjectFile(w http.ResponseWriter, r *http.Request) {
	if !s.previewAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := path.Clean("/" + strings.TrimPrefix(r.URL.Path, projectFilesURL))
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") { // .git、.creght、.shuttle 这些不给看
			http.NotFound(w, r)
			return
		}
	}
	root, err := filepath.EvalSymlinks(s.ws.Dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || (full != root && !strings.HasPrefix(full, root+string(filepath.Separator))) { // 软链接指到项目外面的不给
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if st.IsDir() {
		if idx := filepath.Join(full, "index.html"); fileExists(idx) {
			full = idx
		} else {
			http.Error(w, i18n.T("这是一个目录", "This is a directory"), http.StatusNotFound)
			return
		}
	}
	w.Header().Set("Content-Security-Policy", previewCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, full)
}

func (s *Server) handlePort(w http.ResponseWriter, r *http.Request) {
	if !s.previewAllowed(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, portURL)
	ps, sub, _ := strings.Cut(rest, "/")
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1024 || port > 65535 || port == s.cfg.Port {
		http.Error(w, i18n.T("端口要是 1024 到 65535 之间、不是 Annulo 自己的", "The port must be between 1024 and 65535 and not Annulo's own"), http.StatusBadRequest)
		return
	}
	if sub == "" && !strings.HasSuffix(r.URL.Path, "/") { // /_shuttle/port/8000 → /_shuttle/port/8000/：相对路径才对
		http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
		return
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		http.Error(w, i18n.Tf("这台机器的 %d 端口上没有服务在跑", "Nothing is running on port %d on this machine", port), http.StatusBadGateway)
		return
	}
	conn.Close()
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	prefix := portURL + ps
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + sub
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Cookie") // Annulo 的登录 cookie 不带给用户起的服务
			pr.Out.Header.Del(cliTokenHeader)
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("Content-Security-Policy", previewCSP)
			resp.Header.Del("Set-Cookie") // 不让它在 Annulo 的源上写 cookie
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") {
				resp.Header.Set("Location", prefix+loc) // 服务里跳到 /xxx：留在这个端口下
			}
			return nil
		},
	}
	rp.ServeHTTP(w, r)
}
