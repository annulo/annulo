package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// 本机文件：页面把用户选的文件（主要是视频）直接存在这台电脑上，不传到云端素材库。
// 社媒发布本来就是本机浏览器做的，大视频（几个 GB）没必要先传到 creght 再下载回来；素材库那条路（local/upload）单个最多 200MB。
//
//	POST   /_shuttle/api/local/files         请求体就是文件本身（流式写盘），头 X-Filename（URL 编码的原文件名）
//	                                          → { ref: "local:<name>", url: "/_shuttle/files/<name>", name, filename, size, content_type }
//	DELETE /_shuttle/api/local/files/<name>
//	GET    /_shuttle/files/<name>            给 <video> 预览（带不了 X-Shuttle 头，所以不在 /_shuttle/api 下；文件名随机，猜不到）。带 CSP sandbox：网页文件直接打开也拿不到项目源的权限
//
// 本机函数里 b.upload(选择器, ['local:<name>']) 直接用它（localfn 的 Host.FilesDir）。助手用 annulo upload --local 走同一条路。
// 存在 ~/.shuttle/files/<项目 id>/；超过 maxLocalFileAge 没动过、业务表里也没引用的文件，在下次上传后清掉（视频的各个版本、资料库里的视频一直留着）。

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
	s.pruneLocalFilesLater(dir)
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
	// 本机文件可能是 AI 做的网页：直接打开这个地址时它跑在项目的源下，能调 /_shuttle/api。加上沙箱，脚本跑在不透明的源里（和 preview.go 一样）；
	// <img>、<video> 引用不受影响
	w.Header().Set("Content-Security-Policy", previewCSP)
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// pruneLocalFilesLater 在后台清理本机文件：要读一遍业务表找引用，不让上传等它。同一时间只跑一个。
func (s *Server) pruneLocalFilesLater(dir string) {
	if !s.pruning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.pruning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		pruneLocalFiles(dir, maxLocalFileAge, func() (map[string]bool, error) { return s.localFileRefs(ctx) })
	}()
}

// localFileRefRe 认表里写的 local:<name>，和页面预览地址 /_annulo/files/<name>（老的 /_shuttle/files/）
var localFileRefRe = regexp.MustCompile(`(?:local:|/files/)([0-9a-f]{32}(?:\.[a-z0-9]{1,6})?)`)

// localFileRefs 读一遍项目所有业务表（包括插件的），找出被引用的本机文件名。
func (s *Server) localFileRefs(ctx context.Context) (map[string]bool, error) {
	defs, err := s.tableDefs()
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	db := localDB{s.cur()}
	for _, d := range defs {
		cursor := ""
		for {
			list, next, _, err := db.Query(ctx, d.Key, localfn.Query{Cursor: &cursor, Limit: 500})
			if err != nil {
				return nil, err
			}
			for _, r := range list {
				b, _ := json.Marshal(r)
				for _, m := range localFileRefRe.FindAllSubmatch(b, -1) {
					refs[string(m[1])] = true
				}
			}
			if next == "" || next == cursor {
				break
			}
			cursor = next
		}
	}
	return refs, nil
}

// pruneLocalFiles 删掉超过 age 没动过（发布时会读，但不改 mtime：按上传时间算）、又没被引用的文件。
// 有该删的才去找引用；找引用出错就一个都不删。
func pruneLocalFiles(dir string, age time.Duration, refs func() (map[string]bool, error)) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cut := time.Now().Add(-age)
	var old []string
	for _, e := range ents {
		if info, err := e.Info(); err == nil && !e.IsDir() && info.ModTime().Before(cut) {
			old = append(old, e.Name())
		}
	}
	if len(old) == 0 {
		return
	}
	keep, err := refs()
	if err != nil {
		log.Printf("清理本机文件：读业务表失败，这次不清：%v", err)
		return
	}
	for _, name := range old {
		if !keep[name] {
			os.Remove(filepath.Join(dir, name))
		}
	}
}
