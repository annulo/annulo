package localsite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/annulo/annulo/internal/i18n"
)

// 按文件编译（照 folia-web 编辑器预览的做法，但编译放在 Go 里）：
//
//   - 每个本地文件单独编成一个 ESM，挂在 /_client/m/<路径>?v=<版本>；第三方包照旧 external，浏览器按 importMap 加载。
//   - 第一步（compileFile）只看这个文件自己：esbuild 编译，本地 import 换成占位符 /@shuttle-dep/<路径>，记下依赖和导出名。
//     结果按「路径 + 内容」缓存在内存和 ~/.shuttle/render/modules/ 下，改一个文件只重编这一个。
//   - 第二步（link）把占位符换成依赖的 URL。版本号是「自己的内容 + 依赖的版本」的哈希，依赖变了往上一路变，
//     没变的子树 URL 不变（浏览器按 URL 缓存模块，热更新时 import 新 URL 就拿到新代码）。循环 import 的一组文件共用一个版本。

// compiled 是一个文件编译后的结果，只取决于它自己的内容。
type compiled struct {
	Code       string            `json:"code"`        // 本地依赖是占位符 /@shuttle-dep/<路径>
	Map        string            `json:"map"`         // sourcemap（sources 是项目里的相对路径）
	Deps       []string          `json:"deps"`        // 静态 import 的本地文件
	Lazy       []string          `json:"lazy"`        // 动态 import() 的本地文件
	Exports    []string          `json:"exports"`     // 导出名
	ExportStar bool              `json:"export_star"` // 有 export * from：判断不了导出了什么，不当 Refresh 边界
	Error      string            `json:"error,omitempty"`
	Resolved   map[string]string `json:"-"`
}

const depPlaceholder = "/@shuttle-dep/"

// 编译选项变了（改了下面的 esbuild 参数）要改这个，旧缓存就不用了
const compileCacheVersion = "3"

var moduleExts = []string{".tsx", ".ts", ".jsx", ".js", ".mjs", ".json"}

type moduleCache struct {
	dir string
	mu  sync.Mutex
	mem map[string]*compiled // key：路径 + 内容的哈希
}

func (c *moduleCache) key(rel string, src []byte) string {
	h := sha256.New()
	h.Write([]byte(compileCacheVersion + "\x00" + rel + "\x00"))
	h.Write(src)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (c *moduleCache) get(key string) *compiled {
	c.mu.Lock()
	m := c.mem[key]
	c.mu.Unlock()
	if m != nil {
		return m
	}
	b, err := os.ReadFile(filepath.Join(c.dir, key[:2], key+".json"))
	if err != nil {
		return nil
	}
	var v compiled
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	c.put(key, &v, false)
	return &v
}

func (c *moduleCache) put(key string, v *compiled, disk bool) {
	c.mu.Lock()
	if c.mem == nil || len(c.mem) > 4000 {
		c.mem = map[string]*compiled{}
	}
	c.mem[key] = v
	c.mu.Unlock()
	if disk && v.Error == "" {
		if b, err := json.Marshal(v); err == nil {
			p := filepath.Join(c.dir, key[:2], key+".json")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			tmp := p + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				_ = os.Rename(tmp, p)
			}
		}
	}
}

// compileFile 编译一个文件（rel 是相对项目根目录的路径，/ 分隔）。缓存命中就不跑 esbuild。
func (c *moduleCache) compileFile(dir, rel string) (*compiled, error) {
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	key := c.key(rel, src)
	if m := c.get(key); m != nil {
		return m, nil
	}
	m := compileSource(dir, rel, src)
	c.put(key, m, true)
	return m, nil
}

var exportStarRE = regexp.MustCompile(`(?m)^\s*export\s*\*`)

func loaderFor(rel string) api.Loader {
	switch path.Ext(rel) {
	case ".tsx":
		return api.LoaderTSX
	case ".ts":
		return api.LoaderTS
	case ".jsx":
		return api.LoaderJSX
	case ".json":
		return api.LoaderJSON
	}
	return api.LoaderJS
}

