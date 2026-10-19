// Package server 是 Shuttle 的本地 HTTP 服务，一个端口上三样东西：
//
//	/_shuttle/api/*  JSON 接口：代为读取 creght 数据、驱动本地 agent
//	/_shuttle/img    图片代理（外站图片防盗链，页面里用 /_shuttle/img?url=…）
//	/_shuttle/*      Shuttle 自己的界面（左边运营后台、右边 agent 对话）
//	其余路径          运营后台的页面：本机渲染项目目录（internal/localsite），/api、/func 这类平台接口反代到站点的 creght 预览域名
//
// 运营后台和 Shuttle 界面同源（都在 localhost），所以后台页面能直接调 /_shuttle/api，
// 也避开了「线上页面 iframe 嵌 localhost」那套第三方 cookie、混合内容的问题。
package server

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/auth"
	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/connect"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/extbridge"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/localsite"
	"github.com/annulo/annulo/internal/mcphub"
	"github.com/annulo/annulo/internal/relay"
	"github.com/annulo/annulo/internal/tunnel"
	"github.com/annulo/annulo/internal/wsgit"
)

// Server 是一个项目的视图：共用的部分（core：设置、密钥、浏览器、定时、中转…）加上这个项目自己的部分（project）。
// 几个项目可以同时开着（docs/server-and-multi-project.md「多项目同时开」）：每个请求按地址里的项目（<项目>.localhost）
// 或 X-Annulo-Project 头找到它的视图（view.go），方法照旧写成 s.ws、s.agent，拿到的就是那个项目的。
type Server struct {
	*core
	*project
}

type core struct {
	cfg   *config.Config
	web   fs.FS
	mcp   *mcphub.Hub
	root  *agent.Agent // 最初的 Agent：没有项目时用它，每个项目的 Agent 从它 Fork（共用设置）
	setup setupState
	login loginState
	user  userCache

	secrets *config.Secrets  // 本机密钥：只给本机函数 ctx.secrets（不进进程环境变量，助手的命令行里没有）
	local   localRuns        // 正在跑的本机函数
	browser *browser.Manager // 本机函数的 ctx.browser：每个 profile 同时只给一个任务用
	sched   scheduler        // 本机定时（schedules/）
	inbox   inboxState       // 收件箱（inboxes/，inbox.go）

	inboxRelay relayState // 收件箱的云端代收（inboxrelay.go）
	ent        entCache   // Annulo 订阅的权益（subscription.go）
	conns   *connect.Store   // 连接（Google 等 OAuth 账号），本机函数 ctx.oauth

	relay      *relay.Client  // 手机上调到这台电脑的本机函数（relay.go）
	tunnel     *tunnel.Client // 远程访问：手机上打开这台电脑的 Annulo（remote.go）；服务端部署是 nil
	remoteURLs remoteURLCache // 各项目的远程地址（remote.go）

	// 服务端部署（cfg.Server）：账号和容器里命令行的令牌（authz.go）；本机版是 nil / 空
	auth     *auth.Store
	cliToken string
	// 节点（nodes.go）：服务器上是 nodeHub（节点连进来）和令牌；本机版是 nodeClient（作为节点连到服务器）
	domains    *domainManager // 服务端部署：域名和证书（domain.go）
	nodeHub    *relay.Hub
	nodeTokens *nodeStore
	nodeClient *relay.Client

	projMu   sync.Mutex
	projects map[string]*project // 打开着的项目，按项目 id
	openMu   sync.Mutex          // 同一时间只打开一个项目：两个请求同时来，不会把同一个项目挂两份
	none     *project            // 没有项目时的视图（ready 是 false）
}

// project 是一个打开着的项目自己的状态。
type project struct {
	ws      *creght.Workspace
	creght  *creght.Client
	agent   *agent.Agent // 这个项目的助手：对话、工作目录、工具是它自己的
	proxy   *httputil.ReverseProxy
	preview string
	site    *localsite.Renderer // 本机渲染运营后台页面（docs/local-render.md）
	unwatch context.CancelFunc  // 停掉这个项目的文件监听
	// ready：项目已经挂上（ws 等字段已经设好）。没有项目时服务照样起来，只开放初始化和设置。
	ready atomic.Bool
	mux   http.Handler // 这个项目的路由（view.go）

	tids     tableIDs
	wsTables workspaceTables // 运营后台 tables/ 声明的表
	ensureMu sync.Mutex      // EnsureTables 同时只跑一个：先查有哪些表再建，并发跑会每张表建两份
	tasks    taskRunner      // 项目的任务（tasks/*.md）交给助手跑
	page     pageState       // 左侧运营后台页面的运行状态和报错（page_errors 工具）

	runsMu sync.Mutex
	runs   map[string]*agentRun // 正在进行的 agent 对话，按对话 id

	localData *localdb.DB // 离线项目的业务表（本机 SQLite，offline.go）
	dataMu    sync.Mutex  // localData 没打开时用到再开（cur）

	pruning atomic.Bool // 正在清理本机文件（localfiles.go），同时只跑一个

	changes dataChanges // 业务表变了，合并一下再告诉页面（datachanged.go）
}

