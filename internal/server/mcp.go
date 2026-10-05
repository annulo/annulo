package server

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/connect"
	"github.com/annulo/annulo/internal/creght"
)

func (s *Server) apiMCPGet(w http.ResponseWriter, r *http.Request) {
	b, err := s.mcp.ReadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"config": string(b), "file": s.mcp.File(), "servers": s.mcp.Status()})
}

func (s *Server) apiMCPSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config string `json:"config"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err := json.Unmarshal(b, &in); err != nil {
		fail(w, http.StatusBadRequest, i18n.New("请求体不是合法 JSON", "The request body isn't valid JSON"))
		return
	}
	if err := s.mcp.SaveConfig([]byte(in.Config)); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiMCPGet(w, r)
}

func (s *Server) apiMCPTool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Tools   []string `json:"tools"`
		Enabled bool     `json:"enabled"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err := json.Unmarshal(b, &in); err != nil || in.Name == "" || len(in.Tools) == 0 {
		fail(w, http.StatusBadRequest, i18n.New("要给出 name、tools 和 enabled", "name, tools and enabled are required"))
		return
	}
	if err := s.mcp.SetToolsEnabled(in.Name, in.Tools, in.Enabled); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiMCPGet(w, r)
}

func (s *Server) apiMCPAction(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	if err := fn(r.URL.Query().Get("name")); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.apiMCPGet(w, r)
}

// handleOAuthCallback 是 MCP 远程 server 授权完成后的跳转地址。浏览器顶层打开，返回一个简单页面。
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	errMsg := q.Get("error_description")
	if errMsg == "" {
		errMsg = q.Get("error")
	}
	title, body, back := i18n.T("授权完成", "Authorization complete"), i18n.T("可以关掉这个页面，回到 Annulo 的设置页查看连接状态。", "You can close this page and check the connection in Annulo's Settings."), "/_shuttle/settings#mcp"
	connected := "" // 连接（Google 等）成功时是连上的名字
	var err error
	// 两种授权共用这个回调：连接（Google 等，state 带前缀）和 MCP
	if st := q.Get("state"); strings.HasPrefix(st, creght.OAuthStatePrefix) {
		back = "/_shuttle/settings#connections"
		if err = s.finishCreghtOAuth(r.Context(), st, q.Get("code"), errMsg); err == nil {
			connected = "creght"
			body = i18n.T("已经连上 creght。可以关掉这个页面，回到 Annulo。", "Connected to creght. You can close this page and return to Annulo.")
		}
	} else if strings.HasPrefix(st, connect.StatePrefix) {
		back = "/_shuttle/settings#connections"
		var name string
		name, err = s.conns.Callback(r.Context(), st, q.Get("code"), q.Get("error"))
		if err == nil {
			connected = name
			body = i18n.Tf("已经连上 %s。可以关掉这个页面，回到 Annulo。", "Connected to %s. You can close this page and return to Annulo.", name)
		}
	} else {
		err = s.mcp.Callback(st, q.Get("code"), q.Get("iss"), errMsg)
	}
	if err != nil {
		title, body = i18n.T("授权没有完成", "Authorization didn't complete"), err.Error()
		if strings.Contains(body, "授权链接已经用过或者过期") || strings.Contains(body, "already used or has expired") {
			title = i18n.T("这个授权链接失效了", "This authorization link has expired")
		}
	}
	// 在 App 里用的：授权页是在系统浏览器里打开的，链接用 annulo:// 回到 App（安装包注册了 annulo:// 和老的 shuttle://），
	// 成功时顺便自动跳一次；浏览器里用的（shuttle 命令直接起的服务）才是打开设置页。
	links := `<a href="` + back + `" style="color:#2563eb">` + i18n.T("打开设置页", "Open Settings") + `</a>`
	auto := ""
	if brand.Env("IN_APP") == "1" {
		app := "annulo://" + strings.TrimPrefix(back, "/_shuttle/")
		links = `<a href="` + app + `" style="display:inline-block;background:#2563eb;color:#fff;padding:8px 18px;border-radius:8px;text-decoration:none">` + i18n.T("回到 Annulo", "Back to Annulo") + `</a>`
		if err == nil {
			auto = `<script>location.href = ` + strconv.Quote(app) + `</script>`
			body = i18n.T("正在回到 Annulo…", "Returning to Annulo…")
			if connected != "" {
				body = i18n.Tf("已经连上 %s。正在回到 Annulo…", "Connected to %s. Returning to Annulo…", connected)
			}
		}
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>` + title + `</title>
<body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#fafafa">
<div style="background:#fff;border:1px solid #e4e4e7;border-radius:12px;padding:32px 40px;text-align:center;max-width:420px">
<h2 style="margin:0 0 8px">` + title + `</h2><p style="color:#71717a;margin:0 0 20px">` + html.EscapeString(body) + `</p>
` + links + `</div>` + auto))
}
