// Package localcmd 跑本机命令（外部 agent、本机函数的 ctx.exec 共用）。Electron 从 Finder 启动时 PATH 只有系统目录，
// 所以按登录 shell 的 PATH 加上常见的安装目录找命令，找到的缓存 1 分钟（装了马上能用）。
package localcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var lookup struct {
	sync.Mutex
	path  string // 登录 shell 的 PATH
	found map[string]string
	at    time.Time
}

// Find 返回命令的完整路径，找不到是空。
func Find(bin string) string {
	lookup.Lock()
	defer lookup.Unlock()
	if time.Since(lookup.at) > time.Minute {
		lookup.found, lookup.at = map[string]string{}, time.Now()
	}
	if p, ok := lookup.found[bin]; ok {
		return p
	}
	p := ""
	if lp, err := exec.LookPath(bin); err == nil {
		p = lp
	} else {
	dirs:
		for _, dir := range filepath.SplitList(loginPath()) {
			for _, ext := range exts() {
				if c := filepath.Join(dir, bin+ext); isExec(c) {
					p = c
					break dirs
				}
			}
		}
	}
	lookup.found[bin] = p
	return p
}

// Path 是登录 shell 的 PATH 加上常见的安装目录。codex 是 node 脚本，跑它也要这份 PATH 才找得到 node。
func Path() string {
	lookup.Lock()
	defer lookup.Unlock()
	return loginPath()
}

// Env 是当前进程的环境变量，PATH 换成 Path()，再加上 extra（KEY=VALUE，后面的覆盖前面的）。
func Env(extra ...string) []string {
	p := Path()
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	return append(append(env, "PATH="+p), extra...)
}

// Windows 上命令带扩展名（.exe、.cmd…），按 PATHEXT 一个个试
func exts() []string {
	if runtime.GOOS != "windows" {
		return []string{""}
	}
	list := []string{""}
	for _, e := range strings.Split(os.Getenv("PATHEXT"), ";") {
		if e != "" {
			list = append(list, strings.ToLower(e))
		}
	}
	if len(list) == 1 {
		list = append(list, ".com", ".exe", ".bat", ".cmd")
	}
	return list
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0)
}

// loginPath 调用方持有 lookup。
func loginPath() string {
	if lookup.path != "" {
		return lookup.path
	}
	home, _ := os.UserHomeDir()
	parts := []string{os.Getenv("PATH")}
	if runtime.GOOS != "windows" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/zsh"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, sh, "-lic", "echo __PATH__$PATH").Output()
		cancel()
		if err == nil {
			if _, p, ok := strings.Cut(string(out), "__PATH__"); ok {
				parts = append(parts, strings.TrimSpace(p))
			}
		}
		parts = append(parts, filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local"), "/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".npm-global", "bin"))
	} else {
		parts = append(parts, filepath.Join(home, ".local", "bin"), filepath.Join(os.Getenv("APPDATA"), "npm"))
	}
	seen := map[string]bool{}
	var dirs []string
	for _, p := range parts {
		for _, d := range filepath.SplitList(p) {
			if d != "" && !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	lookup.path = strings.Join(dirs, string(os.PathListSeparator))
	return lookup.path
}
