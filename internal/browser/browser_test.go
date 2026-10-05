package browser

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// 测试用的页面：普通输入框、富文本编辑器、按文字点的按钮、会发 fetch 的按钮、文件上传。
const testPage = `<!doctype html><html><head><title>t</title></head><body>
<input id="name">
<div id="ed" contenteditable="true" style="min-height:40px;border:1px solid #ccc"></div>
<div class="decoy" style="position:absolute;left:-9999px;top:-9999px"><span>发布</span></div>
<div class="btn"><span>发布</span></div>
<div id="done" style="display:none">发好了</div>
<button id="load">加载</button>
<input id="f" type="file" multiple style="display:none">
<div id="files"></div>
<x-bar id="bar"></x-bar>
<div id="shadowed" style="display:none">点到了</div>
<script>
customElements.define('x-bar', class extends HTMLElement { connectedCallback() {
  const r = this.attachShadow({ mode: 'closed' })
  r.innerHTML = '<button>暂存</button><button>提交</button>'
  r.querySelectorAll('button')[1].addEventListener('click', e => { if (e.isTrusted) document.getElementById('shadowed').style.display = 'block' })
} })
document.querySelector('.decoy').addEventListener('click', () => document.title = 'decoy')
document.querySelector('.btn').addEventListener('click', e => { if (e.isTrusted) document.getElementById('done').style.display = 'block' })
document.getElementById('load').addEventListener('click', () => fetch('/api/data?x=1').then(r => r.json()))
document.getElementById('f').addEventListener('change', e => { document.getElementById('files').innerText = [...e.target.files].map(f => f.name + ':' + f.size).join(',') })
</script></body></html>`

func testServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/data":
			w.Header().Set("content-type", "application/json")
			w.Write([]byte(`{"notes":[{"id":"n1","likes":3}]}`))
		default:
			w.Header().Set("content-type", "text/html; charset=utf-8")
			w.Write([]byte(testPage))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func needChrome(t *testing.T) {
	if FindChrome() == "" {
		t.Skip("本机没有 Chrome / Edge")
	}
}

