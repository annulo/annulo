package localfn

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/browser"
)

// 发帖页：先有个推广弹窗挡着，关掉后点「继续」才出现说明框；「分享」是不许点的
const actTestPage = `<!doctype html><body>
<div id="promo" role="dialog"><p>试试新功能</p><button onclick="this.parentNode.remove()">以后再说</button></div>
<button onclick="document.getElementById('share').dataset.clicked = '1'" id="share">分享</button>
<button onclick="if (!document.getElementById('promo')) document.getElementById('cap').style.display = 'block'">继续</button>
<div id="cap" contenteditable="true" aria-label="添加配文..." style="display:none;min-height:20px"></div>
</body>`

func TestBrowserAct(t *testing.T) {
	needChrome(t)
	next := "继续" // 平台改版时把「继续」改名「下一步」
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Replace(actTestPage, ">继续<", ">"+next+"<", 1)))
	}))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.ActCache = filepath.Join(t.TempDir(), "browser-act.json")
	// 假的模型：有「以后再说」先点它，否则点「继续」或「下一步」；第一次先回一句不是 JSON 的话，看能不能重来
	calls := 0
	idOf := func(prompt, text string) string {
		m := regexp.MustCompile(`\[(\d+)\] [^\n]*text="` + text + `"`).FindStringSubmatch(prompt)
		if m == nil {
			return ""
		}
		return m[1]
	}
	h.BrowserLLM = func(_ context.Context, system, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return "let me think", nil
		}
		if !strings.Contains(prompt, "Forbidden (never click): 分享") {
			t.Errorf("prompt 里要写不许点的按钮：%s", prompt)
		}
		// 推广弹窗开着时只列弹窗里的元素：「继续」「分享」在弹窗外面，不该出现
		if strings.Contains(prompt, `text="以后再说"`) && (strings.Contains(prompt, `text="继续"`) || !strings.Contains(prompt, "A dialog is open")) {
			t.Errorf("弹窗开着时清单里只该有弹窗里的元素：%s", prompt)
		}
		if strings.Contains(prompt, "#cap") {
			t.Errorf("完成条件的选择器不该发给模型：%s", prompt)
		}
		for _, text := range []string{"以后再说", "继续", "下一步"} {
			if id := idOf(prompt, text); id != "" {
				return fmt.Sprintf(`好的 {"action":"click","id":%s}`, id), nil
			}
		}
		return `{"action":"fail","reason":"没有按钮"}`, nil
	}
	write(t, dir, "post.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  const r = await b.act('点「继续」，进到写说明的页面', { until: '#cap[contenteditable]:not([style*="none"])', avoid: ['分享', 'Share'] })
  const shared = await b.eval(() => document.getElementById('share').dataset.clicked || '')
  return { ...r, shared }
}
`)
	run := func() map[string]any {
		t.Helper()
		res, err := Run(context.Background(), h, "post.run", map[string]any{"url": srv.URL + "/"}, func(Event) {})
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)
	}
	// 第一次：模型关弹窗、点「继续」，「分享」一次都没点；记下这两步
	if out := run(); out["ok"] != true || out["cached"] != false || out["shared"] != "" || calls != 3 {
		t.Fatalf("第一次：%v，问了模型 %d 次", out, calls)
	}
	// 第二次：整串重放（关弹窗、点继续），不问模型
	before := calls
	if out := run(); out["cached"] != true || calls != before || len(out["actions"].([]any)) != 2 {
		t.Fatalf("第二次应该整串重放：%v，又问了模型 %d 次", out, calls-before)
	}
	// 平台改版：「继续」改叫「下一步」。重放第二步找不到 → 删掉旧的，模型从当前页面（弹窗已经关了）接着点「下一步」，记下新的整串
	next = "下一步"
	before = calls
	if out := run(); out["ok"] != true || out["cached"] != false || calls != before+1 {
		t.Fatalf("改版后：%v，问了模型 %d 次", out, calls-before)
	}
	b, _ := os.ReadFile(h.ActCache)
	if !strings.Contains(string(b), "以后再说") || !strings.Contains(string(b), "下一步") || strings.Contains(string(b), `"继续"`) {
		t.Fatalf("记下的应该是新的整串（以后再说 → 下一步）：%s", b)
	}
	// 再跑一次：用新记下的整串，不问模型
	before = calls
	if out := run(); out["cached"] != true || calls != before {
		t.Fatalf("改版后再跑应该重放新的整串：%v，问了模型 %d 次", out, calls-before)
	}
}

// 验证码：模型交给人，浏览器调到前台；人处理完（这里页面 5 秒后自己去掉验证）自动接着走。后台无界面的调不出来，报错说清楚
const actCaptchaPage = `<!doctype html><body>
<div id="cap-box"><p>请完成人机验证</p><button>我不是机器人</button></div>
<button id="go" style="display:none" onclick="document.getElementById('cap').style.display='block'">继续</button>
<div id="cap" contenteditable="true" style="display:none;min-height:20px"></div>
<script>setTimeout(() => { document.getElementById('cap-box').remove(); document.getElementById('go').style.display = 'block' }, 5000)</script>
</body>`

func TestBrowserActHandoff(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(actCaptchaPage)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	m := browser.NewManager(t.TempDir(), nil)
	m.Quiet = true // 测「交给人」：窗口不真的调到前台，不在屏幕上弹出来
	h.Browser = testBrowser{m}
	asked := 0
	h.BrowserLLM = func(_ context.Context, system, prompt string) (string, error) {
		if strings.Contains(prompt, "我不是机器人") {
			asked++
			return `{"action":"ask_user","reason":"要完成人机验证"}`, nil
		}
		m := regexp.MustCompile(`\[(\d+)\] [^\n]*text="继续"`).FindStringSubmatch(prompt)
		if m == nil {
			return `{"action":"wait","ms":1000}`, nil
		}
		return `{"action":"click","id":` + m[1] + `}`, nil
	}
	write(t, dir, "post.ts", `
