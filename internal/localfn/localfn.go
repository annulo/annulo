// Package localfn 是本机函数：运营后台项目里 local/*.ts 的函数，由 Shuttle 在用户电脑上直接执行，不经过模型。
//
// 为什么要它：GEO 检测、调外部 API 这类确定性的活，每次都让 agent 跑一整轮（几十次模型请求、几十万 token）
// 又慢又贵，结果也不稳定。agent 写一次函数、试跑通过；之后页面按钮直接调它，几秒跑完。
//
// 形状和 creght 的云端 Func 一致（export function name(input, ctx)，ctx.db 同步），agent 不用再学一套：
//
//	export async function check(input, ctx) {
//	  const qs = ctx.db.query('geo_questions', { where: { channel_id: input.channel_id } }).list
//	  const r = await fetch('https://api.perplexity.ai/chat/completions', { method: 'POST', headers: {...}, body })
//	  ctx.progress({ done: 1, total: qs.length, message: '...' })
//	  return { ok: true }
//	}
//
// 运行：esbuild 把 TS 打包成 CommonJS（纯 Go，不依赖 node），goja 执行（纯 Go 的 JS 引擎）。
// 函数能做的事只有 ctx 给的这些（读写运营后台的表、出网请求、本机密钥、一次模型调用、MCP 工具、本机浏览器、进度），
// 不能读写本机文件、不能执行命令：它比 agent 的 bash 小得多，也好审。
package localfn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/net/html"
)

// Dir 是工作区里放本机函数的目录。
const Dir = "local"

// DB 是函数能用的业务表操作，以 creght 站点 Func 的 ctx.db 为准（同一份函数要能打包成站点 Func 在云端跑，docs/mobile-remote.md）。
type DB interface {
	// Query 查一页；q.Cursor 不为 nil 时按 id 翻页，next 是下一页的游标（读完了是空）；total 是满足条件的总行数
	Query(ctx context.Context, table string, q Query) (list []map[string]any, next string, total int64, err error)
	// Aggregate 是 ctx.db.aggregate：按字段分组算 count / sum / avg / min / max / first / last，在平台上算，不把行拉回来。
	// req 是 { where, filter, group_by, metrics, order_by, limit, timezone }（filter 已换成 Cond）
	Aggregate(ctx context.Context, table string, req map[string]any, filter []Cond) (list []map[string]any, truncated bool, err error)
	Get(ctx context.Context, table, id string) (map[string]any, error) // 不存在返回 nil, nil
	Insert(ctx context.Context, table string, data map[string]any) (map[string]any, error)
	Update(ctx context.Context, table, id string, data map[string]any) (map[string]any, error) // 顶层浅合并，值为 null 删字段
	Delete(ctx context.Context, table, id string) error
}

// Query 是 ctx.db.query 的条件：Where 字段相等，Filter 是其余条件（AND），OrderBy 如 "date desc"，Limit 1~1000，Offset 跳过前几行。
// Cursor 不为 nil 时按 id 顺序翻页：第一页传 ""，之后传上一页返回的 next（这时 OrderBy 固定是 "id asc"）。
type Query struct {
	Where   map[string]any
	Filter  []Cond
	OrderBy string
	Limit   int
	Offset  int
	Cursor  *string
}

// Cond 是一个过滤条件：op 是 eq / neq / in / gt / gte / lt / lte / between（value 是 [起, 止]，两端都含）。
// 数字按数值比，字符串按字典序比（YYYY-MM-DD、ISO 时间可以直接比大小）；类型不同的值不参与比较。
type Cond struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// FetchRequest / FetchResponse 是函数里 fetch 的一次请求。出网规则（不许访问内网）由 Host 决定。
type FetchRequest struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string
	// Stream：收到响应头就返回，响应体放在 FetchResponse.Stream 里由函数边读边用（fetch 用它）。
	// 这时超时按「多久没收到数据」算，不限总时长，SSE 这种一两分钟才说完的接口也读得完
	Stream bool
}

type FetchResponse struct {
	Status     int
	StatusText string
	URL        string
	Headers    map[string]string
	Body       []byte
	TTFBMs     int64 // 到第一个字节的时间
	TotalMs    int64 // 到读完的时间
	Truncated  bool  // 响应太大或没读完（超时）
	// Stream 不为空时响应体在这里（请求带了 Stream），Body、TotalMs、Truncated 不填；读完或运行结束时关掉
	Stream io.ReadCloser
}

