package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/version"
	"github.com/annulo/annulo/internal/wsgit"
)

// 初始化运营后台：用户选一个模板，把模板项目复制一份到自己的 creght 账号，拉到本地，然后挂上。
//
// 模板项目开了「公开复制」（can_public_copy），不是 creght 官网模板，不会出现在模板中心。
// 一个模板是一个行业方案（现在只有外贸），各自发版本，项目跟着自己从哪个模板复制来的那个升级。
// creght 的集群各是各的（creght.Clusters），模板项目也各在各的集群里。哪些项目是 Shuttle 模板在平台上登记
// （console「模板管理 → Shuttle 模板」），Shuttle 从用户选的集群读（creght.OpsTemplates）；读不到时用 builtinTemplates。
type templateDef struct {
	Key       string
	Host      string // 模板项目所在的集群
	ProjectID string
	SiteID    string
	name      [2]string // 中文、英文
	desc      [2]string
	preview   string // 平台给的预览地址，空就按站点 id 拼
	// Origins：模板当初是从这些项目复制出来的，它们也算这个模板的项目（跟着它升级）
	Origins []string
	// Git：git 模板的站点（git:<仓库>#<子目录>，wsgit.GitSite），这时 Host、ProjectID、SiteID 都是空的
	Git string
	// 付费模板（平台 ops_tpl_list）：entitled 是当前连着的账号能不能用，packages 是开通哪个套餐能解锁
	paid, entitled bool
	packages       []creght.TplPackage
}

// view 是给界面的模板：付费模板带价格和有没有权限。
func (t templateDef) view(connected bool) map[string]any {
	v := map[string]any{"key": t.Key, "id": t.ProjectID, "name": t.Name(), "description": t.Desc(), "preview_url": t.PreviewURL(),
		"source": t.sourceLabel()}
	if t.Git != "" && isUserSource(t.Git) {
		v["removable"] = true // 自己加的 git 模板
	}
	if t.paid {
		v["paid"], v["entitled"] = true, t.entitled && connected
		pk := []map[string]any{}
		for _, p := range t.packages {
			pk = append(pk, map[string]any{"id": p.ID, "name": i18n.T(locales(p.NameLocales)[0], locales(p.NameLocales)[1]),
				"price_by_month": p.PriceByMonth, "price_by_year": p.PriceByYear, "currency": p.PriceCurrency})
		}
		v["packages"] = pk
		pid := 0
		if len(t.packages) > 0 {
			pid = t.packages[0].ID
		}
		v["subscribe_url"] = creght.SubscribeURL(t.Host, pid)
	}
	return v
}

// sourceLabel 是界面上显示的模板来源：creght 模板是集群（creght.cn），git 模板是仓库（github.com/annulo/templates，子目录不写）。
func (t templateDef) sourceLabel() string {
	if t.Git == "" {
		return strings.TrimPrefix(strings.TrimPrefix(creght.NormHost(t.Host), "https://"), "http://")
	}
	repo, _ := wsgit.ParseGitSite(t.Git)
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		return u.Host + strings.TrimSuffix(u.Path, ".git")
	}
	if i := strings.Index(repo, "@"); i >= 0 && strings.Contains(repo, ":") { // git@github.com:a/b.git
		return strings.Replace(strings.TrimSuffix(repo[i+1:], ".git"), ":", "/", 1)
	}
	return filepath.Base(strings.TrimSuffix(repo, ".git")) // 本机路径只给目录名
}

// resetTemplateCache：连上、断开 creght 后重新读模板（有没有权限按账号算）。
func resetTemplateCache() {
	tplCache.Lock()
	tplCache.m = nil
	tplCache.Unlock()
}

// Site 是模板站点：creght 模板是 <project_id>/<site_id>，git 模板是 git:<仓库>#<子目录>。项目的 template 分支记的就是它。
func (t templateDef) Site() string {
	if t.Git != "" {
		return t.Git
	}
	return t.ProjectID + "/" + t.SiteID
}

func (t templateDef) Name() string { return i18n.T(t.name[0], t.name[1]) }
func (t templateDef) Desc() string { return i18n.T(t.desc[0], t.desc[1]) }

