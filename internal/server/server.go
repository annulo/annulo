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
	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/connect"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/localsite"
	"github.com/annulo/annulo/internal/mcphub"
	"github.com/annulo/annulo/internal/relay"
	"github.com/annulo/annulo/internal/wsgit"
)

type Server struct {
	cfg     *config.Config
	web     fs.FS
	ws      *creght.Workspace
	creght  *creght.Client
	agent   *agent.Agent
	mcp     *mcphub.Hub
	proxy   *httputil.ReverseProxy
	preview string
	site    *localsite.Renderer // 本机渲染运营后台页面（docs/local-render.md）
	unwatch context.CancelFunc  // 停掉上一个项目的文件监听
	// ready：运营后台已经初始化（ws 等字段已经设好）。没有运营后台时服务照样起来，只开放初始化和设置。
	ready atomic.Bool
	setup setupState
	login loginState
	user  userCache

	tids     tableIDs
	wsTables workspaceTables  // 运营后台 tables/ 声明的表
	secrets  *config.Secrets  // 本机密钥：只给本机函数 ctx.secrets（不进进程环境变量，助手的命令行里没有）
	local    localRuns        // 正在跑的本机函数
	browser  *browser.Manager // 本机函数的 ctx.browser：每个 profile 同时只给一个任务用
	sched    scheduler        // 本机定时（schedules/）
	tasks    taskRunner       // 项目的任务（tasks/*.md）交给助手跑
	page     pageState        // 左侧运营后台页面的运行状态和报错（page_errors 工具）
	conns    *connect.Store   // 连接（Google 等 OAuth 账号），本机函数 ctx.oauth

	runsMu sync.Mutex
	runs   map[string]*agentRun // 正在进行的 agent 对话，按对话 id

	relay *relay.Client // 手机上调到这台电脑的本机函数（relay.go）

	localData *localdb.DB // 离线项目的业务表（本机 SQLite，offline.go）
}

// New 建服务。ws 为 nil 表示还没有运营后台：界面会显示初始化页面，初始化后再 Attach。
func New(cfg *config.Config, web fs.FS, ws *creght.Workspace, ag *agent.Agent, hub *mcphub.Hub) *Server {
	s := &Server{
		cfg:     cfg,
		web:     web,
		agent:   ag,
		mcp:     hub,
		runs:    map[string]*agentRun{},
		secrets: cfg.LoadSecrets(),
		conns:   connect.New(cfg.Dir),
		browser: browser.NewManager(filepath.Join(cfg.Dir, "browser"), checkTarget),
	}
	ag.MCPURL = "http://" + cfg.Addr() + "/_shuttle/mcp"
	setTemplateSources(cfg.Dir, cfg.TemplateSources) // git 模板（gittemplates.go）
	s.relay = s.newRelay()
	s.relay.Start(context.Background())
	go s.runSchedules(context.Background())
	go s.retryFeedback(context.Background())
	go s.creghtEnvLoop(context.Background())
	// agent 的工具 = 读业务表的原语（db_query / db_aggregate，见 dbtools.go）+ 已连接的 MCP 工具；MCP 连上 / 断开 / 改配置时重新组装。
	// Shuttle 不给 agent 写死业务工具：渠道、访问数据这些在运营后台的本机函数里（shuttle run channels.stats …）
	refresh := func() { ag.SetTools(append(append(s.dbTools(), s.pageTools()...), hub.Tools()...)) }
	hub.OnChange(refresh)
	refresh()
	if ws != nil {
		s.Attach(ws)
	}
	return s
}

// Detach 放下当前项目（切 creght 集群时）：界面回到选项目，本机副本和对话历史都不动。
func (s *Server) Detach() {
	s.ready.Store(false)
	if s.unwatch != nil {
		s.unwatch()
		s.unwatch = nil
	}
	s.agent.SetWorkspace("", "")
	s.relay.Reconnect()
}

// Attach 挂上运营后台工作区，然后在后台补齐业务表、迁移旧数据。
func (s *Server) Attach(ws *creght.Workspace) {
	s.ws = ws
	// 表 id 是每个项目各自的：切换项目后还用旧的，读写会落到不存在（或别的项目）的表上
	s.tids.mu.Lock()
	s.tids.ids = nil
	s.tids.mu.Unlock()
	s.creght = creght.NewClient(ws.APIHost)
	s.localData, s.preview, s.proxy = nil, "", nil
	if ws.Offline {
		// 离线项目：业务表在本机 SQLite，没有站点预览可以反代（/api、/func 这类平台接口不可用）
		db, err := localdb.Open(s.offlineDBPath(ws.ProjectID))
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
	s.agent.Reset()
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
// 连了 creght（登录了这个集群）才带上；没连的不出现在 MCP 列表里（docs/annulo-plan.md 第 2 步）。第一次用要在 设置 → MCP 里授权。
func (s *Server) ensureCreghtMCP(host string) {
	if _, err := creght.ReadToken(host); err != nil {
		return
	}
	if err := s.mcp.EnsureServer("creght", mcphub.ServerConfig{Type: "http", URL: host + "/api/mcp"}); err != nil {
		log.Printf("添加 creght MCP 失败：%v", err)
	}
}

func (s *Server) PreviewURL() string {
	if !s.ready.Load() {
		return ""
	}
	return s.preview
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_shuttle/api/", logAPI(s.handleAPI))
	mux.HandleFunc("/_shuttle/oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc(agent.UploadURLPrefix, s.handleUpload)
	mux.HandleFunc(localFilesURL, s.handleLocalFile)  // 本机文件预览（localfiles.go）
	mux.HandleFunc("/_shuttle/img", s.handleImage)    // 图片代理：绕开图床的防盗链，见 imgproxy.go
	mux.HandleFunc(assetsURL, s.handleAsset)          // 离线项目页面上传的文件（offline.go）
	mux.Handle("/_shuttle/mcp", s.agent.MCPHandler()) // 给外部 agent（Claude Code / Codex）用的 Shuttle 工具，带 token 才能调
	mux.HandleFunc("/_shuttle/", s.handleStatic)
	mux.HandleFunc("/_shuttle", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/_shuttle/", http.StatusFound)
	})
	mux.HandleFunc("/", s.handleSite)
	return canonicalPaths(s.guard(mux))
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
	if s.proxy == nil {
		// 离线项目没有 creght 站点：平台接口（站点 Func、CMS、表单）都用不了
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