// Host 是 Shuttle 给函数提供的能力。
type Host struct {
	LogDir    string                                                            // 持久运行日志目录；不记录 input 或返回正文
	RunID     string                                                            // 可由页面运行管理器指定，否则自动生成
	WorkDir   string                                                            // 运营后台的本地副本，函数在 WorkDir/local 下
	Workspace map[string]any                                                    // ctx.workspace：project_id / site_id / api_host / shuttle_projects / machine
	DB        DB                                                                // ctx.db
	Secret    func(name string) (string, bool)                                  // ctx.secrets.get
	Fetch     func(ctx context.Context, r FetchRequest) (*FetchResponse, error) // fetch / ctx.fetch
	// Download 把一个网址流式存进本机文件（b.upload 传网址时用：视频动辄几百 MB，不能像 fetch 那样读进内存），
	// 返回 content-type。出网规则和 fetch 一样。nil 时 upload 退回用 Fetch（只能传 10MB 以内）
	Download func(ctx context.Context, url, dst string) (contentType string, err error)
	LLM      func(ctx context.Context, system, prompt string) (string, error) // ctx.llm
	// LLMProviders / LLMFetch 是 ctx.llm.providers() / ctx.llm.fetch(服务商, 路径, init)：用用户在 设置 → 模型 里配的服务商，
	// 列表不含 key；fetch 和 fetch 一样（流式），r.URL 是接在服务商地址后面的路径，地址和鉴权由 Host 补
	LLMProviders func(ctx context.Context) []map[string]any
	LLMFetch     func(ctx context.Context, provider string, r FetchRequest) (*FetchResponse, error)
	// MCP 是 ctx.mcp(server, tool, args)：调用户在 Shuttle 里连上的 MCP 工具（和 agent 同一个连接、同一套开关）。
	// 集成（creght、WordPress…）的数据读写走它，Shuttle 不给具体平台写死接口。
	MCP func(ctx context.Context, server, tool string, args map[string]any) (any, error)
	// OAuth 是 ctx.oauth(provider, { account })：用户在 设置 → 连接 里授权过的账号（google…）的 access token，过期自动刷新。
	// 一种连接可以连多个账号，account 空是最早连的那个；OAuthAccounts 是 ctx.oauth.accounts(provider)，连上的账号名。
	// 调哪个接口、怎么读数据写在本机函数里，Shuttle 只管授权和 token。
	OAuth         func(ctx context.Context, provider, account string) (string, error)
	OAuthAccounts func(provider string) ([]string, error)
	// Browser 是 ctx.browser：用本机 Chrome 自动操作网页（见 browser.go）。
	Browser BrowserHost
	// SnapshotDir 存浏览器操作失败时的现场（截图、可操作元素、源码，见 snapshot.go）；空就不存
	SnapshotDir string
	// FilesDir 是页面存在本机的文件（POST local/files）：b.upload 传 'local:<文件名>' 时从这里取
	FilesDir string
}

// Event 是运行过程中推给调用方的：progress / log。
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Fn 是一个可调用的函数：文件名.导出名（geo.check）。
type Fn struct {
	Name string `json:"name"`
	File string `json:"file"`
}

var fileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// source 找函数所在的文件：local/<file>.ts（也认 .js）。
func source(workDir, file string) (string, error) {
	if !fileRe.MatchString(file) {
		return "", i18n.Errorf("函数名不对：%q（格式是 文件名.函数名，比如 geo.check）", "Invalid function name: %q (the format is file.function, e.g. geo.check)", file)
	}
	for _, ext := range []string{".ts", ".js"} {
		p := filepath.Join(workDir, Dir, file+ext)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", i18n.Errorf("没有 %s/%s.ts", "%s/%s.ts doesn't exist", Dir, file)
}

// compile 用 esbuild 打包成一段 CommonJS。相对 import 会一起打进来；talizen 的类型导入会被去掉。
func compile(path string) (string, error) {
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{path},
		Bundle:      true,
		Write:       false,
		Format:      api.FormatCommonJS,
		Platform:    api.PlatformNeutral,
		Target:      api.ES2017, // goja 支持 async/await；更新的语法（?. ?? 等）由 esbuild 降级
		External:    []string{"talizen", "talizen/*"},
		LogLevel:    api.LogLevelSilent,
		Sourcemap:   api.SourceMapNone,
	})
	if len(res.Errors) > 0 {
		var msgs []string
		for _, e := range res.Errors {
			loc := ""
			if e.Location != nil {
				loc = fmt.Sprintf("%s:%d:%d ", filepath.Base(e.Location.File), e.Location.Line, e.Location.Column)
			}
			msgs = append(msgs, loc+e.Text)
		}
		return "", i18n.Errorf("编译失败：%s", "Compile failed: %s", strings.Join(msgs, i18n.T("；", "; ")))
	}
	if len(res.OutputFiles) == 0 {
		return "", i18n.New("编译没有产物", "Compilation produced no output")
	}
	return string(res.OutputFiles[0].Contents), nil
}

