package extbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/annulo/annulo/internal/i18n"
)

// Endpoint 是给 chromedp 连的假浏览器（DevTools 的浏览器级 WebSocket）：后面只有插件开的那一个标签页。
//
// chromedp 先在浏览器级管标签页（Target.*），再对标签页的会话发命令。浏览器级的这边自己答（只有一个标签页），
// 标签页会话里的命令经插件的 chrome.debugger 转给 Chrome，事件带上会话 id 推回去。
// 这样 internal/browser 打开独立 Chrome 和用户自己的 Chrome 走的是同一套代码。
type Endpoint struct {
	URL string // ws://127.0.0.1:<端口>/devtools/browser/<随机>

	c      *Conn
	tab    int
	window int
	ln     net.Listener
	srv    *http.Server
	unsub  func()

	mu      sync.Mutex
	ws      *websocket.Conn
	gone    bool   // 标签页没了（被关、跑出允许的网站、用户点了调试条上的「取消」）
	why     string // 为什么没了，给人看的
	keep    bool   // 结束时把窗口交给用户（Keep），不关
	closeMu sync.Once
}

// Endpoint 给这个标签页开一个假浏览器端点
func (c *Conn) Endpoint(tab, window int) (*Endpoint, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 16)
	rand.Read(buf)
	path := "/devtools/browser/" + hex.EncodeToString(buf)
	e := &Endpoint{URL: "ws://" + ln.Addr().String() + path, c: c, tab: tab, window: window, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc(path, e.serve)
	e.srv = &http.Server{Handler: mux}
	go e.srv.Serve(ln)
	e.unsub = c.Subscribe(tab, e.onEvent)
	return e, nil
}

// Close 关掉端点（不关标签页：那是 Conn.Close）
func (e *Endpoint) Close() {
	e.closeMu.Do(func() {
		e.unsub()
		e.mu.Lock()
		if e.ws != nil {
			e.ws.Close(websocket.StatusNormalClosure, "")
		}
		e.mu.Unlock()
		e.srv.Close()
	})
}

func (e *Endpoint) targetID() string  { return "annulo-tab-" + strconv.Itoa(e.tab) }
func (e *Endpoint) sessionID() string { return "annulo-session-" + strconv.Itoa(e.tab) }

func (e *Endpoint) targetInfo() map[string]any {
	return map[string]any{"targetId": e.targetID(), "type": "page", "title": "", "url": "", "attached": true, "canAccessOpener": false, "browserContextId": "annulo"}
}

type cdpMsg struct {
	ID        int64           `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId"`
}

func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 20)
	e.mu.Lock()
	if e.ws != nil {
		e.mu.Unlock()
		ws.Close(websocket.StatusPolicyViolation, "one client only")
		return
	}
	e.ws = ws
	e.mu.Unlock()
	ctx := r.Context()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m cdpMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		go e.handle(ctx, m)
	}
}

func (e *Endpoint) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	e.mu.Lock()
	ws := e.ws
	e.mu.Unlock()
	if ws == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// coder/websocket 的 Write 可以并发调用
	ws.Write(ctx, websocket.MessageText, b)
}

func (e *Endpoint) result(m cdpMsg, res any) {
	out := map[string]any{"id": m.ID, "result": res}
	if m.SessionID != "" {
		out["sessionId"] = m.SessionID
	}
	e.send(out)
}

func (e *Endpoint) fail(m cdpMsg, err error) {
	out := map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": err.Error()}}
	if m.SessionID != "" {
		out["sessionId"] = m.SessionID
	}
	e.send(out)
}

func (e *Endpoint) event(method string, params any, session bool) {
	out := map[string]any{"method": method, "params": params}
	if session {
		out["sessionId"] = e.sessionID()
	}
	e.send(out)
}