func compileSource(dir, rel string, src []byte) *compiled {
	m := &compiled{Resolved: map[string]string{}}
	var deps, lazy []string
	var mu sync.Mutex
	var resolveErrs []string
	res := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   string(src),
			ResolveDir: filepath.Join(dir, filepath.FromSlash(path.Dir(rel))),
			Sourcefile: path.Base(rel), // 相对 ResolveDir：报错、sourcemap、jsxDev 的 fileName 才是 <目录>/<文件>
			Loader:     loaderFor(rel),
		},
		Bundle:        true, // 只为了让插件看到每个 import；所有 import 都 external，产物就是这个文件自己
		Format:        api.FormatESModule,
		Platform:      api.PlatformBrowser,
		Target:        api.ES2020,
		JSX:           api.JSXAutomatic,
		JSXDev:        true, // 开发版 React（dev_import_map）：组件栈带文件名
		Sourcemap:     api.SourceMapExternal,
		Outfile:       filepath.Join(dir, "out.js"), // 不落盘（Write: false），只用来定 sourcemap 里 sources 的相对路径：<目录>/<文件>
		AbsWorkingDir: dir,
		Write:         false,
		Metafile:      true,
		LogLevel:      api.LogLevelSilent,
		Define: map[string]string{
			"process.env.NODE_ENV":    `"development"`,
			"process.env.RENDER_ENV":  `"production"`,
			"process.env.RENDER_MODE": `"production"`,
		},
		Plugins: []api.Plugin{{
			Name: "shuttle-module-imports",
			Setup: func(b api.PluginBuild) {
				b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					spec := args.Path
					local := strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".." || strings.HasPrefix(spec, "/")
					if !local {
						return api.OnResolveResult{Path: spec, External: true}, nil
					}
					target, err := resolveLocal(dir, rel, spec)
					if err != nil {
						mu.Lock()
						resolveErrs = append(resolveErrs, err.Error())
						mu.Unlock()
						return api.OnResolveResult{Path: spec, External: true}, nil
					}
					mu.Lock()
					if args.Kind == api.ResolveJSDynamicImport {
						lazy = append(lazy, target)
					} else {
						deps = append(deps, target)
					}
					m.Resolved[spec] = target
					mu.Unlock()
					return api.OnResolveResult{Path: depPlaceholder + target, External: true}, nil
				})
			},
		}},
	})
	if len(res.Errors) > 0 {
		var msgs []string
		for _, e := range res.Errors {
			msgs = append(msgs, formatMessage(e))
		}
		m.Error = strings.Join(msgs, "\n\n")
		return m
	}
	if len(resolveErrs) > 0 {
		m.Error = strings.Join(resolveErrs, "\n")
		return m
	}
	for _, f := range res.OutputFiles {
		switch {
		case strings.HasSuffix(f.Path, ".map"):
			m.Map = string(f.Contents)
		case strings.HasSuffix(f.Path, ".js"):
			m.Code = string(f.Contents)
		}
	}
	var meta struct {
		Outputs map[string]struct {
			Exports []string `json:"exports"`
		} `json:"outputs"`
	}
	if json.Unmarshal([]byte(res.Metafile), &meta) == nil {
		for _, o := range meta.Outputs {
			m.Exports = append(m.Exports, o.Exports...)
		}
	}
	m.Deps, m.Lazy = uniq(deps), uniq(lazy)
	m.ExportStar = exportStarRE.Match(src)
	return m
}

func uniq(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// resolveLocal 把 from 文件里的 import 路径解析成项目里的文件：补扩展名、目录的 index。
func resolveLocal(dir, from, spec string) (string, error) {
	if strings.Contains(spec, "?") {
		return "", i18n.Errorf("%s: 本地渲染不支持 %q 这种带 ? 的 import（?raw / ?url）", "%s: local rendering doesn't support imports with ?, like %q (?raw / ?url)", from, spec)
	}
	var p string
	if strings.HasPrefix(spec, "/") {
		p = path.Clean(strings.TrimPrefix(spec, "/"))
	} else {
		p = path.Clean(path.Join(path.Dir(from), spec))
	}
	if strings.HasPrefix(p, "../") || p == ".." {
		return "", i18n.Errorf("%s: import %q 指到了项目目录外面", "%s: import %q points outside the project", from, spec)
	}
	if strings.HasSuffix(strings.ToLower(p), ".css") {
		return "", i18n.Errorf("%s: 本地渲染不支持在代码里 import CSS（%q），样式写进 index.css", "%s: local rendering doesn't support importing CSS from code (%q); put styles in index.css", from, spec)
	}
	exists := func(q string) bool {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(q)))
		return err == nil && !fi.IsDir()
	}
	cands := []string{p}
	for _, ext := range moduleExts {
		cands = append(cands, p+ext)
	}
	for _, ext := range moduleExts {
		cands = append(cands, p+"/index"+ext)
	}
	for _, q := range cands {
		if exists(q) {
			for _, ext := range moduleExts {
				if strings.HasSuffix(q, ext) {
					return q, nil
				}
			}
			return "", i18n.Errorf("%s: import %q 不是代码文件", "%s: import %q is not a code file", from, spec)
		}
	}
	return "", i18n.Errorf("%s: 找不到 import 的文件 %q", "%s: can't find the imported file %q", from, spec)
}