// List 列出 local/ 下所有文件导出的函数。
func List(workDir string) ([]Fn, error) {
	ents, err := os.ReadDir(filepath.Join(workDir, Dir))
	if os.IsNotExist(err) {
		return []Fn{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Fn{}
	for _, e := range ents {
		name := e.Name()
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if e.IsDir() || (ext != ".ts" && ext != ".js") || !fileRe.MatchString(base) || strings.HasSuffix(base, ".d") {
			continue
		}
		code, err := compile(filepath.Join(workDir, Dir, name))
		if err != nil {
			continue
		}
		vm := goja.New()
		exports, err := load(vm, code)
		if err != nil {
			continue
		}
		for _, k := range exports.Keys() {
			if _, ok := goja.AssertFunction(exports.Get(k)); ok {
				out = append(out, Fn{Name: base + "." + k, File: Dir + "/" + name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// load 执行打包好的 CommonJS，返回 module.exports。
func load(vm *goja.Runtime, code string) (*goja.Object, error) {
	module := vm.NewObject()
	exports := vm.NewObject()
	module.Set("exports", exports)
	vm.Set("module", module)
	vm.Set("exports", exports)
	vm.Set("require", func(name string) goja.Value {
		panic(vm.NewTypeError(i18n.T("本机函数里不能 require(%q)：只能用相对路径 import 同目录的代码，能力都在 ctx 上", "Local functions can't require(%q): only import code from the same folder by relative path; capabilities are on ctx"), name))
	})
	if _, err := vm.RunScript("local.js", code); err != nil {
		return nil, jsError(err)
	}
	return module.Get("exports").ToObject(vm), nil
}

// Run 执行一个函数（name 形如 geo.check），input 是 JSON 值。返回函数的返回值（已转成普通 Go 值）。
func Run(ctx context.Context, h Host, name string, input any, emit func(Event)) (result any, runErr error) {
	emit, finishLog := runLogger(h, name, emit)
	defer func() { finishLog(runErr) }()
	file, fn, ok := strings.Cut(name, ".")
	if !ok || fn == "" {
		return nil, i18n.Errorf("函数名不对：%q（格式是 文件名.函数名，比如 geo.check）", "Invalid function name: %q (the format is file.function, e.g. geo.check)", name)
	}
	path, err := source(h.WorkDir, file)
	if err != nil {
		return nil, err
	}
	code, err := compile(path)
	if err != nil {
		return nil, err
	}

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	// 停止、超时：打断正在跑的 JS（阻塞在 fetch 里的也会因为 ctx 取消而返回）
	stop := context.AfterFunc(ctx, func() { vm.Interrupt(i18n.T("已停止", "Stopped")) })
	defer stop()

	r := &runner{ctx: ctx, vm: vm, h: h, emit: emit}
	defer r.cleanup() // 关掉这次打开的浏览器、删掉临时文件
	r.installGlobals()
	exports, err := load(vm, code)
	if err != nil {
		return nil, err
	}
	f, ok := goja.AssertFunction(exports.Get(fn))
	if !ok {
		return nil, i18n.Errorf("%s 没有导出函数 %s", "%s doesn't export a function named %s", filepath.Base(path), fn)
	}
	res, err := f(goja.Undefined(), vm.ToValue(input), r.ctxObject())
	if err != nil {
		return nil, jsError(err)
	}
	// async 函数返回 Promise。宿主能力都是同步完成的，调用返回时微任务已经跑完，Promise 应该已经有结果
	if p, ok := res.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			res = p.Result()
		case goja.PromiseStateRejected:
			return nil, jsError(nil, p.Result())
		default:
			return nil, i18n.New("函数返回的 Promise 一直没有完成（本机函数里没有 setTimeout，等待请用 await ctx.sleep(ms)）", "The function's Promise never settled (there's no setTimeout in local functions; to wait, use await ctx.sleep(ms))")
		}
	}
	return toPlain(res.Export()), nil
}

type runner struct {
	ctx  context.Context
	vm   *goja.Runtime
	h    Host
	emit func(Event)

	pages []BrowserPage // 这次运行打开的浏览器，结束时关掉
	tmp   string        // 临时目录：upload 下载的图片、截图
	nfile int
	nsnap int // 这次运行存了几份现场

	bodies []*fetchBody // fetch 的响应体，结束时没读完的关掉
}

// throw 把 Go 错误变成 JS 异常，函数里可以 try/catch。
func (r *runner) throw(err error) {
	panic(r.vm.NewGoError(err))
}

// rejected 是失败的 Promise。返回 Promise 的宿主能力出错时用它，不要同步 throw：
// 同步抛出时 ctx.mcp(…).catch(…) 这种写法接不住（.catch 还没挂上去异常就出来了），和 JS 里的 async 函数行为不一样。
func (r *runner) rejected(err error) *goja.Promise {
	p, _, reject := r.vm.NewPromise()
	reject(r.vm.NewGoError(err))
	return p
}

func (r *runner) resolved(v any) *goja.Promise {
	p, resolve, _ := r.vm.NewPromise()
	resolve(v)
	return p
}

func (r *runner) log(level string) func(args ...goja.Value) {
	return func(args ...goja.Value) {
		parts := make([]string, len(args))
		for i, a := range args {
			if o, ok := a.(*goja.Object); ok && o.ClassName() != "Error" {
				b, _ := json.Marshal(toPlain(o.Export()))
				parts[i] = string(b)
			} else {
				parts[i] = a.String()
			}
		}
		r.emit(Event{Type: "log", Data: map[string]any{"level": level, "message": strings.Join(parts, " ")}})
	}
}

func (r *runner) installGlobals() {
	console := r.vm.NewObject()
	console.Set("log", r.log("info"))
	console.Set("info", r.log("info"))
	console.Set("warn", r.log("warn"))
	console.Set("error", r.log("error"))
	r.vm.Set("console", console)
	r.vm.Set("fetch", r.fetch)
	r.installText()
	// 浏览器里常用、goja 没有的：URL（最常用的字段）、btoa / atob
	r.vm.Set("__parseURL", func(raw, base string) map[string]any {
		u, err := url.Parse(raw)
		if err == nil && base != "" {
			var b *url.URL
			if b, err = url.Parse(base); err == nil {
				u = b.ResolveReference(u)
			}
		}
		if err != nil || u.Scheme == "" || u.Host == "" {
			panic(r.vm.NewTypeError("Invalid URL: %s", raw))
		}
		q := map[string]any{}
		for k, v := range u.Query() {
			q[k] = v[0]
		}
		search := ""
		if u.RawQuery != "" {
			search = "?" + u.RawQuery
		}
		hash := ""
		if u.Fragment != "" {
			hash = "#" + u.Fragment
		}
		return map[string]any{"href": u.String(), "protocol": u.Scheme + ":", "host": strings.ToLower(u.Host), "hostname": strings.ToLower(u.Hostname()), "port": u.Port(),
			"pathname": u.EscapedPath(), "search": search, "hash": hash, "origin": u.Scheme + "://" + u.Host, "query": q}
	})
	r.vm.Set("btoa", func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) })
	r.vm.Set("atob", func(s string) string {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			panic(r.vm.NewTypeError("atob: %v", err))
		}
		return string(b)
	})
	if _, err := r.vm.RunString(urlPolyfill); err != nil {
		panic(err)
	}
}

const urlPolyfill = `
class URL {
  constructor(u, base) {
    const p = __parseURL(String(u), base === undefined ? '' : String(base))
    Object.assign(this, p)
    const q = p.query
    delete this.query
    this.searchParams = { get: (k) => (k in q ? q[k] : null), has: (k) => k in q }
  }
  toString() { return this.href }
  toJSON() { return this.href }
}
globalThis.URL = URL
`

func (r *runner) ctxObject() *goja.Object {
	c := r.vm.NewObject()
	db := r.vm.NewObject()
	db.Set("query", r.dbQuery)
	db.Set("aggregate", r.dbAggregate)
	db.Set("get", func(table, id string) any {
		m, err := r.h.DB.Get(r.ctx, table, id)
		if err != nil {
			r.throw(err)
		}
		if m == nil {
			return nil
		}
		return m
	})
	db.Set("insert", func(table string, data map[string]any) any {
		m, err := r.h.DB.Insert(r.ctx, table, data)
		if err != nil {
			r.throw(err)
		}
		return m
	})
	db.Set("update", func(table, id string, data map[string]any) any {
		if _, err := r.h.DB.Update(r.ctx, table, id, data); err != nil {
			r.throw(err)
		}
		return map[string]any{"ok": true} // 和站点 Func 一样，不返回更新后的行
	})
	db.Set("delete", func(table, id string) {
		if err := r.h.DB.Delete(r.ctx, table, id); err != nil {
			r.throw(err)
		}
	})
	c.Set("db", db)

	secrets := r.vm.NewObject()
	secrets.Set("get", func(name string) any {
		if v, ok := r.h.Secret(name); ok && v != "" {
			return v
		}
		return nil
	})
	c.Set("secrets", secrets)

	// 出网请求用全局 fetch，和站点 Func 一样（平台的 ctx 上没有 fetch）；ctx.fetch 留一句提示给旧写法
	c.Set("fetch", func() {
		r.throw(i18n.New("ctx.fetch 已去掉，直接用全局 fetch(url, init)，和站点 Func 一样", "ctx.fetch is gone: use the global fetch(url, init), same as site Funcs"))
	})
	c.Set("fetchAll", r.fetchAll)
	c.Set("html", r.html)
	// await ctx.mcp('server', 'tool', { …参数 })：返回工具的结构化结果（文本是 JSON 就解析好），工具报错就抛出
	c.Set("mcp", func(server, tool string, args goja.Value) *goja.Promise {
		if r.h.MCP == nil {
			r.throw(i18n.New("这里不能用 ctx.mcp", "ctx.mcp isn't available here"))
		}
		var a map[string]any
		if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
			m, ok := toPlain(args.Export()).(map[string]any)
			if !ok {
				r.throw(i18n.New("ctx.mcp 的第三个参数要是对象：ctx.mcp('server', 'tool', { … })", "The third argument of ctx.mcp must be an object: ctx.mcp('server', 'tool', { … })"))
			}
			a = m
		}
		res, err := r.h.MCP(r.ctx, server, tool, a)
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(res)
	})
	// await ctx.oauth('google', { account })：返回 access token，没连接就抛出（message 里说去哪连）；account 不给是最早连的账号。
	// ctx.oauth.accounts('google')：这台电脑连着的账号名（邮箱），最早连的在前
	oauth := r.vm.ToValue(func(provider string, opts map[string]any) *goja.Promise {
		if r.h.OAuth == nil {
			r.throw(i18n.New("这里不能用 ctx.oauth", "ctx.oauth isn't available here"))
		}
		account, _ := opts["account"].(string)
		tok, err := r.h.OAuth(r.ctx, provider, account)
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(tok)
	}).(*goja.Object)
	oauth.Set("accounts", func(provider string) []string {
		if r.h.OAuthAccounts == nil {
			r.throw(i18n.New("这里不能用 ctx.oauth", "ctx.oauth isn't available here"))
		}
		list, err := r.h.OAuthAccounts(provider)
		if err != nil {
			r.throw(err)
		}
		return list
	})
	c.Set("oauth", oauth)
	c.Set("browser", r.browserObject())
	c.Set("workspace", r.h.Workspace)
	// ctx.locale：界面语言 zh / en（设置里的语言，跟随系统时按系统语言），给用户看的报错和说明按它出两种语言
	c.Set("locale", i18n.Locale())
	c.Set("progress", func(v goja.Value) {
		r.emit(Event{Type: "progress", Data: toPlain(v.Export())})
	})
	c.Set("log", r.log("info"))
	c.Set("sleep", func(ms int64) *goja.Promise {
		select {
		case <-time.After(time.Duration(min(max(ms, 0), 60_000)) * time.Millisecond):
		case <-r.ctx.Done():
			r.throw(r.ctx.Err())
		}
		return r.resolved(goja.Undefined())
	})
	// ctx.llm('问题') 或 ctx.llm({ system, prompt })：用当前模型答一次，不带工具、不进对话历史
	llm := r.vm.ToValue(func(v goja.Value) *goja.Promise {
		system, prompt := "", ""
		if o, ok := v.(*goja.Object); ok && o.ClassName() == "Object" {
			if s := o.Get("system"); s != nil && !goja.IsUndefined(s) {
				system = s.String()
			}
			if p := o.Get("prompt"); p != nil && !goja.IsUndefined(p) {
				prompt = p.String()
			}
		} else {
			prompt = v.String()
		}
		text, err := r.h.LLM(r.ctx, system, prompt)
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(text)
	}).(*goja.Object)
	// ctx.llm.providers()：设置 → 模型 里的服务商和它们的模型 [{ id, name, api, base_url, builtin, enabled, ready, error, models: [{ id, model, name, api }] }]
	llm.Set("providers", func() any {
		if r.h.LLMProviders == nil {
			return []any{}
		}
		return r.h.LLMProviders(r.ctx)
	})
	// await ctx.llm.fetch(服务商 id, '/responses', { method, headers, body })：和 fetch 一样返回 Response（能流式读），
	// Shuttle 补服务商的地址和 key。要模型做联网搜索这类 ctx.llm 不管的事时用它
	llm.Set("fetch", func(provider, path string, init map[string]any) *goja.Promise {
		if r.h.LLMFetch == nil {
			return r.rejected(i18n.New("这里不能用 ctx.llm.fetch", "ctx.llm.fetch isn't available here"))
		}
		req := r.request(path, init)
		req.Stream = true
		start := time.Now()
		resp, err := r.h.LLMFetch(r.ctx, provider, req)
		if err != nil {
			return r.rejected(i18n.Errorf("ctx.llm.fetch %s %s 失败：%w", "ctx.llm.fetch %s %s failed: %w", provider, path, err))
		}
		return r.resolved(r.response(resp, start))
	})
	c.Set("llm", llm)
	return c
}

// dbQuery：ctx.db.query(table, { where, filter, order_by, limit, offset, cursor })，返回 { total, list, limit, next_cursor }。
// 和站点 Func 一样：where 是字段相等；filter 是 { match, conditions: [{ fieldId, operator, value }] }（也认 [{ field, op, value }]，见 ParseConds），
// 过滤和排序都在平台上做；limit 默认 20、最多 1000；total 是满足条件的总行数，不是这一页的行数。
// 翻页用 offset，或者 cursor：第一页传 cursor: ”，之后传上一页的 next_cursor，它为空就读完了（按 id 顺序，不能再指定 order_by，也不能和 offset 一起用）。
func (r *runner) dbQuery(table string, q map[string]any) any {
	where, _ := q["where"].(map[string]any)
	limit := min(max(intOf(q["limit"], 20), 1), 1000)
	order, _ := q["order_by"].(string)
	query := Query{Where: where, Filter: r.conds(q["filter"]), OrderBy: order, Limit: limit, Offset: max(intOf(q["offset"], 0), 0)}
	if c, ok := q["cursor"]; ok && c != nil {
		s, _ := c.(string)
		if order != "" && order != "id asc" {
			r.throw(i18n.Errorf("用 cursor 翻页时按 id 顺序读，不能同时指定 order_by（%q）；要排序就用 offset 翻页，或者用 ctx.db.aggregate", "Paging with cursor reads in id order, so order_by (%q) can't be set at the same time; page with offset if you need sorting, or use ctx.db.aggregate", order))
		}
		if query.Offset > 0 {
			r.throw(i18n.New("cursor 和 offset 不能一起用：按 id 读完全表用 cursor，按别的顺序翻页用 offset", "cursor and offset can't be used together: use cursor to read the whole table in id order, offset to page in another order"))
		}
		query.Cursor, query.OrderBy = &s, "id asc"
	}
	list, next, total, err := r.h.DB.Query(r.ctx, table, query)
	if err != nil {
		r.throw(err)
	}
	if list == nil {
		list = []map[string]any{}
	}
	if total < 0 {
		total = int64(query.Offset + len(list))
	}
	return map[string]any{"total": total, "list": list, "limit": limit, "next_cursor": next}
}

// dbAggregate：ctx.db.aggregate(table, { where, filter, group_by, metrics, order_by, limit, timezone })，返回 { list, truncated }。
//
//	group_by: ['post_id', { field: 'date', trunc: 'day' | 'week' | 'month' | 'year', as: 'day' }]
//	metrics:  [{ op: 'sum' | 'avg' | 'min' | 'max' | 'count' | 'first' | 'last', field: 'views', as: 'views', order_by: 'date' }]
//
// 在平台上算，行再多也不用拉回来；结果最多 10000 行（truncated 为 true 表示可能没取全）。
func (r *runner) dbAggregate(table string, req map[string]any) any {
	filter := r.conds(req["filter"])
	body := map[string]any{}
	for k, v := range req {
		if k != "filter" {
			body[k] = v
		}
	}
	list, truncated, err := r.h.DB.Aggregate(r.ctx, table, body, filter)
	if err != nil {
		r.throw(err)
	}
	if list == nil {
		list = []map[string]any{}
	}
	return map[string]any{"list": list, "truncated": truncated}
}

// condOps 是站点 Func 认的运算符写法，都换成 Cond 的标准写法
var condOps = map[string]string{
	"eq": "eq", "": "eq", "=": "eq", "equal": "eq",
	"neq": "neq", "!=": "neq", "not_equal": "neq", "notEqual": "neq",
	"in": "in", "between": "between",
	"gt": "gt", ">": "gt", "gte": "gte", ">=": "gte", "lt": "lt", "<": "lt", "lte": "lte", "<=": "lte",
}

// conds 把 [{ field, op, value }] 转成 Cond，写错了直接抛出（见 ParseConds）
func (r *runner) conds(v any) []Cond {
	out, err := ParseConds(v)
	if err != nil {
		r.throw(err)
	}
	return out
}

// ParseConds 把 filter 转成 Cond。两种写法都认：
//
//	站点 Func 的 { match: 'and', conditions: [{ fieldId, operator, value }] }（operator 也认 = != > >= < <= 这类写法）
//	简写 [{ field, op, value }]
//
// 条件之间都是 AND。op 不认识、缺字段、多了不认识的键都报错：写错了别悄悄变成没过滤。
// 本机函数的 ctx.db 和 agent 的 db_query / db_aggregate 工具共用它。
func ParseConds(v any) ([]Cond, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if f, isObj := v.(map[string]any); isObj {
		match, _ := f["match"].(string)
		bad := match != "" && !strings.EqualFold(match, "and") && !strings.EqualFold(match, "all")
		for k := range f {
			bad = bad || (k != "match" && k != "conditions")
		}
		items, ok = f["conditions"].([]any)
		if bad || !ok {
			return nil, i18n.Errorf("filter 要写成 { match: 'and', conditions: [{ fieldId, operator, value }, …] }（条件之间只能是 AND）：%v", "filter must be { match: 'and', conditions: [{ fieldId, operator, value }, …] } (conditions are always ANDed): %v", v)
		}
	}
	if !ok {
		return nil, i18n.Errorf("filter 要写成 { conditions: [{ fieldId, operator, value }, …] } 或 [{ field, op, value }, …]：%v", "filter must be { conditions: [{ fieldId, operator, value }, …] } or [{ field, op, value }, …]: %v", v)
	}
	var out []Cond
	for _, it := range items {
		m, _ := it.(map[string]any)
		bad := m == nil
		for k := range m {
			bad = bad || (k != "field" && k != "op" && k != "fieldId" && k != "operator" && k != "value")
		}
		field, _ := m["field"].(string)
		if f, ok := m["fieldId"].(string); ok {
			bad = bad || field != ""
			field = f
		}
		opName, hasOp := m["op"].(string)
		if o, ok := m["operator"].(string); ok {
			bad = bad || hasOp
			opName = o
		}
		op, known := condOps[opName] // 不写 operator 是 eq，和站点 Func 一样
		if bad || field == "" || !known {
			return nil, i18n.Errorf("filter 的每一项要写成 { fieldId, operator, value }（或 { field, op, value }），operator 是 eq / neq / in / gt / gte / lt / lte / between：%v", "Each filter item must be { fieldId, operator, value } (or { field, op, value }), where operator is eq / neq / in / gt / gte / lt / lte / between: %v", it)
		}
		out = append(out, Cond{Field: strings.TrimPrefix(field, "body."), Op: op, Value: m["value"]}) // 和 creght CLI 的 order_by 写法一样带 body. 也认
	}
	return out, nil
}

func intOf(v any, def int) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return def
}

// fetchAll：await ctx.fetchAll([url 或 { url, method, headers, body }, …]) 并发请求（最多 4 个同时），
// 返回和输入一一对应的 Response；某个失败的那一项是 { ok: false, error }，不影响其他的。
// 抓一批页面时用它，一个个 await fetch 太慢。
func (r *runner) fetchAll(items []any) *goja.Promise {
	reqs := make([]FetchRequest, len(items))
	for i, it := range items {
		switch v := it.(type) {
		case string:
			reqs[i] = r.request(v, nil)
		case map[string]any:
			u, _ := v["url"].(string)
			reqs[i] = r.request(u, v)
		default:
			reqs[i] = r.request(fmt.Sprint(v), nil)
		}
	}
	type res struct {
		resp *FetchResponse
		err  error
	}
	out := make([]res, len(reqs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, q := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i].resp, out[i].err = r.h.Fetch(r.ctx, q)
		}()
	}
	wg.Wait()
	// goja 的对象只能在执行 JS 的这个 goroutine 里建
	arr := make([]any, len(out))
	for i, o := range out {
		if o.err != nil {
			arr[i] = map[string]any{"ok": false, "status": 0, "url": reqs[i].URL, "error": o.err.Error()}
		} else {
			arr[i] = r.response(o.resp, time.Now())
		}
	}
	return r.resolved(arr)
}

