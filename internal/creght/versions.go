package creght

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 站点版本的只读接口（publish/state、file_list?version=），和 creght CLI 的 GetSitePublishState、GetFileListAtVersion 一样。
// 开了公开复制的项目（Shuttle 模板）非成员也能读；登录了就带上 token，没登录不带（离线模式，平台对公开复制的项目开放免登录只读）。

type SiteVersion struct {
	ID        int64     `json:"id"`
	VersionNo int       `json:"version_no"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type SiteFile struct {
	Path     string `json:"path"`
	Body     string `json:"body"`
	IsDir    bool   `json:"is_dir"`
	Readonly bool   `json:"readonly"`
}

// token 是这个集群登录的 token，没登录是空（发请求时就不带）。
func (c *Client) token() string {
	t, _ := ReadToken(c.APIHost)
	return t
}

// SiteVersions 是站点发过的版本。
func (c *Client) SiteVersions(ctx context.Context, projectID, siteID string) ([]SiteVersion, error) {
	var ret struct {
		Versions []SiteVersion `json:"versions"`
	}
	err := c.send(ctx, http.MethodGet, fmt.Sprintf("/api/u/project/%s/site/%s/publish/state", url.PathEscape(projectID), url.PathEscape(siteID)), nil, nil, &ret, c.token())
	return ret.Versions, err
}

// SiteFilesAt 是站点第 n 版的全部文件（路径去掉开头的 /，目录和只读的系统文件不算）。
func (c *Client) SiteFilesAt(ctx context.Context, projectID, siteID string, n int) (map[string]string, error) {
	var ret struct {
		List []SiteFile `json:"list"`
	}
	err := c.send(ctx, http.MethodGet, fmt.Sprintf("/api/u/project/%s/site/%s/file_list", url.PathEscape(projectID), url.PathEscape(siteID)),
		url.Values{"version": {fmt.Sprint(n)}}, nil, &ret, c.token())
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, f := range ret.List {
		if f.IsDir || f.Readonly {
			continue
		}
		files[strings.TrimLeft(f.Path, "/")] = f.Body
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("version %d of %s/%s has no files", n, projectID, siteID)
	}
	return files, nil
}
