package localfn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/browser"
)

type testBrowser struct{ m *browser.Manager }

func (b testBrowser) Open(_ context.Context, o BrowserOptions) (BrowserPage, error) {
	p, err := b.m.Open(browser.Options{Profile: o.Profile, URL: o.URL, Show: o.Show, Offscreen: o.Offscreen})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (b testBrowser) ProfileID(profile string) (string, error) { return b.m.ProfileID(profile) }

const uploadPage = `<!doctype html><body>
<textarea id="t"></textarea><div class="go"><b>发布</b></div><div id="ok"></div>
<input id="f" type="file" multiple style="display:none"><div id="files"></div>
<script>
document.querySelector('.go').onclick = () => fetch('/api/publish', { method: 'POST' }).then(r => r.json()).then(j => { document.getElementById('ok').innerText = j.id })
document.getElementById('f').onchange = e => { document.getElementById('files').innerText = [...e.target.files].map(f => f.name.split('.').pop() + ':' + f.size).join(',') }
</script></body>`

func TestBrowser(t *testing.T) {
	if browser.FindChrome() == "" {
		t.Skip("本机没有 Chrome / Edge")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/publish":
			w.Header().Set("content-type", "application/json")
			w.Write([]byte(`{"id":"note-1"}`))
		case "/cover.jpg":
			w.Header().Set("content-type", "image/jpeg")
			w.Write([]byte("jpegbytes"))
		default:
			w.Write([]byte(uploadPage))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	db := &memDB{rows: map[string][]map[string]any{}}
	h := host(dir, db)
	h.Fetch = func(ctx context.Context, r FetchRequest) (*FetchResponse, error) {
		resp, err := http.Get(r.URL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return &FetchResponse{Status: resp.StatusCode, Headers: map[string]string{"Content-Type": resp.Header.Get("content-type")}, Body: b}, nil
	}
	m := browser.NewManager(t.TempDir(), nil)
	h.Browser = testBrowser{m}

	write(t, dir, "post.ts", `
export async function publish(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  await b.type('#t', '标题\n正文')
  b.listen('/api/publish')
  await b.click({ text: '发布' })
  const [r] = await b.responses('/api/publish')
  // 出错是 rejected 的 Promise：.catch 接得住（不是同步抛出）
  const missed = await b.responses('/api/nothing', { min: 1, timeout: 300 }).catch((e: any) => 'caught: ' + e.message)
  const badUpload = await b.upload('#f', [123]).catch((e: any) => 'caught: ' + e.message)
  await b.waitFor('#ok')
  const value = await b.eval(() => document.querySelector('#t').value)
  // 截图生成的卡片和网上的图片一起上传
  await b.setContent('<div id="c" style="width:200px;height:100px;background:#fde">卡片</div>')
  const card = await b.screenshot({ selector: '#c' })
  await b.goto(input.url)
  await b.upload('#f', [input.url + 'cover.jpg', card])
  const files = await b.text('#files')
  return { id: r.json.id, files, value, url: b.url(), missed, badUpload }
}
export async function crash(input: { url: string }, ctx: any) {
  await ctx.browser.open({ profile: 'demo', url: input.url })
  throw new Error('脚本出错了')
}
`)
	res, err := Run(context.Background(), h, "post.publish", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]any)
	if out["id"] != "note-1" || out["value"] != "标题\n正文" || !strings.HasPrefix(out["url"].(string), srv.URL) {
		t.Fatalf("结果：%v", out)
	}
	if m, _ := out["missed"].(string); !strings.HasPrefix(m, "caught: ") {
		t.Fatalf(".catch 没接住 responses 的错误：%v", out["missed"])
	}
	if m, _ := out["badUpload"].(string); !strings.HasPrefix(m, "caught: upload 的文件不对") {
		t.Fatalf(".catch 没接住 upload 的错误：%v", out["badUpload"])
	}
	if f := out["files"].(string); !strings.HasPrefix(f, "jpg:9,png:") {
		t.Fatalf("上传的文件：%q", f)
	}

	// 抛错时浏览器也被关掉：同一个 profile 马上能再用（没关的话会报「正在被另一个任务使用」）
	if _, err := Run(context.Background(), h, "post.crash", map[string]any{"url": srv.URL + "/"}, func(Event) {}); err == nil || err.Error() != "脚本出错了" {
		t.Fatalf("crash 应该报「脚本出错了」：%v", err)
	}
	p, err := m.Open(browser.Options{Profile: "demo"})
	if err != nil {
		t.Fatalf("函数结束后 profile 应该已经释放：%v", err)
	}
	p.Close()

	// 没注入浏览器的地方用 ctx.browser 报能看懂的错
	h.Browser = nil
	if _, err := Run(context.Background(), h, "post.crash", map[string]any{"url": srv.URL + "/"}, func(Event) {}); err == nil || !strings.Contains(err.Error(), "不能用 ctx.browser") {
		t.Fatalf("没有浏览器时：%v", err)
	}
}

func TestBalanceParens(t *testing.T) {
	for in, want := range map[string]string{
		"() => ({\n  a: [1].map((e) => { return e })\n}": "() => ({\n  a: [1].map((e) => { return e })\n})",
		"() => document.title":                           "() => document.title",
		"function () { return ')' + \"(\" /* ( */ }":     "function () { return ')' + \"(\" /* ( */ }",
		"() => ({ s: `(${1})` // (\n}":                   "() => ({ s: `(${1})` // (\n})",
	} {
		if got := balanceParens(in); got != want {
			t.Errorf("balanceParens(%q) = %q，应该是 %q", in, got, want)
		}
	}
}

func TestBrowserSnapshot(t *testing.T) {
	if browser.FindChrome() == "" {
		t.Skip("本机没有 Chrome / Edge")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><title>demo</title><body><button aria-label="Post" data-view-name="share-post">发布</button></body>`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.SnapshotDir = t.TempDir()
	write(t, dir, "snap.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  const e = await b.click('#nothing', { timeout: 300 }).then(() => null, (e: any) => e)
  const own = await b.snapshot({ label: 'compose' })
  return { message: e?.message, snapshot: e?.snapshot, own: own.dir }
}`)
	out, err := Run(context.Background(), h, "snap.run", map[string]any{"url": srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	snap, _ := m["snapshot"].(string)
	if snap == "" || !strings.Contains(m["message"].(string), snap) {
		t.Fatalf("报错要带现场目录：%v", m)
	}
	outline, err := os.ReadFile(filepath.Join(snap, "outline.txt"))
	if err != nil || !strings.Contains(string(outline), `data-view-name="share-post"`) || !strings.Contains(string(outline), `aria-label="Post"`) {
		t.Fatalf("outline 要列出按钮和它的属性：%s %v", outline, err)
	}
	for _, f := range []string{"shot.png", "page.html", "meta.json"} {
		if _, err := os.Stat(filepath.Join(snap, f)); err != nil {
			t.Fatalf("现场少了 %s", f)
		}
	}
	if own, _ := m["own"].(string); own == "" || own == snap {
		t.Fatalf("b.snapshot 要另存一份：%v", m)
	}
}

func TestBrowserUploadLocalFile(t *testing.T) {
	if browser.FindChrome() == "" {
		t.Skip("本机没有 Chrome / Edge")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(uploadPage)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.FilesDir = t.TempDir()
	os.WriteFile(filepath.Join(h.FilesDir, "0123456789abcdef0123456789abcdef.mp4"), []byte("videobytes"), 0o600)
	write(t, dir, "up.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  await b.upload('#f', ['local:0123456789abcdef0123456789abcdef.mp4'])
  await b.waitFor('#files', { visible: false })
  const gone = await b.upload('#f', ['local:ffffffffffffffffffffffffffffffff.mp4']).catch((e: any) => 'caught: ' + e.message)
  const sneaky = await b.upload('#f', ['local:../secret']).catch((e: any) => 'caught: ' + e.message)
  return { files: await b.text('#files'), gone, sneaky }
}`)
	out, err := Run(context.Background(), h, "up.run", map[string]any{"url": srv.URL}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["files"] != "mp4:10" {
		t.Fatalf("本机文件要选进 file input：%v", m)
	}
	if !strings.HasPrefix(m["gone"].(string), "caught:") || !strings.HasPrefix(m["sneaky"].(string), "caught:") {
		t.Fatalf("不存在的、带路径的本机文件要报错：%v", m)
	}
}

type retainedBrowser struct {
	m    *browser.Manager
	page *browser.Page
}

func (b *retainedBrowser) ProfileID(profile string) (string, error) { return b.m.ProfileID(profile) }

func (b *retainedBrowser) Open(_ context.Context, o BrowserOptions) (BrowserPage, error) {
	p, release, err := b.m.Acquire(browser.Options{Profile: o.Profile, URL: o.URL, Show: o.Show, KeepOpen: o.KeepOpen})
	if err != nil {
		return nil, err
	}
	b.page = p
	return &retainedLease{Page: p, release: release}, nil
}

type retainedLease struct {
	*browser.Page
	release func()
}

func (p *retainedLease) Close() error { p.release(); return nil }

func TestVisibleBrowserKeptOpen(t *testing.T) {
	if browser.FindChrome() == "" {
		t.Skip("No Chrome")
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("没有显示器，开不了可见窗口")
	}
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	b := &retainedBrowser{m: browser.NewManager(t.TempDir(), nil)}
	h.Browser = b
	write(t, dir, "inspect.ts", `export async function open(input:any,ctx:any){await ctx.browser.open({profile:'inspect',show:input.show,keep_open:true});return {opened:true}}
export async function take(input:any,ctx:any){const b=await ctx.browser.open({profile:'inspect'});await b.eval(()=>document.body.dataset.assistant='operated');return {done:true}}
export async function crash(input:any,ctx:any){await ctx.browser.open({profile:'inspect'});throw new Error('expected failure')}
export async function stale(input:any,ctx:any){const b=await ctx.browser.open({profile:'inspect'});await b.close();return await b.eval(()=>42).catch((e:any)=>e.message)}`)

	if _, err := Run(context.Background(), h, "inspect.open", map[string]any{"show": false}, func(Event) {}); err == nil {
		t.Fatal("keep_open should reject hidden windows")
	}
	if _, err := Run(context.Background(), h, "inspect.open", map[string]any{"show": true}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	defer b.page.Close()
	if _, err := b.page.URL(context.Background()); err != nil {
		t.Fatalf("visible window must survive function cleanup: %v", err)
	}
	if p, err := b.m.Open(browser.Options{Profile: "inspect"}); err == nil {
		p.Close()
		t.Fatal("profile should remain locked while user window is open")
	}
	before := b.page
	if _, err := Run(context.Background(), h, "inspect.take", map[string]any{}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if b.page != before {
		t.Fatal("assistant must reuse exact same window")
	}
	value, err := before.Eval(context.Background(), "document.body.dataset.assistant")
	if err != nil || value != "operated" {
		t.Fatalf("assistant did not operate the preserved window: %v %v", value, err)
	}
	if _, err := Run(context.Background(), h, "inspect.crash", map[string]any{}, func(Event) {}); err == nil {
		t.Fatal("expected script error")
	}
	if _, err := before.URL(context.Background()); err != nil {
		t.Fatalf("script failure closed user's window: %v", err)
	}
	stale, err := Run(context.Background(), h, "inspect.stale", map[string]any{}, func(Event) {})
	if err != nil || !strings.Contains(stale.(string), "操作已结束") {
		t.Fatalf("stale handle should not control the returned window: %v %v", stale, err)
	}
	_, release, err := b.m.Acquire(browser.Options{Profile: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if _, next, err := b.m.Acquire(browser.Options{Profile: "inspect"}); err == nil {
		next()
		t.Fatal("two tasks must not control one window concurrently")
	}
	release()
	b.page.Close()
	p, err := b.m.Open(browser.Options{Profile: "inspect"})
	if err != nil {
		t.Fatalf("closing user window should release profile: %v", err)
	}
	p.Close()
}
