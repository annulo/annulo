package wsgit

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 离线项目（只在本机，没有 creght 项目）：从 Shuttle 自带的模板快照建出来，git 照常记历史，
// template 分支记下它基于模板哪一版，以后连上 creght 能照常升级、也能转成在线项目。

// excludeLine 把 line 写进 .git/info/exclude（只在本机生效，不改项目文件）。
func excludeLine(dir, line string) error {
	p := filepath.Join(dir, ".git", "info", "exclude")
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte(s+line+"\n"), 0o644)
}

// InitOffline 给刚从模板快照（已经写进 dir）建出来的离线项目初始化 git：第一个提交就是模板 v 版本，
// 同时记成 template 分支的起点（模板站点 site），升级时以它为基准合并。.shuttle/（离线项目的标记）不进历史。
func InitOffline(dir string, v Version, site string) error {
	if err := Available(); err != nil {
		return err
	}
	if _, err := git(dir, "init", "-q"); err != nil {
		return err
	}
	git(dir, "config", "core.autocrlf", "false")
	if out, _ := git(dir, "config", "user.email"); strings.TrimSpace(out) == "" {
		git(dir, "config", "user.name", "Annulo")
		git(dir, "config", "user.email", "shuttle@localhost")
	}
	for _, d := range brand.MarkerDirs {
		if err := excludeLine(dir, "/"+d+"/"); err != nil {
			return err
		}
	}
	// 模板快照：此刻目录里的全部文件（.gitignore 还没写，.shuttle 已排除）
	idx := filepath.Join(dir, ".git", "shuttle-template.index")
	os.Remove(idx)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitEnv(dir, env, "add", "-A", "--", ".", ":(exclude).creght", ":(exclude)AGENTS.md"); err != nil {
		return err
	}
	tree, err := gitEnv(dir, env, "write-tree")
	if err != nil {
		return err
	}
	sha, err := commitTemplate(dir, strings.TrimSpace(tree), "", v, site)
	if err != nil {
		return err
	}
	// main 从模板这一版开始，再加上项目自己的 .gitignore
	if _, err := git(dir, "reset", "-q", strings.TrimSpace(sha)); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(ignore), 0o644); err != nil {
			return err
		}
	}
	_, err = commit(dir, fmt.Sprintf("初始化：从模板 v%s 新建的离线项目", vlabel(site, v.No)))
	return err
}

// CommitOffline 是离线项目的 shuttle push：只提交到本机 git，没有 creght 可推。
func CommitOffline(dir, msg string, log io.Writer) error {
	if err := Ensure(dir); err != nil {
		return err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if msg == "" {
		msg = "推送前的改动"
	}
	ok, err := commit(dir, msg)
	if err != nil {
		return err
	}
	if ok {
		fmt.Fprintln(log, i18n.T("  · 已提交本地改动：", "  · Committed local changes: ")+msg)
	} else {
		fmt.Fprintln(log, i18n.T("  · 没有要提交的改动", "  · Nothing to commit"))
	}
	fmt.Fprintln(log, i18n.T("  ✓ 这是离线项目：改动只记在本机 git，左侧后台已经是最新的（转成在线项目后才会推到 creght）", "  ✓ This is an offline project: changes are only kept in local git; the back office already shows them (they go to creght after converting to an online project)"))
	return nil
}

// PushNew 把项目推到一个刚建好的 creght 站点（离线项目转在线）：本机的文件就是准，覆盖远端（--delete）。
func PushNew(ctx context.Context, dir string, log io.Writer) error {
	if err := excludeCloud(dir); err != nil {
		return err
	}
	return pushRemote(ctx, dir, log, true, true)
}
