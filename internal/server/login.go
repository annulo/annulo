package server

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
)

// 设置 → 连接 里的 creght：开始 → 前端在浏览器里打开平台的授权页（OAuth，internal/creght/oauth.go）→ 平台跳回本机的
// /_shuttle/oauth/callback 存下 token → 前端轮询看连上没有。

type loginState struct {
	mu    sync.Mutex
	state string // 进行中的授权（OAuth 的 state）
	host  string
	exp   time.Time
	err   string // 回调失败的原因（轮询时告诉前端）
}

// loginHost 是登录用的集群：有运营后台就用它的，没有就用设置里选的集群。
// 切集群时正在用的项目不在新集群就会先放下（apiCreghtCluster），所以两者一致。
func (s *Server) loginHost() string {
	if s.ready.Load() {
		return s.ws.APIHost
	}
	return s.creghtHost()
}

// creghtHost 是设置里选的 creght 集群，没选过是 creght.cn。
func (s *Server) creghtHost() string {
	if s.cfg.Creght == "" {
		return creght.DefaultHost
	}
	return creght.NormHost(s.cfg.Creght)
}

// apiCreghtClusterGet：能选的集群和现在用的（Hidden 的集群只在正在用时列出）。
func (s *Server) apiCreghtClusterGet(w http.ResponseWriter, r *http.Request) {
	cur := s.loginHost()
	var shown []creght.Cluster
	for _, c := range creght.Clusters {
		if !c.Hidden || c.Host == cur {
			shown = append(shown, c)
		}
	}
	list := make([]map[string]any, len(shown))
	var wg sync.WaitGroup
	for i, c := range shown {
		wg.Add(1)
		go func() { // 各集群的模板列表一起读，一个连不上不拖着别的
			defer wg.Done()
			_, tokErr := creght.ReadToken(c.Host)
			list[i] = map[string]any{"host": c.Host, "name": c.Name(), "logged_in": tokErr == nil, "templates": len(templatesOn(c.Host))}
		}()
	}
	wg.Wait()
	writeJSON(w, map[string]any{"current": cur, "clusters": list})
}

// apiCreghtClusterSet：{"host":…} 换 creght 集群。账号、项目、模板都是按集群分开的：
// 正在用的项目不在新集群，就先放下它（本机副本、对话历史都留着，切回来再打开），界面回到登录 / 选项目。
func (s *Server) apiCreghtClusterSet(w http.ResponseWriter, r *http.Request) {
	var in struct{ Host string }
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c := creght.ClusterOf(in.Host)
	if c == nil {
		fail(w, http.StatusBadRequest, i18n.Errorf("不认识这个 creght 集群：%s", "Unknown creght cluster: %s", in.Host))
		return
	}
	if c.Host == s.loginHost() && c.Host == s.creghtHost() {
		s.apiCreghtClusterGet(w, r)
		return
	}
	detach := s.ready.Load() && s.ws.APIHost != c.Host
	if detach && s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再切换", "The assistant is working; wait until it finishes before switching"))
		return
	}
	s.cfg.Creght = c.Host
	if c.Host == creght.DefaultHost {
		s.cfg.Creght = ""
	}
	if detach {
		s.cfg.Backend = ""
	}
	if err := s.cfg.Save(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if detach {
		s.Detach()
	}
	s.agent.SetCreghtHost(s.loginHost())
	os.Setenv("CREGHT_API_HOST", s.loginHost())
	s.apiCreghtClusterGet(w, r)
}

func (s *Server) apiLoginStart(w http.ResponseWriter, r *http.Request) {
	host := s.loginHost()
	authURL, state, err := creght.StartOAuth(r.Context(), host, "http://"+s.cfg.Addr()+brand.URLPrefix+"oauth/callback")
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.login.mu.Lock()
	s.login.state, s.login.host, s.login.exp, s.login.err = state, host, time.Now().Add(15*time.Minute), ""
	s.login.mu.Unlock()
	writeJSON(w, map[string]any{"verify_url": authURL, "expires_in": 900})
}

