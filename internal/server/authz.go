package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/annulo/annulo/internal/auth"
	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// 服务端部署的登录和角色（docs/server-and-multi-project.md「账号」，账号本身在 internal/auth）。
// 本机版（cfg.Server 是 false）这里什么都不做：只听 127.0.0.1，靠 guard.go 的 Host / Origin 检查。
//
// 每个请求先认人：会话 cookie，或者容器里 annulo 命令行带的令牌（X-Annulo-Token，按管理员算）。
// 没登录只能拿到外壳的静态文件（界面自己显示登录页）和登录相关的接口；权限在接口上卡（authorize），不靠界面藏按钮。

const (
	sessionCookie  = "annulo_session"
	cliTokenHeader = "X-Annulo-Token"
)

type userKey struct{}

// cliUser 是容器里的 annulo 命令行：能读到令牌文件就能在服务器上执行命令，按管理员算
var cliUser = &auth.User{ID: "cli", Name: "annulo", Role: auth.Admin}

func userOf(ctx context.Context) *auth.User {
	u, _ := ctx.Value(userKey{}).(*auth.User)
	return u
}

// requestUser：会话 cookie 或命令行令牌对应的账号；都没有返回 nil。
func (s *Server) requestUser(r *http.Request) *auth.User {
	if tok := r.Header.Get(cliTokenHeader); tok != "" && s.cliToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cliToken)) == 1 {
		return cliUser
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.auth.UserBySession(c.Value)
	}
	return nil
}

// publicPath：没登录也能访问的。外壳的静态文件（界面里显示登录页）、登录接口、OAuth 回调；
// /_shuttle/mcp 自己按 token 认（给外部 agent），也放过去。
func publicPath(p string) bool {
	switch {
	case p == "/_shuttle" || p == "/_shuttle/":
		return true
	case strings.HasPrefix(p, "/_shuttle/api/"):
		switch strings.TrimPrefix(p, "/_shuttle/api/") {
		case "auth/me", "auth/setup", "auth/login", "auth/invite", "auth/invite/accept",
			"nodes/connect": // 节点自己用令牌认证（relay.Hub）
			return true
		}
		return false
	case p == "/_shuttle/oauth/callback", p == "/_shuttle/mcp":
		return true
	case strings.HasPrefix(p, inboxURL): // 收件箱的 webhook 地址：外面的平台调，靠地址里的令牌（和签名）认
		return true
	case strings.HasPrefix(p, "/_shuttle/"):
		// 外壳的静态文件：不是这些动态路由的都是
		for _, dyn := range []string{"/_shuttle/api/", "/_shuttle/img", "/_shuttle/files/", "/_shuttle/media/", "/_shuttle/uploaded/", "/_shuttle/uploads/", projectFilesURL, workFilesURL, portURL} {
			if strings.HasPrefix(p, dyn) {
				return false
			}
		}
		return true
	}
	return false
}

// authenticate 是 guard 里服务端模式的那一步：认出人放进 context，没登录又不是公开的路径就拒绝。返回 nil 表示已经回了响应。
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) *http.Request {
	u := s.requestUser(r)
	if u == nil && !publicPath(r.URL.Path) {
		if strings.HasPrefix(r.URL.Path, "/_shuttle/api/") {
			w.Header().Set("content-type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"` + i18n.T("请先登录", "Please sign in first") + `","login_required":true}`))
			return nil
		}
		if r.Header.Get("Sec-Fetch-Dest") == "document" {
			http.Redirect(w, r, "/_shuttle/", http.StatusFound) // 外壳显示登录页
			return nil
		}
		http.Error(w, i18n.T("请先登录", "Please sign in first"), http.StatusUnauthorized)
		return nil
	}
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
	}
	return r
}

var errForbidden = i18n.New("你的角色没有这个权限，找管理员", "Your role doesn't allow this; ask an admin")

// authorize：这个账号能不能调这个接口（p 是 /_shuttle/api/ 后面那段）。本机版总是能。
func (s *Server) authorize(r *http.Request, p string) error {
	if !s.cfg.Server {
		return nil
	}
	u := userOf(r.Context())
	if u == nil {
		if publicPath("/_shuttle/api/" + p) {
			return nil
		}
		return errForbidden
	}
	switch u.Role {
	case auth.Admin:
		return nil
	case auth.Member:
		if adminOnly(r.Method, p) {
			return errForbidden
		}
		return nil
	case auth.Viewer:
		if viewerAllowed(r.Method, p) {
			return nil
		}
		return errForbidden
	}
	return errForbidden
}

// adminOnly：只有管理员能做的。设置（模型、密钥、MCP、skill、连接…）的修改、creght 连接、项目的增删改名转在线、模板和插件的变更、成员管理。
func adminOnly(method, p string) bool {
	read := method == http.MethodGet
	switch {
	case strings.HasPrefix(p, "settings/") && !read,
		strings.HasPrefix(p, "login"),
		p == "setup/delete", p == "setup/rename", p == "setup/online",
		strings.HasPrefix(p, "template/") && !read,
		strings.HasPrefix(p, "plugins") && !read,
		strings.HasPrefix(p, "auth/users"), strings.HasPrefix(p, "auth/invites"),
		p == "nodes" || strings.HasPrefix(p, "nodes/"):
		return true
	}
	return false
}

// viewerAllowed：只读角色能调的：看状态、项目列表、读表、页面运行状态；跑声明了 readonly 的本机函数（apiLocalRun 里再查）；改自己的密码、退出。
func viewerAllowed(method, p string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		switch {
		case p == "status", p == "setup", p == "local/functions", p == "project/instructions", p == "auth/me",
			strings.HasPrefix(p, "db/"), strings.HasPrefix(p, "ui/"), strings.HasPrefix(p, "local/runs/"), strings.HasPrefix(p, "local/files/"):
			return true
		}
		return false
	}
	switch p {
	case "ui/page", "ui/symbolicate", "local/run", "auth/logout", "auth/password":
		return true
	}
	return false
}