// html：ctx.html(text) 解析 HTML，.find(css 选择器) 返回 [{ tag, text, attrs }]（text 已合并空白），
// .text() 是 body 里的可见文字（去掉 script / style / noscript / svg），.markdown() 转成 Markdown。
func (r *runner) html(text string) *goja.Object {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(text))
	if err != nil {
		r.throw(err)
	}
	o := r.vm.NewObject()
	o.Set("find", func(sel string) []any {
		// goquery 遇到写错的选择器会静默地什么都匹配不到，先校验，写错了直接报出来
		if _, err := cascadia.ParseGroup(sel); err != nil {
			r.throw(i18n.Errorf("选择器不对：%q（%v）", "Invalid selector: %q (%v)", sel, err))
		}
		var out []any
		func() {
			doc.Find(sel).Each(func(_ int, s *goquery.Selection) {
				attrs := map[string]any{}
				if n := s.Get(0); n != nil {
					for _, a := range n.Attr {
						attrs[a.Key] = a.Val
					}
				}
				tag := ""
				if n := s.Get(0); n != nil {
					tag = n.Data
				}
				out = append(out, map[string]any{"tag": tag, "text": visibleText(s), "attrs": attrs})
			})
		}()
		if out == nil {
			out = []any{}
		}
		return out
	})
	o.Set("text", func() string { return visibleText(doc.Find("body")) })
	// .markdown()：转成 Markdown（发到只收 Markdown 的 CMS 字段时用）
	o.Set("markdown", func() string {
		md, err := htmltomarkdown.ConvertString(text)
		if err != nil {
			r.throw(i18n.Errorf("转 Markdown 失败：%w", "Failed to convert to Markdown: %w", err))
		}
		return strings.TrimSpace(md)
	})
	return o
}

