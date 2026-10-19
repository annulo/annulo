// Package extbridge 连着用户自己的 Chrome 里的 Annulo 插件（docs/browser-extension.md）：
// 每个装了插件的 Chrome profile 一条连接，插件经 native messaging 拉起 annulo（host.go），annulo 再连回本机的 Annulo 服务。
// ctx.browser.open({ profile: '@user' }) 通过它在用户的浏览器里开一个窗口，再用一个假的浏览器端点（endpoint.go）让 chromedp 照常操作。
//
// 插件只在用户允许的网站上、只在它自己开的标签页里干活，只转发白名单里的 DevTools 命令：这些限制写在插件里，这边改不了。
package extbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/annulo/annulo/internal/i18n"
)

// ExtensionIDs 是 native host 清单里允许的插件：Chrome 应用商店版，和开发版（manifest 里带 key，id 固定）
var ExtensionIDs = []string{"gbionmhpkofgoeheaibcmgikpboddbgp", "emhbcpdadcannpcngbkjiaccpckjeend"}

// HostName 是 native host 的名字，插件里 connectNative 用同一个
const HostName = "com.annulo.browser"

// Bridge 管所有插件连接。
type Bridge struct {
	mu    sync.Mutex
	conns map[string]*Conn
	wake  chan struct{} // 连接、网站授权变了就关掉换一个新的：等着的人重新看
}

func New() *Bridge {
	return &Bridge{conns: map[string]*Conn{}, wake: make(chan struct{})}
}

func (b *Bridge) changed() {
	b.mu.Lock()
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}

// Error 是插件返回的错误：Code 是 not_granted、blocked、closed、denied 这类
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// errorText 把插件的错误换成界面语言；插件的原话（英文）放在后面方便查
func errorText(code, msg string) string {
	switch code {
	case "not_granted":
		return i18n.T("还没允许 Annulo 在这个网站上操作你的浏览器", "Annulo isn't allowed on this site in your browser") + "（" + msg + "）"
	case "blocked":
		return i18n.T("页面跳到了没允许 Annulo 操作的网站，已停止操作", "The page went to a site Annulo isn't allowed on, so Annulo stopped") + "（" + msg + "）"
	case "denied":
		return i18n.T("插件不允许这个操作", "The extension doesn't allow this operation") + "（" + msg + "）"
	case "closed", "not_owned":
		return i18n.T("这个窗口已经关了", "This window was closed")
	}
	return msg
}

// Event 是插件推过来的事件
type Event struct {
	Kind   string          // cdp、closed、detached、blocked
	Tab    int             // 哪个标签页
	Method string          // cdp 事件的方法名
	Params json.RawMessage // cdp 事件的参数
	Reason string          // detached 的原因
	URL    string          // blocked：跳去了哪个没授权的地址
}

// Conn 是一个 Chrome profile 里的插件。
type Conn struct {
	b  *Bridge
	ws *websocket.Conn

	ID      string // 插件在这个 Chrome profile 里随机生成的 id，跟着 profile 走
	Version string // 插件版本

	mu         sync.Mutex
	sites      []string // 允许的网站，比如 https://www.doubao.com/*
	lastActive int64    // 用户最近一次切到这个 profile 的窗口（毫秒）
	next       int64
	pending    map[int64]chan reply
	subs       map[int]func(Event)
	done       chan struct{}
}

type reply struct {
	result json.RawMessage
	err    error
}

