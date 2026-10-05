// Package wsgit 用本地 Git 管运营后台项目的改动历史（见 docs/workspace-git.md）。
//
// 只管运营后台：渠道站点按渠道自己的流程走，不经过这里。
// Git 管历史，creght 平台版本只管上线；推送永远不 --force，远端有别人的改动先合进来再推。
package wsgit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
)

// ignore 是项目 .gitignore 的内容：.creght/ 是 creght CLI 的同步状态和备份，AGENTS.md 是它生成的本地说明。
const ignore = ".creght/\nAGENTS.md\n"

var (
	availOnce sync.Once
	availErr  error
)

// Available 报告本机能不能用 git（macOS 没装命令行工具时 /usr/bin/git 会报错）。结果缓存。
func Available() error {
	availOnce.Do(func() {
		if out, err := exec.Command("git", "--version").CombinedOutput(); err != nil {
			availErr = i18n.Errorf("本机没有可用的 git（macOS 在终端运行 xcode-select --install 安装）：%s", "No usable git on this machine (on macOS, run xcode-select --install in a terminal): %s", strings.TrimSpace(string(out)))
		}
	})
	return availErr
}

func git(dir string, args ...string) (string, error) { return gitEnv(dir, nil, args...) }

func gitEnv(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), i18n.Errorf("git %s 失败：%s", "git %s failed: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Lock 用 .git/shuttle.lock 串行化 Git 操作：shuttle push（CLI 进程）和每轮自动提交（服务进程）会同时碰一个仓库。
func Lock(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".git", "shuttle.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlockFile(f); f.Close() }, nil
}

// Ensure 保证项目是个 Git 仓库：没有就 init、写 .gitignore、做初始提交。已经是就什么都不做。
func Ensure(dir string) error {
	if err := Available(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if _, err := git(dir, "init", "-q"); err != nil {
		return err
	}
	// Git for Windows 默认 core.autocrlf=true：回滚、检出会把文件写成 CRLF，再推到 creght 就是满屏的换行差异
	git(dir, "config", "core.autocrlf", "false")
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(ignore), 0o644); err != nil {
			return err
		}
	}
	// 没配过全局身份的用户第一次提交会失败（助手在 bash 里 git commit 也一样）：只在这个仓库里补上
	if out, _ := git(dir, "config", "user.email"); strings.TrimSpace(out) == "" {
		git(dir, "config", "user.name", "Annulo")
		git(dir, "config", "user.email", "shuttle@localhost")
	}
	_, err := commit(dir, "初始化：从 creght 拉下来的运营后台")
	return err
}

