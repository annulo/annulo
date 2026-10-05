// Package browser 是本机函数的浏览器（ctx.browser）：用用户电脑上装好的 Chrome（没有就用 Edge），
// 通过 DevTools 协议（chromedp）自动操作网页。社媒发布、采集这类没有开放接口的平台都靠它。
//
// 每个 profile 一个持久的用户目录（~/.shuttle/browser/<profile>），一个账号一个 profile：
// 用户在弹出的窗口里登录一次，登录态留在目录里，之后后台直接用。cookie 不导出、不上传。
//
// 用的是一个独立的 Chrome 实例，和用户日常开着的浏览器互不影响。
package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// DefaultTimeout 是单个操作（打开页面、等元素、点击…）默认最多等多久。
const DefaultTimeout = 30 * time.Second

var profileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Options 是打开浏览器的选项。
type Options struct {
	Profile string // 必填：~/.shuttle/browser/<profile>，一个账号一个
	URL     string // 打开后直接去的地址，可以不填
	// Show 弹出可见窗口（让用户扫码登录）。默认后台跑：--headless=new。
	Show bool
	// Offscreen 用正常的有界面 Chrome，但把窗口放到屏幕外面。
	// 有些网站能认出 headless（渲染、字体、WebGL 的特征和有界面的不一样），被识别时改用这个。
	Offscreen bool
	KeepOpen  bool // 可见窗口由用户关闭，任务结束只归还操作权
}

// Manager 管所有 profile：同一个 profile 同时只能被一个任务打开（Chrome 的用户目录只能给一个进程用）。
type Manager struct {
	Dir string // ~/.shuttle/browser
	// CheckHost 检查要打开的地址能不能访问（不许本机、内网），nil 表示不检查（测试用）。
	CheckHost func(host string) error

	mu   sync.Mutex
	busy map[string]bool
	kept map[string]*Page
}

func NewManager(dir string, checkHost func(host string) error) *Manager {
	return &Manager{Dir: dir, CheckHost: checkHost, busy: map[string]bool{}, kept: map[string]*Page{}}
}

// CloseKept 在 Shuttle 退出时关掉保留的窗口，避免下次启动遗留 profile 占用。
func (m *Manager) CloseKept() {
	m.mu.Lock()
	pages := make([]*Page, 0, len(m.kept))
	for _, p := range m.kept {
		pages = append(pages, p)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range pages {
		wg.Add(1)
		go func() { defer wg.Done(); p.Close() }()
	}
	wg.Wait()
}

// Acquire 取得一次独占操作权。用户保留的窗口直接复用，release 后继续留着。
func (m *Manager) Acquire(o Options) (*Page, func(), error) {
	if o.KeepOpen && !o.Show {
		return nil, nil, i18n.New("保留窗口需要 show: true", "Keeping a window open requires show: true")
	}
	m.mu.Lock()
	p := m.kept[o.Profile]
	reused := p != nil
	if p != nil {
		if m.busy[o.Profile] {
			m.mu.Unlock()
			return nil, nil, i18n.New("这个账号的浏览器正在执行另一个任务，请等它完成", "This account's browser is running another task. Wait for it to finish.")
		}
		m.busy[o.Profile] = true
	}
	m.mu.Unlock()
	if p == nil {
		var err error
		p, err = m.Open(o)
		if err != nil {
			return nil, nil, err
		}
		if !o.KeepOpen {
			return p, func() { p.Close() }, nil
		}
		m.mu.Lock()
		if p.ctx.Err() != nil {
			m.mu.Unlock()
			p.Close()
			return nil, nil, i18n.New("浏览器已关闭，请重新打开", "The browser was closed. Open it again.")
		}
		m.kept[o.Profile] = p
		m.mu.Unlock()
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			if m.kept[o.Profile] == p {
				delete(m.busy, o.Profile)
			}
			m.mu.Unlock()
		})
	}
	// 前一个任务收集的响应不能当作这次任务的新响应。
	p.mu.Lock()
	p.listens, p.captured = nil, nil
	p.pending = map[network.RequestID]*Response{}
	p.mu.Unlock()
	if reused && o.URL != "" {
		if err := p.Goto(context.Background(), o.URL, "", 0); err != nil {
			release()
			return nil, nil, err
		}
	}
	return p, release, nil
}

