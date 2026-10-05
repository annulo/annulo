// Package localsite 在本机渲染运营后台的页面（docs/local-render.md）：直接读项目目录，改完保存就能看，不用先推到 creght。
//
// 只做客户端渲染，热更新照 folia-web 编辑器预览的做法：每个文件单独编译成一个 ESM（modules.go，按内容缓存），
// 浏览器按 URL 加载；文件一变只重编那一个，推给页面（events.go），页面里的运行时用 React Refresh 就地替换（runtime.go）。
// pages/ 下的页面由 react-router 切换；第三方包按 importMap 从 CDN 加载（平台的 dev_import_map，开发版 React 才能 Refresh）；
// 样式用 Tailwind browser 在浏览器里生成。服务端导出（getServerSideProps）不跑。
// 路由、importMap、i18n、HTML 外壳的约定都照平台渲染器（talizen 渲染）。
package localsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

type Renderer struct {
	// UILocale 是 Shuttle 界面的语言：页面没带语言前缀、也还没有 CREGHT_LOCALE cookie 时（外壳第一次打开，cookie 还没写）按它选，
	// 不看浏览器的 Accept-Language，外壳和后台才不会一个中文一个英文
	UILocale func() string

	dir       string
	projectID string
	siteID    string
	sys       systemConfig
	modules   *moduleCache
	events    hub

	mu        sync.Mutex // 同一时间只跑一次构建
	cur, prev *build     // prev 留着：还在用上一版的页面也拿得到它的模块

	mapsMu sync.Mutex
	maps   map[string]*sourceMap // 解析过的 sourcemap，按「路径@版本」
}

// build 是某一份源码（sig）的构建结果。
type build struct {
	id        string // 这次构建的内容哈希：页面拿它和推送对比，知道自己是不是最新的
	sig       string
	cfg       *siteConfig
	importMap map[string]string
	messages  map[string]json.RawMessage // 语言 → 合并好的 messages
	css       string                     // index.css（去掉了 Tailwind browser 编不了的）
	tailwind  string                     // Tailwind browser 的地址（跟着 CDN）
	routes    []route
	graph     *graph
	entry     string                // 入口模块的 URL
	manifest  map[string]moduleInfo // 路径 → 模块 URL，给运行时
	served    map[string]served     // 路径 → 编译好的模块（@entry.js 是入口）
	inputs    map[string]bool       // 页面用到的本地文件
	fatal     string                // 页面跑不起来的原因（编译出错、配置写错……）
	issues    []errorItem           // 页面还能跑、但要让人知道的（CSS 里去掉的 @import、getServerSideProps 不执行……）
}

type moduleInfo struct {
	URL  string `json:"url"`
	Lazy bool   `json:"lazy,omitempty"` // 只被动态 import()：页面加载时不去注册它
}

type served struct {
	ver, code, sourcemap string
}

