package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
)

// handleStatic 提供 Shuttle 界面（Vite 构建产物，base=/_shuttle/）。SPA：没有扩展名的路径
// 都回 index.html；带扩展名但找不到的直接 404，免得把 HTML 当 JS 返回。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(i18n.T("前端没有构建：先运行 make web，或用 --web 指定目录", "The frontend isn't built: run make web first, or pass a directory with --web")))
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/_shuttle/")
	if p == "" {
		p = "index.html"
	}
	// 浏览器打开没带项目的地址（127.0.0.1:<端口>）：跳到默认项目自己的源 <项目>.localhost（每个项目一个源，见 view.go）。
	// ANNULO_SINGLE_ORIGIN=1 时不跳（浏览器解析不了 *.localhost 时用）
	if r.Header.Get("Sec-Fetch-Dest") == "document" && (path.Ext(p) == "" || p == "index.html") {
		if s.ready.Load() && remoteOf(r) == nil && hostProject(r.Host) == "" && s.projectURL(s.ws.ProjectID) != "" {
			target := strings.TrimSuffix(s.projectURL(s.ws.ProjectID), "/_shuttle/") + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
	}
	f, err := s.web.Open(p)
	if err != nil {
		if path.Ext(p) != "" {
			http.NotFound(w, r)
			return
		}
		p = "index.html"
		f, err = s.web.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if p == "index.html" {
		w.Header().Set("cache-control", "no-cache")
	} else if strings.HasPrefix(p, "assets/") {
		w.Header().Set("cache-control", "public, max-age=31536000, immutable")
	}
	rs, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		b, _ := fs.ReadFile(s.web, p)
		w.Write(b)
		return
	}
	http.ServeContent(w, r, p, st.ModTime(), rs)
}
