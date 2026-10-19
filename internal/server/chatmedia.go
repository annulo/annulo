package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/annulo/annulo/internal/i18n"
)

// 对话里的本机图片、视频、音频、PDF：助手在回答里写 ![说明](/本机/绝对路径.png)，界面把它显示出来，旁边有按钮用系统程序打开、在文件夹里显示。
// 比如 catbus 登录的二维码图片、下载下来的视频，用户不用去终端或 Finder 里找。
//
//	POST /_shuttle/api/local/media  {path}          → { url: "/_shuttle/media/<token>/<文件名>", name, kind: image|video|audio|pdf, size }
//	POST /_shuttle/api/local/open   {path, reveal}  用系统默认程序打开；reveal 时在 Finder / 资源管理器里显示
//	                                                path 也可以是项目的本机文件 local:<name>（localfiles.go）：页面上的产物「在浏览器中打开」用它，
//	                                                能开的类型多一些（网页、音频、PDF…）；服务端部署时不在用户电脑上开，返回 { open_url } 让页面在新标签页打开
//	GET  /_shuttle/media/<token>/<文件名>             给 <img> / <video> / <audio> 用，PDF 在新标签页打开（带不了 X-Shuttle 头）：token 是服务端发的，猜不到
//
// 只认图片、视频、音频、PDF 这几种扩展名：本服务背后是用户的整个磁盘，接口不能变成任意读文件；
// 打开也只开这几种，免得「用系统程序打开」去执行 .app、.command 这类东西（在文件夹里显示不限）。

const chatMediaURL = "/_shuttle/media/"

var chatMediaKinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".bmp": "image", ".avif": "image", ".svg": "image",
	".mp4": "video", ".m4v": "video", ".mov": "video", ".webm": "video",
	".mp3": "audio", ".m4a": "audio", ".wav": "audio", ".ogg": "audio", ".oga": "audio", ".opus": "audio", ".aac": "audio", ".flac": "audio",
	".pdf": "pdf",
}

var (
	chatMediaKey   = func() []byte { b := make([]byte, 32); rand.Read(b); return b }()
	chatMediaPaths sync.Map // token → 绝对路径；进程重启后界面会重新要
)

// chatMediaPath 把助手写的路径（绝对路径、~/…、file://…）整理成绝对路径，并确认是能显示的图片、视频、音频或 PDF 文件。
func chatMediaPath(raw string, anyFile bool) (path, kind string, size int64, err error) {
	p := strings.TrimSpace(raw)
	if strings.HasPrefix(p, "file://") {
		if u, e := url.Parse(p); e == nil {
			p = u.Path
			if runtime.GOOS == "windows" {
				p = strings.TrimPrefix(p, "/") // file:///C:/a.png
			}
		}
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, e := os.UserHomeDir(); e == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if !filepath.IsAbs(p) {
		return "", "", 0, i18n.Errorf("要本机文件的绝对路径：%s", "Need an absolute local file path: %s", raw)
	}
	p = filepath.Clean(p)
	kind = chatMediaKinds[strings.ToLower(filepath.Ext(p))]
	if kind == "" && !anyFile {
		return "", "", 0, i18n.Errorf("只能显示图片、视频、音频和 PDF：%s", "Only images, videos, audio and PDFs can be shown: %s", filepath.Base(p))
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", "", 0, i18n.Errorf("文件不存在：%s", "File not found: %s", p)
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, i18n.Errorf("不是文件：%s", "Not a file: %s", p)
	}
	return p, kind, info.Size(), nil
}

func chatMediaToken(path string) string {
	m := hmac.New(sha256.New, chatMediaKey)
	m.Write([]byte(path))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (s *Server) apiChatMedia(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	path, kind, size, err := chatMediaPath(in.Path, false)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	token := chatMediaToken(path)
	chatMediaPaths.Store(token, path)
	name := filepath.Base(path)
	writeJSON(w, map[string]any{"url": chatMediaURL + token + "/" + url.PathEscape(name), "name": name, "kind": kind, "size": size, "path": path})
}

func (s *Server) apiChatMediaOpen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path   string `json:"path"`
		Reveal bool   `json:"reveal"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.HasPrefix(in.Path, "local:") {
		s.openLocalFile(w, strings.TrimPrefix(in.Path, "local:"), in.Reveal)
		return
	}
	path, _, _, err := chatMediaPath(in.Path, in.Reveal)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := openInSystem(path, in.Reveal); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// localOpenExts 是项目本机文件里能「用系统程序打开」的类型：AI 做出来的产物（视频、图片、网页、音频、文档）。
// 不开可执行的东西（.app、.command、.sh…）；在文件夹里显示不限。
var localOpenExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".avif": true, ".svg": true,
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true,
	".mp3": true, ".m4a": true, ".wav": true, ".ogg": true, ".aac": true, ".flac": true,
	".html": true, ".htm": true, ".pdf": true, ".txt": true, ".md": true, ".json": true, ".csv": true,
}

// openLocalFile 用系统程序打开项目的本机文件（local:<name>）。HTML 用系统浏览器打开时跑在 file:// 下，不在项目的源里，调不了 Annulo 的接口。
func (s *Server) openLocalFile(w http.ResponseWriter, name string, reveal bool) {
	dir := s.localFilesDir()
	if dir == "" || !localFileRe.MatchString(name) {
		fail(w, http.StatusBadRequest, i18n.New("文件名不对", "Invalid file name"))
		return
	}
	if !reveal && !localOpenExts[filepath.Ext(name)] {
		fail(w, http.StatusBadRequest, i18n.Errorf("这种文件不能直接打开：%s", "This kind of file can't be opened directly: %s", name))
		return
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		fail(w, http.StatusNotFound, i18n.Errorf("文件不存在：%s", "File not found: %s", name))
		return
	}
	// 服务端部署：用户不在这台机器前面，让页面在新标签页打开（本机文件的响应带 CSP sandbox，见 handleLocalFile）
	if s.cfg.Server {
		writeJSON(w, map[string]any{"open_url": localFilesURL + name})
		return
	}
	if err := openInSystem(path, reveal); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// setSandboxCSP 给预览的文件加 CSP sandbox（网页、svg 里的脚本不能拿 Annulo 的源干事）。
// PDF 不加：浏览器自带的 PDF 查看器在 sandbox 的文档里会被拦，PDF 也不会跑页面脚本（nosniff 保证按 application/pdf 处理）。
func setSandboxCSP(w http.ResponseWriter, path, csp string) {
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		return
	}
	w.Header().Set("Content-Security-Policy", csp)
}

// openInSystem 用系统默认程序打开文件；reveal 时在 Finder / 资源管理器里选中它。
func openInSystem(path string, reveal bool) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin" && reveal:
		cmd = exec.Command("open", "-R", path)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", path)
	case runtime.GOOS == "windows" && reveal:
		cmd = exec.Command("explorer", "/select,", path)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case reveal:
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// handleChatMedia 给界面显示本机图片、视频、音频（支持 Range，能拖进度条）。文件会被覆盖（比如重新生成的二维码），不长缓存。
func (s *Server) handleChatMedia(w http.ResponseWriter, r *http.Request) {
	token, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, chatMediaURL), "/")
	v, ok := chatMediaPaths.Load(token)
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	path := v.(string)
	if _, kind, _, err := chatMediaPath(path, false); err != nil || kind == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setSandboxCSP(w, path, "sandbox")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}
