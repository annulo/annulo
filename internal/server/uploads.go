package server

import (
	"net/http"
	"strings"

	"github.com/annulo/annulo/internal/agent"
)

// apiUpload：对话里粘贴、上传的图片，请求体就是图片本身。
func (s *Server) apiUpload(w http.ResponseWriter, r *http.Request) {
	name, err := s.agent.SaveUpload(http.MaxBytesReader(w, r.Body, agent.MaxUploadBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"url": agent.UploadURLPrefix + name, "name": name})
}

// handleUpload 给界面显示图片。<img> 带不了 X-Shuttle 头，所以不在 /_shuttle/api 下；
// 门卫照样挡非本机 Host，文件名是内容哈希，猜不到。
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	path := s.agent.UploadPath(strings.TrimPrefix(r.URL.Path, agent.UploadURLPrefix))
	if path == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}
