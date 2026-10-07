package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	piagent "github.com/sky-valley/pi/agent"
)

// 运营后台页面（外壳里的 iframe）的运行状态：现在开的是哪个页面、加载后报了哪些错。
//
// 外壳每次 iframe 加载完就挂上监听，把平台运行时记下的渲染错误（__TALIZEN_RENDER_ERRORS__）和
// window 的 error / unhandledrejection 报上来（POST ui/page）；外壳每 2 秒 GET ui/page 一次，
// 既是心跳，也从这里领「刷新 / 切到某个页面」的指令。
//
// 助手通过 page_errors 工具主动来查，不自动塞进对话：两个人同时在改后台时，一个人改到一半的报错
// 不该推给另一个不知情的人，否则两边会互相乱改。什么时候查、查到别人的错怎么办写在内置 annulo skill 的 pages.md 里。

const (
	pageAliveWithin = 8 * time.Second // 外壳多久没来问就当作窗口没开
	maxPageErrors   = 20
)

type pageError struct {
	Message string    `json:"message"`
	Stack   string    `json:"stack,omitempty"`
	Source  string    `json:"source,omitempty"` // render（平台运行时记的）/ error / unhandledrejection
	At      time.Time `json:"at"`
}

type pageState struct {
	mu        sync.Mutex
	ws        string // 属于哪个项目：切项目时清空
	loadID    string
	path      string
	loadedAt  time.Time
	errors    []pageError
	lastSeen  time.Time
	cmdSeq    int    // 外壳处理过的指令号小于它就执行
	cmdPath   string // 空 = 原地刷新
	loadWaker chan struct{}
}

func (p *pageState) reset(ws string) {
	if p.ws != ws { // 调用方持有锁：只清数据，不动 mu
		p.ws, p.loadID, p.path, p.loadedAt, p.errors, p.cmdPath = ws, "", "", time.Time{}, nil, ""
	}
	if p.loadWaker == nil {
		p.loadWaker = make(chan struct{})
	}
}

// GET  ui/page   外壳心跳，返回 { cmd_seq, cmd_path }
// POST ui/page   外壳报告 { load_id, path, errors: [{ message, stack, source, at }] }（每次都是这次加载以来的全部）
func (s *Server) apiPage(w http.ResponseWriter, r *http.Request) {
	p := &s.page
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reset(s.ws.ProjectID)
	p.lastSeen = time.Now()
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"cmd_seq": p.cmdSeq, "cmd_path": p.cmdPath})
		return
	}
	var in struct {
		LoadID string      `json:"load_id"`
		Path   string      `json:"path"`
		Errors []pageError `json:"errors"`
	}
	if err := readJSON(r, &in); err != nil || in.LoadID == "" {
		fail(w, http.StatusBadRequest, i18n.New("要给出 load_id", "load_id is required"))
		return
	}
	if in.LoadID != p.loadID {
		p.loadID, p.loadedAt = in.LoadID, time.Now()
		close(p.loadWaker)
		p.loadWaker = make(chan struct{})
	}
	p.path = in.Path
	if len(in.Errors) > maxPageErrors {
		in.Errors = in.Errors[:maxPageErrors]
	}
	for i := range in.Errors {
		// 本机渲染的页面，堆栈里是打包后的 app_<哈希>.js 行列：换回源码位置再给助手看
		if s.site != nil {
			in.Errors[i].Message = s.site.Symbolicate(in.Errors[i].Message)
			in.Errors[i].Stack = s.site.Symbolicate(in.Errors[i].Stack)
		}
		in.Errors[i].Message = truncateRunes(in.Errors[i].Message, 500)
		in.Errors[i].Stack = truncateRunes(in.Errors[i].Stack, 2000)
	}
	p.errors = in.Errors
	writeJSON(w, map[string]any{"ok": true})
}