// errorItem 和平台运行时记在 window.__TALIZEN_RENDER_ERRORS__ 里的格式一致，Shuttle 的报错监控（web/src/lib/pageMonitor.ts）读它。
type errorItem struct {
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// 不是页面代码的目录：本机函数、表、任务、说明文档……里面的文件只有被页面 import 了才算页面源码
var nonPageDirs = []string{"local", "tables", "schedules", "tasks", "prompts", "skills", "docs", "backend", "user", "types", "node_modules"}

// New：dir 是项目目录，cacheDir 放编译缓存和平台配置的缓存（~/.shuttle/render）。
// apiHost 是取渲染配置的 creght 集群，空就不连 creght、用随安装包带的那份，包从 cdn 加载（空是 DefaultCDN）。
func New(dir, apiHost, projectID, siteID, cacheDir, cdn string) *Renderer {
	if u, err := url.Parse(cdn); err == nil && u.Hostname() != "" {
		configuredCDNs.Store(strings.ToLower(u.Hostname()), true)
	}
	return &Renderer{
		dir:       dir,
		projectID: projectID,
		siteID:    siteID,
		sys:       systemConfig{apiHost: apiHost, cdn: cdn, file: filepath.Join(cacheDir, "system-"+hostKey(apiHost)+".json")},
		modules:   &moduleCache{dir: filepath.Join(cacheDir, "modules")},
	}
}

func hostKey(apiHost string) string {
	s := sha256.Sum256([]byte(apiHost))
	return hex.EncodeToString(s[:4])
}

func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ensure 返回当前源码的构建结果，源码没变就用上一次的。建了新的就把变化推给打开着的页面。
func (r *Renderer) ensure(ctx context.Context) *build {
	r.mu.Lock()
	var inputs map[string]bool
	if r.cur != nil {
		inputs = r.cur.inputs
	}
	files, sig := r.scan(inputs)
	if r.cur != nil && r.cur.sig == sig {
		b := r.cur
		r.mu.Unlock()
		return b
	}
	start := time.Now()
	b := r.build(ctx, files, sig)
	// 构建过程中文件又变了：下一次再建。没变就按这次用到的文件重算签名（多了被 import 的 local/ 文件）
	if _, sig2 := r.scan(inputs); sig2 != sig {
		b.sig = ""
	} else {
		_, b.sig = r.scan(b.inputs)
	}
	msg := diff(r.cur, b)
	r.prev, r.cur = r.cur, b
	r.mu.Unlock()
	log.Printf("本地渲染：构建 %s，%d 个模块，用时 %v%s", r.dir, len(b.served), time.Since(start).Round(time.Millisecond), map[bool]string{true: "，失败：" + firstLine(b.fatal), false: ""}[b.fatal != ""])
	if msg != nil {
		r.events.broadcast(msg)
	}
	return b
}

func (r *Renderer) build(ctx context.Context, files []string, sig string) *build {
	b := &build{sig: sig, served: map[string]served{}, manifest: map[string]moduleInfo{}, inputs: map[string]bool{}}
	var fatal []string
	cfg, err := loadConfig(r.dir)
	if err != nil {
		fatal = append(fatal, err.Error())
		cfg = &siteConfig{pages: map[string]pageMeta{"": {}}}
	}
	b.cfg = cfg
	sys, _ := r.sys.get(ctx) // 读不到平台的就用随安装包带的，不会失败
	b.importMap = mergeImportMap(sys, cfg.ImportMap)
	b.tailwind = cdnURL(tailwindBrowserURL, sys.CDN)
	b.messages = loadMessages(r.dir, cfg.I18n)
	read := func(p string) string { s, _ := os.ReadFile(filepath.Join(r.dir, p)); return string(s) }
	css, dropped := browserCSS(indexCSS(files, read))
	b.css = css
	for _, d := range dropped {
		b.issues = append(b.issues, errorItem{Level: "error", Source: "css", Message: i18n.Tf("index.css 里的 %s 在本地渲染（Tailwind browser）里用不了，已经跳过", "%s in index.css isn't supported by local rendering (Tailwind browser) and was skipped", d)})
	}

	b.routes = buildRoutes(files)
	var roots []string
	for _, rt := range b.routes {
		roots = append(roots, rt.File)
		if serverExportRE.MatchString(read(rt.File)) {
			b.issues = append(b.issues, errorItem{Level: "error", Source: "build", Message: i18n.Tf("%s 导出了 getServerSideProps：本地渲染只在浏览器里跑页面，不执行它，页面拿不到它给的 props", "%s exports getServerSideProps, which local rendering doesn't run (pages render in the browser only); the page gets no props from it", rt.File)})
		}
	}
	g := r.modules.buildGraph(r.dir, roots)
	b.graph = g
	fatal = append(fatal, g.errs...)

	// 从页面静态 import 走得到的是页面加载时就有的；只被动态 import() 的是 lazy
	static := map[string]bool{}
	var walk func(string)
	walk = func(p string) {
		if static[p] || g.mods[p] == nil {
			return
		}
		static[p] = true
		for _, d := range g.mods[p].Deps {
			walk(d)
		}
	}
	for _, rt := range roots {
		walk(rt)
	}
	for p, m := range g.mods {
		b.inputs[p] = true
		if m.Error != "" {
			continue
		}
		b.served[p] = served{ver: g.version[p], code: g.link(p), sourcemap: m.Map}
		b.manifest[p] = moduleInfo{URL: moduleURL(p, g.version[p]), Lazy: !static[p]}
	}
	entry := entrySource(b.routes, func(f string) string { return moduleURL(f, g.version[f]) })
	ev := shortHash(entry)
	b.served["@entry.js"] = served{ver: ev, code: entry}
	b.entry = moduleURL("@entry.js", ev)

	b.fatal = strings.Join(fatal, "\n\n")
	pages, _ := json.Marshal(cfg.pages)
	im, _ := json.Marshal(b.importMap)
	msgs, _ := json.Marshal(b.messages)
	b.id = shortHash(ev, b.css, string(im), string(msgs), string(pages), b.fatal)
	return b
}

// update 是推给页面的一次变化（runtime.go 的 apply）
type update struct {
	Type       string                `json:"type"` // update / reload / error
	Build      string                `json:"build"`
	Error      string                `json:"error,omitempty"`
	Entry      string                `json:"entry,omitempty"`
	Modules    map[string]moduleInfo `json:"modules,omitempty"`
	Boundaries []string              `json:"boundaries"` // 要热替换的模块；null：换不了，重新渲染入口
	CSS        *string               `json:"css,omitempty"`
}

// diff 比较两次构建，决定页面怎么更新：
//   - 构建出错 → error；importMap、文案、页面外壳（config）变了 → reload（importMap 在页面里改不了）；
//   - 路由变了 → 重新渲染入口；
//   - 否则从改了的文件沿「谁 import 了它」往上找最近的 React Refresh 边界（folia-web chooseHmrUpdate），
//     一路都找得到就热替换这些边界，有一条路走到了不是边界的页面就重新渲染入口；
//   - index.css 变了 → 带上新的 css，只换样式。
func diff(prev, next *build) *update {
	if prev == nil || prev.id == next.id {
		return nil
	}
	u := &update{Type: "update", Build: next.id, Entry: next.entry, Modules: next.manifest}
	if next.fatal != "" {
		return &update{Type: "error", Build: next.id, Error: next.fatal}
	}
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return string(x) == string(y) }
	if !same(prev.importMap, next.importMap) || !same(prev.messages, next.messages) || !same(prev.cfg.pages, next.cfg.pages) || !same(prev.cfg.I18n, next.cfg.I18n) {
		return &update{Type: "reload", Build: next.id}
	}
	if next.css != prev.css {
		u.CSS = &next.css
	}
	if prev.fatal != "" || prev.graph == nil || !same(prev.routes, next.routes) {
		return u // Boundaries 为 nil：重新渲染入口（上一次出错的，页面会整页刷新）
	}
	isRoot := map[string]bool{}
	for _, rt := range next.routes {
		isRoot[rt.File] = true
	}
	var changed []string
	for p, m := range next.graph.mods {
		if old := prev.graph.mods[p]; old == nil || old.Code != m.Code {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	u.Boundaries = []string{}
	seen := map[string]bool{}
	queue := changed
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		m := next.graph.mods[p]
		if m != nil && m.boundary(p) {
			u.Boundaries = append(u.Boundaries, p)
			continue
		}
		importers := next.graph.importers[p]
		if isRoot[p] || len(importers) == 0 {
			u.Boundaries = nil // 走到了入口：换不了，重新渲染
			return u
		}
		queue = append(queue, importers...)
	}
	sort.Strings(u.Boundaries)
	return u
}

// Watch 盯着项目目录：页面源码一变就重新构建（ensure 里把变化推给页面）。
func (r *Renderer) Watch(ctx context.Context) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	r.ensure(ctx)
	pending := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.mu.Lock()
		var inputs map[string]bool
		cur := ""
		if r.cur != nil {
			inputs, cur = r.cur.inputs, r.cur.sig
		}
		_, sig := r.scan(inputs)
		r.mu.Unlock()
		if sig == cur {
			pending = ""
			continue
		}
		// 等一轮没有新改动再建：助手一次改好几个文件时别每个文件推一次
		if sig != pending {
			pending = sig
			continue
		}
		pending = ""
		r.ensure(ctx)
	}
}