// excludeCloud 把生成的站点 Func（localfn.CloudDir）写进 .git/info/exclude：它们由本机函数生成，只推到 creght，不进项目历史。
// 不改 .gitignore：那是项目自己的文件，模板升级时要合并。
func excludeCloud(dir string) error {
	p := filepath.Join(dir, ".git", "info", "exclude")
	line := "/" + localfn.CloudDir + "/"
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	b = append(b, []byte("# shuttle push 生成的本机函数云端版本，不进 git\n"+line+"\n")...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Commit 提交项目里的全部改动（加锁）。没有改动返回 false。
func Commit(dir, msg string) (bool, error) {
	if err := Ensure(dir); err != nil {
		return false, err
	}
	unlock, err := Lock(dir)
	if err != nil {
		return false, err
	}
	defer unlock()
	return commit(dir, msg)
}

func commit(dir, msg string) (bool, error) {
	// 模板升级有冲突时合并停在进行中：冲突文件改好了（没有冲突标记）才提交，提交就完成了这次合并
	_, merr := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	merging := merr == nil
	if merging {
		if left := conflictMarked(dir); len(left) > 0 {
			return false, i18n.Errorf("模板升级的冲突还没解决完，这些文件里还有冲突标记（<<<<<<< / >>>>>>>）：%s", "The template upgrade conflicts aren't fully resolved; these files still have conflict markers (<<<<<<< / >>>>>>>): %s", strings.Join(left, ", "))
		}
	}
	if _, err := git(dir, "add", "-A"); err != nil {
		return false, err
	}
	if out, err := git(dir, "status", "--porcelain"); err != nil || (strings.TrimSpace(out) == "" && !merging) {
		return false, err
	}
	if _, err := git(dir, "commit", "-q", "--no-verify", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

// conflictMarked 是合并冲突的文件里还留着冲突标记的那些。
func conflictMarked(dir string) []string {
	out, _ := git(dir, "diff", "--name-only", "-z", "--diff-filter=U")
	var left []string
	for _, f := range splitZ(out) {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err == nil && (bytes.Contains(b, []byte("\n<<<<<<< ")) || bytes.HasPrefix(b, []byte("<<<<<<< "))) {
			left = append(left, f)
		}
	}
	return left
}

// diffPlan 是 creght diff --json 的输出。

// ErrConflict：合并远端改动时有冲突，文件里留了冲突标记，要解决后重跑。
var ErrConflict = i18n.New("合并远端改动有冲突", "Merging remote changes hit conflicts")

// PushConflict 是推送前合并远端改动时撞上的冲突：Files 里留着 <<<<<<< local / >>>>>>> remote 标记。errors.Is(err, ErrConflict) 也成立。
type PushConflict struct{ Files []string }

func (e *PushConflict) Error() string {
	return i18n.Tf("%s，这些文件里有冲突标记（<<<<<<< local / >>>>>>> remote）：%s。改好后重新 shuttle push", "%s; these files have conflict markers (<<<<<<< local / >>>>>>> remote): %s. Fix them, then run shuttle push again", ErrConflict.Error(), strings.Join(e.Files, ", "))
}

func (e *PushConflict) Unwrap() error { return ErrConflict }

// MarkerFiles 是工作目录里还留着推送冲突标记（<<<<<<< local）的文件；没有返回 nil。
func MarkerFiles(dir string) []string { return conflictFiles(dir, true) }

// Push 把运营后台推到 creght：先提交本地改动，远端有别人的改动先 pull 合进来并单独提交，再正常 push。
// 不 --force；本地删了文件才带 --delete。useGit=false（本机没有 git）时只做 creght 同步。
// 过程写到 log。
func Push(ctx context.Context, dir, msg string, useGit bool, log io.Writer) error {
	if useGit {
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
		if err := excludeCloud(dir); err != nil {
			return err
		}
	}
	// 本机函数的云端版本（cloud 里列的函数）每次推送前重新生成，只推到 creght、不进 git
	if files, err := localfn.WriteCloud(dir); err != nil {
		return err
	} else if len(files) > 0 {
		fmt.Fprintln(log, i18n.T("  · 已生成本机函数的云端版本：", "  · Generated the cloud copies of local functions: ")+strings.Join(files, ", "))
	}
	if useGit {
		if ok, err := commit(dir, msg); err != nil {
			return err
		} else if ok {
			fmt.Fprintln(log, i18n.T("  · 已提交本地改动：", "  · Committed local changes: ")+msg)
		}
	}

	plan, err := diff(ctx, dir)
	if err != nil {
		return err
	}
	var remote []string
	for _, f := range plan.Files {
		switch f.Status { // 远端有别处的改动；ignored-remote（被 .creghtignore 挡住、站点上还有的，比如 AGENTS.md）不算
		case "remote-only", "conflict", "no-base":
			remote = append(remote, f.Path)
		}
	}
	if len(remote) > 0 {
		fmt.Fprintf(log, i18n.T("  · 远端有别处的改动（编辑器或其他设备），先合进来：%s\n", "  · The remote has changes from elsewhere (editor or another device); merging them first: %s\n"), strings.Join(remote, ", "))
		conflicted, err := pullRemote(ctx, dir, log)
		if err != nil {
			return err
		}
		if len(conflicted) > 0 {
			files := conflictFiles(dir, useGit)
			if len(files) == 0 {
				for _, f := range conflicted {
					files = append(files, trimSlash(f))
				}
			}
			return &PushConflict{Files: files}
		}
		if useGit {
			if ok, err := commit(dir, "合入远端改动："+strings.Join(remote, ", ")); err != nil {
				return err
			} else if ok {
				fmt.Fprintln(log, i18n.T("  · 已把远端改动单独提交", "  · Committed the remote changes separately"))
			}
		}
		// pull 可能带回别的设备推过的旧云端版本，按本机函数重新生成一遍
		if _, err := localfn.WriteCloud(dir); err != nil {
			return err
		}
		if plan, err = diff(ctx, dir); err != nil {
			return err
		}
	}

	del, changed := false, 0
	for _, f := range plan.Files {
		if f.Status == "local-change" {
			changed++
			if f.Action == "file_delete" {
				del = true
			}
		}
	}
	if changed == 0 {
		fmt.Fprintln(log, i18n.T("  ✓ 没有要推的改动", "  ✓ Nothing to push"))
		return nil
	}
	if err := pushRemote(ctx, dir, log, del, false); err != nil {
		return err
	}
	fmt.Fprintf(log, i18n.T("  ✓ 已推送 %d 个文件，预览已生效（正式环境没变）\n", "  ✓ Pushed %d file(s); preview is updated (production unchanged)\n"), changed)
	return nil
}

// conflictFiles 找出 pull 后留了冲突标记的文件。
func conflictFiles(dir string, useGit bool) []string {
	var cands []string
	if useGit {
		out, _ := git(dir, "diff", "--name-only", "-z")
		cands = strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	} else {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				cands = append(cands, rel)
			}
			return nil
		})
	}
	var out []string
	for _, c := range cands {
		b, err := os.ReadFile(filepath.Join(dir, c))
		if err == nil && bytes.Contains(b, []byte("<<<<<<< local")) {
			out = append(out, c)
		}
	}
	return out
}
