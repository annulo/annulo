package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/wsgit"
)

// 离线模式：不登录 creght 也能用。项目只在本机（~/.shuttle/backends/local-…，标记文件 .shuttle/project.json），
// 业务表存在本机的 SQLite（~/.shuttle/workspaces/<项目>/data.db，internal/localdb），页面上传的文件存在 ~/.shuttle/assets。
// 本机函数、社媒发布采集、助手（自己的模型、Claude Code / Codex）、定时任务都照常；
// 要 creght 云端的（站点 Func、表单 webhook、手机上访问、远程访问）用不了。
// 登录 creght 后（「连接 creght」），creght 服务商、creght MCP、渠道站点能用；也能把项目转成在线项目（数据自动迁过去，不能转回来）。

func (s *Server) offlineDBPath(projectID string) string {
	return filepath.Join(s.cfg.Dir, "workspaces", projectID, "data.db")
}

func newOfflineID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "local-" + hex.EncodeToString(b)
}

// newOfflineBackend 新建离线项目：从平台读模板最新一版的文件（开了公开复制的模板不登录也能读）。
func (s *Server) newOfflineBackend(ctx context.Context, tplKey, name string) (*creght.Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = i18n.T("我的项目", "My project")
	}
	if len([]rune(name)) > 40 {
		return nil, i18n.New("项目名字最多 40 个字", "Project names are at most 40 characters")
	}
	host := s.creghtHost()
	tpls := templatesOn(host)
	if len(tpls) == 0 {
		return nil, i18n.Errorf("%s 上还没有模板，先切到别的 creght 区域新建项目", "There are no templates on %s yet; switch to another creght region to create a project", host)
	}
	tpl := tpls[0]
	if tplKey != "" {
		if tpl = templateByKey(host, tplKey); tpl == nil {
			return nil, i18n.Errorf("没有这个模板：%s", "No such template: %s", tplKey)
		}
	}
	site := tpl.Site()
	if tpl.Git == "" {
		if _, err := creght.ReadToken(host); err != nil {
			return nil, i18n.Errorf("「%s」是 creght 的模板：先在 设置 → 连接 里连接 creght", "“%s” is a creght template: connect creght in Settings → Connections first", tpl.Name())
		}
	}

	id := newOfflineID()
	dir := s.cfg.DirFor(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fail := func(err error) (*creght.Workspace, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	ver, err := s.fetchTemplate(ctx, tpl.wsgit(s.cfg.Dir), dir)
	if err != nil {
		return fail(i18n.Errorf("读不到模板的文件：%w", "Couldn't read the template's files: %w", err))
	}
	if err := creght.WriteOffline(dir, creght.OfflineProject{ProjectID: id, Name: name, Template: site, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		return fail(err)
	}
	if wsgit.Available() == nil {
		if err := wsgit.InitOffline(dir, ver, site); err != nil {
			return fail(i18n.Errorf("初始化项目的 git 失败：%w", "Failed to initialize the project's git: %w", err))
		}
	}
	return creght.OpenWorkspaceOn(dir, host)
}

// fetchTemplate 把模板最新一版的文件写进 dir，返回是哪一版。
func (s *Server) fetchTemplate(ctx context.Context, t wsgit.Template, dir string) (wsgit.Version, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	vs, err := wsgit.Versions(ctx, t)
	if err != nil {
		return wsgit.Version{}, err
	}
	if len(vs) == 0 {
		return wsgit.Version{}, i18n.New("模板还没有发过版本", "The template has no published versions yet")
	}
	files, err := wsgit.FilesAt(ctx, t, vs[0].No)
	if err != nil {
		return wsgit.Version{}, err
	}
	for p, body := range files {
		if p == "AGENTS.md" || strings.HasPrefix(p, ".creght/") {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return wsgit.Version{}, err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return wsgit.Version{}, err
		}
	}
	return vs[0], nil
}

// offlineBackends 是本机的离线项目。
func (s *Server) offlineBackends() []backendInfo {
	out := []backendInfo{}
	ents, _ := os.ReadDir(filepath.Join(s.cfg.Dir, "backends"))
	for _, e := range ents {
		if !e.IsDir() || !creght.IsOfflineID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.cfg.Dir, "backends", e.Name())
		p, err := creght.ReadOffline(dir)
		if err != nil || p.ProjectID != e.Name() {
			continue
		}
		b := backendInfo{ProjectID: p.ProjectID, Name: p.Name, CreatedAt: p.CreatedAt, Local: true, Offline: true,
			Current: s.ready.Load() && s.ws.ProjectID == p.ProjectID, TemplateBase: wsgit.Base(dir)}
		if t := templateBySite(wsgit.TemplateSite(dir)); t != nil {
			b.Template, b.TemplateName = t.Key, t.Name()
		} else if t := templateBySite(p.Template); t != nil {
			b.Template, b.TemplateName = t.Key, t.Name()
		}
		if site := wsgit.TemplateSite(dir); wsgit.IsGitSite(site) && b.TemplateBase > 0 {
			b.TemplateBaseLabel = wsgit.VersionLabel(site, b.TemplateBase)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (s *Server) renameOffline(projectID, name string) error {
	dir := s.cfg.DirFor(projectID)
	p, err := creght.ReadOffline(dir)
	if err != nil {
		return i18n.Errorf("本机没有离线项目 %s", "No offline project %s on this computer", projectID)
	}
	p.Name = name
	return creght.WriteOffline(dir, *p)
}

// deleteOffline：离线项目只在本机，删除就是把目录、对话历史和数据挪到 ~/.shuttle/trash。
func (s *Server) deleteOffline(projectID, name string) error {
	dir := s.cfg.DirFor(projectID)
	p, err := creght.ReadOffline(dir)
	if err != nil {
		return i18n.Errorf("本机没有离线项目 %s", "No offline project %s on this computer", projectID)
	}
	if strings.TrimSpace(name) != p.Name {
		return i18n.New("项目名字不对，删除要输入完整的项目名", "The project name doesn't match; type the full project name to delete it")
	}
	s.moveToTrash(projectID)
	return nil
}

func (s *Server) moveToTrash(projectID string) {
	trash := filepath.Join(s.cfg.Dir, "trash", projectID+"-"+time.Now().Format("20060102-150405"))
	for name, dir := range map[string]string{"backend": s.cfg.DirFor(projectID), "workspace": filepath.Join(s.cfg.Dir, "workspaces", projectID)} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		os.MkdirAll(trash, 0o700)
		if err := os.Rename(dir, filepath.Join(trash, name)); err != nil {
			log.Printf("把 %s 挪到回收站失败：%v", dir, err)
		}
	}
}

// apiOfflineMode：PUT settings/offline {enabled}：登录页上选「不登录，离线使用」。
func (s *Server) apiOfflineMode(w http.ResponseWriter, r *http.Request) {
	var in struct{ Enabled bool }
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.cfg.Offline = in.Enabled
	if err := s.cfg.Save(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"offline_mode": s.cfg.Offline})
}

// ---- 页面上传的文件（离线项目没有 creght 素材库） ----

const assetsURL = "/_shuttle/uploaded/"

func (s *Server) assetsDir() string { return filepath.Join(s.cfg.Dir, "assets") }

// saveOfflineAsset 按内容哈希存进 ~/.shuttle/assets，返回和 creght 素材上传一样的结果，地址是本机的 /_shuttle/uploaded/<文件>。
func (s *Server) saveOfflineAsset(name, ctype string, b []byte) (*creght.Asset, error) {
	sum := sha256.Sum256(b)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(ctype); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if !regexp.MustCompile(`^\.[a-z0-9]{1,8}$`).MatchString(ext) {
		ext = ""
	}
	file := hex.EncodeToString(sum[:16]) + ext
	p := filepath.Join(s.assetsDir(), file)
	existed := false
	if _, err := os.Stat(p); err == nil {
		existed = true
	} else {
		if err := os.MkdirAll(s.assetsDir(), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return nil, err
		}
	}
	return &creght.Asset{URL: "http://" + s.cfg.Addr() + assetsURL + file, Path: assetsURL + file, Size: len(b), ContentType: ctype, Existed: existed}, nil
}

var assetNameRe = regexp.MustCompile(`^[0-9a-f]{32}(\.[a-z0-9]{1,8})?$`)

// handleAsset 提供本机存的上传文件。
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, assetsURL)
	if !assetNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("cache-control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.assetsDir(), name))
}

// localAssetFile：本机函数下载 / 请求的是本机存的上传文件（http://127.0.0.1:端口/_shuttle/uploaded/…）时，直接读文件
// （出网规则不许访问本机，平常这类地址会被拦下）。不是返回 ""。
func (s *Server) localAssetFile(rawURL string) string {
	for _, pre := range []string{"http://" + s.cfg.Addr() + assetsURL, "http://localhost:" + portOf(s.cfg.Addr()) + assetsURL} {
		if name, ok := strings.CutPrefix(rawURL, pre); ok && assetNameRe.MatchString(name) {
			return filepath.Join(s.assetsDir(), name)
		}
	}
	return ""
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// ---- 离线项目转成在线项目 ----

var localAssetRe = regexp.MustCompile(`https?://(?:127\.0\.0\.1|localhost):\d+/_shuttle/uploaded/([0-9a-f]{32}(?:\.[a-z0-9]{1,8})?)`)

// apiGoOnline：POST setup/online：把当前的离线项目转成 creght 上的在线项目。
//  1. 从项目的模板复制一个 creght 项目（拿到项目和站点），拉一份它的 .creght；
//  2. 按 tables/ 建表，把本机 SQLite 里的行全部写过去：记录 id 会变，行里引用旧 id 的地方换成新 id；本机上传的文件传到 creght 素材，地址也换掉；
//  3. 项目目录挪到 backends/<新 id>，换上 .creght，本机的文件推上去（以本机为准）；对话历史、日志跟着挪；
//  4. 切到新项目。本机的 SQLite 留在回收站。不能转回离线。
func (s *Server) apiGoOnline(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() || !s.ws.Offline {
		fail(w, http.StatusBadRequest, i18n.New("当前项目不是离线项目", "The current project isn't offline"))
		return
	}
	if s.agent.Busy() {
		fail(w, http.StatusConflict, i18n.New("助手还在执行，等它做完再转", "The assistant is working; wait until it finishes"))
		return
	}
	host := s.creghtHost()
	if _, err := creght.ReadToken(host); err != nil {
		fail(w, http.StatusUnauthorized, i18n.New("先登录 creght 账号，再转成在线项目", "Sign in to a creght account first, then convert"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	ws, steps, err := s.goOnline(ctx, host)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "project_id": ws.ProjectID, "log": steps})
}

func (s *Server) goOnline(ctx context.Context, host string) (*creght.Workspace, []string, error) {
	old := s.ws
	var steps []string
	step := func(f string, args ...any) {
		msg := fmt.Sprintf(f, args...)
		log.Printf("转在线：%s", msg)
		steps = append(steps, msg)
	}
	meta, err := creght.ReadOffline(old.Dir)
	if err != nil {
		return nil, nil, err
	}
	// 1. 复制模板，拉一份 .creght
	tpl := templateBySite(wsgit.TemplateSite(old.Dir))
	if tpl == nil || creght.NormHost(tpl.Host) != host {
		tpls := creghtTemplatesOn(host) // 转在线要复制一个 creght 模板（git 模板来的项目也是），本机的文件再整份推上去
		if len(tpls) == 0 {
			return nil, nil, i18n.Errorf("%s 上没有模板，转不了：换到 creght.cn 再试", "There are no templates on %s; switch to creght.cn and try again", host)
		}
		tpl = tpls[0]
		for _, t := range tpls {
			if t.Key == "trade" {
				tpl = t
			}
		}
	}
	cl := creght.NewClient(host)
	pid, sid, err := cl.CopyProject(ctx, tpl.ProjectID, meta.Name)
	if err != nil {
		return nil, nil, i18n.Errorf("在 creght 上建项目失败：%w", "Failed to create the creght project: %w", err)
	}
	step("在 creght 上建好了项目 %s", pid)
	cleanup := func(err error) (*creght.Workspace, []string, error) {
		if derr := cl.DeleteProject(context.Background(), pid); derr != nil {
			log.Printf("转在线失败，删掉建了一半的项目 %s 也失败了：%v", pid, derr)
		}
		return nil, steps, err
	}
	tmp, err := os.MkdirTemp("", "shuttle-online-")
	if err != nil {
		return cleanup(err)
	}
	defer os.RemoveAll(tmp)
	pullDir := filepath.Join(tmp, "site")
	if err := wsgit.PullSite(ctx, host, pid+"/"+sid, pullDir, nil); err != nil {
		return cleanup(i18n.Errorf("拉取新项目失败：%w", "Failed to pull the new project: %w", err))
	}

	// 2. 建表、迁数据
	defs, _, err := (&workspaceTables{}).load(old.Dir)
	if err != nil {
		return cleanup(err)
	}
	have, err := cl.TableKeys(ctx, pid)
	if err != nil {
		return cleanup(err)
	}
	for _, d := range defs {
		if !have[d.Key] {
			if err := cl.CreateTable(ctx, pid, d.Key, d.Name, d.Desc, d.JSONSchema); err != nil {
				return cleanup(i18n.Errorf("建表 %s 失败：%w", "Failed to create table %s: %w", d.Key, err))
			}
		}
	}
	if s.localData != nil {
		n, err := s.migrateRows(ctx, cl, pid, sid, defs)
		if err != nil {
			return cleanup(err)
		}
		step("迁移了 %d 条数据", n)
	}

	// 3. 挪目录，换上 .creght，推文件
	if wsgit.Available() == nil {
		wsgit.Commit(old.Dir, "转成在线项目前的改动")
	}
	newDir := s.cfg.DirFor(pid)
	if err := os.Rename(old.Dir, newDir); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(filepath.Join(pullDir, ".creght"), filepath.Join(newDir, ".creght")); err != nil {
		os.Rename(newDir, old.Dir)
		return cleanup(err)
	}
	if b, err := os.ReadFile(filepath.Join(pullDir, "AGENTS.md")); err == nil {
		os.WriteFile(filepath.Join(newDir, "AGENTS.md"), b, 0o644)
	}
	for _, d := range brand.MarkerDirs {
		os.RemoveAll(filepath.Join(newDir, d))
	}
	ensureIgnore(filepath.Join(newDir, ".creghtignore"), "AGENTS.md")
	var pushLog bytes.Buffer
	if err := wsgit.PushNew(ctx, newDir, &pushLog); err != nil {
		step("推送文件失败（在 设置 → 项目 里重试推送，或让助手 shuttle push）：%v", err)
	} else {
		step("项目文件已经推到 creght")
	}

	// 对话、日志、快照跟着新 id；本机的 SQLite 挪进回收站
	for _, d := range []string{"workspaces", filepath.Join("logs", "local"), "snapshots"} {
		from, to := filepath.Join(s.cfg.Dir, d, old.ProjectID), filepath.Join(s.cfg.Dir, d, pid)
		if _, err := os.Stat(from); err == nil {
			os.MkdirAll(filepath.Dir(to), 0o700)
			if err := os.Rename(from, to); err != nil {
				log.Printf("挪 %s 失败：%v", from, err)
			}
		}
	}
	if db := filepath.Join(s.cfg.Dir, "workspaces", pid, "data.db"); fileExists(db) {
		trash := filepath.Join(s.cfg.Dir, "trash", old.ProjectID+"-data-"+time.Now().Format("20060102-150405"))
		os.MkdirAll(trash, 0o700)
		for _, suf := range []string{"", "-wal", "-shm"} {
			os.Rename(db+suf, filepath.Join(trash, "data.db"+suf))
		}
	}

	// 4. 切过去
	ws, err := creght.OpenWorkspace(newDir)
	if err != nil {
		return nil, steps, err
	}
	s.cfg.Backend = ws.ProjectID
	if s.cfg.Creght == "" && host != creght.DefaultHost {
		s.cfg.Creght = host
	}
	s.cfg.Save()
	s.agent.SetWorkspace(ws.ProjectID, ws.Dir)
	s.Attach(ws)
	step("已切到在线项目 %s", ws.ProjectID)
	return ws, steps, nil
}

// migrateRows 把本机 SQLite 里的行写进 creght 项目的表：先原样写一遍拿到新 id，再把行里引用旧 id、本机文件地址的地方换掉。
func (s *Server) migrateRows(ctx context.Context, cl *creght.Client, pid, sid string, defs []tableDef) (int, error) {
	tables, err := s.localData.Tables()
	if err != nil {
		return 0, err
	}
	tids := map[string]string{}
	for _, d := range defs {
		id, err := cl.TableID(ctx, pid, d.Key)
		if err != nil {
			return 0, err
		}
		tids[d.Key] = id
	}
	type moved struct{ table, tid, id, body string }
	var all []moved
	var pairs []string // 旧 id、新 id 交替
	for _, t := range tables {
		tid := tids[t]
		if tid == "" {
			log.Printf("转在线：表 %s 不在 tables/ 里，它的数据不迁", t)
			continue
		}
		recs, err := s.localData.ListRecords(ctx, "", t, creght.RecordQuery{})
		if err != nil {
			return 0, err
		}
		for _, r := range recs {
			id, err := cl.CreateRecord(ctx, pid, tid, json.RawMessage(r.Body))
			if err != nil {
				return 0, i18n.Errorf("迁移 %s 的数据失败：%w", "Failed to migrate data in %s: %w", t, err)
			}
			pairs = append(pairs, r.ID, id)
			all = append(all, moved{t, tid, id, string(r.Body)})
		}
	}
	// 本机上传的文件：传到 creght 素材，地址换成线上的
	uploaded := map[string]string{}
	for _, m := range all {
		for _, g := range localAssetRe.FindAllStringSubmatch(m.body, -1) {
			if _, ok := uploaded[g[0]]; ok {
				continue
			}
			b, err := os.ReadFile(filepath.Join(s.assetsDir(), g[1]))
			if err != nil {
				continue
			}
			a, err := cl.UploadAsset(ctx, pid, sid, g[1], mime.TypeByExtension(filepath.Ext(g[1])), b)
			if err != nil {
				log.Printf("转在线：上传 %s 失败：%v", g[1], err)
				continue
			}
			uploaded[g[0]] = a.URL
			pairs = append(pairs, g[0], a.URL)
		}
	}
	rep := strings.NewReplacer(pairs...)
	for _, m := range all {
		nb := rep.Replace(m.body)
		if nb == m.body {
			continue
		}
		if err := cl.UpdateRecord(ctx, pid, m.tid, m.id, json.RawMessage(nb)); err != nil {
			return 0, i18n.Errorf("更新 %s 里的引用失败：%w", "Failed to update references in %s: %w", m.table, err)
		}
	}
	return len(all), nil
}

func ensureIgnore(file, line string) {
	b, _ := os.ReadFile(file)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return
		}
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	os.WriteFile(file, []byte(s+line+"\n"), 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