// visibleText 是元素里的文字：跳过 script / style / noscript / svg / template，
// 各段文字之间用空格隔开（goquery 的 Text() 会把相邻段落直接粘在一起）。
func visibleText(s *goquery.Selection) string {
	var parts []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			if t := strings.Join(strings.Fields(n.Data), " "); t != "" {
				parts = append(parts, t)
			}
			return
		case n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript" || n.Data == "svg" || n.Data == "template"):
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range s.Nodes {
		walk(n)
	}
	return strings.Join(parts, " ")
}

func (r *runner) request(url string, init map[string]any) FetchRequest {
	req := FetchRequest{URL: url, Method: "GET", Headers: map[string]string{}}
	if m, ok := init["method"].(string); ok && m != "" {
		req.Method = strings.ToUpper(m)
	}
	if hs, ok := init["headers"].(map[string]any); ok {
		for k, v := range hs {
			req.Headers[k] = fmt.Sprint(v)
		}
	}
	switch b := init["body"].(type) {
	case string:
		req.Body = b
	case nil:
	default:
		j, _ := json.Marshal(toPlain(b))
		req.Body = string(j)
	}
	return req
}

// fetch 是浏览器风格的 fetch：await fetch(url, { method, headers, body }) → Response。
// 请求是同步发出的（函数里一次一个），收到响应头就返回已完成的 Promise，await 照常写；
// 响应体可以 res.body.getReader() 边读边用，也可以 text() / json() 一次读完（见 fetchbody.go）。
// Response 多了 timing { ttfb_ms, total_ms } 和 truncated，测速用。
func (r *runner) fetch(url string, init map[string]any) *goja.Promise {
	req := r.request(url, init)
	req.Stream = true
	start := time.Now()
	resp, err := r.h.Fetch(r.ctx, req)
	if err != nil {
		return r.rejected(i18n.Errorf("fetch %s 失败：%w", "fetch %s failed: %w", url, err))
	}
	return r.resolved(r.response(resp, start))
}

