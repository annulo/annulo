package main

import (
	"fmt"
	"github.com/annulo/annulo/internal/brand"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/annulo/annulo/internal/version"
)

// --app：原生外壳（macos/Shuttle.swift、windows/）把 shuttle 当子进程跑时用。窗口归外壳管，服务归这里：
//   - 日志同时写 ~/.shuttle/logs/app.log，外壳只留最后一段用来在出错时显示；
//   - 外壳崩了或被强制结束时自己退出，不然会一直占着端口、没有窗口能关它（watchHost，各平台不同）。

func appMode(cancel func()) {
	// 服务知道自己跑在 App 里：授权完成页的链接用 shuttle:// 回到 App，而不是在浏览器里开设置页
	os.Setenv("SHUTTLE_IN_APP", "1")
	logToFile()
	watchHost(cancel)
}

func logToFile() {
	dir := filepath.Join(brand.DataDir(), "logs")
	os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "\n==== %s Annulo %s ====\n", time.Now().Format(time.RFC3339), version.Version)
	// stdout / stderr 换成管道，同时写文件和原来的 stderr（App 读它）
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	orig := os.Stderr
	os.Stdout, os.Stderr = w, w
	go io.Copy(io.MultiWriter(f, orig), r)
}
