package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/version"
	"github.com/annulo/annulo/internal/wsgit"
)

// 插件（docs/plugins.md）：项目 = 模板 + 插件。插件装在项目的 plugins/<id>/，版本和三方合并见 internal/wsgit/plugin.go。
// 项目要哪些插件、从哪来：模板写在 annulo.json 的 "plugins": {"<id>": "<仓库>#<子目录>"}（新建项目时自动装上，模板升级带来新的在设置里提示），
// 用户在这里装、卸的记在 user/annulo.json（见 recordPlugin）。
//
//	GET  plugins          { installed: [...], missing: [...], available: [...] }
//	POST plugins/install  { source }    装一个插件（<仓库>#<子目录>）
//	POST plugins/upgrade  { id, to? }   升级（to 是版本号，0 是最新）
//	POST plugins/remove   { id }        卸载：删 plugins/<id>/，表里的数据和 user/plugins/<id>/ 不动

// 能装的插件：开源插件仓库 github.com/annulo/plugins 里的（实时读仓库最新版本里有哪些子目录，见 catalog.go）。

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.apiPluginsGet(w, r)
	case rest == "install" && r.Method == http.MethodPost:
		s.apiPluginInstall(w, r)
	case rest == "upgrade" && r.Method == http.MethodPost:
		s.apiPluginUpgrade(w, r)
	case rest == "remove" && r.Method == http.MethodPost:
		s.apiPluginRemove(w, r)
	default:
		fail(w, http.StatusNotFound, i18n.New("没有这个接口", "No such endpoint"))
	}
}

// installProjectPlugins 装上项目要（模板写了、用户装了）、还没装的插件：新建项目、模板合并进来之后调。返回装了哪些。
// 装不上的（没网、仓库没权限）跳过，设置 → 项目 → 插件 里会列成「还没装」让用户再装。不推送：调用方合并完自己推。
func (s *Server) installProjectPlugins(ctx context.Context, dir string) []string {
	return s.installPlugins(ctx, dir, projectPlugins(dir))
}

// installTemplatePlugins 模板升级 / 换模板之前，先装上新版本模板要、项目还没装的插件（用户卸掉的不装）。
// 要在合并之前装：合并有冲突时会停在进行中，那时装不了插件；而新版本模板的代码可能已经用到插件（外贸模板 import 了社媒插件的统计），
// 不先装，解决完冲突一推送，云端函数打包就会因为找不到插件的文件失败。to 是 0 表示最新版。
func (s *Server) installTemplatePlugins(ctx context.Context, dir string, t wsgit.Template, to int) []string {
	if to == 0 {
		vs, err := wsgit.Versions(ctx, t)
		if err != nil || len(vs) == 0 {
			return nil
		}
		to = vs[0].No
	}
	snap, err := wsgit.VersionDir(ctx, t, to)
	if err != nil {
		log.Printf("读模板 %s 第 %d 版要哪些插件失败：%v", t.Site, to, err)
		return nil
	}
	want := templatePlugins(snap)
	for id, src := range userPlugins(dir) {
		if src == nil || *src == "" {
			delete(want, id) // 用户卸掉了模板带的这个插件
		}
	}
	return s.installPlugins(ctx, dir, want)
}

func (s *Server) installPlugins(ctx context.Context, dir string, want map[string]string) []string {
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var done []string
	for _, id := range ids {
		if slices.Contains(plugin.IDs(dir), id) {
			continue
		}
		res, err := wsgit.UpgradePlugin(ctx, dir, id, s.pluginSource(want[id]), 0)
		switch {
		case err != nil:
			log.Printf("装模板要的插件 %s 失败：%v", id, err)
		case res.Status == "merged":
			done = append(done, id)
			log.Printf("装了模板要的插件 %s（%s）", id, wsgit.VersionLabel(s.pluginSource(want[id]).Site, res.To))
		}
	}
	return done
}

// pluginSource 把用户填的来源（<仓库>#<子目录>，或已经是 git:…）变成插件站点和它在本机的缓存目录。
func (s *Server) pluginSource(src string) wsgit.Template {
	site := strings.TrimSpace(src)
	if !wsgit.IsGitSite(site) {
		repo, sub, _ := strings.Cut(normalizeSource(site), "#")
		site = wsgit.GitSite(repo, sub)
	}
	sum := sha1.Sum([]byte(site))
	return wsgit.Template{Site: site, Cache: filepath.Join(s.cfg.Dir, "plugins", hex.EncodeToString(sum[:6])), API: version.API}
}