// Page 是打开的一个浏览器（一个标签页）。
type Page struct {
	m        *Manager
	profile  string
	ctx      context.Context // chromedp 的标签页上下文，浏览器活多久它就活多久
	cancel   context.CancelFunc
	alloc    context.CancelFunc
	closeOne sync.Once

	mu       sync.Mutex
	listens  []string
	pending  map[network.RequestID]*Response
	captured []*Response
	wake     chan struct{}
}

// Response 是 listen 记录下来的一条接口响应。
type Response struct {
	URL    string
	Status int64
	Text   string
}

// FindChrome 找本机的 Chrome，没有就找 Edge（都是 Chromium，DevTools 协议一样）。
func FindChrome() string {
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			paths = append(paths,
				filepath.Join(root, "Google Chrome.app/Contents/MacOS/Google Chrome"),
				filepath.Join(root, "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
				filepath.Join(root, "Chromium.app/Contents/MacOS/Chromium"),
			)
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if root := os.Getenv(env); root != "" {
				paths = append(paths,
					filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				)
			}
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
			if p, err := exec.LookPath(name); err == nil {
				paths = append(paths, p)
			}
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// profileIDFile 放在每个 profile 目录里：这个浏览器（登录态）是谁。Chrome 不碰用户目录根上不认识的文件。
const profileIDFile = "shuttle-profile-id"

// ProfileID 是这个 profile 的 id：第一次用到时随机生成，存在 profile 目录里，跟着登录态走——
// 换了数据目录、电脑改了名、Shuttle 重装都不变，profile 目录没了（登录态也就没了）才没有。
// 本机没有这个 profile 返回 ""。
func (m *Manager) ProfileID(profile string) (string, error) {
	if !profileRe.MatchString(profile) {
		return "", i18n.Errorf("profile 名不对：%q（只能用小写字母、数字、- 和 _，比如 xiaohongshu 或 xiaohongshu-2）", "Invalid profile name: %q (use lowercase letters, digits, - and _, e.g. xiaohongshu or xiaohongshu-2)", profile)
	}
	dir := filepath.Join(m.Dir, profile)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", nil
	}
	return ensureProfileID(dir)
}

func ensureProfileID(dir string) (string, error) {
	file := filepath.Join(dir, profileIDFile)
	if b, err := os.ReadFile(file); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	return id, os.WriteFile(file, []byte(id+"\n"), 0o600)
}

// Open 启动这个 profile 的浏览器。用完一定要 Close（本机函数结束时会自动关）。
func (m *Manager) Open(o Options) (*Page, error) {
	if !profileRe.MatchString(o.Profile) {
		return nil, i18n.Errorf("profile 名不对：%q（只能用小写字母、数字、- 和 _，比如 xiaohongshu 或 xiaohongshu-2）", "Invalid profile name: %q (use lowercase letters, digits, - and _, e.g. xiaohongshu or xiaohongshu-2)", o.Profile)
	}
	exe := FindChrome()
	if exe == "" {
		return nil, i18n.New("没找到 Chrome：请先安装 Google Chrome", "Chrome not found: please install Google Chrome first")
	}
	m.mu.Lock()
	if m.busy[o.Profile] || m.kept[o.Profile] != nil {
		m.mu.Unlock()
		return nil, i18n.Errorf("%s 的浏览器正在被另一个任务使用，等它跑完再试", "The %s browser is being used by another task; try again when it finishes", o.Profile)
	}
	m.busy[o.Profile] = true
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.busy, o.Profile)
		delete(m.kept, o.Profile)
		m.mu.Unlock()
	}

	dir := filepath.Join(m.Dir, o.Profile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		release()
		return nil, err
	}
	if _, err := ensureProfileID(dir); err != nil {
		release()
		return nil, err
	}
	// 不用 chromedp 的默认参数：里面有 --enable-automation（页面上会出「受自动测试软件控制」，
	// navigator.webdriver 也会是 true），这是网站识别自动化最直接的特征
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exe),
		chromedp.UserDataDir(dir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(1280, 900),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// 上面这个参数在有界面的窗口顶上会出「You are using an unsupported command-line flag」的警告条；test-type 让 Chrome 不显示它
		chromedp.Flag("test-type", true),
		chromedp.Flag("disable-features", "Translate"),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-breakpad", true),
		// macOS 上不去碰钥匙串（不然会弹「允许访问钥匙串」），cookie 用固定的 key 加密存在 profile 里
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),
	}
	switch {
	case o.Show:
	case o.Offscreen:
		opts = append(opts, chromedp.Flag("window-position", "-32000,-32000"))
	default:
		// 新版 headless 和有界面的 Chrome 是同一套实现，比老 headless 难识别得多；
		// 还是被识别（登录后跳验证、接口返回异常）时改用 Offscreen
		opts = append(opts, chromedp.Flag("headless", "new"), chromedp.Flag("hide-scrollbars", true), chromedp.Flag("mute-audio", true))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	p := &Page{m: m, profile: o.Profile, ctx: ctx, cancel: cancel, pending: map[network.RequestID]*Response{}, wake: make(chan struct{})}
	p.alloc = func() {
		allocCancel()
		release()
	}
	// 第一次 Run 启动浏览器；不能给它带超时的 ctx，否则超时时整个浏览器跟着被关掉
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		p.Close()
		msg := err.Error()
		if strings.Contains(msg, "ProcessSingleton") || strings.Contains(msg, "profile") && strings.Contains(msg, "in use") {
			return nil, i18n.Errorf("%s 的浏览器已经在别处打开了（比如之前的登录窗口还没关），关掉再试", "The %s browser is already open elsewhere (e.g. an earlier sign-in window); close it and try again", o.Profile)
		}
		return nil, i18n.Errorf("启动 Chrome 失败：%v", "Failed to start Chrome: %v", err)
	}
	if !o.Show && !o.Offscreen {
		// headless 的 UA 里带 HeadlessChrome，一眼就能认出来，换成正常的
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
			_, _, _, ua, _, err := cdpbrowser.GetVersion().Do(c)
			if err != nil {
				return err
			}
			return emulation.SetUserAgentOverride(strings.ReplaceAll(ua, "HeadlessChrome", "Chrome")).WithAcceptLanguage("zh-CN,zh;q=0.9,en;q=0.8").Do(c)
		})); err != nil {
			p.Close()
			return nil, i18n.Errorf("启动 Chrome 失败：%v", "Failed to start Chrome: %v", err)
		}
	}
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(shadowHookJS).Do(c)
		return err
	})); err != nil {
		p.Close()
		return nil, i18n.Errorf("启动 Chrome 失败：%v", "Failed to start Chrome: %v", err)
	}
	chromedp.ListenTarget(ctx, p.onEvent)
	// 可见窗口可以留给用户操作；Chrome 退出时也要释放 profile 的占用。
	go func() { <-ctx.Done(); p.Close() }()
	// 用户把窗口关了：Chrome 退出时 chromedp 会取消 ctx，但 macOS 上关掉最后一个窗口 Chrome 还在，
	// 标签页没了、ctx 却还活着，之后每个操作都要等到超时。标签页一关就当浏览器关了，后面的调用立刻报错
	if t := chromedp.FromContext(ctx).Target; t != nil {
		tid, sid := t.TargetID, t.SessionID
		chromedp.ListenBrowser(ctx, func(ev any) {
			switch e := ev.(type) {
			case *target.EventTargetDestroyed:
				if e.TargetID == tid {
					go p.Close()
				}
			case *target.EventDetachedFromTarget:
				if e.SessionID == sid {
					go p.Close()
				}
			}
		})
	}
	if o.URL != "" {
		if err := p.Goto(context.Background(), o.URL, "", 0); err != nil {
			p.Close()
			return nil, err
		}
	}
	return p, nil
}

