package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/i18n"
)

// 项目的工作目录（本机 ~/Annulo/<项目名>，服务端部署 <数据目录>/work/<项目名>）：助手做视频、写脚本的工程放这里，
// 模板也可以把一条内容的文件夹放这里。本机函数用 ctx.fs 读写（internal/localfn/fs.go），页面预览用：
//
//	GET /_shuttle/work-files/<工作目录里的路径>   给 <img>、<video>、<audio> 用（支持 Range），PDF 在新标签页打开；. 开头的不给，软链接指到外面的不给。带 CSP sandbox（PDF 除外，见 setSandboxCSP）
//
// 和项目文件预览（preview.go）一样要登录、不给只读角色。

const workFilesURL = "/_shuttle/work-files/"

func (s *Server) workFilesDir() string {
	if s.ws.ProjectID == "" {
		return ""
	}
	return agent.WorkFilesDir(s.cfg.Dir, s.cfg.Server, s.projectName(s.ws.ProjectID, s.ws.Dir), s.ws.ProjectID)
}

// openForFns 是 ctx.fs.open：本机版用系统程序打开；服务端部署用户不在这台机器前面，没有
func (s *Server) openForFns() func(path string, reveal bool) error {
	if s.cfg.Server {
		return nil
	}
	return openInSystem
}

func (s *Server) handleWorkFile(w http.ResponseWriter, r *http.Request) {
	if !s.previewAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := s.workFilesDir()
	rel := path.Clean("/" + strings.TrimPrefix(r.URL.Path, workFilesURL))
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}
	root, err := filepath.EvalSymlinks(dir)
	if dir == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || !strings.HasPrefix(full, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		http.Error(w, i18n.T("没有这个文件", "No such file"), http.StatusNotFound)
		return
	}
	setSandboxCSP(w, full, previewCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, full)
}