func (t templateDef) PreviewURL() string {
	if t.Git != "" {
		return "" // git 模板没有在线预览
	}
	if t.preview != "" {
		return strings.TrimRight(t.preview, "/") + "/"
	}
	return creght.PreviewURL(t.Host, t.SiteID) + "/"
}

// builtinTemplates 是平台接口读不到时用的（按推荐顺序，第一个是默认），也管平台上没有的信息：
// 老模板的 key（项目和界面里存的是它）和 Origins。平台上的同一个项目沿用这里的 key 和 Origins。
// 2026-09-29 起只维护外贸一个模板（模板仓库 templates/trade/），通用运营后台不再列在这里。
var builtinTemplates = []templateDef{
	{
		Key: "trade", Host: "https://creght.cn", ProjectID: "p9ok3myl0ne6", SiteID: "p9ok3mzxgpzm",
		name: [2]string{"外贸助手", "Export assistant"},
		desc: [2]string{"外贸独立站：询盘、谷歌排名和 AI 搜索推荐、网站检查、获客内容，总览里一步一步带你做。",
			"For export businesses: inquiries, Google rankings and AI search visibility, site checks and content, with a step-by-step checklist."},
		// 外贸模板是从通用运营后台复制的，通用运营后台当初又是从这个项目复制的
		Origins: []string{"p9kmhz3stqqa"},
	},
}

// retiredTemplates：不再维护、平台上也下架了的模板。新建项目选不到它们，但从它们复制的老项目还要认得
// （算运营后台、能在 设置 → 项目 里换成现在的模板：换模板的三方合并要知道原来的模板）。
var retiredTemplates = []templateDef{
	{
		Key: "ops", Host: "https://creght.cn", ProjectID: "p9k3n5y5hbbm", SiteID: "p9k3n5zpgh26",
		name: [2]string{"通用运营后台", "General back office"},
		desc: [2]string{"独立站 + 社媒运营（2026-09-29 起不再维护，请换成外贸助手）。",
			"Site + social operations (no longer maintained since 2026-09-29; switch to the Export assistant)."},
	},
}

// fetchOpsTemplates 读集群里登记的 Shuttle 模板（测试里换掉，不连平台）。
var fetchOpsTemplates = func(ctx context.Context, host string) ([]creght.OpsTemplate, error) {
	return creght.NewClient(host).OpsTemplates(ctx)
}

// tplCache：每个集群平台上登记的模板。读成功的缓存 5 分钟，读失败 1 分钟内不再试（用上次读到的或内置的）。
var tplCache struct {
	sync.Mutex
	m map[string]tplEntry
}

type tplEntry struct {
	list []*templateDef
	ok   bool // 读成功过：list 是平台上的（可能是空的）
	at   time.Time
}

// templatesOn：新建项目能选的模板，按推荐顺序：git 模板（内置的 + 设置里加的），连着这个 creght 集群时再加上它的模板（排前面，
// 现在的用户默认还是外贸助手）。没连 creght 时 creght 的模板不出现（docs/annulo-plan.md 第 5 步）。
func templatesOn(host string) []*templateDef {
	git := gitTemplates()
	if _, err := creght.ReadToken(host); err != nil {
		return git
	}
	return append(creghtTemplatesOn(host), git...)
}

// creghtTemplatesOn：这个 creght 集群里的模板，按推荐顺序。
func creghtTemplatesOn(host string) []*templateDef {
	h := creght.NormHost(host)
	tplCache.Lock()
	e, has := tplCache.m[h]
	tplCache.Unlock()
	ttl := time.Minute
	if e.ok {
		ttl = 5 * time.Minute
	}
	if !has || time.Since(e.at) > ttl {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		list, err := fetchOpsTemplates(ctx, h)
		cancel()
		if err == nil {
			e = tplEntry{list: fromPlatform(h, list), ok: true}
		} else {
			log.Printf("读 %s 的 Annulo 模板失败，用%s：%v", h, map[bool]string{true: "上次读到的", false: "内置的"}[e.ok], err)
		}
		e.at = time.Now()
		tplCache.Lock()
		if tplCache.m == nil {
			tplCache.m = map[string]tplEntry{}
		}
		tplCache.m[h] = e
		tplCache.Unlock()
	}
	if e.ok {
		return e.list
	}
	var out []*templateDef
	for i := range builtinTemplates {
		if builtinTemplates[i].Host == h {
			out = append(out, &builtinTemplates[i])
		}
	}
	return out
}