// Error 是函数抛出的异常：Message 给界面看，Stack 给命令行和日志（定位是哪一行）。
type Error struct {
	Message string
	Stack   string
}

func (e *Error) Error() string { return e.Message }

// jsError 把 JS 异常转成可读的错误：message 和栈分开。
func jsError(err error, vals ...goja.Value) error {
	var v goja.Value
	if len(vals) > 0 {
		v = vals[0]
	} else if ex, ok := err.(*goja.Exception); ok {
		v = ex.Value()
		if s := ex.String(); s != "" && v == nil {
			return errors.New(s)
		}
	} else if ie, ok := err.(*goja.InterruptedError); ok {
		return fmt.Errorf("%v", ie.Value())
	} else {
		return err
	}
	if o, ok := v.(*goja.Object); ok {
		e := &Error{}
		if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
			e.Message = m.String()
		}
		if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
			e.Stack = st.String()
		}
		if e.Message == "" {
			e.Message = v.String()
		}
		return e
	}
	if v != nil {
		return errors.New(v.String())
	}
	return err
}

// toPlain 把 goja 导出的值转成能 JSON 序列化的普通值。
func toPlain(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = toPlain(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = toPlain(vv)
		}
		return out
	case *goja.Promise:
		return nil
	default:
		if x != nil && reflect.TypeOf(x).Kind() == reflect.Func {
			return nil
		}
		return x
	}
}
