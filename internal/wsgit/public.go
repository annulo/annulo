package wsgit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cr "github.com/annulo/annulo/internal/creght"
)

// creght 模板的版本列表和某一版的文件走平台接口：免费模板不登录也能读；连着 creght 时带 token，付费模板按账号判断（没订阅是 ErrSubscriptionRequired）。

func splitSite(site string) (string, string) {
	pid, sid, _ := strings.Cut(site, "/")
	return pid, sid
}

// templateHost 是 creght 模板所在的集群：没给就是默认集群（测试里换成假的平台）。
var templateHost = func(t Template) string {
	if t.Host != "" {
		return t.Host
	}
	return cr.DefaultHost
}

func versionsPublic(ctx context.Context, t Template) ([]Version, error) {
	pid, sid := splitSite(t.Site)
	list, err := cr.NewClient(templateHost(t)).SiteVersions(ctx, pid, sid)
	if err != nil {
		return nil, err
	}
	vs := make([]Version, 0, len(list))
	for _, v := range list {
		vs = append(vs, Version{No: v.VersionNo, Note: v.Note, CreatedAt: v.CreatedAt.Format("2006-01-02T15:04:05Z07:00")})
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].No > vs[j].No })
	return vs, nil
}

// pullVersionPublic 把模板第 n 版的文件写进 t.Cache（目录里只留这一版的文件），记下版本号（cachedVersion 读它）。
func pullVersionPublic(ctx context.Context, t Template, n int) error {
	pid, sid := splitSite(t.Site)
	files, err := cr.NewClient(templateHost(t)).SiteFilesAt(ctx, pid, sid, n)
	if err != nil {
		return err
	}
	return WriteSnapshot(t.Cache, t.Site, n, files)
}

// WriteSnapshot 让 dir 正好是这些文件（.creght 里记着是哪个站点的第几版，和 creght pull --version_no 的格式一样）。
func WriteSnapshot(dir, site string, n int, files map[string]string) error {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != ".creght" {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	st, _ := json.Marshal(map[string]any{"site_id": site, "snapshot": map[string]any{"version_no": n}})
	os.MkdirAll(filepath.Join(dir, ".creght"), 0o755)
	return os.WriteFile(filepath.Join(dir, ".creght", "state.json"), st, 0o644)
}

// FilesAt 是模板第 n 版的全部文件（git 模板从仓库的 tag 取，creght 模板走平台的公开只读接口）。新建离线项目用它。
func FilesAt(ctx context.Context, t Template, n int) (map[string]string, error) {
	if IsGitSite(t.Site) {
		return filesGit(ctx, t, n)
	}
	pid, sid := splitSite(t.Site)
	return cr.NewClient(templateHost(t)).SiteFilesAt(ctx, pid, sid, n)
}