// Close 关掉浏览器（正常退出，让 Chrome 把 cookie 写回 profile），重复调用没关系。
func (p *Page) Close() error {
	p.closeOne.Do(func() {
		done := make(chan struct{})
		go func() {
			chromedp.Cancel(p.ctx)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		p.cancel()
		p.alloc()
		p.mu.Lock()
		close(p.wake)
		p.wake = make(chan struct{})
		p.mu.Unlock()
	})
	return nil
}

// op 给一个操作建上下文：默认 30 秒超时，调用方（本机函数被停止）取消时也跟着停。
func (p *Page) op(parent context.Context, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() { stop(); cancel() }
}

// explain 把 chromedp 的错误换成运营人员能看懂的话。
func (p *Page) explain(parent, ctx context.Context, err error, what string, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	switch {
	case p.ctx.Err() != nil:
		return i18n.Errorf("%s 的浏览器已经关了（窗口被关掉，或者这个任务结束了）", "The %s browser was closed (the window was closed, or the task ended)", p.profile)
	case parent.Err() != nil:
		return i18n.New("已停止", "Stopped")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		return i18n.Errorf("%s：等了 %s 还没好", "%s: still not done after %s", what, secs(timeout))
	}
	return fmt.Errorf("%s：%v", what, err)
}

func (p *Page) run(parent context.Context, timeout time.Duration, what string, actions ...chromedp.Action) error {
	ctx, done := p.op(parent, timeout)
	defer done()
	return p.explain(parent, ctx, chromedp.Run(ctx, actions...), what, timeout)
}

func (p *Page) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return i18n.Errorf("浏览器只能打开 http / https 地址：%q", "The browser can only open http / https URLs: %q", raw)
	}
	if p.m.CheckHost != nil {
		if err := p.m.CheckHost(u.Hostname()); err != nil {
			return err
		}
	}
	return nil
}

