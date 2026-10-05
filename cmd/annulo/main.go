package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/annulo/annulo/internal/app"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/version"
)

func main() {
	version.FromBuildInfo()
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "  ✗ "+err.Error())
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var webDir string
	var noOpen, appFlag bool
	root := &cobra.Command{
		Use:           "annulo",
		Short:         "Annulo：AI 为你造的，都囤在这儿。打开项目，让助手做页面、表和脚本（老名字 shuttle 也能用）",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
		RunE: func(*cobra.Command, []string) error {
			return serve(webDir, !noOpen && !appFlag, appFlag)
		},
	}
	root.Flags().StringVarP(&webDir, "web", "w", "", "开发用：从这个目录提供前端，而不是内嵌的产物")
	root.Flags().BoolVar(&noOpen, "no-open", false, "启动后不自动打开浏览器")
	root.Flags().BoolVar(&appFlag, "app", false, "由 Mac App 启动（见 appmode.go）")
	root.Flags().MarkHidden("app")
	root.AddCommand(runCmd(), pushCmd(), logsCmd())
	return root
}

func serve(webDir string, open, appFlag bool) error {
	quit := make(chan struct{})
	if appFlag {
		appMode(func() { close(quit) })
		// 已经有一个 Shuttle 在跑（终端里开的，或者另一个 App 的）：窗口直接连它，这里不用再起
		if cfg, err := config.Load(); err == nil && app.Running(cfg.Addr()) {
			fmt.Fprintf(os.Stderr, "已有 Annulo 在 %s 运行，直接用它\n", cfg.Addr())
			return nil
		}
	}
	inst, err := app.Start(webDir)
	if err != nil {
		return err
	}
	if open {
		go openBrowser(inst.URL)
	}
	errc := make(chan error, 1)
	go func() { errc <- inst.Serve() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		return err
	case <-sig:
	case <-quit:
	}
	inst.Shutdown()
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
