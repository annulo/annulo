package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/wsgit"
)

// git 模板（docs/annulo-plan.md 第 3 步）：模板是一个 git 仓库，版本是 semver tag。新建离线项目能选，升级和 creght 模板一样。
// 列表 = 内置的（defaultTemplateSources）+ 设置里加的（config.json 的 template_sources）。名字和简介读模板最新一版的 annulo.json。

// defaultTemplateSources：内置的 git 模板，每项 <仓库>[#<子目录>]（开源的模板仓库 github.com/annulo/templates）。
var defaultTemplateSources = []string{"https://github.com/annulo/templates#blank"}

// templateSources 是设置里加的 git 模板（server 启动时从配置读，测试里直接改）。
var templateSources struct {
	sync.Mutex
	list []string
	dir  string // ~/.shuttle：模板镜像放在 templates/ 下
}

func setTemplateSources(dir string, list []string) {
	templateSources.Lock()
	templateSources.dir, templateSources.list = dir, append([]string(nil), list...)
	templateSources.Unlock()
}

// gitMetaCache：每个 git 模板的名字和简介（要 fetch 一次仓库）。读成功的缓存 5 分钟，失败 1 分钟内不再试。
var gitMetaCache struct {
	sync.Mutex
	m map[string]gitMetaEntry
}

type gitMetaEntry struct {
	meta wsgit.TemplateMeta
	at   time.Time
	ok   bool
}

// gitTemplate 是一个 git 模板站点（git:<仓库>#<子目录>）的定义；名字和简介读不到就用仓库（子目录）名。
func gitTemplate(site string) *templateDef {
	templateSources.Lock()
	dir := templateSources.dir
	templateSources.Unlock()
	t := &templateDef{Key: site, Git: site}
	gitMetaCache.Lock()
	e, has := gitMetaCache.m[site]
	gitMetaCache.Unlock()
	ttl := time.Minute
	if e.ok {
		ttl = 5 * time.Minute
	}
	if !has || time.Since(e.at) > ttl {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		meta, err := wsgit.GitMeta(ctx, t.wsgit(dir))
		cancel()
		if err != nil {
			log.Printf("读 git 模板 %s 失败：%v", site, err)
		}
		if err == nil || !e.ok {
			e.meta = meta
		}
		e.ok, e.at = err == nil || e.ok, time.Now()
		gitMetaCache.Lock()
		if gitMetaCache.m == nil {
			gitMetaCache.m = map[string]gitMetaEntry{}
		}
		gitMetaCache.m[site] = e
		gitMetaCache.Unlock()
	}
	t.name, t.desc = e.meta.Name, e.meta.Desc
	return t
}

// gitTemplates 是能选的 git 模板，内置的在前；同一个仓库（子目录）只列一次。
func gitTemplates() []*templateDef {
	templateSources.Lock()
	src := append(append([]string(nil), defaultTemplateSources...), templateSources.list...)
	templateSources.Unlock()
	var out []*templateDef
	seen := map[string]bool{}
	for _, s := range src {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		repo, sub, _ := strings.Cut(strings.TrimPrefix(s, wsgit.GitPrefix), "#")
		site := wsgit.GitSite(repo, sub)
		if seen[site] {
			continue
		}
		seen[site] = true
		out = append(out, gitTemplate(site))
	}
	return out
}

// normalizeSource 把用户填的地址整理成 <仓库>#<子目录>：去空白、去掉 git: 前缀。
func normalizeSource(src string) string {
	repo, sub, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(src), wsgit.GitPrefix), "#")
	repo, sub = strings.TrimSpace(repo), strings.Trim(strings.TrimSpace(sub), "/")
	if sub == "" {
		return repo
	}
	return repo + "#" + sub
}

// isUserSource：设置里自己加的 git 模板（内置的不能删）。
func isUserSource(site string) bool {
	templateSources.Lock()
	defer templateSources.Unlock()
	for _, s := range templateSources.list {
		repo, sub, _ := strings.Cut(normalizeSource(s), "#")
		if wsgit.GitSite(repo, sub) == site {
			return true
		}
	}
	return false
}

// apiTemplateSourceAdd：POST settings/templates {source: "<仓库>[#<子目录>]"}。先读一遍仓库（有没有权限、有没有 vX.Y.Z 版本），读得到才加。
func (s *Server) apiTemplateSourceAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || normalizeSource(in.Source) == "" {
		fail(w, http.StatusBadRequest, i18n.New("填一个 git 仓库地址", "Enter a git repository address"))
		return
	}
	src := normalizeSource(in.Source)
	repo, sub, _ := strings.Cut(src, "#")
	site := wsgit.GitSite(repo, sub)
	def := &templateDef{Key: site, Git: site}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	vs, err := wsgit.Versions(ctx, def.wsgit(s.cfg.Dir))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(vs) == 0 {
		fail(w, http.StatusBadRequest, i18n.Errorf("%s 里还没有版本：打一个 v1.0.0 这样的 tag（tag 上是完整的模板）再添加", "%s has no versions yet: push a tag like v1.0.0 (the tag must contain the finished template), then add it", repo))
		return
	}
	if files, err := wsgit.FilesAt(ctx, def.wsgit(s.cfg.Dir), vs[0].No); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	} else if !hasPages(files) {
		fail(w, http.StatusBadRequest, i18n.Errorf("%s 的 %s 里没有 pages/：不像是一个模板（子目录写对了吗？）", "%s at %s has no pages/: it doesn't look like a template (is the subdirectory right?)", repo, vs[0].Label))
		return
	}
	if !slices.Contains(s.cfg.TemplateSources, src) {
		s.cfg.TemplateSources = append(s.cfg.TemplateSources, src)
		if err := s.cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		setTemplateSources(s.cfg.Dir, s.cfg.TemplateSources)
	}
	gitMetaCache.Lock()
	delete(gitMetaCache.m, site)
	gitMetaCache.Unlock()
	_, tokErr := creght.ReadToken(s.loginHost())
	writeJSON(w, map[string]any{"template": gitTemplate(site).view(tokErr == nil)})
}

func hasPages(files map[string]string) bool {
	for p := range files {
		if strings.HasPrefix(p, "pages/") {
			return true
		}
	}
	return false
}

// apiTemplateSourceRemove：DELETE settings/templates?key=git:…。只是不再列出来；从它建的项目照样能跟着它升级。
func (s *Server) apiTemplateSourceRemove(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	out := s.cfg.TemplateSources[:0:0]
	for _, src := range s.cfg.TemplateSources {
		repo, sub, _ := strings.Cut(normalizeSource(src), "#")
		if wsgit.GitSite(repo, sub) != key {
			out = append(out, src)
		}
	}
	s.cfg.TemplateSources = out
	if err := s.cfg.Save(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	setTemplateSources(s.cfg.Dir, s.cfg.TemplateSources)
	writeJSON(w, map[string]any{"ok": true})
}