func TestPage(t *testing.T) {
	needChrome(t)
	srv := testServer(t)
	m := NewManager(t.TempDir(), nil)
	ctx := context.Background()

	p, err := m.Open(Options{Profile: "t1", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// 同一个 profile 同时只能开一个
	if _, err := m.Open(Options{Profile: "t1"}); err == nil || !strings.Contains(err.Error(), "正在被另一个任务使用") {
		t.Fatalf("profile 占用应该报错，得到 %v", err)
	}
	if _, err := m.Open(Options{Profile: "Bad Name"}); err == nil {
		t.Fatal("profile 名不对应该报错")
	}

	if err := p.WaitFor(ctx, "#name", true, 0); err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.Exists(ctx, "#nope"); ok {
		t.Fatal("#nope 不应该存在")
	}
	if err := p.WaitFor(ctx, "#nope", true, 300*time.Millisecond); err == nil || !strings.Contains(err.Error(), "等了") {
		t.Fatalf("等不到应该超时报错，得到 %v", err)
	}

	// 反自动化检测的基本特征
	v, err := p.Eval(ctx, "({ wd: navigator.webdriver, ua: navigator.userAgent })")
	if err != nil {
		t.Fatal(err)
	}
	env := v.(map[string]any)
	if env["wd"] != false || strings.Contains(env["ua"].(string), "Headless") {
		t.Fatalf("自动化特征没去掉：%v", env)
	}

	// 输入：普通输入框、富文本（换行分段）、清空后重写
	if err := p.Type(ctx, "#name", "你好 world", false, 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := p.Eval(ctx, "document.querySelector('#name').value"); v != "你好 world" {
		t.Fatalf("输入框的值：%v", v)
	}
	if err := p.Type(ctx, "#ed", "第一行\n第二行 😀", false, 0); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Text(ctx, "#ed"); !strings.Contains(s, "第一行") || !strings.Contains(s, "第二行 😀") || !strings.Contains(s, "\n") {
		t.Fatalf("富文本内容：%q", s)
	}
	if err := p.Type(ctx, "#ed", "重写", true, 0); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Text(ctx, "#ed"); strings.TrimSpace(s) != "重写" {
		t.Fatalf("清空后重写：%q", s)
	}

	// 按文字点击，走的是真实点击（isTrusted）；页面外面的同名陷阱元素不算
	if err := p.Click(ctx, "", "发布", 0); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitFor(ctx, "#done", true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	// closed shadow root 里的按钮
	if err := p.Click(ctx, "", "提交", 0); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitFor(ctx, "#shadowed", true, 3*time.Second); err != nil {
		t.Fatal("没点到 closed shadow root 里的按钮：", err)
	}
	if err := p.Click(ctx, "", "没有这个按钮", 500*time.Millisecond); err == nil || !strings.Contains(err.Error(), "没有这段文字") {
		t.Fatalf("找不到文字应该报错，得到 %v", err)
	}

	// 记录接口响应
	if _, err := p.Responses(ctx, "/api/data", 1, time.Second); err == nil {
		t.Fatal("没 listen 就等响应应该报错")
	}
	p.Listen("/api/data")
	if err := p.Click(ctx, "#load", "", 0); err != nil {
		t.Fatal(err)
	}
	rs, err := p.Responses(ctx, "/api/data", 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	notes := rs[0]["json"].(map[string]any)["notes"].([]any)
	if rs[0]["status"].(int64) != 200 || notes[0].(map[string]any)["id"] != "n1" {
		t.Fatalf("响应：%v", rs[0])
	}

	// 上传（隐藏的 file input）
	f := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(f, []byte("fakepng"), 0o600)
	if err := p.Upload(ctx, "#f", []string{f}, 0); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Text(ctx, "#files"); s != "a.png:7" {
		t.Fatalf("上传结果：%q", s)
	}

	// 截图、渲染 HTML
	png, err := p.Screenshot(ctx, "#name", false)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("截图：%v %d", err, len(png))
	}
	if err := p.SetContent(ctx, `<div id="card" style="width:300px;height:200px;background:#fce">小红书卡片</div>`); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Text(ctx, "#card"); s != "小红书卡片" {
		t.Fatalf("SetContent 后：%q", s)
	}
	if png, err := p.Screenshot(ctx, "", true); err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("整页截图：%v", err)
	}

	// 页面脚本出错要报出来
	if _, err := p.Eval(ctx, "nope.x"); err == nil {
		t.Fatal("脚本出错应该报错")
	}
	if _, err := p.Text(ctx, "#nope"); err == nil || !strings.Contains(err.Error(), "页面上没有") {
		t.Fatalf("读不存在的元素：%v", err)
	}

	// 关掉之后同一个 profile 能再打开；操作已关的页面报能看懂的错
	p.Close()
	if err := p.WaitFor(ctx, "#name", true, 0); err == nil || !strings.Contains(err.Error(), "已经关了") {
		t.Fatalf("关掉后操作：%v", err)
	}
	p2, err := m.Open(Options{Profile: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	p2.Close()
}

func TestGotoRules(t *testing.T) {
	needChrome(t)
	srv := testServer(t)
	m := NewManager(t.TempDir(), func(host string) error {
		if host == "127.0.0.1" {
			return errors.New("不允许访问本机或内网地址")
		}
		return nil
	})
	p, err := m.Open(Options{Profile: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	if err := p.Goto(ctx, srv.URL, "", 0); err == nil || !strings.Contains(err.Error(), "内网") {
		t.Fatalf("访问本机应该被拦住：%v", err)
	}
	if err := p.Goto(ctx, "file:///etc/passwd", "", 0); err == nil || !strings.Contains(err.Error(), "http / https") {
		t.Fatalf("file:// 应该被拦住：%v", err)
	}
	// 调用方停止时操作马上返回
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.WaitFor(cctx, "#x", true, 10*time.Second); err == nil || err.Error() != "已停止" {
		t.Fatalf("停止后应该返回「已停止」：%v", err)
	}
}

// 用户把窗口关了（Chrome 本身还在，macOS 上常见）：后面的操作要立刻报「浏览器已经关了」，而不是一直等到超时
func TestTabClosedByUser(t *testing.T) {
	needChrome(t)
	srv := testServer(t)
	m := NewManager(t.TempDir(), nil)
	p, err := m.Open(Options{Profile: "t3", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tid := chromedp.FromContext(p.ctx).Target.TargetID
	// 走浏览器那条连接关（chromedp 不让在标签页自己的会话里关），和用户点关闭一样
	c := chromedp.FromContext(p.ctx)
	if err := target.CloseTarget(tid).Do(cdp.WithExecutor(p.ctx, c.Browser)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		start := time.Now()
		_, err := p.URL(context.Background())
		if err != nil && strings.Contains(err.Error(), "已经关了") {
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("关了之后还等了 %s 才报错", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("标签页关了 10 秒还没当成关闭：%v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// 关了之后 profile 要能再打开
	p2, err := m.Open(Options{Profile: "t3"})
	if err != nil {
		t.Fatal(err)
	}
	p2.Close()
}

// profile 的 id：没有 profile 目录时是空，有了就生成、之后不变，跟着目录走
func TestProfileID(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	if id, err := m.ProfileID("x-1"); err != nil || id != "" {
		t.Fatalf("没有 profile 目录应该是空：%q %v", id, err)
	}
	if _, err := m.ProfileID("../x"); err == nil {
		t.Fatal("profile 名不对要报错")
	}
	os.MkdirAll(filepath.Join(m.Dir, "x-1"), 0o700)
	id, err := m.ProfileID("x-1")
	if err != nil || len(id) != 24 {
		t.Fatalf("应该生成 id：%q %v", id, err)
	}
	if again, _ := m.ProfileID("x-1"); again != id {
		t.Fatalf("id 要不变：%q → %q", id, again)
	}
	moved := NewManager(t.TempDir(), nil) // 换一个数据目录，profile 目录搬过去，id 跟着走
	os.Rename(filepath.Join(m.Dir, "x-1"), filepath.Join(moved.Dir, "x-1"))
	if got, _ := moved.ProfileID("x-1"); got != id {
		t.Fatalf("id 跟着 profile 目录走：%q → %q", id, got)
	}
}