// finishCreghtOAuth：平台的授权页跳回来（/_shuttle/oauth/callback，state 带 creght. 前缀）。
func (s *Server) finishCreghtOAuth(ctx context.Context, state, code, errCode string) error {
	_, err := creght.FinishOAuth(ctx, state, code, errCode)
	s.login.mu.Lock()
	if s.login.state == state && err != nil {
		s.login.err = err.Error()
	}
	s.login.mu.Unlock()
	if err == nil {
		s.creghtConnected()
	}
	return err
}

// creghtConnected：刚连上 creght，顶栏账号重新查，带上 creght MCP。
func (s *Server) creghtConnected() {
	resetTemplateCache()
	s.user.mu.Lock()
	s.user.failedAt = time.Time{}
	s.user.mu.Unlock()
	if s.ready.Load() {
		s.ensureCreghtMCP(s.ws.APIHost)
	}
}

func (s *Server) apiLoginPoll(w http.ResponseWriter, r *http.Request) {
	s.login.mu.Lock()
	state, host, exp, errMsg := s.login.state, s.login.host, s.login.exp, s.login.err
	s.login.mu.Unlock()
	if state == "" {
		fail(w, http.StatusBadRequest, i18n.New("没有进行中的连接", "No connection in progress"))
		return
	}
	st := "pending"
	switch {
	case errMsg != "":
		st = "error"
	case func() bool { _, err := creght.ReadToken(host); return err == nil }():
		st = "approved"
	case time.Now().After(exp):
		st = "expired"
	}
	if st != "pending" {
		s.login.mu.Lock()
		s.login.state = ""
		s.login.mu.Unlock()
	}
	writeJSON(w, map[string]any{"status": st, "error": errMsg})
}

// userCache：顶栏显示当前账号，缓存一会儿，别每次 status 都去查平台。
type userCache struct {
	mu       sync.Mutex
	at       time.Time
	user     *creght.User
	failedAt time.Time // 上次查不到用户（没登录、token 失效）的时间：后台的定时任务每 30 秒问一次，别每次都去平台查
}

func (s *Server) currentUser(ctx context.Context) *creght.User {
	s.user.mu.Lock()
	defer s.user.mu.Unlock()
	if s.user.user != nil && time.Since(s.user.at) < 5*time.Minute {
		return s.user.user
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, err := creght.NewClient(s.loginHost()).Self(ctx)
	if err != nil {
		s.user.failedAt = time.Now()
		return nil
	}
	s.user.user, s.user.at = u, time.Now()
	return u
}

// signedIn：creght 登录着、token 还有效（和顶栏一样按「查得到账号」算）。查不到的结果记 1 分钟，后台轮询时用它。
func (s *Server) signedIn() bool {
	if _, err := creght.ReadToken(s.loginHost()); err != nil {
		return false
	}
	s.user.mu.Lock()
	recentFail := time.Since(s.user.failedAt) < time.Minute
	s.user.mu.Unlock()
	if recentFail {
		return false
	}
	return s.currentUser(context.Background()) != nil
}

// apiLogout 断开 creght 连接：删掉本机存的 token。
func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("助手还在执行任务，先停止或等它做完再断开 creght", "The assistant is still working; stop it or wait until it finishes before disconnecting creght"))
		return
	}
	if err := creght.Disconnect(s.loginHost()); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.user.mu.Lock()
	s.user.user = nil
	s.user.mu.Unlock()
	s.relay.Reconnect() // 断开了：中转也断开
	resetTemplateCache()
	// 当前是在线项目：没连 creght 就用不了，换到离线项目（见 openOfflineFallback）。switched 让界面整页刷新
	switched := s.ready.Load() && !s.ws.Offline
	if switched {
		s.openOfflineFallback()
	}
	writeJSON(w, map[string]any{"ok": true, "switched": switched})
}