// Goto 打开一个地址，等页面加载完（load 事件）；wait 是选择器时再等这个元素出现。
func (p *Page) Goto(parent context.Context, raw, wait string, timeout time.Duration) error {
	if err := p.checkURL(raw); err != nil {
		return err
	}
	actions := []chromedp.Action{chromedp.Navigate(raw)}
	if wait != "" && wait != "load" {
		actions = append(actions, chromedp.WaitReady(wait, chromedp.ByQuery))
	}
	return p.run(parent, timeout, i18n.T("打开 ", "Opening ")+raw, actions...)
}

// WaitFor 等元素出现在页面上（visible：还要看得见）。
func (p *Page) WaitFor(parent context.Context, sel string, visible bool, timeout time.Duration) error {
	var a chromedp.Action = chromedp.WaitReady(sel, chromedp.ByQuery)
	if visible {
		a = chromedp.WaitVisible(sel, chromedp.ByQuery)
	}
	return p.run(parent, timeout, i18n.Tf("等页面上出现「%s」", "Waiting for \"%s\" to appear", sel), a)
}

// Exists 现在页面上有没有这个元素，不等待。
func (p *Page) Exists(parent context.Context, sel string) (bool, error) {
	var ok bool
	err := p.run(parent, 0, i18n.Tf("查找「%s」", "Finding \"%s\"", sel), chromedp.Evaluate(fmt.Sprintf("!!document.querySelector(%s)", jsString(sel)), &ok))
	return ok, err
}

