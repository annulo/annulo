package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/annulo/annulo/internal/browser"
	"github.com/annulo/annulo/internal/extbridge"
	"github.com/annulo/annulo/internal/i18n"
)

// handleBrowserExt：用户 Chrome 里的 Annulo 插件经 native host（annulo 程序，extbridge.RunHost）连进来。
// 在 /_shuttle/api/ 下面，门卫（guard.go）要求带 X-Annulo 头：网页里的 WebSocket 设不了这个头，连不进来。
func (s *Server) handleBrowserExt(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s.browser.Ext.Serve(r.Context(), ws)
}

// registerBrowserHost 把这个 annulo 程序登记成 Chrome 的 native host（每次启动都写：App 挪了位置、更新了也对）。
// go run 的临时程序不登记：它跑完就没了。
func registerBrowserHost(dataDir string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return
	}
	if err := extbridge.Register(exe, dataDir); err != nil {
		log.Printf("登记浏览器插件的 native host 失败：%v", err)
	}
}

// browserSettings：设置 → 浏览器。profiles 是连着的 Chrome profile（插件），chrome 是本机有没有 Chrome
func (s *Server) browserSettings() map[string]any {
	return map[string]any{"profiles": s.browser.Ext.Profiles(), "chrome": browser.FindChrome() != ""}
}

// apiBrowserRevoke：收回一个 Chrome profile 对一个网站的授权，返回新的状态
func (s *Server) apiBrowserRevoke(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID, Site string }
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	c := s.browser.Ext.Conn(in.ID)
	if c == nil {
		fail(w, http.StatusConflict, i18n.New("这个 Chrome 现在没连上：打开它再试", "That Chrome isn't connected: open it and try again"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := c.Revoke(ctx, in.Site); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	// 插件收回后会推 sites 事件更新连接上记的网站，等它一下再回
	for i := 0; i < 20 && c.Allows(siteURL(in.Site)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	writeJSON(w, s.browserSettings())
}

// siteURL：https://www.doubao.com/* → https://www.doubao.com/
func siteURL(pattern string) string {
	if i := len(pattern) - 1; i >= 0 && pattern[i] == '*' {
		return pattern[:i]
	}
	return pattern
}