// sourceText 是给人看、写进 annulo.json 的来源：去掉 git: 前缀。
func sourceText(site string) string { return strings.TrimPrefix(site, wsgit.GitPrefix) }

type pluginView struct {
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Base        int             `json:"base,omitempty"`
	BaseLabel   string          `json:"base_label,omitempty"`
	Latest      int             `json:"latest,omitempty"`
	LatestLabel string          `json:"latest_label,omitempty"`
	Updates     []wsgit.Version `json:"updates,omitempty"`
	Requires    int             `json:"requires,omitempty"` // 最新版要的能力版本（比这台 Annulo 高时界面提示先更新 Annulo）
	Installed   bool            `json:"installed"`
	Error       string          `json:"error,omitempty"` // 查不到新版本（没网、仓库没权限）
}

// pluginErr：读仓库的报错（gitsource.go）是按模板写的，插件这里换个说法。
func pluginErr(err error) string {
	return strings.NewReplacer("模板仓库", "插件仓库", "template repository", "plugin repository").Replace(err.Error())
}

func lang(m [2]string) string { return i18n.T(m[0], m[1]) }

// pluginVersions 查来源的版本，填进 v（最新版、比 base 新的、最新版要的能力版本）。
func (s *Server) pluginVersions(ctx context.Context, t wsgit.Template, v *pluginView) {
	vs, err := wsgit.Versions(ctx, t)
	if err != nil {
		v.Error = pluginErr(err)
		return
	}
	if len(vs) == 0 {
		v.Error = i18n.T("插件还没有发过版本", "The plugin has no published versions yet")
		return
	}
	v.Latest, v.LatestLabel = vs[0].No, wsgit.VersionLabel(t.Site, vs[0].No)
	for _, x := range vs {
		if x.No > v.Base {
			v.Updates = append(v.Updates, x)
		}
	}
	if len(v.Updates) > 0 || !v.Installed {
		if m, err := wsgit.PluginVersionMeta(ctx, t, v.ID, vs[0].No); err == nil {
			v.Requires = m.MinAPI
			if !v.Installed {
				v.Name, v.Description = lang(m.Name), lang(m.Desc)
			}
		}
	}
}

func (s *Server) apiPluginsGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	declared := projectPlugins(s.ws.Dir)
	res := map[string]any{"api": version.API}
	installed := []pluginView{}
	have := map[string]bool{}
	for _, id := range plugin.IDs(s.ws.Dir) {
		have[id] = true
		m, _ := plugin.ReadMeta(plugin.Root(s.ws.Dir, id), id)
		v := pluginView{ID: id, Name: lang(m.Name), Description: lang(m.Desc), Installed: true, Base: wsgit.PluginBase(s.ws.Dir, id)}
		site := wsgit.PluginSource(s.ws.Dir, id)
		if site == "" && declared[id] != "" {
			site = s.pluginSource(declared[id]).Site
		}
		if site != "" {
			v.Source = sourceText(site)
			if v.Base > 0 {
				v.BaseLabel = wsgit.VersionLabel(site, v.Base)
			}
			if wsgit.Available() == nil {
				s.pluginVersions(ctx, s.pluginSource(site), &v)
			}
		}
		installed = append(installed, v)
	}
	// 项目要的（annulo.json 里写了，通常是模板带来的）但还没装
	missing := []pluginView{}
	ids := make([]string, 0, len(declared))
	for id := range declared {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if have[id] {
			continue
		}
		v := pluginView{ID: id, Source: declared[id], Name: id}
		s.pluginVersions(ctx, s.pluginSource(declared[id]), &v)
		missing = append(missing, v)
		have[id] = true
	}
	// 能装的：模板要、用户卸掉了的（还能装回来），再加内置的插件里还没装的
	available := []pluginView{}
	tpl := templatePlugins(s.ws.Dir)
	tplIDs := make([]string, 0, len(tpl))
	for id := range tpl {
		tplIDs = append(tplIDs, id)
	}
	sort.Strings(tplIDs)
	for _, id := range tplIDs {
		if have[id] {
			continue
		}
		v := pluginView{ID: id, Source: tpl[id], Name: id}
		s.pluginVersions(ctx, s.pluginSource(tpl[id]), &v)
		available = append(available, v)
		have[id] = true
	}
	for _, src := range pluginCatalog.sources(s.cfg.Dir) {
		t := s.pluginSource(src)
		id, err := wsgit.PluginID(t.Site)
		if err != nil || have[id] {
			continue
		}
		v := pluginView{ID: id, Source: sourceText(t.Site), Name: id}
		s.pluginVersions(ctx, t, &v)
		available = append(available, v)
	}
	merging, conflicts := wsgit.Conflicts(s.ws.Dir)
	res["installed"], res["missing"], res["available"] = installed, missing, available
	res["merging"], res["conflicts"] = merging, conflicts
	writeJSON(w, res)
}

