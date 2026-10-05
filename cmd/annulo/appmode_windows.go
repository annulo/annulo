package main

import (
	"fmt"
	"io"
	"os"
)

// watchHost（Windows）：外壳（windows/）通过管道给服务的 stdin，自己不往里写。管道断了（外壳要退出时主动关掉，
// 或者外壳崩了、被结束）就收尾退出。Windows 没有 SIGTERM，这也是外壳让服务「好好退出」的办法：
// 对话收尾、存好历史，再关端口。环境变量不用补：从开始菜单启动的程序本来就拿到用户的 PATH。
func watchHost(cancel func()) {
	go func() {
		io.Copy(io.Discard, os.Stdin)
		fmt.Fprintln(os.Stderr, "Annulo 窗口已退出，关闭服务")
		cancel()
	}()
}
