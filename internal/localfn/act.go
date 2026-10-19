package localfn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
)

// b.act(goal, { until, avoid, maxSteps })：脚本某一步认不出来时，让浏览器助手（一个便宜快速的模型）看页面帮这一步，过了就还给脚本。
// 方案见 docs/browser-act.md。每一步：收集页面上看得见的可操作元素（编号、文字、aria-label、角色）→ 问模型要一个动作 → 真实的鼠标键盘执行
// → 看 until 出现没有。成功时把最后点的元素按「网站 + 目标」记在 <数据目录>/cache/browser-act.json，下次先直接试它，满足 until 就不问模型。
// 不传截图（便宜、快）；不许跳到别的网址；avoid 里的按钮（分享、发布这类发出去收不回来的）拒绝点。

const (
	actDefaultSteps = 6
	actMaxSteps     = 12
	actTimeout      = 2 * time.Minute
	actMaxBad       = 3 // 模型连续几次回得不对（不是 JSON、编号不存在、点了 avoid）就放弃
	// 交给人（验证码、重新登录这类）：把浏览器调到前台等人处理，最多等这么久；等人的时间不算进 actTimeout，一次求助最多交给人两次
	actHandoffWait = 10 * time.Minute
	actMaxHandoffs = 2
)

type actOptions struct {
	Until    string
	Avoid    []string
	MaxSteps int
}