// findTextJS 按可见文字找元素：先找文字完全相同的，再找包含的；取最里层的那个（点它会冒泡到外面的按钮）。
// 找的范围包括 shadow root（open 的直接读，closed 的由 shadowHookJS 记下来）。
// 在页面自己的 DOM 里的：打上标记，再用选择器点，走真实鼠标点击；在 shadow root 里的：选择器够不着，滚到可见后返回中心坐标按位置点。
const findTextJS = `((t, mark, key) => {
  document.querySelectorAll('[data-shuttle-target]').forEach(e => e.removeAttribute('data-shuttle-target'))
  // 放在页面范围外的（比如 left:-9999px）不算：有的网站用它当陷阱，专门抓按文字点击的脚本
  const d = document.documentElement
  const inPage = r => r.right + scrollX > 0 && r.bottom + scrollY > 0 && r.left + scrollX < Math.max(d.scrollWidth, innerWidth) && r.top + scrollY < Math.max(d.scrollHeight, innerHeight)
  const vis = e => { const r = e.getBoundingClientRect(); const s = getComputedStyle(e); return r.width > 0 && r.height > 0 && inPage(r) && s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0' }
  const txt = e => ((e.innerText || e.value || e.getAttribute('aria-label') || '') + '').trim()
  const SEL = 'button,a,[role=button],[role=tab],[role=menuitem],[role=option],label,input[type=button],input[type=submit],li,span,div,p'
  const closed = typeof window[key] === 'function' ? window[key]() : []
  const all = []
  const collect = root => {
    for (const e of root.querySelectorAll('*')) {
      if (e.matches(SEL)) all.push(e)
      const sr = e.shadowRoot || closed.find(r => r.host === e)
      if (sr) collect(sr)
    }
  }
  collect(document)
  // 文字要真的在元素自己的子树里（textContent 不含 shadow root）：innerText 会把 shadow root 里渲染的字也算上，
  // 那样会挑中宿主元素，点到它的中心，不一定是那个按钮
  const own = e => (e.textContent || '').includes(t) || (e.value || '').includes(t) || (e.getAttribute('aria-label') || '').includes(t)
  const pick = match => { let best = null; for (const e of all) { if (match(txt(e)) && own(e) && vis(e) && (!best || best.contains(e))) best = e } return best }
  const el = pick(x => x === t) || pick(x => x.includes(t))
  if (!el) return false
  if (el.getRootNode() !== document) {
    el.scrollIntoView({ block: 'center', inline: 'center' })
    const r = el.getBoundingClientRect()
    return [r.left + r.width / 2, r.top + r.height / 2]
  }
  el.setAttribute('data-shuttle-target', mark)
  return true
})`

// shadowRootsKey 是 shadowHookJS 挂在 window 上的函数名（每次启动随机一个，网页猜不到）。
var shadowRootsKey = fmt.Sprintf("__s%x", time.Now().UnixNano())

// shadowHookJS 在每个页面加载前执行：网页创建 closed shadow root 时悄悄记下来，按文字点击时才找得到里面的按钮
// （小红书的发布按钮就在一个 closed 的 Web Component 里）。对网页来说 shadow root 仍然是 closed，行为不变。
// 不要改 Function.prototype.toString：小红书会认出来，整个页面不渲染。
var shadowHookJS = `(() => {
  const key = '` + shadowRootsKey + `'
  const orig = Element.prototype.attachShadow
  const roots = []
  const attachShadow = function attachShadow(init) {
    const r = orig.call(this, init)
    if (init && init.mode === 'closed') roots.push(new WeakRef(r))
    return r
  }
  Object.defineProperty(Element.prototype, 'attachShadow', { value: attachShadow, writable: true, configurable: true })
  Object.defineProperty(window, key, { value: () => roots.map(w => w.deref()).filter(Boolean), enumerable: false })
})()`

// Click 点一个元素：sel 是选择器；sel 为空时按可见文字 text 找。
func (p *Page) Click(parent context.Context, sel, text string, timeout time.Duration) error {
	what := i18n.Tf("点击「%s」", "Clicking \"%s\"", sel)
	if sel == "" {
		what = i18n.Tf("点击文字是「%s」的按钮", "Clicking the button labeled \"%s\"", text)
		mark := fmt.Sprint(time.Now().UnixNano())
		ctx, done := p.op(parent, timeout)
		defer done()
		// 页面可能还在加载，轮询到找到为止
		for {
			// 返回 true：找到了，打了标记（按选择器点）；返回 [x, y]：在 shadow root 里，选择器够不着，按坐标点
			var found any
			if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf("%s(%s, %s, %s)", findTextJS, jsString(text), jsString(mark), jsString(shadowRootsKey)), &found)); err != nil {
				return p.explain(parent, ctx, err, what, timeout)
			}
			if xy, ok := found.([]any); ok && len(xy) == 2 {
				x, _ := xy[0].(float64)
				y, _ := xy[1].(float64)
				return p.run(parent, timeout, what, chromedp.MouseClickXY(x, y))
			}
			if found == true {
				break
			}
			select {
			case <-ctx.Done():
				return p.explain(parent, ctx, ctx.Err(), what+i18n.T("（页面上没有这段文字）", " (no such text on the page)"), timeout)
			case <-time.After(200 * time.Millisecond):
			}
		}
		sel = fmt.Sprintf(`[data-shuttle-target="%s"]`, mark)
	}
	return p.run(parent, timeout, what, chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible))
}

