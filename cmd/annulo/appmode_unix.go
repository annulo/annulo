//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// watchHost（macOS）：从访达打开的 App 拿不到终端的 PATH 和环境变量（creght、git 找不到，api_key_env 读不到），
// 先用登录 shell 读一遍。App 没了就退出：macOS 没有 PDEATHSIG，被过继给 launchd（父进程变成 1）就是宿主没了。
func watchHost(cancel func()) {
	loadShellEnv()
	go func() {
		for range time.Tick(time.Second) {
			if os.Getppid() == 1 {
				fmt.Fprintln(os.Stderr, "Annulo App 已退出，关闭服务")
				cancel()
				return
			}
		}
	}()
}

// loadShellEnv 用用户的登录 shell 跑一次 env，把结果补进当前进程（已经有的不覆盖，PATH 除外）。
func loadShellEnv() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	const mark = "__SHUTTLE_ENV_BEGIN__"
	out, err := exec.CommandContext(ctx, shell, "-ilc", "printf '"+mark+"'; env -0").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取登录 shell 的环境变量失败：%v\n", err)
		return
	}
	if i := bytes.Index(out, []byte(mark)); i >= 0 {
		out = out[i+len(mark):]
	}
	for _, kv := range bytes.Split(out, []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if !ok || k == "" || strings.ContainsAny(k, "\n ") {
			continue
		}
		if k == "PATH" || os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
