// Package app 组装并启动 Shuttle：配置、agent、MCP、HTTP 服务。命令行（cmd/shuttle）和
// Mac App（cmd/shuttle-app）共用这一份。
package app

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/mcphub"
	"github.com/annulo/annulo/internal/server"
	"github.com/annulo/annulo/internal/version"
	"github.com/annulo/annulo/internal/webui"
)

type Instance struct {
	URL string
	srv *server.Server
	hub *mcphub.Hub
	hs  *http.Server
	ln  net.Listener
}

// Start 准备好所有组件并开始监听（还没有开始处理请求，调 Serve）。webDir 非空时从这个目录提供前端（开发用）。
func Start(webDir string) (*Instance, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	// 先占端口：授权跳回地址、MCP 地址这些都按实际端口拼
	ln, err := listen(cfg)
	if err != nil {
		return nil, err
	}
	// 没有运营后台也照样启动：界面里有登录和初始化页面
	var ws *creght.Workspace
	if dir := cfg.BackendDir(); dir != "" {
		// 项目不在设置里选的 creght 集群（手改过配置）：不打开，界面回到选项目
		host := creght.DefaultHost
		if cfg.Creght != "" {
			host = creght.NormHost(cfg.Creght)
		}
		if w, err := creght.OpenWorkspaceOn(dir, host); err == nil && (cfg.Creght == "" || w.APIHost == host) {
			ws = w
		}
	}

	var web fs.FS = webui.FS()
	if webDir != "" {
		web = os.DirFS(webDir)
	}
	// agent 的 bash 里要能直接用 annulo 命令（skill 会调 annulo run），把它所在目录放到 PATH 最前面（creght 命令行不再随安装包带，用户自己装的照常在 PATH 里）：
	// CLI 就是自己所在目录；Mac App 里在 Contents/Resources/bin
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		if bin := filepath.Join(dir, "..", "Resources", "bin"); isDir(bin) {
			dir = filepath.Clean(bin)
		}
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	ag := agent.New(cfg)
	ag.ImportUsageOnce()
	if err := ag.InstallSkills(); err != nil {
		fmt.Fprintf(os.Stderr, "  ! 安装 skill 失败：%v\n", err)
	}
	hub := mcphub.New(cfg.Dir)
	hub.CallbackURL = fmt.Sprintf("http://%s/_shuttle/oauth/callback", cfg.Addr())
	srv := server.New(cfg, web, ws, ag, hub)
	if err := hub.Reload(); err != nil {
		fmt.Fprintf(os.Stderr, "  ! MCP 配置有误：%v\n", err)
	}
	inst := &Instance{
		URL: fmt.Sprintf("http://%s/", cfg.Addr()),
		srv: srv, hub: hub, ln: ln,
		hs: &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second},
	}
	fmt.Printf("  ✓ Annulo %s\n", version.Version)
	fmt.Printf("    运营后台  %s\n", inst.URL)
	// 用服务端最后打开的项目：记着的在线项目没连 creght 时不会打开（server.New 换到离线项目）
	if dir := srv.WorkspaceDir(); dir != "" {
		if p := srv.PreviewURL(); p != "" {
			fmt.Printf("    站点预览  %s\n", p)
		}
		fmt.Printf("    工作目录  %s\n", dir)
	} else {
		fmt.Printf("    还没有运营后台：打开上面的地址，按提示登录和初始化\n")
	}
	return inst, nil
}

// Serve 处理请求，直到 Shutdown。
func (i *Instance) Serve() error {
	err := i.hs.Serve(i.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown 先让正在跑的 agent 收尾（部分回复存进历史），再关服务。
func (i *Instance) Shutdown() {
	i.srv.StopRuns(5 * time.Second)
	i.srv.CloseBrowsers()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	i.hs.Shutdown(ctx)
	i.hub.Close()
}

// Running 看这个地址上是不是已经有一个 Shuttle 在跑（比如终端里开着的）。
func Running(addr string) bool {
	c := &http.Client{Timeout: time.Second}
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/_shuttle/api/status", nil)
	req.Header.Set("X-Shuttle", "1")
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.Header.Get(brand.Header) != "" || resp.Header.Get(brand.HeaderNew) != ""
}

// listen 占端口：先试上次用的（默认 7799）。被别的程序占了就往后找一个空的，记进 PortFile，
// App 外壳和命令行按它连。占着的是另一个 Annulo，或者端口是环境变量指定的，就报错不换：
// 同一个数据目录同时跑两个服务，定时任务会跑两遍、配置互相覆盖。
func listen(cfg *config.Config) (net.Listener, error) {
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		if cfg.PortFixed {
			return nil, err
		}
		if Running(cfg.Addr()) {
			return nil, i18n.Errorf("已有 Annulo 在 %s 运行", "Annulo is already running at %s", cfg.Addr())
		}
		taken := cfg.Port
		for p := 7799; p < 7899 && ln == nil; p++ {
			if p == taken {
				continue
			}
			cfg.Port = p
			ln, _ = net.Listen("tcp", cfg.Addr())
		}
		if ln == nil { // 这一段都被占了：让系统随便给一个
			cfg.Port = 0
			if ln, err = net.Listen("tcp", cfg.Addr()); err != nil {
				return nil, err
			}
			cfg.Port = ln.Addr().(*net.TCPAddr).Port
		}
		fmt.Fprintf(os.Stderr, "  ! 端口 %d 被别的程序占用，改用 %d\n", taken, cfg.Port)
	}
	if !cfg.PortFixed {
		os.WriteFile(cfg.PortFile(), []byte(strconv.Itoa(cfg.Port)), 0o600)
	}
	return ln, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
