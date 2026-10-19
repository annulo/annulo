package localfn

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/annulo/annulo/internal/i18n"
)

// b.agent(task, opts)：让 AI 看着页面完成一件事（比如在投稿页把一条视频投出去），脚本只当它的工具。方案见 docs/browser-agent.md。
// 循环写在 agent.js（JS 里插件的工具可以正常 await）；这里给它几样原语（都挂在页面对象上，名字带 __ 的只给 agent.js 用）：
//
//	b.observe()              看页面：可操作元素（编号、文字、所属表单项的标签、是否必填、当前值）、上传框、弹窗、提示文字
//	b.__clickId(id)          点 observe 给的编号（平时藏着、悬停才出来的浮层在页面里 element.click()）
//	b.__llm(system, prompt)  问模型：设置里的浏览器助手模型（没选用对话的模型）、不思考（Host.AgentLLM）。主导一整件事要会推理，b.act 用的便宜快模型做不好（2026-10-19 实测：回乱码、一次回好几轮）
//	b.__notes(key)           读笔记：插件自带的 plugins/<插件>/notes/<key>.md 和项目自己的 user/notes/<插件>/<key>.md
//	b.__saveNotes(key, text) 写项目自己的那份笔记
//	b.__sleep(ms) / b.__progress(text) / b.__debug(text)

//go:embed agent.js
var agentJS string

