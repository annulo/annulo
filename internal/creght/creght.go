// Package creght 是 Shuttle 访问 creght 平台的唯一出口。
//
// 登录态直接复用 creght CLI 的：`creght login` 之后 token 按 API host 存在
// <UserConfigDir>/creght/config.json。Shuttle 只读这个文件、从不写，避免和 CLI 抢写；
// token 过期（30 天）或被吊销时提示用户重新 `creght login`。
package creght

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// CodeSubscriptionRequired 是平台「付费模板，这个账号没有订阅」的业务码（读模板版本、读文件、复制时返回）。
const CodeSubscriptionRequired = 4021

// ErrSubscriptionRequired：付费模板，当前账号（或没连 creght）没有能解锁它的订阅。
var ErrSubscriptionRequired = i18n.New("这是付费模板：开通对应的套餐后才能用", "This is a paid template: subscribe to a plan that unlocks it first")

var ErrNotLoggedIn = i18n.New("还没连接 creght：在 设置 → 连接 里连接 creght", "creght isn't connected: connect it in Settings → Connections")

func normHost(h string) string {
	u, err := url.Parse(strings.TrimSpace(h))
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.ToLower(h), "/")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// ReadToken 是 apiHost 上 creght 连接的 access token（快过期时自动刷新，oauth.go）。没连过返回 ErrNotLoggedIn，
// 授权失效（要重新连接）返回 ErrReconnect。
func ReadToken(apiHost string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return oauthToken(ctx, normHost(apiHost))
}

type Client struct {
	APIHost string
	http    *http.Client
}

func NewClient(apiHost string) *Client {
	return &Client{APIHost: normHost(apiHost), http: &http.Client{Timeout: 30 * time.Second}}
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Get 调平台接口，结果解到 out。每次都重新读 token：用户在另一个终端重新 login 后不用重启。
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	tok, err := ReadToken(c.APIHost)
	if err != nil {
		return err
	}
	return c.send(ctx, method, path, q, in, out, tok)
}

