package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/wsgit"
)

// shuttle push -m '说明'：把运营后台推到 creght。先 git 提交，远端有别人的改动先合进来，再推（不 --force）。
// 只管运营后台；渠道站点用渠道自己的工具（creght 站点就是 creght push）。见 docs/workspace-git.md。
func pushCmd() *cobra.Command {
	var msg, dir string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "把项目的改动提交到本地 git；在线项目再同步到 creght 预览（渠道站点用它自己的工具推）",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if dir == "" {
				dir, _ = os.Getwd()
			}
			ws, err := backendWorkspace(cfg, dir)
			if err != nil {
				return err
			}
			useGit := true
			if err := wsgit.Available(); err != nil {
				fmt.Fprintln(os.Stderr, "  ! "+err.Error()+i18n.T("。这次只同步到 creght，不记 git 历史", ". Syncing to creght only this time, without git history"))
				useGit = false
			}
			if _, err := creght.ReadOffline(ws); err == nil {
				return wsgit.CommitOffline(ws, msg, os.Stderr) // 离线项目：只记本机 git
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			return wsgit.Push(ctx, ws, msg, useGit, os.Stderr)
		},
	}
	cmd.Flags().StringVarP(&msg, "message", "m", "", "这次改动的说明（写为什么改），作为 git 提交说明")
	cmd.Flags().StringVar(&dir, "dir", "", "项目里的任意目录，默认当前目录")
	return cmd
}

// backendWorkspace 从 dir 往上找 creght 工作区根目录，并确认它是运营后台（~/.shuttle/backends/<项目> 或早期的 ~/.shuttle/backend）。
func backendWorkspace(cfg *config.Config, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root := ""
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".creght", "state.json")); err == nil {
			root = d
			break
		}
		if _, err := os.Stat(filepath.Join(d, creght.OfflineFile)); err == nil {
			root = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	notBackend := i18n.Errorf("shuttle push 只用于运营后台（%s 下的项目）；渠道站点按渠道自己的流程推，creght 站点用 creght push", "shuttle push is only for the back office (projects under %s); push channel sites with their own workflow — creght sites use creght push", filepath.Join(cfg.Dir, "backends"))
	if root == "" {
		return "", notBackend
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	r := real(root)
	if real(filepath.Dir(root)) == real(filepath.Join(cfg.Dir, "backends")) {
		return root, nil
	}
	if p := cfg.LegacyProject(); p != "" && r == real(cfg.DirFor(p)) {
		return root, nil
	}
	return "", notBackend
}
