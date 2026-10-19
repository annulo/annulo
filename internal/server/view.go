package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
)

// 多项目同时开（docs/server-and-multi-project.md「多项目同时开」）：
//
//   - 每个项目一个源：http://<项目 id>.localhost:<端口>。外壳、左侧后台页面、页面调的 /_shuttle/api 都在这个源下，
//     请求按 Host 找到项目；不同项目的 localStorage、cookie、service worker 自然分开。Chromium 把 *.localhost 解析到本机。
//   - 没带项目的地址（127.0.0.1:<端口>、localhost:<端口>）是「默认项目」（config.json 的 backend，最近打开的那个）：
//     annulo 命令行带 X-Annulo-Project 头指定项目，不带就是默认项目；浏览器打开外壳时跳到默认项目的源。
//   - 打开着的项目都挂着：各自的助手、文件监听、表；对话在后台照常跑，不因为另一个窗口换了项目而停。

// ProjectHeader 指定请求属于哪个项目（annulo 命令行按当前目录带上）。
const ProjectHeader = "X-Annulo-Project"

// projectCookie：不分源时这个浏览器开着哪个项目（setup 切换时写）
const projectCookie = "annulo_project"

// view 是同一个 core 上某个项目的视图。
func (s *Server) view(p *project) *Server { return &Server{core: s.core, project: p} }

// views 是打开着的项目。
func (s *Server) views() []*Server {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	out := make([]*Server, 0, len(s.projects))
	for _, p := range s.projects {
		if p.ready.Load() {
			out = append(out, s.view(p))
		}
	}
	return out
}

// openView 是已经打开着的项目的视图；没打开返回 nil。
func (s *Server) openView(pid string) *Server {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	if p := s.projects[pid]; p != nil && p.ready.Load() {
		return s.view(p)
	}
	return nil
}

// activate 打开一个项目（已经开着就直接用），返回它的视图。不改默认项目（调用方自己设 cfg.Backend）。
func (s *Server) activate(ws *creght.Workspace) *Server {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if v := s.openView(ws.ProjectID); v != nil && v.ws.Dir == ws.Dir {
		return v
	}
	p := &project{runs: map[string]*agentRun{}}
	v := s.view(p)
	v.Attach(ws)
	s.projMu.Lock()
	old := s.projects[ws.ProjectID]
	s.projects[ws.ProjectID] = p
	s.projMu.Unlock()
	if old != nil && old != p {
		ov := s.view(old)
		ov.ready.Store(false)
		if ov.unwatch != nil {
			ov.unwatch()
		}
	}
	return v
}

// projectView 按 id 找到项目的视图，没打开就从本机目录打开。在线项目要连着 creght。
func (s *Server) projectView(pid string) (*Server, error) {
	if v := s.openView(pid); v != nil {
		return v, nil
	}
	if !validProjectID(pid) {
		return nil, i18n.Errorf("没有项目 %s", "There's no project %s", pid)
	}
	ws, err := creght.OpenWorkspaceOn(s.cfg.DirFor(pid), s.creghtHost())
	if err != nil || ws.ProjectID != pid {
		return nil, i18n.Errorf("这台电脑上没有项目 %s", "This computer doesn't have project %s", pid)
	}
	if !ws.Offline && !hasToken(ws.APIHost) {
		return nil, i18n.Errorf("项目 %s 在 creght 上：先在 设置 → 连接 里连接 creght", "Project %s is on creght: connect creght in Settings → Connections first", pid)
	}
	return s.activate(ws), nil
}

// defaultView 是默认项目（最近打开的）的视图；没有就是空视图（界面显示新建 / 选项目）。
func (s *Server) defaultView() *Server {
	if pid := s.cfg.Backend; pid != "" {
		if v, err := s.projectView(pid); err == nil {
			return v
		}
	}
	return s.view(s.none)
}

// requestView：Host 是 <项目>.localhost 的按它；否则看 X-Annulo-Project 头；都没有是默认项目。
// 地址里的项目打不开（删了、在线项目没连 creght）给空视图：界面显示选项目，选了就跳到那个项目的地址。
func (s *Server) requestView(r *http.Request) *Server {
	pid := hostProject(r.Host)
	if pid == "" {
		pid = r.Header.Get(ProjectHeader)
	}
	if pid == "" {
		// 不分源（服务端部署、ANNULO_SINGLE_ORIGIN）时每个浏览器记着自己开的项目：几个人共用一个地址，A 换项目不会把 B 也换走。
		// cookie 不分端口，同一台机器上别的 Annulo 留下的可能是这里没有的项目：打不开就回到默认项目
		if c, err := r.Cookie(projectCookie); err == nil {
			if v, err := s.projectView(c.Value); err == nil {
				return v
			}
		}
	}
	if pid == "" {
		return s.defaultView()
	}
	if v, err := s.projectView(pid); err == nil {
		return v
	}
	return s.view(s.none)
}

// hostProject 从 <项目>.localhost[:端口] 取出项目 id；不是这种地址返回空。
func hostProject(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if pid, ok := strings.CutSuffix(host, ".localhost"); ok && validProjectID(pid) {
		return pid
	}
	return ""
}

// validProjectID：项目 id 只有小写字母、数字和 -（creght 项目 id、离线项目的 local-<随机>），能放进域名。
func validProjectID(pid string) bool {
	if pid == "" || len(pid) > 63 || strings.HasPrefix(pid, "-") || strings.HasSuffix(pid, "-") {
		return false
	}
	for _, c := range pid {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// rememberProject：不分源时把这个浏览器的项目记进 cookie（分源时地址里就有项目，不用记）。
func (s *Server) rememberProject(w http.ResponseWriter, pid string) {
	if s.projectURL(pid) != "" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: projectCookie, Value: pid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
}

// projectURL 是项目在本机的地址（外壳）。ANNULO_SINGLE_ORIGIN=1（浏览器解析不了 *.localhost 时）不分源，返回空：
// 界面原地刷新，没带项目的地址就是默认项目。
func (s *Server) projectURL(pid string) string {
	if brand.Env("SINGLE_ORIGIN") == "1" || s.cfg.Server { // 服务器上一个域名：不分源，靠 cookie 记项目
		return ""
	}
	return fmt.Sprintf("http://%s.localhost:%d/_shuttle/", pid, s.cfg.Port)
}

// projectURLFor 是给这次请求的界面用的项目地址：经隧道远程打开的，是项目的远程地址（平台分配，还不知道时是空：界面原地刷新）；
// 本机打开的是 projectURL。
func (s *Server) projectURLFor(r *http.Request, pid string) string {
	if remoteOf(r) != nil {
		return s.remoteProjectURL(pid)
	}
	return s.projectURL(pid)
}