type message struct {
	// 回答
	ID     *int64          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Code   string          `json:"code"`
	// 事件
	Event      string          `json:"event"`
	Tab        int             `json:"tab"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	Reason     string          `json:"reason"`
	URL        string          `json:"url"`
	At         int64           `json:"at"`
	Sites      []string        `json:"sites"`
	LastActive int64           `json:"lastActive"`
}

type helloInfo struct {
	ID         string   `json:"id"`
	Version    string   `json:"version"`
	Sites      []string `json:"sites"`
	LastActive int64    `json:"lastActive"`
}

// Serve 接管一条 native host 连过来的 WebSocket，直到断开。先问插件是谁（hello），再登记。
func (b *Bridge) Serve(ctx context.Context, ws *websocket.Conn) {
	ws.SetReadLimit(64 << 20)
	c := &Conn{b: b, ws: ws, pending: map[int64]chan reply{}, subs: map[int]func(Event){}, done: make(chan struct{})}
	go c.read(ctx)
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	raw, err := c.Call(hctx, "hello", nil)
	cancel()
	var h helloInfo
	if err == nil {
		err = json.Unmarshal(raw, &h)
	}
	if err != nil || !strings.HasPrefix(h.ID, "annulo-") {
		log.Printf("浏览器插件连上了但没说自己是谁：%v", err)
		ws.Close(websocket.StatusPolicyViolation, "hello failed")
		return
	}
	c.ID, c.Version = h.ID, h.Version
	c.mu.Lock()
	c.sites, c.lastActive = h.Sites, h.LastActive
	c.mu.Unlock()
	b.mu.Lock()
	if old := b.conns[c.ID]; old != nil {
		old.ws.Close(websocket.StatusGoingAway, "replaced")
	}
	b.conns[c.ID] = c
	b.mu.Unlock()
	b.changed()
	log.Printf("浏览器插件已连接：%s（插件 %s）", c.ID, c.Version)
	<-c.done
	b.mu.Lock()
	if b.conns[c.ID] == c {
		delete(b.conns, c.ID)
	}
	b.mu.Unlock()
	b.changed()
	log.Printf("浏览器插件断开：%s", c.ID)
}

func (c *Conn) read(ctx context.Context) {
	defer func() {
		close(c.done)
		c.mu.Lock()
		for id, ch := range c.pending {
			ch <- reply{err: &Error{Code: "disconnected", Message: i18n.T("浏览器插件断开了（Chrome 关了，或者插件被停用）", "The browser extension disconnected (Chrome closed, or the extension was turned off)")}}
			delete(c.pending, id)
		}
		subs := c.subs
		c.subs = map[int]func(Event){}
		c.mu.Unlock()
		for tab, fn := range subs {
			fn(Event{Kind: "closed", Tab: tab})
		}
	}()
	for {
		_, b, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Event == "" && m.ID != nil {
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				if m.OK {
					ch <- reply{result: m.Result}
				} else {
					ch <- reply{err: &Error{Code: m.Code, Message: errorText(m.Code, m.Error)}}
				}
			}
			continue
		}
		switch m.Event {
		case "hello":
			c.mu.Lock()
			c.sites, c.lastActive = m.Sites, m.LastActive
			c.mu.Unlock()
			c.b.changed()
		case "sites":
			c.mu.Lock()
			c.sites = m.Sites
			c.mu.Unlock()
			c.b.changed()
		case "active":
			c.mu.Lock()
			c.lastActive = m.At
			c.mu.Unlock()
		case "cdp", "closed", "detached", "blocked":
			c.mu.Lock()
			fn := c.subs[m.Tab]
			c.mu.Unlock()
			if fn != nil {
				fn(Event{Kind: m.Event, Tab: m.Tab, Method: m.Method, Params: m.Params, Reason: m.Reason, URL: m.URL})
			}
		}
	}
}

// Call 发一个请求给插件，等它回答。
func (c *Conn) Call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"id": id, "op": op, "args": args})
	if err == nil {
		err = c.ws.Write(ctx, websocket.MessageText, b)
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, &Error{Code: "disconnected", Message: i18n.T("浏览器插件断开了", "The browser extension disconnected")}
	}
}

// Subscribe 收这个标签页的事件，返回取消订阅
func (c *Conn) Subscribe(tab int, fn func(Event)) func() {
	c.mu.Lock()
	c.subs[tab] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subs, tab)
		c.mu.Unlock()
	}
}

// Allows：这个 profile 允许了这个地址所在的网站
func (c *Conn) Allows(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sites {
		if siteMatch(s, u) {
			return true
		}
	}
	return false
}

// siteMatch：插件的网站写法是 Chrome 的匹配模式，scheme://host/*（host 可以是 *.example.com）
func siteMatch(pattern string, u *url.URL) bool {
	scheme, rest, ok := strings.Cut(pattern, "://")
	if !ok || (scheme != "*" && scheme != u.Scheme) {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	h := strings.ToLower(u.Hostname())
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "*.") {
		return h == host[2:] || strings.HasSuffix(h, host[1:])
	}
	return host == "*" || h == host
}

// Profile 是给界面、本机函数看的一个 Chrome profile
type Profile struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`  // Chrome 里这个 profile 的名字（对得上时）
	Email      string   `json:"email,omitempty"` // 登录的 Google 账号
	Version    string   `json:"version"`
	Sites      []string `json:"sites"`
	LastActive int64    `json:"last_active"`
}