const pageErrorsSchema = `{"type":"object","properties":{
  "reload":{"type":"boolean","description":"先刷新左侧后台再查（改了页面代码之后查要传 true，否则看到的可能是旧代码的状态）"},
  "path":{"type":"string","description":"项目真实的页面地址，保留查询参数，比如使用查询参数切页时传 /?view=<实际视图值>。先读页面入口和导航确认，不能把组件名猜成 /组件名。会改变用户正在看的页面；不传就查当前页面"},
  "wait_seconds":{"type":"integer","description":"刷新或切页后等多少秒再收集报错（等数据加载、异步报错），默认 5，最多 20"}
},"additionalProperties":false}`

func (s *Server) pageTools() []piagent.AgentTool {
	return []piagent.AgentTool{{
		Name:  "page_errors",
		Label: "查后台页面报错",
		Description: "查左侧项目页面在浏览器里运行时的报错（渲染错误、未捕获的异常，带调用栈）。curl 拿不到这些：页面返回 200 也可能白屏。" +
			"改完项目页面后用它确认（reload: true，path 传改的页面）；用户说页面空白、报错时先用它拿报错原文。" +
			"返回 { open, page, loaded_at, errors }；open 为 false 说明用户没开着 Annulo 的窗口，查不到。",
		Parameters: mustSchema(pageErrorsSchema),
		Execute: func(ctx context.Context, _ string, p map[string]any, _ piagent.ToolUpdateFunc) (piagent.AgentToolResult, error) {
			reload, _ := p["reload"].(bool)
			path, _ := p["path"].(string)
			wait := 5
			if v, ok := p["wait_seconds"].(float64); ok {
				wait = min(max(int(v), 0), 20)
			}
			return jsonResult(s.checkPage(ctx, reload, strings.TrimSpace(path), time.Duration(wait)*time.Second)), nil
		},
	}}
}

func (s *Server) checkPage(ctx context.Context, reload bool, path string, wait time.Duration) map[string]any {
	p := &s.page
	p.mu.Lock()
	p.reset(s.ws.ProjectID)
	if time.Since(p.lastSeen) > pageAliveWithin {
		p.mu.Unlock()
		return map[string]any{"open": false, "note": i18n.T("用户现在没开着 Annulo 的窗口（左侧后台没在运行），拿不到页面状态；请用户打开 Annulo 再查", "The user doesn't have the Annulo window open (the back office isn't running), so there's no page state; ask them to open Annulo, then check again")}
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if reload || path != "" {
		p.cmdSeq++
		p.cmdPath = path
		waker := p.loadWaker
		p.mu.Unlock()
		// 外壳 2 秒内来领指令，页面再加载几秒
		select {
		case <-waker:
		case <-time.After(20 * time.Second):
			return map[string]any{"open": true, "note": i18n.T("左侧后台 20 秒内没有重新加载完，可能窗口被关了或者页面卡住；稍后再查一次", "The back office didn't finish reloading within 20 seconds; the window may be closed or the page stuck. Check again later")}
		case <-ctx.Done():
			return map[string]any{"open": true, "note": i18n.T("已取消", "Canceled")}
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		p.mu.Lock()
	}
	defer p.mu.Unlock()
	out := map[string]any{"open": true, "page": p.path, "loaded_at": p.loadedAt, "errors": p.errors}
	if len(p.errors) == 0 {
		out["note"] = i18n.T("这个页面加载后没有报错", "No errors since this page loaded")
	} else {
		out["note"] = i18n.T("报错可能来自别人正在改的代码：先看调用栈里的文件是不是这次对话里改过的，不是就先告诉用户，别直接改", "The errors may come from code someone else is editing: check whether the files in the stack were changed in this chat; if not, tell the user first instead of changing them")
	}
	return out
}

// POST ui/symbolicate { text } → { text }：本机渲染的页面把报错堆栈换回源码位置（页面里的报错框用）。
func (s *Server) apiSymbolicate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.site != nil {
		in.Text = s.site.Symbolicate(in.Text)
	}
	writeJSON(w, map[string]any{"text": in.Text})
}