func (e *Endpoint) handle(ctx context.Context, m cdpMsg) {
	if m.SessionID == "" {
		e.browserLevel(ctx, m)
		return
	}
	// 窗口、标签页的命令 chromedp 有时在标签页会话里发（Reveal 的 Browser.getWindowForTarget、Target.activateTarget）：
	// 和浏览器级的一样自己答，窗口交给插件（插件不转 Browser.* / Target.*）
	switch {
	case strings.HasPrefix(m.Method, "Browser."), m.Method == "Target.activateTarget", m.Method == "Target.closeTarget", m.Method == "Target.getTargetInfo":
		e.browserLevel(ctx, m)
		return
	case strings.HasPrefix(m.Method, "Target."):
		// 子目标（iframe、worker）不自动接，其余 Target.* 当成做了
		e.result(m, map[string]any{})
		return
	}
	e.mu.Lock()
	gone := e.gone
	e.mu.Unlock()
	if gone {
		e.fail(m, errors.New("the tab is gone"))
		return
	}
	var params any = map[string]any{}
	if len(m.Params) > 0 && string(m.Params) != "null" {
		params = m.Params
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := e.c.Call(cctx, "cdp", map[string]any{"tab": e.tab, "method": m.Method, "params": params})
	if err != nil {
		e.fail(m, err)
		return
	}
	if len(res) == 0 || string(res) == "null" {
		res = json.RawMessage("{}")
	}
	e.result(m, res)
}

// browserLevel 答浏览器级的命令：只有这一个标签页，窗口操作交给插件
func (e *Endpoint) browserLevel(ctx context.Context, m cdpMsg) {
	switch m.Method {
	case "Target.setDiscoverTargets":
		e.result(m, map[string]any{})
		e.event("Target.targetCreated", map[string]any{"targetInfo": e.targetInfo()}, false)
	case "Target.createTarget":
		e.result(m, map[string]any{"targetId": e.targetID()})
		e.event("Target.targetCreated", map[string]any{"targetInfo": e.targetInfo()}, false)
	case "Target.attachToTarget":
		e.result(m, map[string]any{"sessionId": e.sessionID()})
	case "Target.getTargets":
		e.result(m, map[string]any{"targetInfos": []any{e.targetInfo()}})
	case "Target.getTargetInfo":
		e.result(m, map[string]any{"targetInfo": e.targetInfo()})
	case "Target.activateTarget":
		if err := e.c.Focus(ctx, e.tab); err != nil {
			e.fail(m, err)
			return
		}
		e.result(m, map[string]any{})
	case "Target.closeTarget":
		e.CloseTab()
		e.result(m, map[string]any{"success": true})
	case "Target.detachFromTarget", "Target.setAutoAttach":
		e.result(m, map[string]any{})
	case "Browser.getVersion":
		e.result(m, map[string]any{"protocolVersion": "1.3", "product": "Chrome (Annulo extension)", "revision": "", "userAgent": "", "jsVersion": ""})
	case "Browser.getWindowForTarget":
		e.result(m, map[string]any{"windowId": e.window, "bounds": map[string]any{"windowState": "normal"}})
	case "Browser.setWindowBounds":
		// 只认「恢复成普通窗口」（Reveal 的第一步）：调到前台；挪位置不管，窗口在用户自己的屏幕上
		var p struct {
			Bounds struct {
				WindowState string `json:"windowState"`
			} `json:"bounds"`
		}
		json.Unmarshal(m.Params, &p)
		if p.Bounds.WindowState == "normal" {
			if err := e.c.Focus(ctx, e.tab); err != nil {
				e.fail(m, err)
				return
			}
		}
		e.result(m, map[string]any{})
	case "Browser.close":
		e.CloseTab()
		e.result(m, map[string]any{})
	default:
		e.fail(m, errors.New("not available in your own Chrome: "+m.Method))
	}
}

// KeepForUser：结束时窗口留给用户，不关（失败了用户正看着，见 internal/browser 的 Keep）
func (e *Endpoint) KeepForUser() {
	e.mu.Lock()
	e.keep = true
	e.mu.Unlock()
}

// CloseTab 是 chromedp 要关标签页时：留给用户的交出去，否则关掉
func (e *Endpoint) CloseTab() {
	e.mu.Lock()
	keep := e.keep
	e.mu.Unlock()
	if keep {
		e.c.Release(e.tab)
	} else {
		e.c.Close(e.tab)
	}
}

// Gone 是标签页不能再操作的原因（被关了是空串：照常当浏览器关了）
func (e *Endpoint) Gone() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.why
}

// onEvent：插件推来的这个标签页的事件
func (e *Endpoint) onEvent(ev Event) {
	switch ev.Kind {
	case "cdp":
		var params any = json.RawMessage("{}")
		if len(ev.Params) > 0 {
			params = ev.Params
		}
		e.event(ev.Method, params, true)
	case "closed", "detached", "blocked":
		// 标签页没了，或者插件不再让操作它：告诉 chromedp 会话断了，internal/browser 收到就把这个浏览器当关了
		e.mu.Lock()
		already := e.gone
		e.gone = true
		switch {
		case ev.Kind == "blocked":
			e.why = i18n.Errorf("页面跳到了没允许 Annulo 操作的网站（%s），已停止操作", "The page went to a site Annulo isn't allowed on (%s), so Annulo stopped", ev.URL).Error()
		case ev.Kind == "detached" && ev.Reason == "canceled_by_user":
			e.why = i18n.T("你在浏览器的调试提示条上点了「取消」，Annulo 停止操作这个窗口", "You clicked Cancel on the browser's debugging bar, so Annulo stopped working in this window")
		case ev.Kind == "detached":
			e.why = i18n.T("浏览器断开了 Annulo 对这个窗口的操作", "The browser detached Annulo from this window")
		}
		e.mu.Unlock()
		if already {
			return
		}
		e.event("Target.detachedFromTarget", map[string]any{"sessionId": e.sessionID(), "targetId": e.targetID()}, false)
		if ev.Kind == "closed" {
			e.event("Target.targetDestroyed", map[string]any{"targetId": e.targetID()}, false)
		}
	}
}