// graph 是从页面出发能走到的全部本地模块（静态 import 和动态 import 都算）。
type graph struct {
	mods      map[string]*compiled // 路径 → 编译结果
	importers map[string][]string  // 路径 → import 了它的文件
	version   map[string]string    // 路径 → 版本（URL 里的 v）
	errs      []string             // 编译报错（带文件、行列）
}

func moduleURL(rel, version string) string { return "/_client/m/" + rel + "?v=" + version }

// buildGraph 从 roots（页面文件）出发编译整张图，并发编译、结果按文件缓存。
func (c *moduleCache) buildGraph(dir string, roots []string) *graph {
	g := &graph{mods: map[string]*compiled{}, importers: map[string][]string{}, version: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var visit func(rel string)
	visit = func(rel string) {
		mu.Lock()
		if _, ok := g.mods[rel]; ok {
			mu.Unlock()
			return
		}
		g.mods[rel] = nil // 占位，别的 goroutine 不再重复编
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			m, err := c.compileFile(dir, rel)
			<-sem
			if err != nil {
				m = &compiled{Error: fmt.Sprintf("%s: %v", rel, err)}
			}
			mu.Lock()
			g.mods[rel] = m
			if m.Error != "" {
				g.errs = append(g.errs, m.Error)
			}
			mu.Unlock()
			for _, d := range append(append([]string{}, m.Deps...), m.Lazy...) {
				visit(d)
			}
		}()
	}
	for _, r := range roots {
		visit(r)
	}
	wg.Wait()
	sort.Strings(g.errs)
	for p, m := range g.mods {
		for _, d := range append(append([]string{}, m.Deps...), m.Lazy...) {
			g.importers[d] = append(g.importers[d], p)
		}
	}
	for d := range g.importers {
		sort.Strings(g.importers[d])
	}
	g.computeVersions(c)
	return g
}

// computeVersions：版本 = 哈希（这个文件的内容 + 依赖的版本）。先求强连通分量（循环 import），同一组共用一个版本，
// 按依赖在前的顺序（Tarjan 出栈的顺序就是）算。
func (g *graph) computeVersions(c *moduleCache) {
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	i := 0
	paths := make([]string, 0, len(g.mods))
	for p := range g.mods {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	edges := func(p string) []string {
		m := g.mods[p]
		return append(append([]string{}, m.Deps...), m.Lazy...)
	}
	var strong func(v string)
	strong = func(v string) {
		index[v], low[v] = i, i
		i++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range edges(v) {
			if _, ok := index[w]; !ok {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var scc []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			scc = append(scc, w)
			if w == v {
				break
			}
		}
		sort.Strings(scc)
		in := map[string]bool{}
		for _, p := range scc {
			in[p] = true
		}
		h := sha256.New()
		var outside []string
		for _, p := range scc {
			m := g.mods[p]
			h.Write([]byte(p + "\x00" + m.Code + "\x00"))
			for _, d := range edges(p) {
				if !in[d] {
					outside = append(outside, d+"@"+g.version[d])
				}
			}
		}
		for _, o := range uniq(outside) {
			h.Write([]byte(o + "\n"))
		}
		ver := hex.EncodeToString(h.Sum(nil))[:12]
		for _, p := range scc {
			g.version[p] = ver
		}
	}
	for _, p := range paths {
		if _, ok := index[p]; !ok {
			strong(p)
		}
	}
}

// link 把占位符换成依赖的 URL，得到发给浏览器的代码。
func (g *graph) link(rel string) string {
	m := g.mods[rel]
	code := m.Code
	for _, d := range append(append([]string{}, m.Deps...), m.Lazy...) {
		code = strings.ReplaceAll(code, `"`+depPlaceholder+d+`"`, `"`+moduleURL(d, g.version[d])+`"`)
	}
	return code + "\n//# sourceMappingURL=" + path.Base(rel) + ".map?v=" + g.version[rel] + "\n"
}

// boundary：这个模块能不能自己热替换（React Refresh）——.tsx/.jsx，导出的全是组件（default 或大写开头）。
// 这样的模块改了只要重新 import 它、再 performReactRefresh；不是边界就往上找 import 了它的文件。
func (m *compiled) boundary(rel string) bool {
	if ext := path.Ext(rel); ext != ".tsx" && ext != ".jsx" {
		return false
	}
	if m.ExportStar || len(m.Exports) == 0 {
		return false
	}
	for _, e := range m.Exports {
		if e != "default" && (e == "" || e[0] < 'A' || e[0] > 'Z') {
			return false
		}
	}
	return true
}