export async function run(input: { url: string, offscreen: boolean }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url, offscreen: input.offscreen })
  return await b.act('点「继续」，出现输入框', { until: '#cap:not([style*="none"])' })
}
`)
	var msgs []string
	onEvent := func(e Event) {
		if m, ok := e.Data.(map[string]any); ok {
			if s, ok := m["message"].(string); ok {
				msgs = append(msgs, s)
			}
		}
	}
	// 后台无界面：调不出来，报错里写清楚要人处理什么
	if _, err := Run(context.Background(), h, "post.run", map[string]any{"url": srv.URL + "/", "offscreen": false}, onEvent); err == nil || !strings.Contains(err.Error(), "要完成人机验证") {
		t.Fatalf("无界面时应该报要人处理：%v", err)
	}
	// 屏幕外的真窗口：调到前台，等「人」处理完（5 秒后验证没了）接着点「继续」
	msgs = nil
	res, err := Run(context.Background(), h, "post.run", map[string]any{"url": srv.URL + "/", "offscreen": true}, onEvent)
	if err != nil {
		t.Fatal(err)
	}
	if out := res.(map[string]any); out["ok"] != true || asked < 2 {
		t.Fatalf("结果：%v，交给人 %d 次", out, asked)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "需要你在浏览器里处理：要完成人机验证") {
		t.Fatalf("进度里要告诉人去处理：%v", msgs)
	}
}

// 焦点停在「分享」上时按 Enter 会替脚本点了它：要拒绝；往不是输入框的元素里输入也拒绝
func TestBrowserActGuardsKeys(t *testing.T) {
	needChrome(t)
	page := `<!doctype html><body>
