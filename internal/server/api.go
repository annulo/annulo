package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/version"
	"github.com/annulo/annulo/internal/wsgit"
)

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(brand.Header, version.Version) // 页面靠它判断在不在 App 里
	w.Header().Set(brand.HeaderNew, version.Version)
	p := strings.TrimPrefix(r.URL.Path, "/_shuttle/api/")
	if err := s.authorize(r, p); err != nil { // 服务端部署按角色限制（authz.go）
		fail(w, http.StatusForbidden, err)
		return
	}
	if s.cfg.Server && strings.HasPrefix(p, "settings/mcp") && r.Method != http.MethodGet {
		// 远程 MCP 的 OAuth 回调：服务端部署按这次请求的地址算（https 域名或本机地址），算不出来就不发起授权
		s.mcp.CallbackURL = s.callbackURL(r)
	}
	if rest, ok := strings.CutPrefix(p, "auth/"); ok {
		s.handleAuth(w, r, rest)
		return
	}
	// 还没有运营后台时只开放：状态、初始化、设置（模型 / MCP / skill）、用量
	if !s.ready.Load() && p != "status" && !strings.HasPrefix(p, "setup") && !strings.HasPrefix(p, "login") && !strings.HasPrefix(p, "settings/") && p != "usage" {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": i18n.T("还没有选择项目", "No project selected yet"), "setup_required": true})
		return
	}
	switch {
	case p == "login" && r.Method == http.MethodPost:
		s.apiLoginStart(w, r)
	case p == "login" && r.Method == http.MethodGet:
		s.apiLoginPoll(w, r)
	case p == "agent/uploads" && r.Method == http.MethodPost:
		s.apiUpload(w, r)
	case p == "login/logout" && r.Method == http.MethodPost:
		s.apiLogout(w, r)
	case p == "setup/rename" && r.Method == http.MethodPost:
		s.apiSetupRename(w, r)
	case p == "setup/delete" && r.Method == http.MethodPost:
		s.apiSetupDelete(w, r)
	case p == "project/instructions" && r.Method == http.MethodGet:
		s.apiInstructionsGet(w, r)
	case p == "project/instructions" && r.Method == http.MethodPut:
		s.apiInstructionsPut(w, r)
	case p == "template" && r.Method == http.MethodGet:
		s.apiTemplateGet(w, r)
	case p == "template/switch" && r.Method == http.MethodPost:
		s.apiTemplateSwitch(w, r)
	case p == "template/upgrade" && r.Method == http.MethodPost:
		s.apiTemplateUpgrade(w, r)
	case p == "template/restore" && r.Method == http.MethodPost:
		s.apiTemplateRestore(w, r)
	case p == "template/take-incoming" && r.Method == http.MethodPost:
		s.apiTemplateTakeIncoming(w, r)
	case p == "template/restore/undo" && r.Method == http.MethodPost:
		s.apiTemplateRestoreUndo(w, r)
	case p == "plugins" || strings.HasPrefix(p, "plugins/"):
		s.handlePlugins(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "plugins"), "/"))
	case p == "setup" && r.Method == http.MethodGet:
		s.apiSetupGet(w, r)
	case p == "setup" && r.Method == http.MethodPost:
		s.apiSetupRun(w, r)
	case p == "status" && r.Method == http.MethodGet:
		s.apiStatus(w, r)
	case p == "settings/templates" && r.Method == http.MethodPost:
		s.apiTemplateSourceAdd(w, r)
	case p == "settings/templates" && r.Method == http.MethodDelete:
		s.apiTemplateSourceRemove(w, r)
	case p == "settings/creght" && r.Method == http.MethodGet:
		s.apiCreghtClusterGet(w, r)
	case p == "settings/creght/token" && r.Method == http.MethodPost:
		s.apiCreghtToken(w, r)
	case p == "settings/creght/custom" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		s.apiCreghtCustom(w, r)
	case p == "settings/creght" && r.Method == http.MethodPut:
		s.apiCreghtClusterSet(w, r)
	case p == "settings/offline" && r.Method == http.MethodPut:
		s.apiOfflineMode(w, r)
	case p == "setup/online" && r.Method == http.MethodPost:
		s.apiGoOnline(w, r)
	case p == "settings/browser" && r.Method == http.MethodGet:
		writeJSON(w, s.browserSettings())
	case p == "settings/browser/revoke" && r.Method == http.MethodPost:
		s.apiBrowserRevoke(w, r)
	case p == "settings/browser/chrome" && r.Method == http.MethodPost:
		if err := browser.LaunchUserChrome(); err != nil {
			fail(w, http.StatusBadRequest, i18n.New("没找到 Chrome：请先安装 Google Chrome", "Chrome not found: please install Google Chrome first"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case p == "settings/domain" || strings.HasPrefix(p, "settings/domain/"):
		s.apiDomain(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "settings/domain"), "/"))
	case p == "settings/version" && r.Method == http.MethodGet:
		s.apiServerVersion(w, r)
	case p == "settings/node" && (r.Method == http.MethodGet || r.Method == http.MethodPut) && s.nodeClient != nil:
		s.apiNodeSettings(w, r)
	case p == "nodes" || strings.HasPrefix(p, "nodes/"):
		s.apiNodes(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "nodes"), "/"))
	case p == "settings/remote/tunnel" && (r.Method == http.MethodGet || r.Method == http.MethodPut) && s.tunnel != nil:
		s.apiRemoteTunnel(w, r)
	case (p == "remote/devices" || strings.HasPrefix(p, "remote/devices/")) && s.tunnel != nil:
		s.apiRemoteDevices(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "remote/devices"), "/"))
	case p == "settings/remote" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		s.apiRemoteSettings(w, r)
	case p == "settings/language" && r.Method == http.MethodPut:
		var in struct{ Language string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.agent.SetLanguage(in.Language); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"language": s.agent.LanguagePref(), "locale": s.agent.Locale()})
	case p == "settings/llm" && r.Method == http.MethodGet:
		s.apiLLMGet(w, r)
	case p == "settings/llm" && r.Method == http.MethodPut:
		s.apiLLMUpdate(w, r)
	case p == "settings/llm/models" && r.Method == http.MethodPost:
		s.apiLLMSaveModel(w, r)
	case strings.HasPrefix(p, "settings/llm/models/") && r.Method == http.MethodDelete:
		s.apiLLMDeleteModel(w, r, strings.TrimPrefix(p, "settings/llm/models/"))
	case p == "settings/llm/providers" && r.Method == http.MethodPost:
		s.apiLLMSaveProvider(w, r)
	case strings.HasPrefix(p, "settings/llm/providers/") && r.Method == http.MethodDelete:
		s.apiLLMDeleteProvider(w, r, strings.TrimPrefix(p, "settings/llm/providers/"))
	case p == "settings/llm/model-defaults" && r.Method == http.MethodPost:
		s.apiLLMModelDefaults(w, r)
	case p == "settings/llm/provider-models" && r.Method == http.MethodPost:
		s.apiLLMProviderModels(w, r)
	case p == "settings/llm/test" && r.Method == http.MethodPost:
		s.apiLLMTest(w, r)
	case p == "usage" && r.Method == http.MethodGet:
		rep, err := s.agent.Usage(parseDays(r))
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, rep)
	case p == "settings/skills" && r.Method == http.MethodGet:
		s.apiSkills(w, r)
	case p == "settings/skills" && r.Method == http.MethodDelete:
		s.apiSkillRemove(w, r)
	case p == "settings/skills/install" && r.Method == http.MethodPost:
		s.apiSkillInstall(w, r)
	case p == "settings/skills/toggle" && r.Method == http.MethodPost:
		s.apiSkillToggle(w, r)
	case p == "settings/mcp" && r.Method == http.MethodGet:
		s.apiMCPGet(w, r)
	case p == "settings/mcp" && r.Method == http.MethodPut:
		s.apiMCPSave(w, r)
	case p == "settings/mcp/tool" && r.Method == http.MethodPost:
		s.apiMCPTool(w, r)
	case p == "settings/mcp/reconnect" && r.Method == http.MethodPost:
		s.apiMCPAction(w, r, s.mcp.Reconnect)
	case p == "settings/mcp/logout" && r.Method == http.MethodPost:
		s.apiMCPAction(w, r, func(name string) error {
			if err := s.mcp.Logout(name); err != nil {
				return err
			}
			return s.mcp.Reconnect(name)
		})
	case p == "agent/chat" && r.Method == http.MethodPost:
		s.apiAgentChat(w, r)
	case strings.HasPrefix(p, "agent/chat/") && strings.HasSuffix(p, "/stream") && r.Method == http.MethodGet:
		s.apiAgentResume(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "agent/chat/"), "/stream"))
	case p == "agent/chats" && r.Method == http.MethodGet:
		list, err := s.agent.ListChats()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"list": list})
	case strings.HasPrefix(p, "agent/chats/") && strings.HasSuffix(p, "/feedback") && r.Method == http.MethodPost:
		s.apiChatFeedback(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "agent/chats/"), "/feedback"))
	case strings.HasPrefix(p, "agent/chats/") && r.Method == http.MethodGet:
		c, err := s.agent.LoadChat(strings.TrimPrefix(p, "agent/chats/"))
		if err != nil {
			fail(w, http.StatusNotFound, i18n.New("没有这段对话", "No such chat"))
			return
		}
		writeJSON(w, c)
	case strings.HasPrefix(p, "agent/chats/") && r.Method == http.MethodPatch:
		// {"title": …}：用户改名，之后不再自动起名
		var in struct {
			Title string `json:"title"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		json.Unmarshal(b, &in)
		if err := s.agent.SetTitle(strings.TrimPrefix(p, "agent/chats/"), in.Title, "user"); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case strings.HasPrefix(p, "agent/chats/") && r.Method == http.MethodDelete:
		if err := s.agent.DeleteChat(strings.TrimPrefix(p, "agent/chats/")); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case strings.HasPrefix(p, "db/"):
		s.apiDB(w, r, strings.TrimPrefix(p, "db/"))
	case p == "fetch" && r.Method == http.MethodPost:
		s.apiFetch(w, r)
	case p == "local/functions" && r.Method == http.MethodGet:
		s.apiLocalFunctions(w, r)
	case p == "local/llm/providers" && r.Method == http.MethodGet:
		s.apiLLMProviders(w, r)
	case p == "local/secrets" && r.Method == http.MethodGet:
		// 页面问几个密钥配了没有：?names=A,B → { set: { A: true, B: false } }，不返回值、也不返回尾号
		set := map[string]bool{}
		for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
			if n = strings.TrimSpace(n); n != "" {
				_, set[n] = s.secrets.Get(n)
			}
		}
		writeJSON(w, map[string]any{"set": set})
	case p == "local/secrets" && r.Method == http.MethodPut:
		// 页面写一个密钥（比如添加渠道时填的应用密码）：只能写、不能读回
		var in struct{ Name, Value string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.secrets.Set(strings.TrimSpace(in.Name), strings.TrimSpace(in.Value)); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case p == "ui/page" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.apiPage(w, r)
	case p == "ui/symbolicate" && r.Method == http.MethodPost:
		s.apiSymbolicate(w, r)
	case p == "local/upload" && r.Method == http.MethodPost:
		s.apiAssetUpload(w, r)
	case p == "local/files" && r.Method == http.MethodPost:
		s.apiLocalFileUpload(w, r)
	case strings.HasPrefix(p, "local/files/") && r.Method == http.MethodDelete:
		s.apiLocalFileDelete(w, strings.TrimPrefix(p, "local/files/"))
	case p == "local/media" && r.Method == http.MethodPost:
		s.apiChatMedia(w, r)
	case p == "refs/resolve" && r.Method == http.MethodPost:
		s.apiRefsResolve(w, r)
	case p == "local/open" && r.Method == http.MethodPost:
		s.apiChatMediaOpen(w, r)
	case p == "local/ask" && r.Method == http.MethodPost:
		s.apiLocalAsk(w, r)
	case p == "local/run" && r.Method == http.MethodPost:
		s.apiLocalRun(w, r)
	case p == "local/logs" && r.Method == http.MethodGet:
		s.apiRunLogs(w, r)
	case p == "local/render/reset" && r.Method == http.MethodPost:
		// 顶栏「刷新页面」：清掉本地渲染的编译缓存、重新构建，再刷新后台页面
		if s.site != nil {
			s.site.Reset(r.Context())
		}
		writeJSON(w, map[string]bool{"ok": true})
	case p == "local/browsers" && r.Method == http.MethodGet:
		s.apiLocalBrowsers(w, r)
	case strings.HasPrefix(p, "local/runs/") && strings.HasSuffix(p, "/show") && r.Method == http.MethodPost:
		s.apiLocalShow(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "local/runs/"), "/show"))
	case strings.HasPrefix(p, "local/runs/") && strings.HasSuffix(p, "/abort") && r.Method == http.MethodPost:
		s.apiLocalAbort(w, strings.TrimSuffix(strings.TrimPrefix(p, "local/runs/"), "/abort"))
	case p == "local/tasks" || strings.HasPrefix(p, "local/tasks/"):
		s.apiTasks(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "local/tasks"), "/"))
	case p == "settings/subscription" && r.Method == http.MethodGet:
		s.apiSubscription(w, r)
	case p == "local/inboxes" || strings.HasPrefix(p, "local/inboxes/"):
		s.apiInboxes(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "local/inboxes"), "/"))
	case p == "local/schedules" || strings.HasPrefix(p, "local/schedules/"):
		s.apiSchedules(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "local/schedules"), "/"))
	case p == "settings/connections" && r.Method == http.MethodGet:
		// 连接按项目分开（Google、Facebook…）：列的是当前项目的；creght 账号整台电脑共用，另走 settings/creght
		writeJSON(w, map[string]any{"list": s.conns.List(s.connProject())})
	case strings.HasPrefix(p, "settings/connections/") && strings.HasSuffix(p, "/client") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		// 这种连接用的 OAuth 客户端（服务端部署在页面上配自己的，AGENTS.md「服务端部署：配置都在页面上」）
		key := strings.TrimSuffix(strings.TrimPrefix(p, "settings/connections/"), "/client")
		if r.Method == http.MethodPut {
			var in struct{ ClientID, ClientSecret string }
			if err := readJSON(r, &in); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
			if err := s.conns.SetClient(key, in.ClientID, in.ClientSecret); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
		}
		id, custom, ok := s.conns.Client(key)
		if !ok {
			fail(w, http.StatusNotFound, i18n.Errorf("没有这种连接：%s", "No such connection: %s", key))
			return
		}
		writeJSON(w, map[string]any{"client_id": id, "custom": custom, "redirect_uri": s.callbackURL(r)})
	case strings.HasPrefix(p, "settings/connections/") && strings.HasSuffix(p, "/connect") && r.Method == http.MethodPost:
		cb := s.callbackURL(r)
		if cb == "" {
			fail(w, http.StatusBadRequest, errNeedHTTPS)
			return
		}
		u, err := s.conns.Start(s.connProject(), strings.TrimSuffix(strings.TrimPrefix(p, "settings/connections/"), "/connect"), cb)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"auth_url": u})
	case strings.HasPrefix(p, "settings/connections/") && r.Method == http.MethodDelete:
		// ?account= 只断开这一个账号，不给是这种连接的全部账号
		if err := s.conns.Disconnect(s.connProject(), strings.TrimPrefix(p, "settings/connections/"), r.URL.Query().Get("account")); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"list": s.conns.List(s.connProject())})
	case p == "settings/secrets" && r.Method == http.MethodGet:
		// needed：本机函数里用到的密钥名，设置页据此提示还缺哪个
		writeJSON(w, map[string]any{"list": s.secrets.List(s.secretNamesInUse()...), "needed": s.secretNamesInUse()})
	case p == "settings/secrets" && r.Method == http.MethodPut:
		var in struct{ Name, Value string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.secrets.Set(strings.TrimSpace(in.Name), strings.TrimSpace(in.Value)); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"list": s.secrets.List(s.secretNamesInUse()...)})
	case strings.HasPrefix(p, "settings/secrets/") && r.Method == http.MethodDelete:
		if err := s.secrets.Delete(strings.TrimPrefix(p, "settings/secrets/")); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"list": s.secrets.List(s.secretNamesInUse()...)})
	case p == "agent/steer" && r.Method == http.MethodPost:
		s.apiAgentSteer(w, r)
	case p == "agent/running" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{"chats": s.agent.ChatStatuses()})
	case p == "agent/abort" && r.Method == http.MethodPost:
		// {"chat_id": …} 只停这段对话；不带就全停
		var in struct {
			ChatID string `json:"chat_id"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		json.Unmarshal(b, &in)
		s.agent.Abort(in.ChatID)
		writeJSON(w, map[string]any{"ok": true})
	case p == "agent/reset" && r.Method == http.MethodPost:
		s.agent.Reset()
		writeJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个接口：%s %s", "No such endpoint: %s %s", r.Method, p))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	if errors.Is(err, creght.ErrNotLoggedIn) {
		code = http.StatusUnauthorized
	}
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	// 第一次打开、还没选过模型时，默认模型取决于平台的模型列表：先拉一次（有缓存）
	s.agent.RefreshCreghtModels(r.Context(), false)
	llm, llmErr := s.agent.LLM()
	st := map[string]any{
		"server":     s.cfg.Server,                                     // 服务端部署：界面显示账号、按角色收起助手和设置（authz.go）
		"remote":     remoteOf(r) != nil,                               // 经隧道远程打开的（remote.go）：界面收起只在电脑上能用的东西
		"creght_pat": s.cfg.Server && creght.Has(s.loginHost(), "pat"), // 服务器上连 creght 用个人令牌（平台支持时）
		// creght_oauth：能不能用账号授权（OAuth）连：本机版总能；服务端部署要 https 域名或用 localhost 打开（授权方只认这两种回调）
		"creght_oauth": s.callbackURL(r) != "",
		"user":         userOf(r.Context()),
		"cert":         s.certWarning(r),  // 服务端部署：证书签不下来、快到期、还没绑域名（domain.go）
		"update":       s.updateNotice(r), // 服务端部署：官网有新版本（serverupdate.go）
		"version":      version.Version,
		"agent":        map[string]any{"busy": s.agent.Busy(), "running": s.agent.RunningChats(), "running_all": s.root.AllRunningChats()}, // running_all：所有打开着的项目（退出 App 前确认）
		// language：设置里的选择（auto / zh / en）；locale：生效的界面语言（zh / en），外壳、左侧后台、助手都用它
		"language": s.agent.LanguagePref(),
		"locale":   s.agent.Locale(),
		"llm": map[string]any{
			"ready":     llmErr == nil,
			"model":     llm.Label(),
			"error":     errString(llmErr),
			"reasoning": llm.Reasoning,
			"provider":  llm.Provider,
			// thinking：当前模型实际用的档（选的档它没有时是最接近的那档）；thinking_levels：当前模型支持的档
			"thinking":        agent.EffectiveThinking(llm, s.agent.ThinkingLevel()),
			"thinking_levels": agent.ModelThinking(llm).Levels,
		},
	}
	host := s.loginHost()
	_, tokErr := creght.ReadToken(host)
	cr := map[string]any{"api_host": host, "logged_in": tokErr == nil}
	if tokErr == nil {
		if u := s.currentUser(r.Context()); u != nil {
			cr["user"] = u
		} else {
			// 有 token 但查不到用户：过期或被吊销了，当作没登录
			cr["logged_in"] = false
		}
	}
	st["creght"] = cr
	st["offline_mode"] = s.cfg.Offline
	if r.URL.Query().Get("recheck_git") == "1" { // 界面上「重新检测」：用户刚装完 git
		wsgit.Recheck()
	}
	st["git"] = map[string]any{"ok": wsgit.Available() == nil, "error": errString(wsgit.Available()), "windows": runtime.GOOS == "windows"}
	if !s.ready.Load() {
		st["setup_required"] = true
		writeJSON(w, st)
		return
	}
	st["api"] = version.API
	st["assistant"] = projectAssistant(s.ws.Dir) // 助手面板的名字、介绍、示例问题（项目的 annulo.json / shuttle.json）
	// 项目的 shuttle.json 要求的能力版本比这台 Shuttle 高：界面上提示先更新 Shuttle（项目在别处用新版升级过）
	if need := wsgit.ProjectMinAPI(s.ws.Dir); need > version.API {
		st["project_requires"] = need
	}
	st["backend"] = map[string]any{
		"project_id":  s.ws.ProjectID,
		"site_id":     s.ws.SiteID,
		"dir":         s.ws.Dir,
		"preview_url": s.preview,
		"offline":     s.ws.Offline,
	}
	if s.ws.Offline {
		if p, err := creght.ReadOffline(s.ws.Dir); err == nil {
			st["backend"].(map[string]any)["name"] = p.Name
		}
	}
	writeJSON(w, st)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sites 不含运营后台自己。
func (s *Server) sites(ctx context.Context) ([]creght.Site, error) {
	all, err := s.creght.Sites(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, x := range all {
		if x.ProjectID == s.ws.ProjectID {
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

func parseDays(r *http.Request) int {
	d, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if d <= 0 {
		d = 30
	}
	return min(d, 365)
}
