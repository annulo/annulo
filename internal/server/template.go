package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/version"
	"github.com/annulo/annulo/internal/wsgit"
)

// 模板升级（见 internal/wsgit/template.go）：项目是从模板复制的，模板发了新版本就能合并进来。

var latestCache struct {
	sync.Mutex
	m map[string]latestEntry // 模板项目 id → 最新版本
}

type latestEntry struct {
	no int
	at time.Time
}

// templateLatest 是模板最新的版本号（缓存 1 分钟：项目列表在初始化时每 1.5 秒刷新一次）；读不到是 0。
func (s *Server) templateLatest(ctx context.Context, tpl templateDef) int {
	latestCache.Lock()
	defer latestCache.Unlock()
	if latestCache.m == nil {
		latestCache.m = map[string]latestEntry{}
	}
	if e, ok := latestCache.m[tpl.Key]; ok && time.Since(e.at) < time.Minute {
		return e.no
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	e := latestCache.m[tpl.Key]
	if vs, err := wsgit.Versions(ctx, tpl.wsgit(s.cfg.Dir)); err == nil && len(vs) > 0 {
		e.no = vs[0].No
	}
	e.at = time.Now()
	latestCache.m[tpl.Key] = e
	return e.no
}

// projectTemplate：当前项目现在跟着的模板（最早那个运营后台也算，模板当初就是从它来的；换过模板的按本机记的）；
// 别处来的项目返回 nil。
func (s *Server) projectTemplate(ctx context.Context) (*wsgit.Template, *templateDef, error) {
	return s.templateOfProject(ctx, s.ws.ProjectID, s.ws.Dir)
}

func (s *Server) templateOfProject(ctx context.Context, projectID, dir string) (*wsgit.Template, *templateDef, error) {
	if creght.IsOfflineID(projectID) {
		// 离线项目：建的时候在 template 分支记了模板站点
		if t := templateBySite(wsgit.TemplateSite(dir)); t != nil {
			wt := t.wsgit(s.cfg.Dir)
			return &wt, t, nil
		}
		return nil, nil, nil
	}
	ps, err := creght.NewClient(s.loginHost()).OpsProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	templatesOn(s.loginHost())
	for _, p := range ps {
		if p.ID != projectID {
			continue
		}
		if t := templateFor(p, dir); t != nil {
			wt := t.wsgit(s.cfg.Dir)
			return &wt, t, nil
		}
	}
	return nil, nil, nil
}

// pushAfterMerge 把合并好的项目推到预览；远端有别处的改动而且撞了冲突时，冲突文件放进 push_conflicts，界面交给助手解决。
func pushAfterMerge(ctx context.Context, dir string, out map[string]any) {
	if _, err := creght.ReadOffline(dir); err == nil {
		return // 离线项目没有 creght 可推
	}
	var log bytes.Buffer
	if err := wsgit.Push(ctx, dir, "", true, &log); err != nil {
		out["push_error"] = err.Error()
		var pc *wsgit.PushConflict
		if errors.As(err, &pc) {
			out["push_conflicts"] = pc.Files
		}
	}
	out["push_log"] = log.String()
}

// apiTemplateGet：{from_template, name, base, latest, updates:[新于 base 的版本], merging, conflicts}
// base 为 0 表示还没记过（第一次升级时自动认出是从哪个版本复制的）。
func (s *Server) apiTemplateGet(w http.ResponseWriter, r *http.Request) {
	// api：这台 Shuttle 的能力版本；project_requires：当前项目的 shuttle.json 要的；requires：要升级到的那一版要的
	res := map[string]any{"from_template": false, "name": "", "api": version.API, "project_requires": wsgit.ProjectMinAPI(s.ws.Dir)}
	t, def, err := s.projectTemplate(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if def != nil {
		res["name"] = def.Name()
		res["key"] = def.Key
	}
	// 能换成的模板（设置里「换模板」用）
	opts := []map[string]string{}
	for _, t := range templatesOn(s.ws.APIHost) {
		opts = append(opts, map[string]string{"key": t.Key, "name": t.Name(), "description": t.Desc()})
	}
	res["templates"] = opts
	if t == nil || wsgit.Available() != nil {
		writeJSON(w, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	vs, err := wsgit.Versions(ctx, *t)
	if errors.Is(err, creght.ErrSubscriptionRequired) {
		// 付费模板、这个账号没订阅（或订阅过期了）：项目照常用，只是拿不到新版本；界面给开通入口
		res["subscription_required"] = true
		if def != nil {
			res["template"] = def.view(true)
		}
		err, vs = nil, nil
	}
	if err != nil {
		if !s.ws.Offline {
			fail(w, http.StatusBadGateway, err)
			return
		}
		// 离线项目没连 creght：查不到模板的新版本，照样显示基于哪一版、改过哪些文件
		res["versions_error"] = i18n.Errorf("查不到模板的新版本：%v", "Couldn't check for template updates: %v", err).Error()
		vs = nil
	}
	base := wsgit.Base(s.ws.Dir)
	updates := []wsgit.Version{}
	for _, v := range vs {
		if v.No > base {
			updates = append(updates, v)
		}
	}
	merging, conflicts := wsgit.Conflicts(s.ws.Dir)
	res["push_conflicts"] = wsgit.MarkerFiles(s.ws.Dir) // 推到预览时撞了远端改动、还没解决的
	res["from_template"] = true
	res["base"] = base
	if base > 0 {
		res["base_label"] = wsgit.VersionLabel(t.Site, base) // git 模板显示 tag（v1.2.0），creght 模板就是版本号
	}
	res["updates"] = updates
	res["merging"] = merging
	res["conflicts"] = conflicts
	if mod, err := wsgit.Modified(s.ws.Dir); err == nil {
		res["modified"] = mod // 和模板不一样的文件（设置里「恢复模板文件」用）
	}
	if len(vs) > 0 {
		res["latest"] = vs[0].No
		res["latest_label"] = wsgit.VersionLabel(t.Site, vs[0].No)
		if len(updates) > 0 {
			if need, err := wsgit.VersionMinAPI(ctx, *t, vs[0].No); err == nil {
				res["requires"] = need
			}
		}
	}
	writeJSON(w, res)
}

// apiTemplateRestore：{"files":[…]}。把这些文件换回模板：先换回项目基于的那一版（单独一个提交，能撤销），
// 有新版本就接着升级到最新（这些文件直接取新版，别的照常合并）；合并干净就推到预览。
// 返回 {commit, files, result?（升级的结果）, upgrade_error?, push_error?, push_conflicts?}。
func (s *Server) apiTemplateRestore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Files []string `json:"files"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	json.Unmarshal(b, &in)
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再恢复", "The assistant is working; wait until it finishes before restoring"))
		return
	}
	t, _, err := s.projectTemplate(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if t == nil {
		fail(w, http.StatusBadRequest, i18n.New("这个项目不是从模板复制的，没有模板可以恢复", "This project wasn't copied from a template, so there's nothing to restore from"))
		return
	}
	sha, err := wsgit.Restore(s.ws.Dir, in.Files)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	out := map[string]any{"commit": sha, "files": in.Files}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := wsgit.Upgrade(ctx, s.ws.Dir, *t, 0)
	if err != nil {
		out["upgrade_error"] = err.Error() // 恢复已经做了，只是没升到最新（比如 Shuttle 太旧）
	} else {
		out["result"] = res
	}
	if res == nil || res.Status != "conflict" {
		pushAfterMerge(ctx, s.ws.Dir, out)
	}
	writeJSON(w, out)
}

// apiTemplateRestoreUndo：{"commit":恢复时返回的提交}。那次恢复的文件改回恢复前，推到预览。
func (s *Server) apiTemplateRestoreUndo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Commit string `json:"commit"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再撤销", "The assistant is working; wait until it finishes before undoing"))
		return
	}
	files, err := wsgit.UndoRestore(s.ws.Dir, in.Commit)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	out := map[string]any{"files": files}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pushAfterMerge(ctx, s.ws.Dir, out)
	writeJSON(w, out)
}

// apiTemplateUpgrade：{"to":版本号，0 是最新}。合并干净就推到预览；有冲突返回冲突文件，界面交给助手解决。
func (s *Server) apiTemplateUpgrade(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To int `json:"to"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再升级模板", "The assistant is working; wait until it finishes before upgrading the template"))
		return
	}
	t, _, err := s.projectTemplate(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if t == nil {
		fail(w, http.StatusBadRequest, i18n.New("这个项目不是从模板复制的，没有模板可以升级", "This project wasn't copied from a template, so there's no template to upgrade"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := wsgit.Upgrade(ctx, s.ws.Dir, *t, in.To)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	out := map[string]any{"result": res}
	if res.Status == "merged" {
		pushAfterMerge(ctx, s.ws.Dir, out)
	}
	writeJSON(w, out)
}

// apiTemplateSwitch：把项目换成另一个模板 {"template": "trade", "project_id"?: 不传是当前项目}。
// 本机三方合并（wsgit.Switch），合并干净就推到预览；有冲突返回冲突文件，打开这个项目后交给助手解决。
func (s *Server) apiTemplateSwitch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Template  string `json:"template"`
		ProjectID string `json:"project_id"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	json.Unmarshal(b, &in)
	to := templateByKey(s.ws.APIHost, in.Template)
	if to == nil {
		fail(w, http.StatusBadRequest, i18n.Errorf("没有这个模板：%q", "No such template: %q", in.Template))
		return
	}
	id, dir := s.ws.ProjectID, s.ws.Dir
	current := in.ProjectID == "" || in.ProjectID == s.ws.ProjectID
	if !current {
		id, dir = in.ProjectID, s.cfg.DirFor(in.ProjectID)
		if _, err := os.Stat(filepath.Join(dir, ".creght", "state.json")); err != nil {
			fail(w, http.StatusBadRequest, i18n.New("这个项目还没拉到这台电脑上：先打开它一次", "This project isn't on this computer yet: open it once first"))
			return
		}
	}
	if current && s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再换模板", "The assistant is working; wait until it finishes before switching templates"))
		return
	}
	from, _, err := s.templateOfProject(r.Context(), id, dir)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if from == nil {
		fail(w, http.StatusBadRequest, i18n.New("这个项目不是从模板复制的，换不了模板", "This project wasn't copied from a template, so its template can't be switched"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := wsgit.Switch(ctx, dir, *from, to.wsgit(s.cfg.Dir), 0)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	out := map[string]any{"result": res, "template": to.Key}
	// 本机已经按新模板记了（有冲突也是新模板的合并在进行中）：平台上的来源模板一起改，换台电脑也认得对
	if err := creght.NewClient(s.loginHost()).SetFromProject(ctx, id, to.ProjectID); err != nil {
		out["record_error"] = err.Error()
	}
	if res.Status == "merged" {
		pushAfterMerge(ctx, dir, out)
	}
	writeJSON(w, out)
}