func (s *Server) apiPluginInstall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Source) == "" {
		fail(w, http.StatusBadRequest, i18n.New("要给出插件来源（<仓库>#<子目录>）", "A plugin source (<repository>#<directory>) is required"))
		return
	}
	t := s.pluginSource(in.Source)
	id, err := wsgit.PluginID(t.Site)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if slices.Contains(plugin.IDs(s.ws.Dir), id) {
		fail(w, http.StatusConflict, i18n.Errorf("已经装了插件 %s", "Plugin %s is already installed", id))
		return
	}
	s.runPluginMerge(w, id, t, 0)
}

func (s *Server) apiPluginUpgrade(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
		To int    `json:"to"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	site := wsgit.PluginSource(s.ws.Dir, in.ID)
	if site == "" {
		site = projectPlugins(s.ws.Dir)[in.ID]
	}
	if site == "" {
		fail(w, http.StatusBadRequest, i18n.Errorf("不知道插件 %s 从哪来：先卸载，再按来源重新装", "Don't know where plugin %s came from: remove it, then install it again from its source", in.ID))
		return
	}
	s.runPluginMerge(w, in.ID, s.pluginSource(site), in.To)
}

// runPluginMerge 装或升级插件，记进 annulo.json，合并干净就推到预览；有冲突返回冲突文件，界面交给助手解决。
func (s *Server) runPluginMerge(w http.ResponseWriter, id string, t wsgit.Template, to int) {
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再装插件", "The assistant is working; wait until it finishes before installing plugins"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := wsgit.UpgradePlugin(ctx, s.ws.Dir, id, t, to)
	if err != nil {
		fail(w, http.StatusBadGateway, errors.New(pluginErr(err)))
		return
	}
	out := map[string]any{"result": res, "id": id}
	if res.Status == "merged" {
		if err := recordPlugin(s.ws.Dir, id, sourceText(t.Site)); err != nil {
			out["record_error"] = err.Error()
		} else if _, err := wsgit.Commit(s.ws.Dir, "记下装了插件 "+id); err != nil {
			out["record_error"] = err.Error()
		}
		pushAfterMerge(ctx, s.ws.Dir, out)
	}
	writeJSON(w, out)
}

func (s *Server) apiPluginRemove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("运营助手正在执行，等它做完再卸载插件", "The assistant is working; wait until it finishes before removing plugins"))
		return
	}
	if !slices.Contains(plugin.IDs(s.ws.Dir), in.ID) {
		fail(w, http.StatusNotFound, i18n.Errorf("没有装插件 %s", "Plugin %s isn't installed", in.ID))
		return
	}
	if err := wsgit.RemovePlugin(s.ws.Dir, in.ID); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	out := map[string]any{"ok": true}
	if err := recordPlugin(s.ws.Dir, in.ID, ""); err != nil {
		out["record_error"] = err.Error()
	} else if _, err := wsgit.Commit(s.ws.Dir, "记下卸载了插件 "+in.ID); err != nil {
		out["record_error"] = err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pushAfterMerge(ctx, s.ws.Dir, out)
	writeJSON(w, out)
}

// 项目要哪些插件，记在两个地方：模板的在 annulo.json（老项目 shuttle.json）的 "plugins"，跟着模板升级；
// 用户自己装、卸的在 user/annulo.json 的 "plugins"（user/ 是用户的，模板不写，升级、「恢复模板文件」都碰不到）。
// 用户的优先：写来源是装了这个插件，写 null 是不要模板带的这个插件（不再提示安装）。

// userProjectFile 是用户的项目设置：user/annulo.json。
const userProjectFile = "user/annulo.json"

// templatePlugins 是模板在 annulo.json 里写的：插件 id → 来源。
func templatePlugins(dir string) map[string]string {
	out := map[string]string{}
	for _, f := range brand.ProjectFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var v struct {
			Plugins map[string]string `json:"plugins"`
		}
		if json.Unmarshal(b, &v) == nil {
			for id, src := range v.Plugins {
				if plugin.IDRe.MatchString(id) && out[id] == "" && src != "" {
					out[id] = src
				}
			}
		}
	}
	return out
}

// userPlugins 是 user/annulo.json 里写的：来源，或 nil（不要这个插件）。
func userPlugins(dir string) map[string]*string {
	out := map[string]*string{}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(userProjectFile)))
	if err != nil {
		return out
	}
	var v struct {
		Plugins map[string]*string `json:"plugins"`
	}
	if json.Unmarshal(b, &v) == nil {
		for id, src := range v.Plugins {
			if plugin.IDRe.MatchString(id) {
				out[id] = src
			}
		}
	}
	return out
}

// projectPlugins 是项目要的插件：模板写的，加上用户装的，去掉用户不要的。插件 id → 来源。
func projectPlugins(dir string) map[string]string {
	out := templatePlugins(dir)
	for id, src := range userPlugins(dir) {
		if src == nil || *src == "" {
			delete(out, id)
		} else {
			out[id] = *src
		}
	}
	return out
}

// recordPlugin 在 user/annulo.json 记下用户装了（src 不空）或卸了（src 空）插件 id。
// 和模板写的一样就不用记；卸掉模板要的插件记成 null，之后不再提示装它。别的字段和顺序原样保留。
func recordPlugin(dir, id, src string) error {
	tpl := templatePlugins(dir)[id]
	var val json.RawMessage // nil：去掉这一条
	switch {
	case src != "" && src != tpl:
		val, _ = json.Marshal(src)
	case src == "" && tpl != "":
		val = json.RawMessage("null")
	}
	p := filepath.Join(dir, filepath.FromSlash(userProjectFile))
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if val == nil {
			return nil
		}
		b = []byte("{}")
	}
	obj, err := readOrdered(b)
	if err != nil {
		return i18n.Errorf("%s 格式不对：%w", "%s is malformed: %w", userProjectFile, err)
	}
	plugins := orderedObject{}
	if raw, ok := obj.get("plugins"); ok {
		if plugins, err = readOrdered(raw); err != nil {
			return i18n.Errorf("%s 的 plugins 格式不对：%w", "plugins in %s is malformed: %w", userProjectFile, err)
		}
	}
	if val == nil {
		plugins.del(id)
	} else {
		plugins.set(id, val)
	}
	if len(plugins) == 0 {
		obj.del("plugins")
	} else {
		obj.set("plugins", plugins.marshal(""))
	}
	if len(obj) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(obj.marshal(""), '\n'), 0o644)
}

// orderedObject 是保留键顺序的 JSON 对象（改 annulo.json 的一个字段，别的原样留着，模板升级时合并少冲突）。
type orderedObject []orderedField

type orderedField struct {
	Key   string
	Val   json.RawMessage
	Fresh bool // 这次改的：写回时重新缩进
}

func readOrdered(b []byte) (orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, i18n.New("不是 JSON 对象", "not a JSON object")
	}
	var out orderedObject
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, orderedField{Key: t.(string), Val: raw})
	}
	return out, nil
}

func (o orderedObject) get(k string) (json.RawMessage, bool) {
	for _, e := range o {
		if e.Key == k {
			return e.Val, true
		}
	}
	return nil, false
}

func (o *orderedObject) set(k string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].Key == k {
			(*o)[i].Val, (*o)[i].Fresh = v, true
			return
		}
	}
	*o = append(*o, orderedField{Key: k, Val: v, Fresh: true})
}

func (o *orderedObject) del(k string) {
	for i := range *o {
		if (*o)[i].Key == k {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

// marshal 两个空格缩进。读进来的值原样写回（一行写的还是一行），只有改过的值（set 进来的）按这一层缩进。
func (o orderedObject) marshal(indent string) json.RawMessage {
	if len(o) == 0 {
		return json.RawMessage("{}")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, e := range o {
		k, _ := json.Marshal(e.Key)
		val := e.Val
		if e.Fresh {
			var v bytes.Buffer
			if json.Indent(&v, e.Val, indent+"  ", "  ") == nil {
				val = v.Bytes()
			}
		}
		b.WriteString(indent + "  " + string(k) + ": " + string(val))
		if i < len(o)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(indent + "}")
	return b.Bytes()
}