// Profiles 是连着的插件，最近用过的在前
func (b *Bridge) Profiles() []Profile {
	b.mu.Lock()
	conns := make([]*Conn, 0, len(b.conns))
	for _, c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	names := chromeProfileNames(conns)
	out := make([]Profile, 0, len(conns))
	for _, c := range conns {
		c.mu.Lock()
		p := Profile{ID: c.ID, Version: c.Version, Sites: append([]string{}, c.sites...), LastActive: c.lastActive}
		c.mu.Unlock()
		if n, ok := names[c.ID]; ok {
			p.Name, p.Email = n.name, n.email
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActive > out[j].LastActive })
	return out
}

// Name 是给人看的名字：Chrome 里这个 profile 的名字（对不上时用插件 id）
func (c *Conn) Name() string {
	if n, ok := chromeProfileNames([]*Conn{c})[c.ID]; ok && n.name != "" {
		return n.name
	}
	return c.ID
}

// Wait 等到至少有一个插件连上（替用户打开 Chrome 以后用），ctx 结束还没有返回 false
func (b *Bridge) Wait(ctx context.Context) bool {
	for {
		b.mu.Lock()
		n, wake := len(b.conns), b.wake
		b.mu.Unlock()
		if n > 0 {
			return true
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

// Conn 按 id 取一条连接
func (b *Bridge) Conn(id string) *Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conns[id]
}

// ErrNotConnected：没有插件连着
var ErrNotConnected = errors.New("browser extension not connected")

// Pick 选在哪个 Chrome profile 里打开这个地址：id 不空就是它；否则看哪个允许了这个网站——
// 只有一个就是它，有几个用最近用过的；都没允许就是最近用过的那个，needGrant 为 true（要先问用户）。
func (b *Bridge) Pick(id, raw string) (c *Conn, needGrant bool, err error) {
	if id != "" {
		if c = b.Conn(id); c == nil {
			return nil, false, i18n.Errorf("这个 Chrome 现在没连上（%s）：打开它，确认 Annulo 插件开着", "That Chrome isn't connected (%s): open it and make sure the Annulo extension is on", id)
		}
		return c, raw != "" && !c.Allows(raw), nil
	}
	b.mu.Lock()
	conns := make([]*Conn, 0, len(b.conns))
	for _, c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	if len(conns) == 0 {
		return nil, false, ErrNotConnected
	}
	recent := func(list []*Conn) *Conn {
		var best *Conn
		var at int64 = -1
		for _, c := range list {
			c.mu.Lock()
			a := c.lastActive
			c.mu.Unlock()
			if a > at {
				best, at = c, a
			}
		}
		return best
	}
	if raw == "" {
		return recent(conns), false, nil
	}
	var allowed []*Conn
	for _, c := range conns {
		if c.Allows(raw) {
			allowed = append(allowed, c)
		}
	}
	if len(allowed) > 0 {
		return recent(allowed), false, nil
	}
	return recent(conns), true, nil
}

// Grant 让用户允许 Annulo 操作这个地址所在的网站：每个 Chrome profile 的插件各弹一个小窗口，
// 用户在登录了这个网站的那个里点允许（Chrome 再确认一次），其他的自动关掉。返回用户允许了的那个，都没允许返回 nil。
// 只问一个的时候窗口上不提别的 profile。
func Grant(ctx context.Context, conns []*Conn, raw, why string) (*Conn, error) {
	if len(conns) == 0 {
		return nil, ErrNotConnected
	}
	buf := make([]byte, 8)
	rand.Read(buf)
	key := "grant-" + hex.EncodeToString(buf)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		c       *Conn
		granted bool
		err     error
	}
	answers := make(chan answer, len(conns))
	// 几个窗口长得一样，用户分不清哪个是哪个 Chrome：插件拿不到 profile 的名字，Annulo 对出来了就告诉它，写在窗口上
	names := chromeProfileNames(conns)
	for _, c := range conns {
		who := ""
		if n, ok := names[c.ID]; ok {
			who = n.name
			if n.email != "" && n.email != n.name {
				who += " · " + n.email
			}
		}
		go func() {
			res, err := c.Call(ctx, "grant", map[string]any{"url": raw, "why": why, "key": key, "multi": len(conns) > 1, "who": who})
			var r struct {
				Granted bool `json:"granted"`
			}
			if err == nil {
				err = json.Unmarshal(res, &r)
			}
			answers <- answer{c, r.Granted, err}
		}()
	}
	// 收尾：还开着的窗口关掉（用户在别的 Chrome 里允许了，或者这次运行停了）
	closeOthers := func(except *Conn) {
		for _, c := range conns {
			if c != except {
				cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
				c.Call(cctx, "cancel_grant", map[string]any{"key": key})
				ccancel()
			}
		}
	}
	var firstErr error
	for range conns {
		select {
		case a := <-answers:
			if a.granted {
				go closeOthers(a.c)
				return a.c, nil
			}
			if a.err != nil && firstErr == nil {
				firstErr = a.err
			}
		case <-ctx.Done():
			go closeOthers(nil)
			return nil, ctx.Err()
		}
	}
	if firstErr != nil && len(conns) == 1 {
		return nil, firstErr
	}
	return nil, nil
}

// Conns 是连着的插件
func (b *Bridge) Conns() []*Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Conn, 0, len(b.conns))
	for _, c := range b.conns {
		out = append(out, c)
	}
	return out
}