// prepareTypeJS 让输入框准备好输入：clear 时全选（接着按退格删掉），否则把光标放到最后。
// 返回元素类型，决定换行怎么输入。
const prepareTypeJS = `((sel, clear) => {
  const el = document.querySelector(sel)
  if (!el) return ''
  el.focus()
  if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
    if (clear) el.select(); else el.setSelectionRange(el.value.length, el.value.length)
    return el.tagName.toLowerCase()
  }
  const r = document.createRange()
  r.selectNodeContents(el)
  if (!clear) r.collapse(false)
  const s = window.getSelection()
  s.removeAllRanges()
  s.addRange(r)
  return 'editable'
})`

// Type 往输入框里输入文字。普通输入框、textarea、富文本编辑器（contenteditable）都行：
// 先真实点击拿到焦点，文字用 Input.insertText 输入（和输入法上屏一样，中文、emoji 都没问题），
// 富文本里的换行用回车键，让编辑器自己分段。
func (p *Page) Type(parent context.Context, sel, text string, clear bool, timeout time.Duration) error {
	what := i18n.Tf("在「%s」里输入", "Typing into \"%s\"", sel)
	ctx, done := p.op(parent, timeout)
	defer done()
	var kind string
	err := chromedp.Run(ctx,
		chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible),
		chromedp.Evaluate(fmt.Sprintf("%s(%s, %v)", prepareTypeJS, jsString(sel), clear), &kind),
	)
	if err == nil && clear {
		err = chromedp.Run(ctx, chromedp.KeyEvent(kb.Backspace))
	}
	if err != nil {
		return p.explain(parent, ctx, err, what, timeout)
	}
	lines := strings.Split(text, "\n")
	if kind == "input" {
		lines = []string{strings.Join(lines, " ")}
	}
	for i, line := range lines {
		if i > 0 {
			if err := chromedp.Run(ctx, enterKey()); err != nil {
				return p.explain(parent, ctx, err, what, timeout)
			}
		}
		if line == "" {
			continue
		}
		if err := chromedp.Run(ctx, input.InsertText(line)); err != nil {
			return p.explain(parent, ctx, err, what, timeout)
		}
	}
	return nil
}

var keyNames = map[string]string{
	"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Esc": kb.Escape, "Backspace": kb.Backspace, "Delete": kb.Delete,
	"ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight,
	"Home": kb.Home, "End": kb.End, "PageUp": kb.PageUp, "PageDown": kb.PageDown, "Space": " ",
}

// enterKey 按一次回车：keyDown 带上 "\r"（浏览器据此执行换行的默认行为），不再单独发字符事件。
// chromedp.KeyEvent(kb.Enter) 会多发一个 "\r" 的 char 事件，ProseMirror 这类编辑器处理完回车又插一次，一个换行变成两段。
func enterKey() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		down := input.DispatchKeyEvent(input.KeyDown).WithKey("Enter").WithCode("Enter").WithText("\r").WithUnmodifiedText("\r").WithWindowsVirtualKeyCode(13).WithNativeVirtualKeyCode(13)
		if err := down.Do(ctx); err != nil {
			return err
		}
		return input.DispatchKeyEvent(input.KeyUp).WithKey("Enter").WithCode("Enter").WithWindowsVirtualKeyCode(13).WithNativeVirtualKeyCode(13).Do(ctx)
	})
}