// send 发请求；tok 为空就不带登录态（平台的公开接口）。
func (c *Client) send(ctx context.Context, method, path string, q url.Values, in, out any, tok string) error {
	u := c.APIHost + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, body)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrNotLoggedIn
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		if json.Unmarshal(respBody, &e) == nil && e.Code == CodeSubscriptionRequired {
			return ErrSubscriptionRequired
		}
		if json.Unmarshal(respBody, &e) == nil && e.Message != "" {
			return fmt.Errorf("creght %s: %s", path, e.Message)
		}
		return fmt.Errorf("creght %s: HTTP %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

type Site struct {
	ProjectID   string `json:"project_id"`
	SiteID      string `json:"site_id"`
	ProjectName string `json:"project_name"`
	SiteName    string `json:"site_name"`
}

func (s Site) Key() string { return s.ProjectID + "/" + s.SiteID }

// Sites 列出当前账号拥有或参与的所有项目下的站点。
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var ret struct {
		List []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			SiteList []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"site_list"`
		} `json:"list"`
	}
	if err := c.Get(ctx, "/api/u/project_list", url.Values{"limit": {"500"}}, &ret); err != nil {
		return nil, err
	}
	var out []Site
	for _, p := range ret.List {
		for _, s := range p.SiteList {
			out = append(out, Site{ProjectID: p.ID, SiteID: s.ID, ProjectName: p.Name, SiteName: s.Name})
		}
	}
	return out, nil
}

// VisitOverview 原样透传平台的 visit_stat/overview（汇总 + 按天趋势 + 各维度 Top N）。
// 这个接口按项目权限校验，只能读自己有权限的站点。
func (c *Client) VisitOverview(ctx context.Context, projectID, siteID string, days int) (json.RawMessage, error) {
	// 平台按 UTC 日期分组统计，起点取 UTC 零点，否则会多出半天、变成 days+1 个桶
	end := time.Now().UTC()
	start := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	q := url.Values{
		"start_at": {fmt.Sprint(start.Unix())},
		"end_at":   {fmt.Sprint(end.Unix())},
		"limit":    {"20"},
	}
	var raw json.RawMessage
	err := c.Get(ctx, fmt.Sprintf("/api/u/project/%s/site/%s/visit_stat/overview", url.PathEscape(projectID), url.PathEscape(siteID)), q, &raw)
	return raw, err
}

// PreviewURL 和 creght CLI 的算法一致：预览环境是 API host 的子域名。
func PreviewURL(apiHost, siteID string) string {
	u, err := url.Parse(apiHost)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + siteID + ".preview." + u.Host
}

// Workspace 是 `creght pull` 出来的本地工作区，或者离线项目（只在本机，没有 creght 项目）。
type Workspace struct {
	Dir       string
	ProjectID string
	SiteID    string
	APIHost   string
	// Offline：离线项目。数据在本机（SQLite），没有 creght 站点；ProjectID 是本机生成的（local-…），SiteID 是空的。
	// APIHost 是打开它时设置里选的 creght 集群：连上 creght 以后，creght 服务商、creght MCP、渠道站点都用它。
	Offline bool
}

// OfflineFile 是离线项目的标记：{"project_id": "local-…", "name": …, "template": "<模板 project_id>/<site_id>", "created_at": …}
// 新建时写在 .shuttle/ 下；新名字 .annulo/project.json 也认（brand.MarkerDirs）。
const OfflineFile = ".shuttle/project.json"

// OfflineProject 是离线项目的 .shuttle/project.json。
type OfflineProject struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Template  string `json:"template,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ReadOffline 读离线项目的标记；不是离线项目返回错误。
func ReadOffline(dir string) (*OfflineProject, error) {
	var b []byte
	var err error
	for _, d := range brand.MarkerDirs {
		if b, err = os.ReadFile(filepath.Join(dir, d, "project.json")); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	var p OfflineProject
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.ProjectID == "" {
		return nil, i18n.Errorf("%s 里没有 project_id", "%s has no project_id", OfflineFile)
	}
	return &p, nil
}

// WriteOffline 写离线项目的标记。
func WriteOffline(dir string, p OfflineProject) error {
	if err := os.MkdirAll(filepath.Join(dir, brand.MarkerDir), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(filepath.Join(dir, OfflineFile), append(b, '\n'), 0o644)
}

// IsOfflineID：本机生成的离线项目 id。
func IsOfflineID(id string) bool { return strings.HasPrefix(id, "local-") }

// OpenWorkspace 打开本机的项目目录：有 .creght/state.json 的是 creght 项目，有 .shuttle/project.json 的是离线项目。
// 离线项目的 APIHost 填 DefaultHost，调用方按设置里选的集群改（OpenWorkspaceOn）。
func OpenWorkspace(dir string) (*Workspace, error) {
	return OpenWorkspaceOn(dir, DefaultHost)
}

// OpenWorkspaceOn 和 OpenWorkspace 一样，离线项目的 APIHost 用 host。
func OpenWorkspaceOn(dir, host string) (*Workspace, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".creght", "state.json"))
	if err != nil {
		if p, oerr := ReadOffline(dir); oerr == nil {
			if host == "" {
				host = DefaultHost
			}
			return &Workspace{Dir: dir, ProjectID: p.ProjectID, APIHost: normHost(host), Offline: true}, nil
		}
		return nil, err
	}
	var st struct {
		SiteID  string `json:"site_id"`
		APIHost string `json:"api_host"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	pid, sid, ok := strings.Cut(st.SiteID, "/")
	if !ok {
		return nil, i18n.Errorf("state.json 里的 site_id %q 格式不对", "site_id %q in state.json is malformed", st.SiteID)
	}
	if st.APIHost == "" {
		st.APIHost = DefaultHost
	}
	return &Workspace{Dir: dir, ProjectID: pid, SiteID: sid, APIHost: normHost(st.APIHost)}, nil
}

// LiveURL 是站点对外的正式地址：优先自定义域名，其次系统域名；从没发布过返回空。
func (c *Client) LiveURL(ctx context.Context, projectID, siteID string) (string, error) {
	var st struct {
		CurrentVersionID int    `json:"current_version_id"`
		SystemDomain     string `json:"system_domain"`
		Domains          []struct {
			Domain string `json:"domain"`
			System bool   `json:"system"`
		} `json:"domains"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/api/u/project/%s/site/%s/publish/state", url.PathEscape(projectID), url.PathEscape(siteID)), nil, &st); err != nil {
		return "", err
	}
	if st.CurrentVersionID == 0 {
		return "", nil
	}
	for _, d := range st.Domains {
		if !d.System && d.Domain != "" {
			return "https://" + d.Domain, nil
		}
	}
	if st.SystemDomain != "" {
		return "https://" + st.SystemDomain, nil
	}
	return "", nil
}

// CopyProject 复制一个项目（对方开了「公开复制」，或者是自己的项目），返回新项目 id 和它的第一个站点 id。
// 接口返回的是新项目的详情：{id, name, …, sites: [{id, project_id, …}]}。
func (c *Client) CopyProject(ctx context.Context, fromID, name string) (projectID, siteID string, err error) {
	var raw json.RawMessage
	if err := c.do(ctx, "POST", "/api/p/project", nil, map[string]any{"name": name, "from_id": fromID}, &raw); err != nil {
		return "", "", err
	}
	projectID, siteID = parseProjectDetail(raw)
	if projectID != "" && siteID == "" { // 详情里没带站点就再查一次
		var d json.RawMessage
		if err := c.Get(ctx, "/api/p/project/"+url.PathEscape(projectID), nil, &d); err == nil {
			_, siteID = parseProjectDetail(d)
		}
	}
	if projectID == "" || siteID == "" {
		return projectID, siteID, i18n.Errorf("复制项目后没拿到项目或站点 id，接口返回：%.300s", "Copied the project but got no project or site id; the API returned: %.300s", raw)
	}
	return projectID, siteID, nil
}

// DeleteProject 删除项目（连同它的站点、表、CMS），不能恢复。
func (c *Client) DeleteProject(ctx context.Context, projectID string) error {
	return c.do(ctx, "DELETE", "/api/u/project/"+url.PathEscape(projectID), nil, nil, nil)
}

// SetFromProject 改运营后台项目的来源模板（from_project_id）：换模板后记到平台上，换台电脑也按新模板认、跟着它升级。
// 目标要是开了公开复制的运营后台模板；设成和现在一样的也返回成功，可以放心重试。
func (c *Client) SetFromProject(ctx context.Context, projectID, fromProjectID string) error {
	return c.do(ctx, "PUT", "/api/u/project/"+url.PathEscape(projectID)+"/from_project", nil, map[string]any{"from_project_id": fromProjectID}, nil)
}

// RenameProject 改项目名。
func (c *Client) RenameProject(ctx context.Context, projectID, name string) error {
	return c.do(ctx, "PUT", "/api/p/project/"+url.PathEscape(projectID), nil, map[string]any{"name": name}, nil)
}

// SendAIFeedback 把一条 AI 回复的点赞 / 点踩发给平台（creght 的公共反馈接口，各产品共用；请求体见 internal/server/feedback.go）。
func (c *Client) SendAIFeedback(ctx context.Context, body any) error {
	return c.do(ctx, "POST", "/api/p/ai/feedback", nil, body, nil)
}

func parseProjectDetail(raw json.RawMessage) (projectID, siteID string) {
	var d struct {
		ID    json.RawMessage `json:"id"`
		Sites []struct {
			ID json.RawMessage `json:"id"`
		} `json:"sites"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return "", ""
	}
	projectID = strings.Trim(string(d.ID), `"`)
	if len(d.Sites) > 0 {
		siteID = strings.Trim(string(d.Sites[0].ID), `"`)
	}
	return projectID, siteID
}

// ProjectInfo 是项目列表里的一项（主路由组 /api/p/project_list，带 from_project_id）。
type ProjectInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	FromProjectID string `json:"from_project_id"`
	CreatedAt     string `json:"created_at"`
	SiteList      []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"site_list"`
}

// OpsProjects 是用户的运营后台项目（creght 上 kind=ops_console 的项目，从 Shuttle 模板复制出来的会自动带上）。
// 2026-09 起运营后台不在默认的项目列表里（会员中心、creght project list 都看不到），要按 kind 取。
func (c *Client) OpsProjects(ctx context.Context) ([]ProjectInfo, error) {
	var ret struct {
		List []ProjectInfo `json:"list"`
	}
	err := c.Get(ctx, "/api/u/project_list", url.Values{"kind": {"ops_console"}, "limit": {"500"}}, &ret)
	return ret.List, err
}