// agentObserveJS 收集页面上看得见的可操作元素（和 b.act 同一个编号属性 data-annulo-act，actClick 能直接点），
// 再给每个元素找它属于哪个表单项（标签文字、有没有必填的 *）、现在的值；另外列出上传框、开着的弹窗、页面上的提示文字。
const agentObserveJS = `(() => {
  document.querySelectorAll('[data-annulo-act]').forEach(e => e.removeAttribute('data-annulo-act'))
  document.querySelectorAll('[data-annulo-file]').forEach(e => e.removeAttribute('data-annulo-file'))
  // a 不只收带 href 的：Vue / React 的页面里很多按钮是没有 href 的 <a>（2026-10 B站 稿件管理的「编辑」「⋮」）
  const sel = 'a,button,input,textarea,select,[role="button"],[role="link"],[role="menuitem"],[role="tab"],[role="checkbox"],[role="radio"],[role="textbox"],[role="option"],[role="switch"],[role="combobox"],[contenteditable="true"],[contenteditable=""],[tabindex]:not([tabindex="-1"])'
  const vw = innerWidth, vh = innerHeight
  const clip = (s, n) => { s = String(s || '').replace(/\s+/g, ' ').trim(); return s.length > n ? s.slice(0, n) + '…' : s }
  const hiddenStyle = s => s.display === 'none' || s.visibility === 'hidden' || Number(s.opacity) === 0
  const sized = e => { const r = e.getBoundingClientRect(); return r.width >= 2 && r.height >= 2 }
  const shown = e => { for (let p = e; p && p !== document.body; p = p.parentElement) if (hiddenStyle(getComputedStyle(p))) return false; return true }
  const dialogOf = e => e.closest('[role="dialog"],[aria-modal="true"],[class*="modal" i],[class*="dialog" i],[class*="popup" i],[class*="drawer" i]')
  // 表单项的标签：<label for>、aria-label(ledby)、往上几层里「在它前面」的短文字（「标题」「* 分区」这类）
  const fieldOf = e => {
    let label = ''
    if (e.id) { const l = document.querySelector('label[for="' + CSS.escape(e.id) + '"]'); if (l) label = l.innerText }
    const lb = e.getAttribute('aria-labelledby'); if (!label && lb) label = lb.split(/\s+/).map(id => document.getElementById(id)?.innerText || '').join(' ')
    let box = e, required = e.required || e.getAttribute('aria-required') === 'true'
    for (let i = 0; !label && box && i < 6; i++) {
      box = box.parentElement
      if (!box) break
      for (let s = box.previousElementSibling, j = 0; s && j < 2; s = s.previousElementSibling, j++) {
        const t = clip(s.innerText, 30)
        if (t && !s.querySelector(sel)) { label = t; break }
      }
      if (!label) {
        const first = [...box.children].find(c => !c.contains(e) && !c.querySelector(sel) && clip(c.innerText, 30))
        if (first && first.compareDocumentPosition(e) & Node.DOCUMENT_POSITION_FOLLOWING) label = first.innerText
      }
    }
    label = clip(label, 30)
    if (/^\*|\*$/.test(label)) { required = true; label = label.replace(/^\*\s*|\s*\*$/g, '') }
    if (box && !required && box.querySelector('[class*="required" i]')) required = true
    return { label, required }
  }
  const items = []
  for (const e of document.querySelectorAll(sel)) {
    if (e.matches('input[type="file"]')) continue
    if (!sized(e)) continue
    const s = getComputedStyle(e)
    if (hiddenStyle(s)) continue
    if (e.matches('[tabindex]:not(a,button,input,textarea,select,[role])') && e.querySelector(sel)) continue
    const editable = e.isContentEditable || e.tagName === 'INPUT' || e.tagName === 'TEXTAREA' || e.tagName === 'SELECT'
    const text = editable ? '' : clip(e.innerText || e.value, 80)
    const icon = e.getAttribute('aria-label') ? null : e.querySelector('[aria-label], img[alt], svg title')
    const label = clip(e.getAttribute('aria-label') || (icon && (icon.getAttribute('aria-label') || icon.getAttribute('alt') || icon.textContent)) || '', 60)
    let hint = clip(e.getAttribute('placeholder') || e.getAttribute('title') || e.getAttribute('data-placeholder') || '', 60)
    // 只有一个字体图标的按钮（⋮、×、齿轮）：没字、没 aria-label，用图标的 class 名说明它（icon_list_more_x 一看就是「更多」）
    if (!text && !label && !hint && !editable) {
      const cls = [e, ...e.querySelectorAll('i,svg,span,use')].map(x => String(x.className && x.className.baseVal !== undefined ? x.className.baseVal : x.className || '') + ' ' + (x.getAttribute('href') || x.getAttribute('xlink:href') || '')).join(' ')
      const icon = cls.split(/\s+/).find(c => /icon|more|close|delete|edit|setting|menu|search/i.test(c) && c.length > 3)
      if (icon) hint = 'icon: ' + clip(icon, 50)
    }
    if (!text && !label && !hint && !editable) continue
    const r = e.getBoundingClientRect()
    const inView = r.bottom > 0 && r.right > 0 && r.top < vh && r.left < vw
    const dialog = !!dialogOf(e)
    const it = { e, rank: (dialog ? 0 : 2) + (inView ? 0 : 1), tag: e.tagName.toLowerCase(), role: e.getAttribute('role') || '', text, label, hint,
      type: e.getAttribute('type') || '', dialog, disabled: !!(e.disabled || e.getAttribute('aria-disabled') === 'true'), editable, rich: !!e.isContentEditable }
    if (editable) {
      Object.assign(it, fieldOf(e))
      it.field = it.label; it.label = label
      it.value = clip(e.isContentEditable ? e.innerText : e.type === 'checkbox' || e.type === 'radio' ? (e.checked ? 'checked' : '') : e.value, 200)
    } else if (/^(radio|checkbox|switch|option|tab)$/.test(it.role) || /checked|selected|active/i.test(e.className)) {
      it.checked = e.getAttribute('aria-checked') === 'true' || e.getAttribute('aria-selected') === 'true' || /(^|[^a-z])(checked|selected|active)/i.test(String(e.className))
    }
    items.push(it)
  }
  // 不是按钮、链接但点得动的入口（手形的最外层）、平时藏着悬停才出来的浮层：和 b.act 一样列进来
  for (const e of document.body.querySelectorAll('div,span,li,p,label,img,section')) {
    if (e.matches(sel) || e.closest(sel) || e.querySelector(sel)) continue
    const s = getComputedStyle(e)
    const parent = e.parentElement
    let target = e, hover = false
    if (hiddenStyle(s)) {
      if (!parent || !sized(parent) || !shown(parent)) continue
      target = [e, ...e.querySelectorAll('*')].find(x => getComputedStyle(x).cursor === 'pointer')
      if (!target) continue
      hover = true
    } else if (s.cursor !== 'pointer' || (parent && getComputedStyle(parent).cursor === 'pointer') || !sized(e) || !shown(e)) continue
    const text = clip(target.textContent, 60)
    const label = clip(target.getAttribute('aria-label') || target.getAttribute('alt') || '', 60)
    const img = !text && !label && (target.tagName === 'IMG' || target.querySelector('img') || /url\(/.test(s.backgroundImage))
    if ((!text && !label && !img) || text.length > (hover ? 20 : 40)) continue
    const r = (hover ? parent : e).getBoundingClientRect()
    const inView = r.bottom > 0 && r.right > 0 && r.top < vh && r.left < vw
    const dialog = !!dialogOf(e)
    items.push({ e: target, rank: (dialog ? 0 : 2) + (inView ? 0 : 1) + (hover ? 0.5 : 0), tag: target.tagName.toLowerCase(), role: '', text: text || (img ? '(image)' : ''), label,
      hint: clip(target.getAttribute('title') || '', 60), type: '', dialog, disabled: false, editable: false, hover })
  }
  items.sort((a, b) => a.rank - b.rank)
  const kept = items.slice(0, 220)
  kept.forEach((x, i) => x.e.setAttribute('data-annulo-act', String(i + 1)))
  // 列表里的一行（作品卡片、表格行）：按钮只写「删除」「编辑」，认这一行是哪条要看行里的标题、日期这些不能点的字，
  // 只列元素的话 AI 分不出哪个「删除」是哪一条（2026-10 抖音作品管理）。
  // 从元素往上找第一个「有同样 tag 和 class 的兄弟、自己还有不属于任何编号元素的字」的容器当行，行的字单独列出来，元素标 row=<n>
  const ownText = new Map()
  const textOf = box => {
    if (ownText.has(box)) return ownText.get(box)
    let t = ''
    const w = document.createTreeWalker(box, NodeFilter.SHOW_TEXT)
    for (let n = w.nextNode(); n && t.length < 160; n = w.nextNode()) {
      const s = n.nodeValue.replace(/\s+/g, ' ').trim(), el = n.parentElement
      if (!s || !el || /^(SCRIPT|STYLE)$/.test(el.tagName)) continue
      const act = el.closest('[data-annulo-act]')
      if (act && box.contains(act)) continue
      if (!el.offsetParent && getComputedStyle(el).position !== 'fixed') continue
      t += (t ? ' · ' : '') + s
    }
    t = clip(t, 140)
    ownText.set(box, t)
    return t
  }
  const repeated = box => {
    const par = box.parentElement
    if (!par || box === document.body) return false
    const cls = String(box.className || '')
    if (!cls && !/^(LI|TR|ARTICLE)$/.test(box.tagName) && !/^(row|listitem|article)$/.test(box.getAttribute('role') || '')) return false
    return [...par.children].some(c => c !== box && c.tagName === box.tagName && String(c.className || '') === cls)
  }
  const rowIds = new Map(), rows = []
  const rowOf = e => {
    for (let box = e.parentElement, i = 0; box && box !== document.body && i < 10; box = box.parentElement, i++) {
      if (!repeated(box) || !textOf(box)) continue
      if (!rowIds.has(box)) { rows.push({ id: rows.length + 1, text: textOf(box) }); rowIds.set(box, rows.length) }
      return rowIds.get(box)
    }
    return 0
  }
  const list = kept.map((x, i) => { const { e, rank, ...rest } = x; const row = x.dialog ? 0 : rowOf(e); return { id: i + 1, ...rest, ...(row ? { row } : {}) } })
  // 上传框：一般是藏起来的 <input type=file>，按附近的文字说明它是传什么的
  const files = [...document.querySelectorAll('input[type="file"]')].map((e, i) => {
    e.setAttribute('data-annulo-file', String(i + 1))
    let near = ''
    for (let p = e.parentElement, j = 0; p && j < 5 && !near; p = p.parentElement, j++) near = clip(p.innerText, 60)
    return { id: i + 1, accept: e.getAttribute('accept') || '', multiple: e.multiple, near, usable: shown(e.parentElement || e) || sized(e.parentElement || e) }
  })
  const dialogs = [...document.querySelectorAll('[role="dialog"],[aria-modal="true"],[class*="modal" i],[class*="dialog" i]')].filter(e => sized(e) && shown(e)).map(e => clip(e.innerText, 160)).filter(Boolean).slice(0, 4)
  const notices = [...document.querySelectorAll('[role="alert"],[class*="toast" i],[class*="message" i],[class*="notice" i],[class*="error" i],[class*="warn" i],[class*="tip" i]')]
    .filter(e => sized(e) && shown(e) && !e.querySelector(sel)).map(e => clip(e.innerText, 100)).filter(t => t && t.length > 1)
  return JSON.stringify({ url: location.href, title: document.title, list, rows: rows.slice(0, 60), files, dialogs, notices: [...new Set(notices)].slice(0, 12),
    scroll: { y: Math.round(scrollY), height: document.documentElement.scrollHeight, view: vh } })
})()`