// Press 按一个键：Enter、Tab、Escape、Backspace、ArrowDown…，或者单个字符。
func (p *Page) Press(parent context.Context, key string) error {
	k, ok := keyNames[key]
	if !ok {
		if len([]rune(key)) != 1 {
			return i18n.Errorf("不认识的按键：%q（可以用 Enter、Tab、Escape、Backspace、Delete、ArrowUp/Down/Left/Right、Space 或单个字符）", "Unknown key: %q (use Enter, Tab, Escape, Backspace, Delete, ArrowUp/Down/Left/Right, Space or a single character)", key)
		}
		k = key
	}
	return p.run(parent, 0, i18n.T("按 ", "Pressing ")+key, chromedp.KeyEvent(k))
}

// Upload 把本机文件放进 <input type=file>（它通常是隐藏的，不要求可见）。
func (p *Page) Upload(parent context.Context, sel string, files []string, timeout time.Duration) error {
	return p.run(parent, timeout, i18n.Tf("上传文件到「%s」", "Uploading files to \"%s\"", sel), chromedp.SetUploadFiles(sel, files, chromedp.ByQuery))
}

// Eval 在页面里执行一段表达式，返回它的值（Promise 会等到结果；undefined 返回 nil）。
func (p *Page) Eval(parent context.Context, expr string) (any, error) {
	var raw json.RawMessage
	wrapped := fmt.Sprintf("(async () => { const __v = await (%s\n); return __v === undefined ? null : __v })()", expr)
	err := p.run(parent, 0, i18n.T("执行页面脚本", "Running the page script"), chromedp.Evaluate(wrapped, &raw, awaitPromise))
	if err != nil {
		return nil, err
	}
	var v any
	if len(raw) > 0 {
		json.Unmarshal(raw, &v)
	}
	return v, nil
}

// Text 是元素的可见文字（innerText）；sel 为空是整个页面的。
func (p *Page) Text(parent context.Context, sel string) (string, error) {
	return p.pick(parent, sel, "innerText", "document.body.innerText")
}

// HTML 是元素的 outerHTML；sel 为空是整个页面的。
func (p *Page) HTML(parent context.Context, sel string) (string, error) {
	return p.pick(parent, sel, "outerHTML", "document.documentElement.outerHTML")
}

func (p *Page) pick(parent context.Context, sel, prop, whole string) (string, error) {
	var s string
	if sel == "" {
		err := p.run(parent, 0, i18n.T("读取页面", "Reading the page"), chromedp.Evaluate(whole, &s))
		return s, err
	}
	var r struct {
		OK bool   `json:"ok"`
		V  string `json:"v"`
	}
	err := p.run(parent, 0, i18n.Tf("读取「%s」", "Reading \"%s\"", sel), chromedp.Evaluate(fmt.Sprintf("(() => { const e = document.querySelector(%s); return e ? { ok: true, v: e.%s } : { ok: false } })()", jsString(sel), prop), &r))
	if err == nil && !r.OK {
		return "", i18n.Errorf("页面上没有「%s」", "\"%s\" isn't on the page", sel)
	}
	return r.V, err
}

// URL 是当前页面的地址。
func (p *Page) URL(parent context.Context) (string, error) {
	var u string
	err := p.run(parent, 0, i18n.T("读取地址", "Reading the URL"), chromedp.Location(&u))
	return u, err
}

// Screenshot 截图（PNG）：sel 截一个元素，full 截整页，都不给截当前窗口。
func (p *Page) Screenshot(parent context.Context, sel string, full bool) ([]byte, error) {
	var buf []byte
	var a chromedp.Action
	switch {
	case sel != "":
		a = chromedp.Screenshot(sel, &buf, chromedp.ByQuery, chromedp.NodeVisible)
	case full:
		a = chromedp.FullScreenshot(&buf, 100) // quality 100 就是 PNG
	default:
		a = chromedp.CaptureScreenshot(&buf)
	}
	err := p.run(parent, 0, i18n.T("截图", "Taking a screenshot"), a)
	return buf, err
}

// SetContent 把页面换成一段 HTML（生成文字卡片图时用：渲染好再截图）。等字体和图片加载完才返回。
func (p *Page) SetContent(parent context.Context, html string) error {
	var ok bool
	return p.run(parent, 0, i18n.T("渲染 HTML", "Rendering HTML"),
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			return page.SetDocumentContent(tree.Frame.ID, html).Do(ctx)
		}),
		chromedp.Evaluate(`Promise.all([document.fonts.ready, ...[...document.images].map(i => i.complete ? 0 : new Promise(r => { i.onload = i.onerror = r }))]).then(() => true)`, &ok, awaitPromise),
	)
}

