package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
)

// 本机的 Annulo 能同时开着几个项目（docs/server-and-multi-project.md「多项目同时开」）：命令行按当前目录告诉它是哪个项目
// （<数据目录>/backends/<项目>/… 下面），环境变量 ANNULO_PROJECT 优先；都没有就是 Annulo 的默认项目（最近打开的）。
func setProjectHeader(req *http.Request) {
	if pid := currentProject(); pid != "" {
		req.Header.Set("X-Annulo-Project", pid)
	}
	// 服务端部署：服务要登录，命令行用数据目录里的令牌（internal/auth.CLIToken；本机版没有这个文件）
	if cfg, err := config.Load(); err == nil {
		if b, err := os.ReadFile(filepath.Join(cfg.Dir, "cli-token")); err == nil {
			req.Header.Set("X-Annulo-Token", strings.TrimSpace(string(b)))
		}
	}
}

func currentProject() string {
	if pid := brand.Env("PROJECT"); pid != "" {
		return pid
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return projectOf(filepath.Join(cfg.Dir, "backends"), wd)
}

// projectOf：dir 在 backends/<项目>/ 下面时返回项目 id。
func projectOf(backends, dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	if r, err := filepath.EvalSymlinks(backends); err == nil {
		backends = r
	}
	rel, err := filepath.Rel(backends, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Split(filepath.ToSlash(rel), "/")[0]
}