// New 建服务。ws 为 nil 表示还没有运营后台：界面会显示初始化页面，初始化后再 Attach。
// localProjects 是本机已有的项目 id（backends/ 下的目录，加上最早的 ~/.annulo/backend），启动时把以前整台电脑共用的连接拷给它们
func localProjects(cfg *config.Config) []string {
	var out []string
	if pid := cfg.LegacyProject(); pid != "" {
		out = append(out, pid)
	}
	ents, _ := os.ReadDir(filepath.Join(cfg.Dir, "backends"))
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func New(cfg *config.Config, web fs.FS, ws *creght.Workspace, ag *agent.Agent, hub *mcphub.Hub) *Server {
	c := &core{
		cfg:      cfg,
		web:      web,
		root:     ag,
		mcp:      hub,
		secrets:  cfg.LoadSecrets(),
		conns:    connect.New(cfg.Dir, localProjects(cfg)),
		browser:  browser.NewManager(filepath.Join(cfg.Dir, "browser"), checkTarget),
		projects: map[string]*project{},
	}
	c.none = &project{agent: ag, runs: map[string]*agentRun{}}
	s := &Server{core: c, project: c.none}
	creght.SetCustom(cfg.CreghtCustom) // 自己部署的 creght（login.go 的 apiCreghtCustom）
	if cfg.Server {
		st, err := auth.Open(cfg.Dir)
		if err != nil {
			log.Fatalf("读账号失败（%s/users.json）：%v", cfg.Dir, err)
		}
		c.auth, c.cliToken = st, auth.CLIToken(cfg.Dir)
		c.nodeTokens = openNodeStore(cfg.Dir)
		c.domains = newDomainManager(cfg.Dir)
		c.nodeHub = relay.NewHub(c.nodeTokens.auth)
	} else {
		c.nodeClient = s.newNodeClient()
		c.nodeClient.Start(context.Background())
	}
	// 用户自己的 Chrome 里的 Annulo 插件（docs/browser-extension.md）：ctx.browser.open({ profile: '@user' })
	s.browser.Ext = extbridge.New()
	if !cfg.Server { // 服务器上没有用户的 Chrome（以后经节点连，docs/server-and-multi-project.md「节点」）
		go registerBrowserHost(cfg.Dir)
	}
	ag.MCPURL = "http://" + cfg.LocalAddr() + "/_shuttle/mcp"
	setTemplateSources(cfg.Dir, cfg.TemplateSources) // git 模板（gittemplates.go）
	s.relay = s.newRelay()
	s.relay.Start(context.Background())
	if !cfg.Server { // 服务器本来就能直接打开，用不上隧道
		s.tunnel = s.newTunnel()
		s.tunnel.Start(context.Background())
		s.serveTunnel()
	}
	go s.runSchedules(context.Background())
	go s.runInboxes(context.Background())
	go s.runInboxRelay(context.Background())
	go s.retryFeedback(context.Background())
	go s.creghtEnvLoop(context.Background())
	// agent 的工具 = 读业务表的原语（db_query / db_aggregate，见 dbtools.go）+ 已连接的 MCP 工具；MCP 连上 / 断开 / 改配置时重新组装。
	// Shuttle 不给 agent 写死业务工具：渠道、访问数据这些在运营后台的本机函数里（annulo run channels.stats …）
	hub.OnChange(s.refreshTools)
	s.refreshTools()
	if ws != nil && !ws.Offline && !hasToken(ws.APIHost) {
		// 记着的是在线项目、但没连 creght：不打开（打开了大半功能都用不了，项目列表里也没有它），换到离线项目
		ws = nil
		s.openOfflineFallback()
	}
	if ws != nil {
		s.activate(ws)
	}
	return s
}

// refreshTools 给每个打开着的项目的助手换上最新的工具（MCP 连上 / 断开时）。
func (s *Server) refreshTools() {
	none := s.view(s.none)
	s.root.SetTools(append(append(none.dbTools(), none.pageTools()...), s.mcp.Tools()...))
	for _, v := range s.views() {
		v.agent.SetTools(append(append(v.dbTools(), v.pageTools()...), s.mcp.Tools()...))
	}
}

func hasToken(host string) bool {
	_, err := creght.ReadToken(host)
	return err == nil
}

// openOfflineFallback：没连 creght 时在线项目不打开（启动时、断开 creght 时）。关掉打开着的在线项目，默认项目换到最近新建的离线项目；
// 一个离线项目都没有就不设默认，界面回到新建 / 选项目。本机的项目目录和数据都不动，重新连上 creght 后在项目列表里点它就回来了。
func (s *Server) openOfflineFallback() {
	for _, v := range s.views() {
		if !v.ws.Offline {
			v.Detach()
		}
	}
	var next *creght.Workspace
	for _, b := range s.offlineBackends() {
		if ws, err := creght.OpenWorkspaceOn(s.cfg.DirFor(b.ProjectID), s.creghtHost()); err == nil && ws.Offline {
			next = ws
			break
		}
	}
	if next == nil {
		s.cfg.Backend = ""
	} else {
		s.activate(next)
		s.cfg.Backend = next.ProjectID
	}
	if err := s.cfg.Save(); err != nil {
		log.Printf("保存当前项目失败：%v", err)
	}
}

// Detach 关掉这个项目（切 creght 集群、断开 creght、转成在线项目换了 id 时）：本机副本和对话历史都不动，再打开时重新挂上。
func (s *Server) Detach() {
	if s.project == s.none {
		return
	}
	s.projMu.Lock()
	if s.projects[s.ws.ProjectID] == s.project {
		delete(s.projects, s.ws.ProjectID)
	}
	s.projMu.Unlock()
	s.ready.Store(false)
	if s.unwatch != nil {
		s.unwatch()
		s.unwatch = nil
	}
	s.relay.Reconnect()
}

// Attach 挂上运营后台工作区（只在新建的 project 上调，见 activate），然后在后台补齐业务表、迁移旧数据。
func (s *Server) Attach(ws *creght.Workspace) {
	s.ws = ws
	if s.runs == nil {
		s.runs = map[string]*agentRun{}
	}
	s.agent = s.root.Fork(ws.ProjectID, ws.Dir)
	s.agent.ProjectName = func() string { return s.projectName(ws.ProjectID, ws.Dir) }
	s.creght = creght.NewClient(ws.APIHost)
	s.localData, s.preview, s.proxy = nil, "", nil
	if ws.Offline {
		// 离线项目：业务表在本机 SQLite，没有站点预览可以反代（/api、/func 这类平台接口不可用）
		db, err := s.openLocalData(ws.ProjectID)
		if err != nil {
			log.Printf("打开离线项目的数据库失败：%v", err)
		}
		s.localData = db
	} else {
		s.preview = creght.PreviewURL(ws.APIHost, ws.SiteID)
		s.proxy = newProxy(s.preview)
	}
	// 页面的渲染配置：creght 项目、连着 creght 的离线项目用平台的（和云端同构）；没连 creght 的离线项目用随安装包带的，包从 CDN 加载
	renderHost := ws.APIHost
	if _, err := creght.ReadToken(ws.APIHost); ws.Offline && err != nil {
		renderHost = ""
	}
	s.site = localsite.New(ws.Dir, renderHost, ws.ProjectID, ws.SiteID, filepath.Join(s.cfg.Dir, "render"), s.cfg.RenderCDN)
	s.site.UILocale = s.agent.Locale
	if s.unwatch != nil {
		s.unwatch()
	}
	var watchCtx context.Context
	watchCtx, s.unwatch = context.WithCancel(context.Background())
	go s.site.Watch(watchCtx) // 文件一变就重新编译，推给打开着的后台页面热更新
	s.agent.OpsSite, s.agent.Offline = ws.ProjectID+"/"+ws.SiteID, ws.Offline
	if ws.Offline {
		s.agent.OpsSite = ""
	}
	s.agent.SetCreghtHost(ws.APIHost) // creght 服务商跟着运营后台所在的集群
	// agent 在 bash 里跑的 creght 命令都用运营后台所在的集群。creght CLI 的默认集群是用户自己配的
	// （可能是 creght.com），在运营后台目录外操作渠道站点时会连错集群：列表是空的、报没权限，agent 以为 CLI 不支持
	os.Setenv("CREGHT_API_HOST", ws.APIHost)
	migrateTablesFile(ws.Dir)
	// 运营后台用本地 git 管改动历史（docs/workspace-git.md）；没有 git 也照常用，只是没有历史
	if err := wsgit.Ensure(ws.Dir); err != nil {
		log.Printf("初始化项目的 git 失败：%v", err)
	}
	s.ensureCreghtMCP(ws.APIHost)
	s.agent.SetTools(append(append(s.dbTools(), s.pageTools()...), s.mcp.Tools()...))
	s.ready.Store(true)
	s.relay.Reconnect() // 换了项目（或集群）：重新告诉平台这台电脑上有哪个项目
	if ws.Offline {
		return // 本机的表不用建
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.EnsureTables(ctx); err != nil {
			log.Printf("补齐业务表失败：%v", err)
		}
	}()
}

// ensureCreghtMCP：creght 的数据（站点、访问统计、CMS）走 creght 自己的 MCP，本机函数用 ctx.mcp('creght', …) 调。
// creght MCP 是内置的：连着哪个集群（creght.cn / creght.com…）就是哪个集群的 MCP，用 设置 → 连接 里 creght 的授权，不用单独授权；
// 没连 creght 就没有它。用户在 设置 → MCP 里只能停用、启用，不能删、不能重新授权。
func (s *Server) ensureCreghtMCP(host string) {
	var m *mcphub.Managed
	if creght.Connected(host) && creght.Has(host, "mcp") {
		m = &mcphub.Managed{URL: host + "/api/mcp", Token: func(ctx context.Context) (string, error) {
			tok, err := creght.ReadToken(host)
			if errors.Is(err, creght.ErrReconnect) || errors.Is(err, creght.ErrNotLoggedIn) {
				return "", i18n.New("creght 的授权失效了：到 设置 → 连接 里重新连接 creght", "The creght connection has expired: reconnect creght in Settings → Connections")
			}
			return tok, err
		}}
	}
	if err := s.mcp.SetManaged("creght", m); err != nil {
		log.Printf("设置 creght MCP 失败：%v", err)
	}
}

// WorkspaceDir 是默认项目（最近打开的）的目录；没有项目时是空的。
func (s *Server) WorkspaceDir() string {
	v := s.defaultView()
	if !v.ready.Load() {
		return ""
	}
	return v.ws.Dir
}

func (s *Server) PreviewURL() string {
	v := s.defaultView()
	if !v.ready.Load() {
		return ""
	}
	return v.preview
}

// Handler：每个请求先找到它属于哪个项目（view.go 的 requestView），再交给那个项目的路由。
func (s *Server) Handler() http.Handler {
	return canonicalPaths(s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestView(r).routes().ServeHTTP(w, r)
	})))
}

