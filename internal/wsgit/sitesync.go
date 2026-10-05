package wsgit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/creght-dev/creght-cli/pkg/sitesync"

	cr "github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
)

// 和 creght 同步站点代码（pull / diff / push）用 creght-cli 的 Go 包 sitesync（docs/annulo-plan.md 第 5 步「同步为什么用 Go 包」）：
// 不起子进程、不解析输出；工作区格式和 creght 命令行一样，两边能交替用同一个目录。token 来自 App 里的 creght 连接（每次请求前现取，会刷新）。

func syncClient(host string, log io.Writer) (*sitesync.Client, error) {
	if host == "" {
		host = cr.DefaultHost
	}
	return sitesync.New(sitesync.Options{
		Host:  host,
		Token: func(context.Context) (string, error) { return cr.ReadToken(host) },
		Log:   log,
	})
}

// syncErr 把没授权换成「去连接 creght」。
func syncErr(err error) error {
	if err != nil && sitesync.IsUnauthorized(err) {
		return cr.ErrNotLoggedIn
	}
	return err
}

// workspaceHost 是工作区记的集群（.creght/state.json 的 api_host），老工作区没记就用 App 设的 CREGHT_API_HOST。
func workspaceHost(dir string) string {
	if ws, err := sitesync.OpenWorkspace(dir); err == nil && ws.APIHost != "" {
		return ws.APIHost
	}
	return os.Getenv("CREGHT_API_HOST")
}

type diffPlan struct {
	HasConflicts bool
	Files        []sitesync.DiffEntry
}

func diff(ctx context.Context, dir string) (*diffPlan, error) {
	c, err := syncClient(workspaceHost(dir), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Diff(ctx, dir, sitesync.DiffOptions{Delete: true})
	if err != nil {
		return nil, syncErr(err)
	}
	return &diffPlan{HasConflicts: res.HasConflicts, Files: res.Files}, nil
}

// pullRemote 把远端的改动合进工作区；有冲突返回冲突的文件（文件里留着 <<<<<<< local / >>>>>>> remote）。
func pullRemote(ctx context.Context, dir string, log io.Writer) ([]string, error) {
	ws, err := sitesync.OpenWorkspace(dir)
	if err != nil {
		return nil, err
	}
	c, err := syncClient(workspaceHost(dir), log)
	if err != nil {
		return nil, err
	}
	res, err := c.Pull(ctx, dir, ws.Site, sitesync.PullOptions{})
	return res.Conflicted, syncErr(err)
}

func pushRemote(ctx context.Context, dir string, log io.Writer, del, force bool) error {
	c, err := syncClient(workspaceHost(dir), log)
	if err != nil {
		return err
	}
	_, err = c.Push(ctx, dir, sitesync.PushOptions{Delete: del, Force: force})
	return syncErr(err)
}

// PullSite 把 creght 站点（<project_id>/<site_id>）拉到 dir：新建在线项目、离线转在线时用。
func PullSite(ctx context.Context, host, site, dir string, log io.Writer) error {
	s, err := sitesync.ParseSite(site)
	if err != nil {
		return err
	}
	c, err := syncClient(host, log)
	if err != nil {
		return err
	}
	if _, err := c.Pull(ctx, dir, s, sitesync.PullOptions{}); err != nil {
		return i18n.Errorf("拉取 %s 失败：%w", "Pulling %s failed: %w", site, syncErr(err))
	}
	// 拉取时生成的 AGENTS.md 是给本机 agent 看的说明，不推到站点
	return addIgnore(dir, "AGENTS.md")
}

// addIgnore 往 .creghtignore 加一行（已经有就不加）。
func addIgnore(dir, line string) error {
	p := filepath.Join(dir, ".creghtignore")
	b, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	return os.WriteFile(p, append(b, line+"\n"...), 0o644)
}

func trimSlash(p string) string { return strings.TrimPrefix(p, "/") }