// Revoke 收回这个 Chrome profile 里对一个网站的授权（设置 → 浏览器）
func (c *Conn) Revoke(ctx context.Context, site string) error {
	_, err := c.Call(ctx, "revoke", map[string]any{"origin": site})
	return err
}

// Open 在这个 Chrome 里开一个新窗口（只放一个标签页），返回标签页和窗口
func (c *Conn) Open(ctx context.Context, raw string, focused bool) (tab, window int, err error) {
	res, err := c.Call(ctx, "open", map[string]any{"url": raw, "focused": focused})
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Tab    int `json:"tab"`
		Window int `json:"window"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return 0, 0, err
	}
	return r.Tab, r.Window, nil
}

// Close 关掉 Annulo 开的这个标签页（窗口里只剩它就连窗口一起关）
func (c *Conn) Close(tab int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Call(ctx, "close", map[string]any{"tab": tab})
}

// Release 把窗口交给用户：插件不再调试、不再管它，窗口留着变成用户自己的
func (c *Conn) Release(tab int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Call(ctx, "release", map[string]any{"tab": tab})
}

// Focus 把这个标签页所在的窗口调到前台
func (c *Conn) Focus(ctx context.Context, tab int) error {
	_, err := c.Call(ctx, "focus", map[string]any{"tab": tab})
	return err
}

func (c *Conn) String() string { return fmt.Sprintf("chrome(%s)", c.ID) }