// readonlyRun：只读角色跑本机函数，只能跑声明了 readonly（或 cloud）的。
func (s *Server) readonlyRun(r *http.Request, fn string) error {
	if !s.cfg.Server {
		return nil
	}
	if u := userOf(r.Context()); u == nil || u.Role != auth.Viewer {
		return nil
	}
	ok, err := localfn.IsReadonly(s.ws.Dir, fn)
	if err != nil {
		return err
	}
	if !ok {
		return i18n.Errorf("只读账号只能跑声明了 readonly 的函数：%s 没有声明", "Read-only accounts can only run functions declared readonly: %s isn't", fn)
	}
	return nil
}

// ---- 登录接口：/_shuttle/api/auth/* ----

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, tok string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.cfg.PublicURL, "https://")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.cfg.Server {
		writeJSON(w, map[string]any{"server": false})
		return
	}
	cred := func() (string, string, bool) {
		var in struct{ Name, Password string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return "", "", false
		}
		return in.Name, in.Password, true
	}
	login := func(u auth.User, tok string, err error) {
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, auth.ErrBadLogin) {
				code = http.StatusUnauthorized
			}
			fail(w, code, err)
			return
		}
		s.setSession(w, r, tok)
		writeJSON(w, map[string]any{"user": u})
	}
	me := userOf(r.Context())
	switch {
	case rest == "me" && r.Method == http.MethodGet:
		out := map[string]any{"server": true, "setup_required": s.auth.Empty()}
		if me != nil {
			out["user"] = me
		}
		writeJSON(w, out)
	case rest == "setup" && r.Method == http.MethodPost:
		if name, pass, ok := cred(); ok {
			login(s.auth.Setup(name, pass))
		}
	case rest == "login" && r.Method == http.MethodPost:
		if name, pass, ok := cred(); ok {
			login(s.auth.Login(name, pass))
		}
	case rest == "logout" && r.Method == http.MethodPost:
		if c, err := r.Cookie(sessionCookie); err == nil {
			s.auth.Logout(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
		writeJSON(w, map[string]any{"ok": true})
	case rest == "invite" && r.Method == http.MethodGet:
		role, ok := s.auth.InviteRole(r.URL.Query().Get("token"))
		writeJSON(w, map[string]any{"valid": ok, "role": role})
	case rest == "invite/accept" && r.Method == http.MethodPost:
		var in struct{ Token, Name, Password string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		login(s.auth.AcceptInvite(in.Token, in.Name, in.Password))
	case rest == "password" && r.Method == http.MethodPost && me != nil:
		var in struct{ Old, New string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.auth.SetPassword(me.ID, in.Old, in.New); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case rest == "users" && r.Method == http.MethodGet:
		users, invites := s.auth.Users()
		writeJSON(w, map[string]any{"users": users, "invites": invites})
	case strings.HasPrefix(rest, "users/") && r.Method == http.MethodPatch:
		var in struct{ Role auth.Role }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.auth.SetRole(strings.TrimPrefix(rest, "users/"), in.Role); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case strings.HasPrefix(rest, "users/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(rest, "users/")
		if me != nil && id == me.ID {
			fail(w, http.StatusBadRequest, i18n.New("不能删自己", "You can't delete yourself"))
			return
		}
		if err := s.auth.Delete(id); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case rest == "invites" && r.Method == http.MethodPost:
		var in struct{ Role auth.Role }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		by := ""
		if me != nil {
			by = me.ID
		}
		tok, err := s.auth.CreateInvite(in.Role, by)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"url": s.publicBase(r) + "/_shuttle/?invite=" + tok})
	case strings.HasPrefix(rest, "invites/") && r.Method == http.MethodDelete:
		if err := s.auth.DeleteInvite(strings.TrimPrefix(rest, "invites/")); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个接口：%s", "No such endpoint: %s", rest))
	}
}

// publicBase 是用户在浏览器里打开的地址（邀请链接用）：配了 ANNULO_PUBLIC_URL 用它，否则按这个请求的地址。
func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	if s.domains != nil { // 绑了域名、证书好了：链接都用它
		if d := s.domains.domain(); d != "" && s.domains.cert(d) != nil {
			return "https://" + d
		}
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

var errNeedHTTPS = i18n.New("用 IP 打开时没法用账号授权（授权方只认 HTTPS 或本机地址）：先在 设置 → 域名 绑一个域名，或者用 creght 个人访问令牌连接",
	"Account authorization doesn't work over a plain IP address (providers only accept HTTPS or this machine's address): bind a domain in Settings → Domain first, or connect creght with a personal access token")

// callbackURL 是 OAuth 授权完成后跳回的地址（creght 连接、MCP、Google 等连接共用）。
// 本机版：http://127.0.0.1:<端口>/…；服务端部署：绑了域名有证书用 https://<域名>/…，用 localhost 打开时用本机地址，
// 用公网 IP + http 打开时返回空（授权方只认 HTTPS 或本机回环地址）。
func (s *Server) callbackURL(r *http.Request) string {
	path := brand.URLPrefix + "oauth/callback"
	if !s.cfg.Server {
		return "http://" + s.cfg.LocalAddr() + path
	}
	u, err := url.Parse(s.publicBase(r))
	if err != nil {
		return ""
	}
	if u.Scheme == "https" {
		return u.Scheme + "://" + u.Host + path
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		port := u.Port()
		if port == "" {
			port = "80"
		}
		return "http://127.0.0.1:" + port + path
	}
	return ""
}