// AgentTool 是 b.agent 交给模型的一个工具（动作），Params 是参数的 JSON Schema
type AgentTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Params      map[string]any `json:"params"`
}

// AgentReply 是模型这一轮的回答：一句想法 + 调用的工具（按顺序），和这一轮的用量
type AgentReply struct {
	Text  string      `json:"text"`
	Calls []AgentCall `json:"calls"`
	Usage AgentUsage  `json:"usage"`
}

// AgentUsage 是一次问模型的用量（token）
type AgentUsage struct {
	Model      string `json:"model"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
}

type AgentCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// ErrNoToolCalls：模型走不了函数调用（本机 agent 这类），b.agent 退回让模型写 JSON 文字
var ErrNoToolCalls = errors.New("no tool calls")

// agentLLMTimeout 是 b.agent 每次问模型最多等多久
const agentLLMTimeout = 90 * time.Second

var noteKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)

// notePaths 是笔记 key 对应的两份文件：插件自带的（只读，跟插件升级）、项目自己的（user/ 下，模板和插件都不写，升级不冲突）。
// 插件里的函数（plugins/<id>/local/…）用 plugins/<id>/notes/ 和 user/notes/<id>/；项目自己的函数只有 user/notes/。
func (r *runner) notePaths(key string) (seed, mine string, err error) {
	if !noteKeyRe.MatchString(key) {
		return "", "", i18n.Errorf("笔记名不对：%q（小写字母、数字、. _ -，比如 bilibili.publish）", "Invalid notes name: %q (lowercase letters, digits, . _ -, e.g. bilibili.publish)", key)
	}
	if r.h.WorkDir == "" {
		return "", "", i18n.New("这里没有项目目录，记不了笔记", "No project directory here, so notes can't be kept")
	}
	if pl, _, ok := strings.Cut(r.name, "/"); ok && pl != "" {
		return filepath.Join(r.h.WorkDir, "plugins", pl, "notes", key+".md"), filepath.Join(r.h.WorkDir, "user", "notes", pl, key+".md"), nil
	}
	return "", filepath.Join(r.h.WorkDir, "user", "notes", key+".md"), nil
}

func readNote(path string) string {
	if path == "" {
		return ""
	}
	b, _ := os.ReadFile(path)
	return string(b)
}

// agentMethods 把 agent 用的原语挂到页面对象上，再把 b.agent 接到 agent.js 的循环。
func (r *runner) agentMethods(o *goja.Object, p BrowserPage) {
	o.Set("observe", func() *goja.Promise {
		v, err := p.Eval(r.ctx, agentObserveJS)
		if err != nil {
			return r.rejected(err)
		}
		var page any
		if s, ok := v.(string); !ok || json.Unmarshal([]byte(s), &page) != nil {
			return r.rejected(i18n.New("读不出页面", "Couldn't read the page"))
		}
		return r.resolved(page)
	})
	// 点：先真实的鼠标点击；点不了（下拉菜单里的选项、弹窗里的按钮常过不了「看得见」的检查，2026-10-19 B站 实测）
	// 再在页面里派发一整套按下、松开、点击事件（有的下拉在 mousedown 时选中）
	o.Set("__clickId", func(id int, hover bool) *goja.Promise {
		e := actElement{ID: id, Hover: hover}
		if err := actClick(r.ctx, p, e); err == nil || hover {
			return r.agentDone(err)
		}
		v, err := p.Eval(r.ctx, fmt.Sprintf(`(() => {
  const e = document.querySelector('[data-annulo-act="%d"]'); if (!e) return false
  e.scrollIntoView({ block: 'center' })
  const r = e.getBoundingClientRect(), o = { bubbles: true, cancelable: true, view: window, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2 }
  for (const t of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) e.dispatchEvent(new (t.startsWith('pointer') ? PointerEvent : MouseEvent)(t, o))
  return true
})()`, id))
		if err == nil && v != true {
			err = fmt.Errorf("element %d is gone", id)
		}
		return r.agentDone(err)
	})
	// __hover(id)：真的把鼠标移上去（:hover 才显示的按钮要这样）；这个浏览器做不到时退回在页面里派发移入事件
	o.Set("__hover", func(id int) *goja.Promise {
		sel := fmt.Sprintf(`[data-annulo-act="%d"]`, id)
		if h, ok := p.(interface {
			Hover(context.Context, string) error
		}); ok {
			if err := h.Hover(r.ctx, sel); err == nil {
				return r.resolved(true)
			}
		}
		_, err := p.Eval(r.ctx, fmt.Sprintf(`(() => { const x = document.querySelector(%q); if (!x) return false; x.scrollIntoView({ block: "center" }); const r = x.getBoundingClientRect(), o = { bubbles: true, cancelable: true, view: window, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2 }; for (const t of ["pointerover", "pointerenter", "mouseover", "mouseenter", "mousemove"]) x.dispatchEvent(new (t.startsWith("pointer") ? PointerEvent : MouseEvent)(t, o)); return true })()`, sel))
		return r.agentDone(err)
	})
	o.Set("__llm", func(system, prompt string) *goja.Promise {
		f := r.h.AgentLLM
		if f == nil {
			f = r.h.LLM
		}
		if f == nil {
			f = r.h.BrowserLLM
		}
		if f == nil {
			return r.rejected(i18n.New("这里不能用浏览器助手（b.agent）", "The browser assistant (b.agent) isn't available here"))
		}
		// 每次最多等 90 秒、超时重试一次：模型请求偶尔卡住不回，没有超时整件事就一直挂着（2026-10-19 B站 投稿卡在第 5 步）
		var out string
		var err error
		for try := 0; try < 2; try++ {
			c, cancel := context.WithTimeout(r.ctx, agentLLMTimeout)
			out, err = f(c, system, prompt)
			cancel()
			if err == nil || r.ctx.Err() != nil {
				break
			}
			r.emit(Event{Type: "log", Data: map[string]any{"level": "warn", "message": i18n.Tf("浏览器助手问模型没回（%v），重试", "The browser assistant's model call failed (%v); retrying", err)}})
		}
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(out)
	})
	// __llmTools(system, prompt, tools)：用函数调用问模型 → { text, calls }；这台 Annulo / 这个模型不支持时 { unsupported: true }
	o.Set("__llmTools", func(system, prompt string, tools goja.Value) *goja.Promise {
		if r.h.AgentTools == nil {
			return r.resolved(map[string]any{"unsupported": true})
		}
		var ts []AgentTool
		b, _ := json.Marshal(tools.Export())
		if err := json.Unmarshal(b, &ts); err != nil {
			return r.rejected(err)
		}
		var rep *AgentReply
		var err error
		for try := 0; try < 2; try++ {
			c, cancel := context.WithTimeout(r.ctx, agentLLMTimeout)
			rep, err = r.h.AgentTools(c, system, prompt, ts)
			cancel()
			if err == nil || errors.Is(err, ErrNoToolCalls) || r.ctx.Err() != nil {
				break
			}
			r.emit(Event{Type: "log", Data: map[string]any{"level": "warn", "message": i18n.Tf("浏览器助手问模型没回（%v），重试", "The browser assistant's model call failed (%v); retrying", err)}})
		}
		if errors.Is(err, ErrNoToolCalls) || (err == nil && rep == nil) {
			return r.resolved(map[string]any{"unsupported": true})
		}
		if err != nil {
			return r.rejected(err)
		}
		calls := make([]any, 0, len(rep.Calls))
		for _, c := range rep.Calls {
			args := c.Args
			if args == nil {
				args = map[string]any{}
			}
			calls = append(calls, map[string]any{"name": c.Name, "args": args})
		}
		u := rep.Usage
		return r.resolved(map[string]any{"text": rep.Text, "calls": calls, "usage": map[string]any{"model": u.Model, "input": u.Input, "output": u.Output, "cache_read": u.CacheRead, "cache_write": u.CacheWrite}})
	})
	o.Set("__notes", func(key string) *goja.Promise {
		seed, mine, err := r.notePaths(key)
		if err != nil {
			return r.rejected(err)
		}
		return r.resolved(map[string]any{"seed": readNote(seed), "mine": readNote(mine)})
	})
	o.Set("__saveNotes", func(key, text string) *goja.Promise {
		_, mine, err := r.notePaths(key)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(mine), 0o755)
		}
		if err == nil {
			err = os.WriteFile(mine, []byte(strings.TrimSpace(text)+"\n"), 0o644)
		}
		return r.agentDone(err)
	})
	o.Set("__sleep", func(ms int64) *goja.Promise {
		actSleep(r.ctx, time.Duration(min(max(ms, 0), 60_000))*time.Millisecond)
		return r.agentDone(r.ctx.Err())
	})
	// __progress(text, extra?)：extra 的字段一起放进进度事件（结束时的耗时、用量汇总 summary，页面单独显示）
	o.Set("__progress", func(text string, extra goja.Value) {
		data := map[string]any{"message": text}
		if extra != nil && !goja.IsUndefined(extra) && !goja.IsNull(extra) {
			if m, ok := toPlain(extra.Export()).(map[string]any); ok {
				for k, v := range m {
					data[k] = v
				}
			}
		}
		r.emit(Event{Type: "progress", Data: data})
	})
	o.Set("__debug", func(text string) {
		if r.h.BrowserDebug != nil && r.h.BrowserDebug() {
			r.emit(Event{Type: "log", Data: map[string]any{"level": "info", "message": "[agent] " + text}})
		}
	})
	o.Set("__log", func(text string) {
		r.emit(Event{Type: "log", Data: map[string]any{"level": "info", "message": text}})
	})
	o.Set("__locale", func() string { return i18n.Locale() })
	// agent(task, opts)：agent.js 里的循环（第一次用时编译一次），失败和别的浏览器操作一样存现场
	o.Set("agent", func(task goja.Value, opts goja.Value) goja.Value {
		fn, err := r.agentFn()
		if err != nil {
			return r.vm.ToValue(r.rejected(err))
		}
		v, err := fn(goja.Undefined(), o, task, opts)
		if err != nil {
			return r.vm.ToValue(r.rejected(fmt.Errorf("%v", err)))
		}
		return v
	})
}

func (r *runner) agentDone(err error) *goja.Promise {
	if err != nil {
		return r.rejected(err)
	}
	return r.resolved(goja.Undefined())
}

func (r *runner) agentFn() (goja.Callable, error) {
	if r.agentLoop != nil {
		return r.agentLoop, nil
	}
	v, err := r.vm.RunScript("agent.js", agentJS)
	if err != nil {
		return nil, err
	}
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return nil, fmt.Errorf("agent.js must evaluate to a function")
	}
	r.agentLoop = fn
	return fn, nil
}