// ServeModule 处理 /_client/m/<路径>?v=<版本>（和 .map）。版本对不上当前和上一次构建的就 404：页面会整页刷新。
func (r *Renderer) ServeModule(w http.ResponseWriter, req *http.Request) bool {
	rel := strings.TrimPrefix(req.URL.Path, "/_client/m/")
	ver := req.URL.Query().Get("v")
	if rel == "@runtime.js" {
		w.Header().Set("content-type", "text/javascript; charset=utf-8")
		w.Header().Set("cache-control", "public, max-age=31536000, immutable")
		w.Write([]byte(runtimeJS))
		return true
	}
	isMap := strings.HasSuffix(rel, ".map")
	rel = strings.TrimSuffix(rel, ".map")
	s, ok := r.lookup(rel, ver)
	if !ok {
		return false
	}
	w.Header().Set("cache-control", "public, max-age=31536000, immutable") // URL 里带版本
	if isMap {
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(s.sourcemap))
		return true
	}
	w.Header().Set("content-type", "text/javascript; charset=utf-8")
	w.Write([]byte(s.code))
	return true
}

func (r *Renderer) lookup(rel, ver string) (served, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range []*build{r.cur, r.prev} {
		if b == nil {
			continue
		}
		if s, ok := b.served[rel]; ok && s.ver == ver {
			return s, true
		}
	}
	return served{}, false
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// loadMessages：messages/<默认语言>.json 打底，当前语言的深合并上去（平台 LoadMessages）。
func loadMessages(dir string, c i18nConfig) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	read := func(loc string) map[string]any {
		if loc == "" {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(dir, "messages", loc+".json"))
		if err != nil {
			return nil
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return nil
		}
		return m
	}
	def := c.defaultLocale()
	locales := c.Locales
	if len(locales) == 0 {
		locales = []string{def}
	}
	for _, loc := range locales {
		m := deepMerge(read(def), read(loc))
		if m == nil {
			continue
		}
		if b, err := json.Marshal(m); err == nil {
			out[loc] = b
		}
	}
	return out
}

