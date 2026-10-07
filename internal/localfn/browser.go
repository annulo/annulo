package localfn

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/dop251/goja"
)

// ctx.browser：用本机 Chrome 自动操作网页（社媒发布、采集这类没有开放接口的平台）。
//
//	ctx.browser.profile('xiaohongshu')  // → { id } 本机这个 profile 的 id；没有返回 null
//	const b = await ctx.browser.open({ profile: 'xiaohongshu', url: 'https://…', show: false })
//	await b.click({ text: '发布' })
//	await b.close()
//
// 具体实现在 internal/browser（chromedp），这里只做 JS 绑定。函数结束时（正常、抛错、停止、超时）
// 这次打开的浏览器都会被关掉，临时文件（下载的图片、截图）也会删掉。

// BrowserOptions 是 ctx.browser.open 的参数。
type BrowserOptions struct {
	Profile   string // 一个账号一个：~/.shuttle/browser/<profile>
	URL       string
	Show      bool // 弹出可见窗口（扫码登录）
	Offscreen bool // 有界面但放在屏幕外（headless 被网站识别时用）
	KeepOpen  bool // 任务结束归还操作权，保留可见窗口
}

// BrowserHost 由 Shuttle 注入（出网规则、profile 目录都在它那里）。
type BrowserHost interface {
	Open(ctx context.Context, o BrowserOptions) (BrowserPage, error)
	// ProfileID 是本机这个 profile 的 id（存在 profile 目录里）；本机没有这个 profile 返回 ""。
	ProfileID(profile string) (string, error)
}

// BrowserPage 是打开的一个浏览器。timeout 为 0 用默认值（30 秒）。
type BrowserPage interface {
	Goto(ctx context.Context, url, wait string, timeout time.Duration) error
	WaitFor(ctx context.Context, sel string, visible bool, timeout time.Duration) error
	Exists(ctx context.Context, sel string) (bool, error)
	Click(ctx context.Context, sel, text string, timeout time.Duration) error
	Type(ctx context.Context, sel, text string, clear bool, timeout time.Duration) error
	Press(ctx context.Context, key string) error
	Upload(ctx context.Context, sel string, files []string, timeout time.Duration) error
	Eval(ctx context.Context, expr string) (any, error)
	Text(ctx context.Context, sel string) (string, error)
	HTML(ctx context.Context, sel string) (string, error)
	URL(ctx context.Context) (string, error)
	Listen(pattern string)
	Responses(ctx context.Context, pattern string, min int, timeout time.Duration) ([]map[string]any, error)
	Screenshot(ctx context.Context, sel string, full bool) ([]byte, error)
	SetContent(ctx context.Context, html string) error
	Close() error
}

// cleanup 关掉这次运行打开的浏览器、删掉临时文件。
func (r *runner) cleanup() {
	for _, b := range r.bodies {
		b.finish()
	}
	r.bodies = nil
	for _, p := range r.pages {
		p.Close()
	}
	r.pages = nil
	if r.tmp != "" {
		os.RemoveAll(r.tmp)
		r.tmp = ""
	}
}

// tempFile 在这次运行的临时目录里建一个文件名。
func (r *runner) tempFile(prefix, ext string) (string, string) {
	if r.tmp == "" {
		d, err := os.MkdirTemp("", "shuttle-run-*")
		if err != nil {
			r.throw(err)
		}
		r.tmp = d
	}
	r.nfile++
	name := fmt.Sprintf("%s-%d%s", prefix, r.nfile, ext)
	return name, filepath.Join(r.tmp, name)
}