// routes 是一个项目视图的路由（建一次，存在 project 上）。
func (s *Server) routes() http.Handler {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	if s.mux != nil {
		return s.mux
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_shuttle/api/", logAPI(s.handleAPI))
	mux.HandleFunc("/_shuttle/api/browser/ext", s.handleBrowserExt) // 浏览器插件经 native host 连进来（browserext.go）
	if s.nodeHub != nil {
		mux.Handle(nodesConnectPath, s.nodeHub) // 用户电脑上的 Annulo 作为节点连进来（nodes.go），令牌认证
	}
	mux.HandleFunc("/_shuttle/oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc(inboxURL, s.handleInbox) // 收件箱的 webhook 地址（inbox.go），靠令牌认、不用登录
	mux.HandleFunc(agent.UploadURLPrefix, s.handleUpload)
	mux.HandleFunc(localFilesURL, s.handleLocalFile)     // 本机文件预览（localfiles.go）
	mux.HandleFunc(chatMediaURL, s.handleChatMedia)      // 对话里的本机图片、视频（chatmedia.go）
	mux.HandleFunc("/_shuttle/img", s.handleImage)       // 图片代理：绕开图床的防盗链，见 imgproxy.go
	mux.HandleFunc(assetsURL, s.handleAsset)             // 离线项目页面上传的文件（offline.go）
	mux.HandleFunc(projectFilesURL, s.handleProjectFile) // 预览项目里的文件、助手起的服务（preview.go）
	mux.HandleFunc(workFilesURL, s.handleWorkFile)       // 预览项目工作目录里的文件（workfiles.go）
	mux.HandleFunc(portURL, s.handlePort)
	mux.Handle("/_shuttle/mcp", s.root.MCPHandler()) // 给外部 agent（Claude Code / Codex）用的 Shuttle 工具，按 token 找到项目
	mux.HandleFunc(manifestURL, s.handleManifest) // 添加到桌面（pwa.go）
	mux.HandleFunc("/_shuttle/", s.handleStatic)
	mux.HandleFunc("/_shuttle", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/_shuttle/", http.StatusFound)
	})
	mux.HandleFunc("/", s.handleSite)
	s.mux = mux
	return mux
}

// canonicalPaths：/_annulo/… 和 /_shuttle/… 是同一套路由（新旧名字，见 internal/brand），进来先换成 /_shuttle/…。
func canonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := brand.CanonicalPath(r.URL.Path); p != r.URL.Path {
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = p, ""
		}
		next.ServeHTTP(w, r)
	})
}