func deepMerge(base, over map[string]any) map[string]any {
	if base == nil && over == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, ok1 := out[k].(map[string]any)
		om, ok2 := v.(map[string]any)
		if ok1 && ok2 {
			out[k] = deepMerge(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

// 不是页面代码的目录（nonPageDirs）里，只有被页面 import 了的文件才算页面源码；其余目录的文件都算。
var sourceExts = map[string]bool{".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".css": true, ".json": true}

// scan 列出项目里的源码文件，按路径、大小、修改时间算一个签名：签名变了就要重新构建。
func (r *Renderer) scan(inputs map[string]bool) (files []string, sig string) {
	h := sha256.New()
	skip := map[string]bool{}
	for _, d := range nonPageDirs {
		skip[d] = true
	}
	_ = filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(r.dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExts[path.Ext(rel)] {
			return nil
		}
		top, _, nested := strings.Cut(rel, "/")
		if nested && skip[top] && !inputs[rel] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, rel)
		h.Write([]byte(rel + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\n"))
		return nil
	})
	sort.Strings(files)
	return files, hex.EncodeToString(h.Sum(nil))
}

// Watch 盯着项目目录：页面源码一变就在后台重新构建，建好了调 onChange（外壳据此刷新左侧后台）。

func (r *Renderer) ServePublic(w http.ResponseWriter, req *http.Request) bool {
	clean := path.Clean("/" + req.URL.Path)
	if clean == "/" {
		return false
	}
	p := filepath.Join(r.dir, "public", filepath.FromSlash(clean))
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return false
	}
	http.ServeFile(w, req, p)
	return true
}