func fromPlatform(host string, list []creght.OpsTemplate) []*templateDef {
	out := make([]*templateDef, 0, len(list))
	for _, p := range list {
		t := &templateDef{Key: p.ProjectID, Host: host, ProjectID: p.ProjectID, SiteID: p.SiteID, preview: p.PreviewURL,
			name: locales(p.NameLocales), desc: locales(p.DescLocales), paid: p.Paid, entitled: p.Entitled || !p.Paid, packages: p.Packages}
		for _, b := range append(builtinTemplates[:len(builtinTemplates):len(builtinTemplates)], retiredTemplates...) {
			if b.ProjectID == p.ProjectID {
				t.Key, t.Origins = b.Key, b.Origins
			}
		}
		out = append(out, t)
	}
	return out
}

// locales 把平台的 {zh-CN, en} 转成 [中文, 英文]，缺一个用另一个。
func locales(m map[string]string) [2]string {
	zh, en := m["zh-CN"], m["en"]
	if zh == "" {
		zh = en
	}
	if en == "" {
		en = zh
	}
	return [2]string{zh, en}
}

// allTemplates：认得的所有模板（各集群读到过的 + 内置的 + 退役的），用来认一个项目是从哪个模板来的。
func allTemplates() []*templateDef {
	var out []*templateDef
	tplCache.Lock()
	for _, e := range tplCache.m {
		out = append(out, e.list...)
	}
	tplCache.Unlock()
	for i := range builtinTemplates {
		out = append(out, &builtinTemplates[i])
	}
	for i := range retiredTemplates {
		out = append(out, &retiredTemplates[i])
	}
	return append(out, gitTemplates()...)
}

// templateOf：项目是从哪个模板复制来的；不是运营后台返回 nil。先 templatesOn 读一下项目所在集群的模板。
func templateOf(p creght.ProjectInfo) *templateDef {
	all := allTemplates()
	// 模板项目本身也是运营后台（外贸模板还是从基座复制的），但不是用户的项目
	for _, t := range all {
		if t.ProjectID == p.ID {
			return nil
		}
	}
	for _, t := range all {
		if p.FromProjectID == t.ProjectID || slices.Contains(t.Origins, p.ID) {
			return t
		}
	}
	return nil
}

// templateBySite：按模板站点（<project_id>/<site_id>）找；换过模板的项目在 git 里记着它（wsgit.TemplateSite）。
func templateBySite(site string) *templateDef {
	for _, t := range allTemplates() {
		if t.Site() == site {
			return t
		}
	}
	if wsgit.IsGitSite(site) {
		return gitTemplate(site) // 设置里删掉了这个 git 模板：项目照样能跟着它升级
	}
	return nil
}

// templateFor：项目现在跟着哪个模板。本机换过模板的以本机 git 里记的为准（creght 上的来源项目可能还没改过来），
// 否则按 creght 上它从哪个模板复制来的。
func templateFor(p creght.ProjectInfo, dir string) *templateDef {
	if dir != "" {
		if t := templateBySite(wsgit.TemplateSite(dir)); t != nil {
			return t
		}
	}
	return templateOf(p)
}

func (t templateDef) wsgit(dir string) wsgit.Template {
	cache := t.ProjectID
	if t.Git != "" {
		sum := sha1.Sum([]byte(t.Git))
		cache = "git-" + hex.EncodeToString(sum[:6])
	}
	return wsgit.Template{Site: t.Site(), Cache: filepath.Join(dir, "templates", cache), API: version.API, Host: t.Host}
}

func templateByKey(host, key string) *templateDef {
	for _, t := range templatesOn(host) {
		if t.Key == key {
			return t
		}
	}
	return nil
}

// isBackend：从模板复制出来的项目都是运营后台。
func isBackend(p creght.ProjectInfo) bool { return templateOf(p) != nil }