// opts 取 JS 传进来的选项对象。
func opts(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}
	}
	m, _ := toPlain(v.Export()).(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func optStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func optBool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func optMs(m map[string]any, k string) time.Duration {
	switch v := m[k].(type) {
	case int64:
		return time.Duration(v) * time.Millisecond
	case float64:
		return time.Duration(v) * time.Millisecond
	}
	return 0
}

func optInt(m map[string]any, k string, def int) int {
	switch v := m[k].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

func (r *runner) browserObject() *goja.Object {
	o := r.vm.NewObject()
	o.Set("open", func(v goja.Value) *goja.Promise {
		if r.h.Browser == nil {
			r.throw(i18n.New("这里不能用 ctx.browser", "ctx.browser isn't available here"))
		}
		m := opts(v)
		keepOpen := optBool(m, "keep_open")
		if keepOpen && !optBool(m, "show") {
			return r.rejected(i18n.New("keep_open 只用于可见的浏览器窗口（show: true）", "keep_open requires a visible browser window (show: true)"))
		}
		page, err := r.h.Browser.Open(r.ctx, BrowserOptions{Profile: optStr(m, "profile"), URL: optStr(m, "url"), Show: optBool(m, "show"), Offscreen: optBool(m, "offscreen"), KeepOpen: keepOpen})
		if err != nil {
			return r.rejected(err)
		}
		r.pages = append(r.pages, page)
		return r.resolved(r.pageObject(page, optStr(m, "profile")))
	})
	// profile(name)：本机的这个 profile（登录态所在的浏览器）→ { id }，没有返回 null。
	// id 跟着 profile 目录走，用来认「账号的登录态是不是在这个浏览器里」，比认电脑可靠。
	o.Set("profile", func(name string) goja.Value {
		if r.h.Browser == nil {
			r.throw(i18n.New("这里不能用 ctx.browser", "ctx.browser isn't available here"))
		}
		id, err := r.h.Browser.ProfileID(name)
		if err != nil {
			r.throw(err)
		}
		if id == "" {
			return goja.Null()
		}
		return r.vm.ToValue(map[string]any{"id": id})
	})
	return o
}

// selectorOrText：click 的第一个参数可以是选择器，也可以是 { text: '发布' }。
func selectorOrText(v goja.Value) (sel, text string) {
	if o, ok := v.(*goja.Object); ok && o.ClassName() == "Object" {
		if t := o.Get("text"); t != nil && !goja.IsUndefined(t) {
			return "", t.String()
		}
		if s := o.Get("selector"); s != nil && !goja.IsUndefined(s) {
			return s.String(), ""
		}
	}
	return v.String(), ""
}

func (r *runner) pageObject(p BrowserPage, profile string) *goja.Object {
	o := r.vm.NewObject()
	done := func(err error) *goja.Promise {
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(goja.Undefined())
	}
	// step 是会因为平台改版失败的操作（打开、等、点、输入、上传、等接口）：失败时存现场（snapshot.go）
	step := func(op, target string, err error) *goja.Promise {
		if err != nil {
			return r.failed(p, profile, op, target, err)
		}
		return r.resolved(goja.Undefined())
	}
	value := func(v any, err error) *goja.Promise {
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(v)
	}
	o.Set("goto", func(u string, v goja.Value) *goja.Promise {
		m := opts(v)
		return step("goto", u, p.Goto(r.ctx, u, optStr(m, "wait"), optMs(m, "timeout")))
	})
	o.Set("waitFor", func(sel string, v goja.Value) *goja.Promise {
		m := opts(v)
		visible := true
		if b, ok := m["visible"].(bool); ok {
			visible = b
		}
		return step("waitFor", sel, p.WaitFor(r.ctx, sel, visible, optMs(m, "timeout")))
	})
	o.Set("exists", func(sel string) *goja.Promise { return value(p.Exists(r.ctx, sel)) })
	o.Set("click", func(target goja.Value, v goja.Value) *goja.Promise {
		sel, text := selectorOrText(target)
		desc := sel
		if text != "" {
			desc = "text=" + text
		}
		return step("click", desc, p.Click(r.ctx, sel, text, optMs(opts(v), "timeout")))
	})
	o.Set("type", func(sel, text string, v goja.Value) *goja.Promise {
		m := opts(v)
		return step("type", sel, p.Type(r.ctx, sel, text, optBool(m, "clear"), optMs(m, "timeout")))
	})
	o.Set("press", func(key string) *goja.Promise { return done(p.Press(r.ctx, key)) })
	o.Set("upload", func(sel string, files goja.Value, v goja.Value) (res *goja.Promise) {
		// 文件参数不对时 uploadFiles 会 throw：也转成失败的 Promise
		defer func() {
			if x := recover(); x != nil {
				o, ok := x.(*goja.Object)
				if !ok {
					panic(x)
				}
				pr, _, reject := r.vm.NewPromise()
				reject(o)
				res = pr
			}
		}()
		return step("upload", sel, p.Upload(r.ctx, sel, r.uploadFiles(files), optMs(opts(v), "timeout")))
	})
	o.Set("eval", func(code goja.Value) *goja.Promise {
		expr := code.String()
		if _, ok := goja.AssertFunction(code); ok {
			expr = "(" + balanceParens(expr) + ")()" // 传函数：在页面里调用它（函数不能引用本机函数里的变量）
		}
		v, err := p.Eval(r.ctx, expr)
		return value(v, err)
	})
	o.Set("text", func(sel goja.Value) *goja.Promise { return value(p.Text(r.ctx, strArg(sel))) })
	o.Set("html", func(sel goja.Value) *goja.Promise { return value(p.HTML(r.ctx, strArg(sel))) })
	o.Set("url", func() string {
		u, err := p.URL(r.ctx)
		if err != nil {
			r.throw(err)
		}
		return u
	})
	o.Set("listen", func(pattern string) { p.Listen(pattern) })
	o.Set("responses", func(pattern string, v goja.Value) *goja.Promise {
		m := opts(v)
		res, err := p.Responses(r.ctx, pattern, optInt(m, "min", 1), optMs(m, "timeout"))
		if err != nil {
			return r.failed(p, profile, "responses", pattern, err)
		}
		return r.resolved(res)
	})
	// snapshot：主动存一份现场（自检每一步都存，失败时给助手看），返回 { dir }
	o.Set("snapshot", func(v goja.Value) *goja.Promise {
		m := opts(v)
		dir := r.snapshot(p, profile, "snapshot", optStr(m, "label"), nil)
		if dir == "" {
			return r.rejected(i18n.New("存不了现场", "Couldn't save a snapshot"))
		}
		return r.resolved(map[string]any{"dir": dir})
	})
	o.Set("screenshot", func(v goja.Value) *goja.Promise {
		m := opts(v)
		png, err := p.Screenshot(r.ctx, optStr(m, "selector"), optBool(m, "fullPage"))
		if err != nil {
			return r.rejected(err)
		}
		name, full := r.tempFile("shot", ".png")
		if err := os.WriteFile(full, png, 0o600); err != nil {
			return r.rejected(err)
		}
		return r.resolved(map[string]any{"file": name, "bytes": len(png)})
	})
	o.Set("setContent", func(html string) *goja.Promise { return done(p.SetContent(r.ctx, html)) })
	closed := false
	o.Set("close", func() *goja.Promise { closed = true; return done(p.Close()) })
	// 归还操作权后的旧句柄不能再去操作另一个任务接管的窗口。
	for _, name := range o.Keys() {
		if name == "close" {
			continue
		}
		fn, ok := goja.AssertFunction(o.Get(name))
		if !ok {
			continue
		}
		o.Set(name, func(call goja.FunctionCall) goja.Value {
			if closed {
				err := i18n.New("浏览器操作已结束，请重新打开后再操作", "This browser session has ended. Open it again before operating.")
				if name == "url" || name == "listen" {
					r.throw(err)
				}
				return r.vm.ToValue(r.rejected(err))
			}
			value, err := fn(call.This, call.Arguments...)
			if err != nil {
				r.throw(err)
			}
			return value
		})
	}
	return o
}

func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func strArg(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "video/mp4": ".mp4", "video/quicktime": ".mov", "video/webm": ".webm", "video/x-msvideo": ".avi", "video/x-matroska": ".mkv"}

// fileExt：按 content-type，认不出再看地址的扩展名，都没有就 .bin
func fileExt(contentType, url string) string {
	ct, _, _ := mime.ParseMediaType(contentType)
	ext := imageExt[ct]
	if ext == "" {
		ext = strings.ToLower(path.Ext(strings.SplitN(url, "?", 2)[0]))
	}
	if ext == "" || len(ext) > 6 {
		ext = ".bin"
	}
	return ext
}

// uploadFiles 把 upload 的参数换成本机文件路径：URL 按 fetch 的出网规则下载到临时目录，
// { file } 是 screenshot 返回的句柄。
func (r *runner) uploadFiles(v goja.Value) []string {
	var items []any
	switch x := toPlain(v.Export()).(type) {
	case []any:
		items = x
	default:
		items = []any{x}
	}
	if len(items) == 0 {
		r.throw(i18n.New("upload 要给出至少一个文件（图片地址，或 screenshot 返回的 { file }）", "upload needs at least one file (an image URL, or the { file } returned by screenshot)"))
	}
	var out []string
	for _, it := range items {
		switch x := it.(type) {
		case map[string]any:
			name, _ := x["file"].(string)
			full := filepath.Join(r.tmp, filepath.Base(name))
			if name == "" || r.tmp == "" {
				r.throw(i18n.Errorf("upload 的文件不对：%v（要是图片地址，或 screenshot 返回的 { file }）", "Invalid upload file: %v (use an image URL, or the { file } returned by screenshot)", x))
			}
			if _, err := os.Stat(full); err != nil {
				r.throw(i18n.Errorf("没有这个截图文件：%s", "No such screenshot file: %s", name))
			}
			out = append(out, full)
		case string:
			if name, ok := strings.CutPrefix(x, "local:"); ok {
				// 页面存在本机的文件（视频直接从本机选，不传云端）
				full := filepath.Join(r.h.FilesDir, filepath.Base(name))
				if r.h.FilesDir == "" || name == "" || filepath.Base(name) != name {
					r.throw(i18n.Errorf("upload 的本机文件不对：%q", "Invalid local file for upload: %q", x))
				}
				if _, err := os.Stat(full); err != nil {
					r.throw(i18n.Errorf("本机没有这个文件了（可能超过 90 天被清理了，重新选一次）：%s", "This local file is gone (it may have been cleaned up after 90 days; pick it again): %s", name))
				}
				out = append(out, full)
				continue
			}
			// 离线项目上传的文件地址是 /_annulo/uploaded/…（不带主机，交给 Download / Fetch 直接读本机文件）
			uploaded := strings.HasPrefix(x, "/_annulo/uploaded/") || strings.HasPrefix(x, "/_shuttle/uploaded/")
			if !uploaded && !strings.HasPrefix(x, "http://") && !strings.HasPrefix(x, "https://") {
				r.throw(i18n.Errorf("upload 只能用图片 / 视频地址（http / https，或本机上传的 /_annulo/uploaded/…）、本机文件（local:…）或 screenshot 返回的 { file }：%q", "upload only accepts image / video URLs (http / https, or /_annulo/uploaded/… uploaded on this computer), local files (local:…) or the { file } returned by screenshot: %q", x))
			}
			if r.h.Download != nil {
				// 流式下载到临时文件（视频可能几百 MB）；先存成 .bin，拿到 content-type 再改成对的扩展名（网站按扩展名认格式）
				_, tmp := r.tempFile("file", ".bin")
				ct, err := r.h.Download(r.ctx, x, tmp)
				if err != nil {
					os.Remove(tmp)
					r.throw(i18n.Errorf("下载 %s 失败：%w", "Failed to download %s: %w", x, err))
				}
				full := strings.TrimSuffix(tmp, ".bin") + fileExt(ct, x)
				if full != tmp {
					if err := os.Rename(tmp, full); err != nil {
						r.throw(err)
					}
				}
				out = append(out, full)
				continue
			}
			resp, err := r.h.Fetch(r.ctx, FetchRequest{URL: x, Method: "GET", Headers: map[string]string{}})
			if err != nil {
				r.throw(i18n.Errorf("下载 %s 失败：%w", "Failed to download %s: %w", x, err))
			}
			if resp.Status < 200 || resp.Status >= 300 || resp.Truncated {
				r.throw(i18n.Errorf("下载 %s 失败：%d", "Failed to download %s: %d", x, resp.Status))
			}
			_, full := r.tempFile("file", fileExt(header(resp.Headers, "content-type"), x))
			if err := os.WriteFile(full, resp.Body, 0o600); err != nil {
				r.throw(err)
			}
			out = append(out, full)
		default:
			r.throw(i18n.Errorf("upload 的文件不对：%v", "Invalid upload file: %v", x))
		}
	}
	return out
}

// balanceParens 补齐函数源码末尾缺的右括号。goja（v0.0.0-20260917113740）的 Function.prototype.toString
// 对「函数体用括号包起来的箭头函数」会漏掉最后的「)」：() => ({ a: 1 }) 变成 "() => ({ a: 1 }"，
// () => (1 + 2) 也一样；普通函数和 { } 函数体的箭头函数没问题。执行不受影响，只有把源码拿去页面里跑时才是语法错误。
// 数括号时跳过字符串、模板字符串和注释。
func balanceParens(src string) string {
	depth := 0
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			for i++; i < len(src) && src[i] != c; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
			} else if i+1 < len(src) && src[i+1] == '*' {
				if end := strings.Index(src[i+2:], "*/"); end >= 0 {
					i += end + 3
				} else {
					i = len(src)
				}
			}
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	if depth > 0 {
		return src + strings.Repeat(")", depth)
	}
	return src
}