<button id="share" autofocus onclick="this.dataset.clicked = '1'">分享</button>
<button onclick="document.getElementById('cap').style.display='block'">继续</button>
<div id="cap" contenteditable="true" style="display:none;min-height:20px"></div></body>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(page)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	step := 0
	h.BrowserLLM = func(_ context.Context, system, prompt string) (string, error) {
		step++
		switch step {
		case 1:
			return `{"action":"press","key":"Enter"}`, nil
		case 2:
			if !strings.Contains(prompt, "forbidden button") {
				t.Errorf("历史里要告诉模型按键被拒：%s", prompt)
			}
			id := regexp.MustCompile(`\[(\d+)\] [^\n]*text="继续"`).FindStringSubmatch(prompt)[1]
			return `{"action":"type","id":` + id + `,"text":"x"}`, nil
		default:
			id := regexp.MustCompile(`\[(\d+)\] [^\n]*text="继续"`).FindStringSubmatch(prompt)[1]
			return `{"action":"click","id":` + id + `}`, nil
		}
	}
	write(t, dir, "post.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  await b.eval(() => document.getElementById('share').focus())
  const r = await b.act('点「继续」', { until: '#cap:not([style*="none"])', avoid: ['分享'] })
  return { ...r, shared: await b.eval(() => document.getElementById('share').dataset.clicked || '') }
}
`)
	res, err := Run(context.Background(), h, "post.run", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if out := res.(map[string]any); out["ok"] != true || out["shared"] != "" || step != 3 {
		t.Fatalf("结果：%v，问了模型 %d 次", out, step)
	}
}

// 只有图标的按钮：说明在里面的 svg 上（aria-label），清单里要能看到它
func TestActIconOnlyLabel(t *testing.T) {
	needChrome(t)
	page := `<!doctype html><body><a href="#" role="link" onclick="document.getElementById('f').style.display='block';return false"><svg aria-label="新帖子" width="24" height="24"><rect width="24" height="24"/></svg></a><input id="f" type="file" style="display:none"></body>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(page)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.BrowserLLM = func(_ context.Context, system, prompt string) (string, error) {
		m := regexp.MustCompile(`\[(\d+)\] a [^\n]*label="新帖子"`).FindStringSubmatch(prompt)
		if m == nil {
			t.Fatalf("清单里没有只有图标的「新帖子」：%s", prompt)
		}
		return `{"action":"click","id":` + m[1] + `}`, nil
	}
	write(t, dir, "post.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  return await b.act('打开新帖子', { until: '#f:not([style*="none"])' })
}
`)
	// 调试开关打开：日志里有每一步发给模型的内容和回复
	h.BrowserDebug = func() bool { return true }
	var logs []string
	if _, err := Run(context.Background(), h, "post.run", map[string]any{"url": srv.URL + "/"}, func(e Event) {
		if m, ok := e.Data.(map[string]any); ok && e.Type == "log" {
			logs = append(logs, fmt.Sprint(m["message"]))
		}
	}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "--- 发给模型 ---") || !strings.Contains(all, "--- 模型回复 ---") || !strings.Contains(all, `label="新帖子"`) {
		t.Fatalf("调试日志里要有发给模型的内容和回复：%s", all)
	}
}

// 2026-10 小红书：封面缩略图是 div，上面盖着一层平时 display: none、鼠标移上去才显示的「编辑封面」；抖音的「选择封面」也是 div（鼠标是手形）。
// 它们不是按钮，以前清单里没有，模型只能说找不到入口
func TestActHoverAndPointerEntries(t *testing.T) {
	needChrome(t)
	page := `<!doctype html><style>.thumb{position:relative;width:200px;height:150px;background:#333}.op{position:absolute;inset:0;display:none}.thumb:hover .op{display:flex}.entry{cursor:pointer}</style><body>
<div class="thumb"><div class="op"><div class="entry" onclick="document.getElementById('editor').style.display='block'"><span>编辑封面</span></div></div></div>
<div style="cursor:pointer;width:80px;height:20px" onclick="this.dataset.clicked='1'"><span>选择封面</span></div>
<div style="cursor:pointer;width:400px;height:20px">这一整块是很长的一段说明文字，有好多好多字，不是一个入口，不应该被列出来的那种，再多写一些字让它超过四十个字</div>
<div id="editor" style="display:none">封面编辑</div></body>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(page)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.ActCache = filepath.Join(t.TempDir(), "browser-act.json")
	h.BrowserLLM = func(_ context.Context, system, prompt string) (string, error) {
		if !strings.Contains(prompt, `text="选择封面"`) {
			t.Errorf("手形的 div 入口要在清单里：%s", prompt)
		}
		if strings.Contains(prompt, "很长的一段说明") {
			t.Errorf("字很多的整块不该列出来：%s", prompt)
		}
		m := regexp.MustCompile(`\[(\d+)\] div [^\n]*text="编辑封面"[^\n]*shows-on-hover`).FindStringSubmatch(prompt)
		if m == nil {
			t.Fatalf("悬停才显示的「编辑封面」要在清单里，并标上 shows-on-hover：%s", prompt)
		}
		return `{"action":"click","id":` + m[1] + `}`, nil
	}
	write(t, dir, "cover.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  return await b.act('打开封面编辑', { until: '#editor:not([style*="none"])' })
}
`)
	if _, err := Run(context.Background(), h, "cover.run", map[string]any{"url": srv.URL + "/"}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
}