type backendInfo struct {
	ProjectID string `json:"project_id"`
	SiteID    string `json:"site_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Local     bool   `json:"local"`   // 本机已经拉过
	Current   bool   `json:"current"` // 正在用
	// TemplateBase 是项目基于模板哪个版本（本机 git 里记的，0 是还没记，第一次升级时自动识别）
	TemplateBase int `json:"template_base,omitempty"`
	// TemplateBaseLabel：git 模板显示的版本（v1.2.0），creght 模板不给，界面显示 TemplateBase
	TemplateBaseLabel string `json:"template_base_label,omitempty"`
	// 从哪个模板复制来的，和那个模板的最新版本
	Template       string `json:"template"`
	TemplateName   string `json:"template_name"`
	TemplateLatest int    `json:"template_latest,omitempty"`
	// Offline：离线项目（只在本机，数据在本机 SQLite）
	Offline bool `json:"offline,omitempty"`
}

func (s *Server) listBackends(ctx context.Context) ([]backendInfo, error) {
	ps, err := creght.NewClient(s.loginHost()).OpsProjects(ctx)
	if err != nil {
		return nil, err
	}
	templatesOn(s.loginHost()) // 认项目的模板之前先读这个集群登记的模板
	out := []backendInfo{}
	for _, p := range ps {
		t := templateOf(p)
		if t == nil || len(p.SiteList) == 0 {
			continue
		}
		_, statErr := os.Stat(filepath.Join(s.cfg.DirFor(p.ID), ".creght", "state.json"))
		if statErr == nil {
			t = templateFor(p, s.cfg.DirFor(p.ID))
		}
		b := backendInfo{
			ProjectID: p.ID, SiteID: p.SiteList[0].ID, Name: p.Name, CreatedAt: p.CreatedAt,
			Local: statErr == nil, Current: s.ready.Load() && s.ws.ProjectID == p.ID,
			Template: t.Key, TemplateName: t.Name(),
		}
		if b.Local {
			b.TemplateBase = wsgit.Base(s.cfg.DirFor(p.ID))
		}
		out = append(out, b)
	}
	return out, nil
}

type setupState struct {
	mu    sync.Mutex
	step  string // 空 / copying / pulling / done
	error string
}

func (s *Server) apiSetupGet(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		resetTemplateCache() // 「开通好了，刷新」：重新按账号读模板有没有权限
	}
	s.setup.mu.Lock()
	step, errMsg := s.setup.step, s.setup.error
	s.setup.mu.Unlock()
	host := s.loginHost()
	_, tokErr := creght.ReadToken(host)
	offline := s.offlineBackends()
	res := map[string]any{
		"ready":        s.ready.Load(),
		"step":         step,
		"error":        errMsg,
		"logged_in":    tokErr == nil,
		"offline_mode": s.cfg.Offline,
		"api_host":     host,
		"dir":          filepath.Join(s.cfg.Dir, "backends"),
		"backends":     offline,
	}
	// 模板：git 的不用连 creght；creght 的连上才出现（外贸助手现在免费，连上就能用），付费的还要有订阅
	tpls := []map[string]any{}
	for _, t := range templatesOn(host) {
		tpls = append(tpls, t.view(tokErr == nil))
	}
	res["templates"] = tpls
	if tokErr == nil {
		if list, err := s.listBackends(r.Context()); err == nil {
			for i := range list {
				if t := templateByKey(host, list[i].Template); t != nil && list[i].Local {
					list[i].TemplateLatest = s.templateLatest(r.Context(), *t)
				}
			}
			res["backends"] = append(list, offline...)
		} else {
			res["error"] = err.Error()
		}
	}
	writeJSON(w, res)
}

// apiSetupRun：{"action":"new","name":…} 从模板新建项目；{"action":"use","project_id":…} 切到已有的
// 项目（本机没有就拉下来）。项目 = 一个运营后台 creght 项目 + 本机副本 + 这个项目里的对话历史。一个项目就是一个业务。
func (s *Server) apiSetupRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action    string `json:"action"`
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Template  string `json:"template"` // new：用哪个模板（templates 的 key），不传用第一个
		Offline   bool   `json:"offline"`  // new：建离线项目（数据只在本机，不用登录）
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	if in.Action == "" {
		in.Action = "new"
	}
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再切换项目", "The assistant is working; wait until it finishes before switching projects"))
		return
	}
	s.setup.mu.Lock()
	if s.setup.step == "copying" || s.setup.step == "pulling" {
		s.setup.mu.Unlock()
		fail(w, http.StatusConflict, i18n.New("正在初始化", "Initializing"))
		return
	}
	s.setup.step, s.setup.error = "copying", ""
	s.setup.mu.Unlock()
	progress := func(step string) {
		s.setup.mu.Lock()
		s.setup.step = step
		s.setup.mu.Unlock()
	}

	var ws *creght.Workspace
	var err error
	switch in.Action {
	case "use":
		ws, err = s.useBackend(r.Context(), in.ProjectID, progress)
	case "new":
		if _, tokErr := creght.ReadToken(s.creghtHost()); in.Offline || tokErr != nil || wsgit.IsGitSite(in.Template) {
			ws, err = s.newOfflineBackend(r.Context(), in.Template, in.Name)
		} else {
			ws, err = s.newBackend(r.Context(), in.Template, in.Name, progress)
		}
	default:
		err = i18n.New("action 只能是 new 或 use", "action must be new or use")
	}
	if err == nil && in.Action == "new" {
		// 模板要的插件一起装上（docs/plugins.md）
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if len(s.installProjectPlugins(ctx, ws.Dir)) > 0 {
			out := map[string]any{}
			pushAfterMerge(ctx, ws.Dir, out)
			if e, ok := out["push_error"]; ok {
				log.Printf("装插件后推到预览失败：%v", e)
			}
		}
		cancel()
	}
	if err == nil {
		s.cfg.Backend = ws.ProjectID
		err = s.cfg.Save()
	}
	s.setup.mu.Lock()
	if err != nil {
		s.setup.step, s.setup.error = "", err.Error()
	} else {
		s.setup.step = "done"
	}
	s.setup.mu.Unlock()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.agent.SetWorkspace(ws.ProjectID, ws.Dir)
	s.Attach(ws)
	s.apiSetupGet(w, r)
}

// apiSetupRename 改项目的名字（就是 creght 项目名）。只能改自己账号下的项目。
func (s *Server) apiSetupRename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 40 {
		fail(w, http.StatusBadRequest, i18n.New("名字不能为空，最多 40 个字", "The name can't be empty and must be at most 40 characters"))
		return
	}
	if creght.IsOfflineID(in.ProjectID) {
		if err := s.renameOffline(in.ProjectID, in.Name); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		s.apiSetupGet(w, r)
		return
	}
	list, err := s.listBackends(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	found := false
	for _, b := range list {
		found = found || b.ProjectID == in.ProjectID
	}
	if !found {
		fail(w, http.StatusNotFound, i18n.Errorf("你的账号下没有这个项目：%s", "Your account has no such project: %s", in.ProjectID))
		return
	}
	if err := creght.NewClient(s.loginHost()).RenameProject(r.Context(), in.ProjectID, in.Name); err != nil {
		fail(w, http.StatusBadGateway, i18n.Errorf("改名失败：%w", "Rename failed: %w", err))
		return
	}
	s.apiSetupGet(w, r)
}

func (s *Server) useBackend(ctx context.Context, projectID string, progress func(string)) (*creght.Workspace, error) {
	if creght.IsOfflineID(projectID) {
		ws, err := creght.OpenWorkspaceOn(s.cfg.DirFor(projectID), s.creghtHost())
		if err != nil || !ws.Offline {
			return nil, i18n.Errorf("本机没有离线项目 %s", "No offline project %s on this computer", projectID)
		}
		return ws, nil
	}
	list, err := s.listBackends(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range list {
		if b.ProjectID != projectID {
			continue
		}
		dir := s.cfg.DirFor(b.ProjectID)
		if b.Local {
			return creght.OpenWorkspace(dir)
		}
		progress("pulling")
		return pullBackend(ctx, s.loginHost(), b.ProjectID, b.SiteID, dir)
	}
	return nil, i18n.Errorf("你的账号下没有这个项目：%s", "Your account has no such project: %s", projectID)
}

func (s *Server) newBackend(ctx context.Context, tplKey, name string, progress func(string)) (*creght.Workspace, error) {
	host := s.creghtHost()
	tpls := creghtTemplatesOn(host) // 在线项目只能从 creght 模板复制；git 模板新建的是离线项目（apiSetupRun）
	if len(tpls) == 0 {
		return nil, i18n.Errorf("%s 上还没有模板，先切到别的 creght 集群新建项目", "There are no templates on %s yet; switch to another creght cluster to create a project", host)
	}
	tpl := tpls[0]
	if tplKey != "" {
		if tpl = templateByKey(host, tplKey); tpl == nil || tpl.Git != "" {
			return nil, i18n.Errorf("没有这个模板：%s", "No such template: %s", tplKey)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = i18n.T("我的项目", "My project")
	}
	if len([]rune(name)) > 40 {
		return nil, i18n.New("项目名字最多 40 个字", "Project names are at most 40 characters")
	}
	if _, err := creght.ReadToken(host); err != nil {
		return nil, i18n.New("还没登录 creght", "Not signed in to creght yet")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	pid, sid, err := creght.NewClient(host).CopyProject(ctx, tpl.ProjectID, name)
	if err != nil {
		return nil, i18n.Errorf("复制模板失败：%w", "Failed to copy the template: %w", err)
	}
	progress("pulling")
	return pullBackend(ctx, host, pid, sid, s.cfg.DirFor(pid))
}

func pullBackend(ctx context.Context, host, pid, sid, dir string) (*creght.Workspace, error) {
	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(dir, ".creght", "state.json")); err == nil {
			return creght.OpenWorkspace(dir)
		}
		return nil, i18n.Errorf("%s 已经有别的文件了，先把它挪走", "%s already has other files; move them away first", dir)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := wsgit.PullSite(ctx, host, pid+"/"+sid, dir, nil); err != nil {
		return nil, i18n.Errorf("拉取运营后台代码失败：%w", "Failed to pull the back-office code: %w", err)
	}
	// 拉取时生成的 AGENTS.md 是本地说明，不推到站点
	os.WriteFile(filepath.Join(dir, ".creghtignore"), []byte("AGENTS.md\n"), 0o644)
	return creght.OpenWorkspace(dir)
}

// apiSetupDelete：{"project_id":…,"name":…} 删除一个项目：creght 上的项目直接删（不能恢复），
// 本机副本（含 git 历史）和对话历史挪到 ~/.shuttle/trash/，想找回本机的内容还在。
// name 要和项目名一致（界面上让用户输入一遍），正在用的项目不能删：先切到别的项目。
func (s *Server) apiSetupDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	if s.ready.Load() && s.ws.ProjectID == in.ProjectID {
		fail(w, http.StatusConflict, i18n.New("正在用的项目不能删，先切换到别的项目", "The project in use can't be deleted; switch to another project first"))
		return
	}
	if creght.IsOfflineID(in.ProjectID) {
		if err := s.deleteOffline(in.ProjectID, in.Name); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		s.apiSetupGet(w, r)
		return
	}
	list, err := s.listBackends(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	var target *backendInfo
	for i := range list {
		if list[i].ProjectID == in.ProjectID {
			target = &list[i]
		}
	}
	if target == nil {
		fail(w, http.StatusNotFound, i18n.Errorf("你的账号下没有这个项目：%s", "Your account has no such project: %s", in.ProjectID))
		return
	}
	if strings.TrimSpace(in.Name) != target.Name {
		fail(w, http.StatusBadRequest, i18n.New("项目名字不对，删除要输入完整的项目名", "The project name doesn't match; type the full project name to delete it"))
		return
	}
	if err := creght.NewClient(s.loginHost()).DeleteProject(r.Context(), in.ProjectID); err != nil {
		fail(w, http.StatusBadGateway, i18n.Errorf("删除失败：%w", "Delete failed: %w", err))
		return
	}
	trash := filepath.Join(s.cfg.Dir, "trash", in.ProjectID+"-"+time.Now().Format("20060102-150405"))
	for name, dir := range map[string]string{"backend": s.cfg.DirFor(in.ProjectID), "workspace": filepath.Join(s.cfg.Dir, "workspaces", in.ProjectID)} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		os.MkdirAll(trash, 0o700)
		if err := os.Rename(dir, filepath.Join(trash, name)); err != nil {
			log.Printf("把 %s 挪到回收站失败：%v", dir, err)
		}
	}
	s.apiSetupGet(w, r)
}