// Listen 开始记录 URL 含 pattern 的接口响应（XHR / fetch）。要在触发请求之前调，不然会漏掉。
func (p *Page) Listen(pattern string) {
	p.mu.Lock()
	p.listens = append(p.listens, pattern)
	p.mu.Unlock()
}

// Responses 等到至少 min 条 URL 含 pattern 的响应（min 为 0 就不等），返回目前记录到的全部匹配项：
// [{ url, status, text, json }]，响应体是 JSON 时 json 是解析好的值。
func (p *Page) Responses(parent context.Context, pattern string, min int, timeout time.Duration) ([]map[string]any, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	min = max(min, 0)
	deadline := time.After(timeout)
	for {
		p.mu.Lock()
		if len(p.listens) == 0 {
			p.mu.Unlock()
			return nil, i18n.New("还没有 listen：先 b.listen('接口地址的一段') 再打开页面，然后才能等响应", "Not listening yet: call b.listen('part of the API URL') before opening the page, then wait for responses")
		}
		var out []*Response
		for _, r := range p.captured {
			if strings.Contains(r.URL, pattern) {
				out = append(out, r)
			}
		}
		wake := p.wake
		p.mu.Unlock()
		if len(out) >= min {
			list := make([]map[string]any, len(out))
			for i, r := range out {
				list[i] = map[string]any{"url": r.URL, "status": r.Status, "text": r.Text}
				var v any
				if json.Unmarshal([]byte(r.Text), &v) == nil {
					list[i]["json"] = v
				}
			}
			return list, nil
		}
		select {
		case <-wake:
		case <-deadline:
			return nil, i18n.Errorf("等了 %s，只收到 %d 条地址含「%s」的响应（要 %d 条）", "Waited %s but got only %d response(s) with \"%s\" in the URL (need %d)", secs(timeout), len(out), pattern, min)
		case <-parent.Done():
			return nil, i18n.New("已停止", "Stopped")
		case <-p.ctx.Done():
			return nil, i18n.Errorf("%s 的浏览器已经关了", "The %s browser was closed", p.profile)
		}
	}
}

func (p *Page) matches(u string) bool {
	for _, l := range p.listens {
		if strings.Contains(u, l) {
			return true
		}
	}
	return false
}

// onEvent 在收到响应头时记下匹配的请求，加载完再取响应体（取响应体不能在事件回调里同步做）。
func (p *Page) onEvent(ev any) {
	switch e := ev.(type) {
	case *network.EventResponseReceived:
		if e.Type != network.ResourceTypeXHR && e.Type != network.ResourceTypeFetch {
			return
		}
		p.mu.Lock()
		if p.matches(e.Response.URL) {
			p.pending[e.RequestID] = &Response{URL: e.Response.URL, Status: e.Response.Status}
		}
		p.mu.Unlock()
	case *network.EventLoadingFinished:
		p.mu.Lock()
		r := p.pending[e.RequestID]
		delete(p.pending, e.RequestID)
		p.mu.Unlock()
		if r == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
			defer cancel()
			chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
				b, err := network.GetResponseBody(e.RequestID).Do(c)
				if err == nil {
					r.Text = string(b)
				}
				return err
			}))
			p.mu.Lock()
			p.captured = append(p.captured, r)
			close(p.wake)
			p.wake = make(chan struct{})
			p.mu.Unlock()
		}()
	case *network.EventLoadingFailed:
		p.mu.Lock()
		delete(p.pending, e.RequestID)
		p.mu.Unlock()
	}
}

func secs(d time.Duration) string {
	if d < 10*time.Second {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", d.Seconds()), ".0") + i18n.T(" 秒", "s")
	}
	return i18n.Tf("%d 秒", "%ds", int(d.Seconds()))
}

func awaitPromise(e *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
	return e.WithAwaitPromise(true)
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