// actElement 是清单里的一个元素（页面里打了 data-annulo-act="<id>"）
type actElement struct {
	ID       int    `json:"id"`
	Tag      string `json:"tag"`
	Role     string `json:"role,omitempty"`
	Text     string `json:"text,omitempty"`
	Label    string `json:"label,omitempty"`
	Hint     string `json:"hint,omitempty"` // placeholder / title
	Type     string `json:"type,omitempty"`
	Dialog   bool   `json:"dialog,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Editable bool   `json:"editable,omitempty"`
	Hover    bool   `json:"hover,omitempty"` // 平时透明、鼠标移上去才显示（缩略图上的「编辑封面」浮层这类）
}

type actPage struct {
	URL        string       `json:"url"`
	Title      string       `json:"title"`
	List       []actElement `json:"list"`
	DialogOnly bool         `json:"dialogOnly"` // 开着弹窗：清单里只有弹窗里的元素
}

// actSnapshotJS 收集看得见的可操作元素，打上编号。弹窗里的、视口里的排前面，最多 150 个。
const actSnapshotJS = `(() => {
  document.querySelectorAll('[data-annulo-act]').forEach(e => e.removeAttribute('data-annulo-act'))
  const sel = 'a[href],button,input,textarea,select,[role="button"],[role="link"],[role="menuitem"],[role="tab"],[role="checkbox"],[role="radio"],[role="textbox"],[role="option"],[role="switch"],[contenteditable="true"],[contenteditable=""],[tabindex]:not([tabindex="-1"])'
  const vw = innerWidth, vh = innerHeight
  const items = []
  for (const e of document.querySelectorAll(sel)) {
    const r = e.getBoundingClientRect()
    if (r.width < 2 || r.height < 2) continue
    const s = getComputedStyle(e)
    if (s.visibility === 'hidden' || s.display === 'none' || Number(s.opacity) === 0) continue
    // 里面还套着可操作元素的外层（整块卡片带 tabindex 这类）跳过，留里面的
    if (e.matches('[tabindex]:not(a,button,input,textarea,select,[role])') && e.querySelector(sel)) continue
    const text = (e.innerText || e.value || '').replace(/\s+/g, ' ').trim().slice(0, 80)
    // 只有图标的按钮（Instagram 左栏的「+」）：自己没字，说明在里面的 svg / img 上，取它的 aria-label、alt 或 <title>
    const icon = e.getAttribute('aria-label') ? null : e.querySelector('[aria-label], img[alt], svg title')
    const label = (e.getAttribute('aria-label') || (icon && (icon.getAttribute('aria-label') || icon.getAttribute('alt') || icon.textContent)) || '').trim().slice(0, 80)
    const hint = (e.getAttribute('placeholder') || e.getAttribute('title') || '').trim().slice(0, 60)
    const editable = e.isContentEditable || e.tagName === 'INPUT' || e.tagName === 'TEXTAREA'
    if (!text && !label && !hint && !editable) continue
    const inView = r.bottom > 0 && r.right > 0 && r.top < vh && r.left < vw
    const dialog = !!e.closest('[role="dialog"],[aria-modal="true"]')
    items.push({ e, rank: (dialog ? 0 : 2) + (inView ? 0 : 1), tag: e.tagName.toLowerCase(), role: e.getAttribute('role') || '', text, label, hint,
      type: e.getAttribute('type') || '', dialog, disabled: !!(e.disabled || e.getAttribute('aria-disabled') === 'true'), editable })
  }
  // 不是按钮、链接，但点得动的也要列：div 做的入口（鼠标是手形的最外层），和悬停才显示的浮层
  // （2026-10 小红书封面缩略图上的「编辑封面」平时 display: none）。模型看不到它们，就只能说「找不到入口」。
  // 浮层列的是里面手形的那个元素，标 hover：真实鼠标点不到，点的时候在页面里 element.click()（actClick）
  const sized = e => { const r = e.getBoundingClientRect(); return r.width >= 2 && r.height >= 2 }
  const hiddenStyle = s => s.display === 'none' || s.visibility === 'hidden' || Number(s.opacity) === 0
  const shown = e => { for (let p = e; p && p !== document.body; p = p.parentElement) if (hiddenStyle(getComputedStyle(p))) return false; return true }
  for (const e of document.body.querySelectorAll('div,span,li,p,label,img,section')) {
    if (e.matches(sel) || e.closest(sel) || e.querySelector(sel)) continue
    const s = getComputedStyle(e)
    const parent = e.parentElement
    let target = e, hover = false
    if (hiddenStyle(s)) {
      // 最外层藏起来的那块，挂在一个看得见的框上（缩略图），里面有手形的元素
      if (!parent || !sized(parent) || !shown(parent)) continue
      target = [e, ...e.querySelectorAll('*')].find(x => getComputedStyle(x).cursor === 'pointer')
      if (!target) continue
      hover = true
    } else if (s.cursor !== 'pointer' || (parent && getComputedStyle(parent).cursor === 'pointer') || !sized(e) || !shown(e)) continue
    const text = (target.textContent || '').replace(/\s+/g, ' ').trim()
    const label = (target.getAttribute('aria-label') || target.getAttribute('alt') || '').trim().slice(0, 80)
    // 一大块卡片、整栏列表这类字很多的不列：要的是一个入口；藏起来的浮层更严，免得把收起的菜单都列进来
    if ((!text && !label) || text.length > (hover ? 20 : 40)) continue
    const r = (hover ? parent : e).getBoundingClientRect()
    const inView = r.bottom > 0 && r.right > 0 && r.top < vh && r.left < vw
    const dialog = !!e.closest('[role="dialog"],[aria-modal="true"]')
    // 悬停项排在同一档的后面：收起的菜单也会被列进来，别挤掉看得见的按钮
    items.push({ e: target, rank: (dialog ? 0 : 2) + (inView ? 0 : 1) + (hover ? 0.5 : 0), tag: target.tagName.toLowerCase(), role: '', text, label, hint: (target.getAttribute('title') || '').trim().slice(0, 60),
      type: '', dialog, disabled: false, editable: false, hover })
  }
  items.sort((a, b) => a.rank - b.rank)
  // 开着弹窗时只给弹窗里的元素：弹窗外面的（左栏、页脚、信息流）这时点不到，也和这一步无关，列出来只会让模型更慢、更容易选错
  const inDialog = items.filter(x => x.dialog)
  const dialogOnly = inDialog.length > 0
  const list = (dialogOnly ? inDialog : items).slice(0, 150).map((x, i) => { x.e.setAttribute('data-annulo-act', String(i + 1)); const { e, rank, ...rest } = x; return { id: i + 1, ...rest } })
  return JSON.stringify({ url: location.href, title: document.title, list, dialogOnly })
})()`

const actSystem = `You help a browser automation script past one step it got stuck on. You see the page as a numbered list of visible interactive elements ("shows-on-hover" ones are overlays that appear when the mouse is over them, such as an edit button on a thumbnail; clicking them works).
Reply with exactly one JSON object and nothing else, one of:
{"action":"click","id":N}
{"action":"type","id":N,"text":"..."}      (replaces the field's content)
{"action":"press","key":"Escape"}          (Escape, Enter, Tab, ArrowDown, ...)
{"action":"scroll","dy":600}               (positive scrolls down)
{"action":"wait","ms":1500}                (the page is still loading or processing)
{"action":"done"}                          (the goal is reached)
{"action":"fail","reason":"..."}           (the goal can't be reached from this page)
{"action":"ask_user","reason":"..."}       (only a person can do this: CAPTCHA, "I'm not a robot", SMS / email code, sign in again, identity check)
Rules: first check whether something covers the page or the goal — a popup, promo, banner, cookie notice, product tour or "what's new" dialog — and if so close it (prefer "Not now", "Close", "Cancel", "Skip", "以后再说", "关闭", "跳过", or an X icon) before anything else; the goal never mentions these, closing them is always part of the job. Then pick the single most direct action toward the goal. Never click elements listed as forbidden. Don't navigate away from the site. Buttons may be labeled in any language.
Never try to solve a CAPTCHA, verification code or sign-in yourself: reply ask_user with a short reason in the page's language and the script will show the browser to the person.`

var actJSONRe = regexp.MustCompile(`(?s)\{.*\}`)

type actAction struct {
	Action string `json:"action"`
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Key    string `json:"key"`
	DY     int    `json:"dy"`
	MS     int    `json:"ms"`
	Reason string `json:"reason"`
}

// actCached 是记下来的解法里的一步：点哪个元素（按标签、文字、aria-label 认），或者按哪个键。
// 按「网站 + 目标」记成功那次的整串点击和按键（输入文字的不记：那是这次特有的内容）；老格式只记了一个元素，读的时候当成一步
type actCached struct {
	Key   string `json:"key,omitempty"` // 按键（press）；空就是点击
	Tag   string `json:"tag,omitempty"`
	Role  string `json:"role,omitempty"`
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
}

var actCacheMu sync.Mutex

// actSleep 等一会儿，运行被停止、超时就提前返回
func actSleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func actNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }

func (r *runner) actCacheFile() string { return r.h.ActCache }

func (r *runner) actCacheLoad() map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if b, err := os.ReadFile(r.actCacheFile()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func (r *runner) actCacheSave(m map[string]json.RawMessage) {
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		os.MkdirAll(filepath.Dir(r.actCacheFile()), 0o755)
		os.WriteFile(r.actCacheFile(), b, 0o644)
	}
}

func (r *runner) actCacheGet(key string) ([]actCached, bool) {
	if r.actCacheFile() == "" {
		return nil, false
	}
	actCacheMu.Lock()
	defer actCacheMu.Unlock()
	raw, ok := r.actCacheLoad()[key]
	if !ok {
		return nil, false
	}
	var steps []actCached
	if json.Unmarshal(raw, &steps) != nil {
		var one actCached // 老格式：只记了最后点的那个元素
		if json.Unmarshal(raw, &one) != nil || one.Tag == "" {
			return nil, false
		}
		steps = []actCached{one}
	}
	return steps, len(steps) > 0
}

// actCachePut 记下成功的整串；steps 为空就删掉这条（记下的解法不灵了）
func (r *runner) actCachePut(key string, steps []actCached) {
	if r.actCacheFile() == "" {
		return
	}
	actCacheMu.Lock()
	defer actCacheMu.Unlock()
	m := r.actCacheLoad()
	if len(steps) == 0 {
		if _, ok := m[key]; !ok {
			return
		}
		delete(m, key)
	} else if b, err := json.Marshal(steps); err == nil {
		m[key] = b
	}
	r.actCacheSave(m)
}

func (r *runner) actSnapshot(ctx context.Context, p BrowserPage) (actPage, error) {
	var pg actPage
	v, err := p.Eval(ctx, actSnapshotJS)
	if err != nil {
		return pg, err
	}
	s, _ := v.(string)
	if err := json.Unmarshal([]byte(s), &pg); err != nil {
		return pg, err
	}
	return pg, nil
}

func actFind(list []actElement, id int) (actElement, bool) {
	for _, e := range list {
		if e.ID == id {
			return e, true
		}
	}
	return actElement{}, false
}

func actAvoided(e actElement, avoid []string) bool {
	for _, a := range avoid {
		n := actNorm(a)
		if n != "" && (actNorm(e.Text) == n || actNorm(e.Label) == n) {
			return true
		}
	}
	return false
}

func actDescribe(e actElement) string {
	parts := []string{fmt.Sprintf("[%d] %s", e.ID, e.Tag)}
	if e.Role != "" {
		parts = append(parts, "role="+e.Role)
	}
	if e.Type != "" {
		parts = append(parts, "type="+e.Type)
	}
	if e.Label != "" {
		parts = append(parts, fmt.Sprintf("label=%q", e.Label))
	}
	if e.Text != "" && e.Text != e.Label {
		parts = append(parts, fmt.Sprintf("text=%q", e.Text))
	}
	if e.Hint != "" {
		parts = append(parts, fmt.Sprintf("hint=%q", e.Hint))
	}
	if e.Editable {
		parts = append(parts, "editable")
	}
	if e.Hover {
		parts = append(parts, "shows-on-hover")
	}
	if e.Dialog {
		parts = append(parts, "in-dialog")
	}
	if e.Disabled {
		parts = append(parts, "disabled")
	}
	return strings.Join(parts, " ")
}

func actHost(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return ""
}

func (r *runner) actDone(ctx context.Context, p BrowserPage, o actOptions) bool {
	if o.Until == "" {
		return false
	}
	ok, _ := p.Exists(ctx, o.Until)
	return ok
}

// act 是 b.act 的实现，返回 { ok, steps, cached, actions }；达不成抛错（和别的浏览器操作一样会存现场）。
func (r *runner) act(p BrowserPage, goal string, o actOptions) (map[string]any, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, i18n.New("b.act 要写目标：这一步要达成什么", "b.act needs a goal: what this step should achieve")
	}
	if r.h.BrowserLLM == nil {
		return nil, i18n.New("这里不能用浏览器助手（b.act）", "The browser assistant (b.act) isn't available here")
	}
	if o.MaxSteps <= 0 {
		o.MaxSteps = actDefaultSteps
	}
	if o.MaxSteps > actMaxSteps {
		o.MaxSteps = actMaxSteps
	}
	// 总时限包含等人处理的时间；模型自己只有 actTimeout（aiDeadline，交给人时顺延）
	ctx, cancel := context.WithTimeout(r.ctx, actTimeout+actMaxHandoffs*actHandoffWait)
	defer cancel()
	aiDeadline := time.Now().Add(actTimeout)
	handoffs := 0
	if r.actDone(ctx, p, o) {
		return map[string]any{"ok": true, "steps": 0, "cached": false, "actions": []any{}}, nil
	}
	start, _ := p.URL(ctx)
	host := actHost(start)
	key := host + "\x00" + goal
	r.emit(Event{Type: "progress", Data: map[string]any{"message": i18n.Tf("浏览器助手在帮忙：%s", "The browser assistant is helping: %s", goal)}})
	r.actDebug("开始：%s（完成条件 %q，不许点 %v，最多 %d 步）", goal, o.Until, o.Avoid, o.MaxSteps)

	// 先按记下的解法重放：每步重新看页面、找到同样的元素再点（找不到就算不灵），中途满足 until 就成功，不问模型。
	// 不灵（某步找不到、做完还不满足 until）就删掉这条，从当前页面接着问模型；模型成功后记下新的
	// 重放到一半不灵时，已经做成的前几步留在 prefix 里：模型接着走完后，记下的是「前几步 + 模型做的」整串
	var prefix []actCached
	if steps, ok := r.actCacheGet(key); ok && o.Until != "" {
		done, played, did := r.actReplay(ctx, p, o, steps)
		r.actDebug("重放记下的解法（%d 步）：做成 %d 步，达成目标：%v", len(steps), len(did), done)
		if done {
			return map[string]any{"ok": true, "steps": len(played), "cached": true, "actions": played}, nil
		}
		r.actCachePut(key, nil)
		prefix = did
	}

	var history []string
	var actions []any
	seq := append([]actCached(nil), prefix...) // 这次做成的点击、按键（含重放成功的前几步），成功后记下来；输入过文字的整串不记
	typed := false
	bad := 0
	for step := 1; step <= o.MaxSteps; step++ {
		if ctx.Err() != nil || time.Now().After(aiDeadline) {
			return nil, i18n.Errorf("浏览器助手 %v 内没完成：%s", "The browser assistant didn't finish within %v: %s", actTimeout, goal)
		}
		pg, err := r.actSnapshot(ctx, p)
		if err != nil {
			return nil, err
		}
		if host != "" && actHost(pg.URL) != host {
			return nil, i18n.Errorf("浏览器助手跳到了别的网站（%s），停下了", "The browser assistant ended up on another site (%s) and stopped", pg.URL)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Goal: %s\n", goal)
		if len(o.Avoid) > 0 {
			fmt.Fprintf(&b, "Forbidden (never click): %s\n", strings.Join(o.Avoid, ", "))
		}
		fmt.Fprintf(&b, "Page: %s — %s\n", pg.Title, pg.URL)
		if len(history) > 0 {
			fmt.Fprintf(&b, "Done so far:\n%s\n", strings.Join(history, "\n"))
		}
		if pg.DialogOnly {
			b.WriteString("A dialog is open: only the elements inside it are listed (press Escape to close it if it's in the way).\n")
		}
		b.WriteString("Elements:\n")
		for _, e := range pg.List {
			b.WriteString(actDescribe(e) + "\n")
		}
		t0 := time.Now()
		out, err := r.h.BrowserLLM(ctx, actSystem, b.String())
		if err != nil {
			r.actDebug("第 %d 步 · 调模型出错（%v）：%v", step, time.Since(t0).Round(time.Millisecond), err)
			return nil, err
		}
		r.actDebug("第 %d 步 · 模型用时 %v · %d 个元素\n--- 发给模型 ---\n%s\n--- 模型回复 ---\n%s", step, time.Since(t0).Round(time.Millisecond), len(pg.List), b.String(), strings.TrimSpace(out))
		var a actAction
		if m := actJSONRe.FindString(out); m == "" || json.Unmarshal([]byte(m), &a) != nil {
			bad++
			history = append(history, fmt.Sprintf("%d. (invalid reply, reply with one JSON object)", step))
			if bad >= actMaxBad {
				return nil, i18n.Errorf("浏览器助手没给出能执行的动作：%s", "The browser assistant gave no usable action: %s", goal)
			}
			continue
		}
		rec := map[string]any{"action": a.Action}
		var err2 error
		switch a.Action {
		case "done":
			if o.Until == "" || r.actDone(ctx, p, o) {
				if len(seq) > 0 && !typed {
					r.actCachePut(key, seq)
				}
				return map[string]any{"ok": true, "steps": step, "cached": false, "actions": actions}, nil
			}
			history = append(history, fmt.Sprintf("%d. said done, but the goal selector isn't on the page yet", step))
			continue
		case "fail":
			return nil, i18n.Errorf("浏览器助手做不到「%s」：%s", "The browser assistant couldn't \"%s\": %s", goal, a.Reason)
		case "ask_user":
			// 只有人能做的（验证码、重新登录…）：把浏览器调到前台，等人处理完自动接着跑
			if handoffs >= actMaxHandoffs {
				return nil, i18n.Errorf("需要你在浏览器里处理：%s", "Needs you in the browser: %s", a.Reason)
			}
			handoffs++
			r.actDebug("第 %d 步 · 交给人处理：%s", step, a.Reason)
			t0 := time.Now()
			done, err := r.actHandoff(ctx, p, o, a.Reason, pg)
			aiDeadline = aiDeadline.Add(time.Since(t0))
			if err != nil {
				return nil, err
			}
			if done {
				return map[string]any{"ok": true, "steps": step, "cached": false, "actions": append(actions, map[string]any{"action": "ask_user", "reason": a.Reason})}, nil
			}
			actions = append(actions, map[string]any{"action": "ask_user", "reason": a.Reason})
			history = append(history, fmt.Sprintf("%d. asked the person (%s); they handled it, continue toward the goal", step, a.Reason))
			continue
		case "click", "type":
			e, ok := actFind(pg.List, a.ID)
			switch {
			case !ok:
				err2 = fmt.Errorf("no element %d", a.ID)
			case actAvoided(e, o.Avoid):
				err2 = fmt.Errorf("element %d is forbidden", a.ID)
			case a.Action == "type" && !e.Editable:
				err2 = fmt.Errorf("element %d isn't a text field", a.ID) // 只许往输入框里写
			case a.Action == "click":
				rec["element"] = actDescribe(e)
				err2 = actClick(ctx, p, e)
				if err2 == nil {
					seq = append(seq, actCached{Tag: e.Tag, Role: e.Role, Text: e.Text, Label: e.Label})
				}
			default:
				rec["element"], rec["text"] = actDescribe(e), a.Text
				err2 = p.Type(ctx, fmt.Sprintf(`[data-annulo-act="%d"]`, e.ID), a.Text, true, 5*time.Second)
				typed = true
			}
		case "press":
			rec["key"] = a.Key
			err2 = r.actPressAllowed(ctx, p, a.Key, o.Avoid)
			if err2 == nil {
				err2 = p.Press(ctx, a.Key)
			}
			if err2 == nil {
				seq = append(seq, actCached{Key: a.Key})
			}
		case "scroll":
			rec["dy"] = a.DY
			_, err2 = p.Eval(ctx, fmt.Sprintf("window.scrollBy(0, %d)", a.DY))
		case "wait":
			ms := min(max(a.MS, 300), 5000)
			rec["ms"] = ms
			actSleep(ctx, time.Duration(ms)*time.Millisecond)
		default:
			err2 = fmt.Errorf("unknown action %q", a.Action)
		}
		if err2 != nil {
			r.actDebug("第 %d 步 · 动作没执行：%v", step, err2)
			bad++
			history = append(history, fmt.Sprintf("%d. %s failed: %v", step, strings.TrimSpace(actJSONRe.FindString(out)), err2))
			if bad >= actMaxBad {
				return nil, i18n.Errorf("浏览器助手连续几次操作失败：%s（%v）", "The browser assistant's actions kept failing: %s (%v)", goal, err2)
			}
			continue
		}
		bad = 0
		actions = append(actions, rec)
		desc, _ := json.Marshal(rec)
		history = append(history, fmt.Sprintf("%d. %s", step, desc))
		actSleep(ctx, 1200*time.Millisecond)
		if r.actDone(ctx, p, o) {
			if len(seq) > 0 && !typed {
				r.actCachePut(key, seq)
			}
			return map[string]any{"ok": true, "steps": step, "cached": false, "actions": actions}, nil
		}
	}
	return nil, i18n.Errorf("浏览器助手 %d 步内没完成：%s", "The browser assistant didn't finish within %d steps: %s", o.MaxSteps, goal)
}

// actHandoff 把浏览器调到前台，等人处理（验证码、登录…）：until 出现了就算达成（done=true）；
// 页面变了（地址或可操作元素变了）并且稳定 3 秒，就回去让模型接着走（done=false）。等太久报错。
// 后台无界面运行的浏览器调不出来（定时任务）：直接报错，说清楚要人处理什么。
func (r *runner) actHandoff(ctx context.Context, p BrowserPage, o actOptions, reason string, blocked actPage) (bool, error) {
	rv, ok := p.(interface{ Reveal(context.Context) error })
	if !ok || rv.Reveal(ctx) != nil {
		return false, i18n.Errorf("需要你在浏览器里处理：%s（这次是在后台无界面运行的，没法弹出浏览器：到页面上点运行，或者点「打开看」再试）",
			"Needs you in the browser: %s (this run's browser is headless in the background and can't be shown: run it from the page, or use Watch, and try again)", reason)
	}
	r.emit(Event{Type: "progress", Data: map[string]any{"message": i18n.Tf("需要你在浏览器里处理：%s。处理完会自动继续", "Needs you in the browser: %s. It continues on its own when you're done", reason)}})
	sig := func(pg actPage) string {
		var b strings.Builder
		b.WriteString(pg.URL)
		for _, e := range pg.List {
			b.WriteString("\x00" + e.Text + "\x01" + e.Label)
		}
		return b.String()
	}
	before := sig(blocked)
	deadline := time.Now().Add(actHandoffWait)
	changed, stableSince := "", time.Time{}
	for time.Now().Before(deadline) {
		actSleep(ctx, 2*time.Second)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if r.actDone(ctx, p, o) {
			return true, nil
		}
		pg, err := r.actSnapshot(ctx, p)
		if err != nil {
			continue // 页面在跳转
		}
		cur := sig(pg)
		if cur == before {
			changed = ""
			continue
		}
		if cur != changed {
			changed, stableSince = cur, time.Now()
			continue
		}
		if time.Since(stableSince) >= 3*time.Second {
			return false, nil
		}
	}
	return false, i18n.Errorf("等了 %v 还是需要你在浏览器里处理：%s", "Waited %v and it still needs you in the browser: %s", actHandoffWait, reason)
}

// actReplay 按记下的整串重放：每步之前重新看页面，按标签、文字、aria-label 找到同样的元素再点（按键直接按），
// 每步之后看 until。满足就 done；某步找不到、点不动，或者做完还不满足就是不灵（done=false），页面停在那儿，交给模型接着走。
func (r *runner) actReplay(ctx context.Context, p BrowserPage, o actOptions, steps []actCached) (bool, []any, []actCached) {
	var played []any
	var did []actCached
	for _, st := range steps {
		if st.Key != "" {
			if r.actPressAllowed(ctx, p, st.Key, o.Avoid) != nil || p.Press(ctx, st.Key) != nil {
				return false, played, did
			}
			played = append(played, map[string]any{"action": "press", "key": st.Key})
			did = append(did, st)
		} else {
			pg, err := r.actSnapshot(ctx, p)
			if err != nil {
				return false, played, did
			}
			var hit *actElement
			for i, e := range pg.List {
				if e.Tag == st.Tag && actNorm(e.Text) == actNorm(st.Text) && actNorm(e.Label) == actNorm(st.Label) && !e.Disabled && !actAvoided(e, o.Avoid) {
					hit = &pg.List[i]
					break
				}
			}
			if hit == nil || actClick(ctx, p, *hit) != nil {
				return false, played, did
			}
			played = append(played, map[string]any{"action": "click", "element": actDescribe(*hit)})
			did = append(did, st)
		}
		actSleep(ctx, 1500*time.Millisecond)
		if r.actDone(ctx, p, o) {
			return true, played, did
		}
	}
	return false, played, did
}

// actClick 点清单里的一个元素：平常用真实鼠标；悬停才显示的浮层（Hover）真实鼠标点不到，在页面里 element.click()
func actClick(ctx context.Context, p BrowserPage, e actElement) error {
	sel := fmt.Sprintf(`[data-annulo-act="%d"]`, e.ID)
	if !e.Hover {
		return p.Click(ctx, sel, "", 5*time.Second)
	}
	v, err := p.Eval(ctx, fmt.Sprintf(`(() => { const e = document.querySelector(%q); if (!e) return false; e.click(); return true })()`, sel))
	if err != nil {
		return err
	}
	if v != true {
		return fmt.Errorf("element %d is gone", e.ID)
	}
	return nil
}

// actPressAllowed：Enter、空格会触发焦点所在的按钮——焦点停在 avoid 里的按钮（分享、发布）上时不许按，不然等于替脚本点了它
func (r *runner) actPressAllowed(ctx context.Context, p BrowserPage, key string, avoid []string) error {
	if len(avoid) == 0 || (key != "Enter" && key != " " && key != "Space" && key != "Spacebar") {
		return nil
	}
	v, err := p.Eval(ctx, `(() => { const e = document.activeElement; if (!e || e === document.body) return ''; return JSON.stringify({ text: (e.innerText || e.value || '').replace(/\s+/g, ' ').trim().slice(0, 80), label: (e.getAttribute('aria-label') || '').trim() }) })()`)
	if err != nil {
		return err
	}
	var f actElement
	if s, _ := v.(string); s != "" {
		json.Unmarshal([]byte(s), &f)
	}
	if actAvoided(f, avoid) {
		return fmt.Errorf("pressing %s would trigger a forbidden button (%s)", key, strings.TrimSpace(f.Text+" "+f.Label))
	}
	return nil
}

// actDebug：调试开关打开时往运行日志里写一条（浏览器助手每一步看到了什么、模型回了什么）
func (r *runner) actDebug(format string, args ...any) {
	if r.h.BrowserDebug == nil || !r.h.BrowserDebug() {
		return
	}
	r.emit(Event{Type: "log", Data: map[string]any{"level": "debug", "message": "[浏览器助手] " + fmt.Sprintf(format, args...)}})
}