// handleSite：浏览器地址栏直接打开的（顶层文档）进 Shuttle 界面；iframe 里打开的页面在本机渲染（localsite），
// 其余请求（/api、/func 这些平台接口）反代到预览域名。靠 Sec-Fetch-Dest 区分：iframe 里的导航是 "iframe"，地址栏是 "document"。
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Dest") == "document" && remoteOf(r) != nil {
		// 远程打开的：外壳直接出在这个地址上，地址栏不带 /_shuttle/（外壳按地址打开后台的这一页，web/src/lib/router.ts）
		r = r.Clone(r.Context())
		r.URL.Path, r.URL.RawPath = "/_shuttle/", ""
		s.handleStatic(w, r)
		return
	}
	if r.Header.Get("Sec-Fetch-Dest") == "document" || !s.ready.Load() {
		target := "/_shuttle/"
		if r.URL.Path != "/" || r.URL.RawQuery != "" {
			target += "?path=" + urlQueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if s.localRender() && r.Method == http.MethodGet {
		if r.URL.Path == "/_client/events" {
			s.site.ServeEvents(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_client/m/") && s.site.ServeModule(w, r) {
			return
		}
		if isPageRequest(r) {
			err := s.site.ServePage(w, r)
			if err == nil {
				return
			}
			log.Printf("本地渲染失败，改用线上预览：%v", err)
		}
		if s.site.ServePublic(w, r) {
			return
		}
	}
	if s.localRender() && strings.HasPrefix(r.URL.Path, "/_client/") {
		// 本机渲染的模块（SHUTTLE_REMOTE_PREVIEW 时照旧转给线上预览）：没出（刚启动还在编译、或者版本号过时了）就直说，不当成平台接口转出去
		fail(w, http.StatusServiceUnavailable, i18n.New("页面模块还没编译好或者已经更新了：刷新页面再试", "The page module isn't compiled yet or has changed: reload the page"))
		return
	}
	if s.proxy == nil {
		// 项目数据存在 Annulo 这边、没有 creght 站点：平台接口（站点 Func、CMS、表单）都用不了
		if s.cfg.Server {
			fail(w, http.StatusNotFound, i18n.Errorf("这个项目没有 creght 站点，%s 用不了", "This project has no creght site, so %s isn't available", r.URL.Path))
			return
		}
		fail(w, http.StatusNotFound, i18n.Errorf("离线项目没有 creght 站点，%s 用不了（转成在线项目后可以用）", "Offline projects have no creght site, so %s isn't available (convert to an online project to use it)", r.URL.Path))
		return
	}
	s.proxy.ServeHTTP(w, r)
}

// SHUTTLE_REMOTE_PREVIEW=1 时回到以前的做法：页面也反代线上预览（对比本地和线上渲染的差别时用）
func (s *Server) localRender() bool { return brand.Env("REMOTE_PREVIEW") != "1" }

// isPageRequest：iframe 里打开一个页面地址。平台接口、带扩展名的文件（/index.md、/favicon.ico）不算。
func isPageRequest(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/func/") || strings.HasPrefix(p, "/_client/") || path.Ext(path.Base(p)) != "" {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "iframe", "frame":
		return true
	case "", "empty": // 经 service worker 转手的导航是 empty
		return strings.Contains(r.Header.Get("Accept"), "text/html")
	}
	return false
}

func urlQueryEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "%20", "?", "%3F", "=", "%3D").Replace(s)
}

// creghtEnvLoop：助手在 bash 里跑的 creght 命令用 App 里 creght 连接的 token（CREGHT_TOKEN，creght-cli 0.22 起认）。
// access token 一小时过期，这里每 10 分钟换一次进程的环境变量，之后起的命令拿到的都是有效的；没连就去掉，命令行用它自己的登录。
func (s *Server) creghtEnvLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		host := s.loginHost()
		if tok, err := creght.ReadToken(host); err == nil {
			os.Setenv("CREGHT_TOKEN", tok)
			os.Setenv("CREGHT_NO_AUTO_UPDATE", "1")
		} else {
			os.Unsetenv("CREGHT_TOKEN")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
