package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// 本机文件：页面把用户选的文件（主要是视频）直接存在这台电脑上，不传到云端素材库。
// 社媒发布本来就是本机浏览器做的，大视频（几个 GB）没必要先传到 creght 再下载回来；素材库那条路（local/upload）单个最多 200MB。
//
//	POST   /_shuttle/api/local/files         请求体就是文件本身（流式写盘），头 X-Filename（URL 编码的原文件名）
//	                                          → { ref: "local:<name>", url: "/_shuttle/files/<name>", name, filename, size, content_type }
//	DELETE /_shuttle/api/local/files/<name>
//	GET    /_shuttle/files/<name>            给 <video> 预览（带不了 X-Shuttle 头，所以不在 /_shuttle/api 下；文件名随机，猜不到）
//
// 本机函数里 b.upload(选择器, ['local:<name>']) 直接用它（localfn 的 Host.FilesDir）。
// 存在 ~/.shuttle/files/<项目 id>/；超过 maxLocalFileAge 没动过的文件在下次上传时清掉。

const (
	maxLocalFile    = 8 << 30
	maxLocalFileAge = 90 * 24 * time.Hour
	localFilesURL   = "/_shuttle/files/"
)

var (
	localFileRe = regexp.MustCompile(`^[0-9a-f]{32}(\.[a-z0-9]{1,6})?$`)
	extRe       = regexp.MustCompile(`^\.[a-z0-9]{1,6}$`)
)

func (s *Server) localFilesDir() string {
	if s.ws.ProjectID == "" {
		return ""
	}
	return filepath.Join(s.cfg.Dir, "files", s.ws.ProjectID)
}

func (s *Server) apiLocalFileUpload(w http.ResponseWriter, r *http.Request) {
	dir := s.localFilesDir()
	if dir == "" {
		fail(w, http.StatusConflict, i18n.New("还没打开项目", "No project is open"))
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	pruneLocalFiles(dir, maxLocalFileAge)
	filename, _ := url.QueryUnescape(r.Header.Get("X-Filename"))
	filename = filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(filename))
	if !extRe.MatchString(ext) {
		ext = ""
	}
	ctype := r.Header.Get("Content-Type")
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(ctype); len(exts) > 0 && extRe.MatchString(exts[0]) {
			ext = exts[0]
		}
	}
	var id [16]byte
	rand.Read(id[:])
	name := hex.EncodeToString(id[:]) + ext
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxLocalFile))
	f.Close()
	if err != nil {
		os.Remove(path)
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			fail(w, http.StatusRequestEntityTooLarge, i18n.Errorf("文件太大：单个最多 %d GB", "File too large: at most %d GB each", maxLocalFile>>30))
			return
		}
		fail(w, http.StatusBadRequest, err)
		return
	}
	if n == 0 {
		os.Remove(path)
		fail(w, http.StatusBadRequest, i18n.New("文件是空的", "The file is empty"))
		return
	}
	if ctype == "" || ctype == "application/octet-stream" {
		ctype = mime.TypeByExtension(ext)
	}
	writeJSON(w, map[string]any{"ref": "local:" + name, "url": localFilesURL + name, "name": name, "filename": filename, "size": n, "content_type": ctype})
}

func (s *Server) apiLocalFileDelete(w http.ResponseWriter, name string) {
	dir := s.localFilesDir()
	if dir == "" || !localFileRe.MatchString(name) {
		fail(w, http.StatusBadRequest, i18n.New("文件名不对", "Invalid file name"))
		return
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLocalFile 给页面预览本机文件（支持 Range，<video> 能拖进度条）。
func (s *Server) handleLocalFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, localFilesURL)
	dir := s.localFilesDir()
	if dir == "" || !localFileRe.MatchString(name) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// pruneLocalFiles 删掉超过 age 没动过的文件（发布时会读，但不改 mtime：按上传时间算）。
func pruneLocalFiles(dir string, age time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cut := time.Now().Add(-age)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && !e.IsDir() && info.ModTime().Before(cut) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
