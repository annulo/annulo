package server

import (
	"encoding/json"
	"net/http"
)

// 在浏览器里打开的 Annulo（远程访问、服务端部署、本机浏览器）能「添加到桌面」：Web App Manifest（web/index.html 里的 <link rel="manifest">）。
// 每个项目一个源，所以每个项目各是一个 App：名字是项目名，从外壳打开。
//
// 不注册 service worker：它会接管同源的所有请求，后台页面（iframe）、对话的事件流都要经它转一手，
// 而服务端靠 Sec-Fetch-Dest 区分「地址栏打开外壳」和「iframe 里的页面」（server.go handleSite），转手后这个头就变了。
// 现在的浏览器装 App 不要求 service worker。
const manifestURL = "/_shuttle/manifest.webmanifest"

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	name := "Annulo"
	if s.ready.Load() {
		name = s.projectName(s.ws.ProjectID, s.ws.Dir)
	}
	// 远程打开时外壳就在根路径上（地址栏不带 /_shuttle/，web/src/lib/router.ts），本机、服务端部署在 /_shuttle/
	start := "/_shuttle/"
	if remoteOf(r) != nil {
		start = "/"
	}
	w.Header().Set("content-type", "application/manifest+json; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	json.NewEncoder(w).Encode(map[string]any{
		"id":               start,
		"name":             name,
		"short_name":       name,
		"description":      "Annulo",
		"start_url":        start,
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#0a0a0a",
		"theme_color":      "#0a0a0a",
		"icons": []map[string]string{
			{"src": "/_shuttle/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/_shuttle/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": "/_shuttle/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	})
}
