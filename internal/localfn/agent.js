// b.agent(task, opts) 的循环（docs/browser-agent.md）：AI 看页面、决定下一步，脚本给的工具由它调用。
// 由 agent.go 编译成一个函数，调用时 this 不用，参数是 (b, task, opts)。在 goja 里直接跑（不经过 esbuild），只用 ES2017 的语法。
//
// opts:
// 每轮最多 12 个动作，浏览器按顺序一个一个做；模型一轮尽量排完能排的（笔记里有页面布局时整张表一次排完），少问几轮就快。
//   tools    { 名字: { description, run(args) } }：插件给的可靠操作（富文本填写并核对、加标签、等上传完、带关卡的发布…）；
//            run 返回字符串（给 AI 看），或者 { finish: '说明' } 直接结束（发布成功、演练核对完：不用再等 AI 回 done）
//   files    { 名字: 文件 }：AI 能上传的文件（b.upload 认的写法），AI 只能按名字选，不能传别的
//   avoid    不许点的按钮文字（发布、删除这类收不回来的）：要做只能经工具
//   notes    笔记名（比如 'bilibili.publish'）：读插件自带的和项目自己的，学到新东西时改写项目的那份；不做机械重放
//   maxSteps 最多问模型几次（默认 30）；timeoutMs 总时长（默认 20 分钟，不含等人的时间）
// 返回 { ok, summary | reason, steps, noted, ask_user? }。
//
// 问模型优先用模型商原生的函数调用（b.__llmTools）：每个动作是一个工具，插件的工具也直接是工具，模型一次可以调好几个（就是一批）。
// 比让模型在文字里写 JSON 稳得多（2026-10-19 GPT-6 Luna 写 JSON 夹着大段自言自语、一次写好几轮）。
// 这台 Annulo 没有、或者这个模型走不了函数调用（本机 agent）时，退回文字 JSON（b.__llm）。
(function () {
  var INTRO = [
    'You operate a web page in a real browser to finish one task for a script. Each turn you get the task, notes from earlier runs, the tools and files you may use, what you did so far, and the current page:',
    'numbered elements (with the form field each belongs to, * for required, current value), file inputs, open dialogs and notices.'
  ].join('\n')
  var TOOL_MODE = [
    'Act by calling the tools. Call several in one turn when you can: they run in order (the browser does them one by one, at most 12 per turn), then you see the page again.',
    'Before the calls, write one short sentence (in the language of the task) saying what you are doing.',
    'Each turn costs time, so do as much as you safely can per turn: fill every field you can already see in one go, and use the notes to plan ahead.',
    'Element numbers can change after something opens, closes or re-renders: after a call that opens a dropdown or dialog, uploads, or confirms, make no more calls that turn. If a call fails, the rest of that turn is skipped.',
    'Dropdowns: always use select (it opens the field and picks the option by text); never click options by number. Rich-text editors (marked rich) and anything irreversible go through the script\'s tools. Call done when the task is complete, fail when it cannot be done, ask_user only for CAPTCHAs, verification codes, signing in, or a decision only the person can make.'
  ].join('\n')
  var JSON_MODE = [
    'Reply with exactly one JSON object and nothing else: {"thought": "<one short sentence in the language of the task>", "actions": [...]}. Up to 12 actions run in order (the browser does them one by one), then you see the page again. Do not predict later turns; you will be asked again.',
    'Each turn costs time, so do as much as you safely can per batch: fill every field you can already see in one go (title, text inputs, tools for rich text and tags), and use the notes to plan ahead.',
    'Element numbers can change after something opens, closes or re-renders: after an action that opens a dropdown or dialog, uploads, or confirms, stop the batch there (you will see the new page next turn). If an action fails, the rest of the batch is skipped.',
    'Actions:',
    '{"do":"click","id":N}',
    '{"do":"type","id":N,"text":"...","clear":true}   plain inputs and search boxes only; rich-text editors (marked rich) need a tool',
    '{"do":"hover","id":N}   move the mouse onto an element (menus that only open on hover)',
    '{"do":"select","id":N,"option":"..."}   choose a dropdown option by its exact text (always use this for dropdowns, not clicks on options)',
    '{"do":"press","key":"Enter"}   also Escape, Tab, Backspace, ArrowDown, ArrowUp',
    '{"do":"scroll","id":N} brings an element into view; {"do":"scroll","dy":600} scrolls the page',
    '{"do":"wait","ms":2000}   when the page is loading or processing (max 10000)',
    '{"do":"upload","file":"<name from Files>","input":N}   N from File inputs',
    '{"do":"tool","name":"<tool>","args":{...}}   the result is shown next turn',
    '{"do":"ask_user","reason":"..."}   only for CAPTCHAs, verification codes, signing in, or a decision only the person can make',
    '{"do":"done","summary":"...","note":"..."}   the task is complete (a tool confirmed it when the task says so)',
    '{"do":"fail","reason":"...","note":"..."}   it cannot be done from here'
  ].join('\n')
  var RULES = [
    'Rules:',
    '- First deal with anything covering the page or the form: popups, product tours, promos, cookie notices (prefer Not now / Close / Skip / 关闭 / 跳过 / 以后再说 / 我知道了 / 暂不).',
    '- Never click forbidden elements. Irreversible steps (publish, delete, pay) only go through the tools.',
    '- Put content (titles, descriptions, tags) in exactly as the task gives it. Never rewrite, shorten or translate it.',
    '- The element list covers the whole page, including parts scrolled out of view (in-view and dialog elements come first). You do not need to scroll to see or check a field; scroll only to reach an element that is not listed, or when the page loads more as you scroll.',
    '- The current page wins over the notes; notes can be out of date. If a field the task or notes mention is not in the element list, it is not on the page: skip it right away (do not go looking for it), say so in the summary, and record it in the note.',
    '- Stay on this site. Do not open other sites or go back to an earlier page unless the task says so.',
    '- If something you did had no effect, try a different way instead of repeating it.',
    '- "note" (on done or fail) is for whoever does this task on this page next time. Add it only when you learned something lasting that the notes above do not say, or that changed: a field moved, disappeared or was added, a new dialog or step, a way of doing something that works or does not.',
    '  Then give the complete updated notes (concise markdown, at most 40 lines): how to recognize each field (by its label text, placeholder or position; never element numbers, they change every time), what works, gotchas, changes dated YYYY-MM-DD.',
    '  Keep what is still true and remove what is wrong. Never write what happened in this run (uploaded, tags added, results), this post\'s content, or anything about tools saying DRY RUN. Omit "note" when nothing is new.'
  ].join('\n')
  var SYSTEM_TOOLS = INTRO + '\n' + TOOL_MODE + '\n' + RULES
  var SYSTEM = INTRO + '\n' + JSON_MODE + '\n' + RULES

  // 内置动作（函数调用时每个是一个工具）
  var INT = { type: 'integer' }, STR = { type: 'string' }
  function obj(props, required) { return { type: 'object', properties: props, required: required || [] } }
  var NOTE = { type: 'string', description: 'Complete updated notes for this page, only when you learned something lasting (see the rules); omit otherwise' }
  var BUILTIN = [
    { name: 'click', description: 'Click an element by its number.', params: obj({ id: INT }, ['id']) },
    { name: 'type', description: 'Type into a plain input or search box (not rich-text editors). clear replaces what is there (default true).', params: obj({ id: INT, text: STR, clear: { type: 'boolean' } }, ['id', 'text']) },
    { name: 'hover', description: 'Move the mouse onto an element: opens menus and toolbars that only show on hover (a ⋮ / More button that does nothing when clicked).', params: obj({ id: INT }, ['id']) },
    { name: 'select', description: 'Choose an option in a dropdown: opens the field (by its number), finds the option by its exact text, clicks it and checks the field. Always use this for dropdowns instead of clicking options by number.', params: obj({ id: INT, option: STR }, ['id', 'option']) },
    { name: 'press', description: 'Press a key: Enter, Escape, Tab, Backspace, ArrowDown, ArrowUp.', params: obj({ key: STR }, ['key']) },
    { name: 'scroll', description: 'Bring an element into view (id), or scroll the page by dy pixels.', params: obj({ id: INT, dy: INT }) },
    { name: 'wait', description: 'Wait while the page loads or processes (ms, max 10000).', params: obj({ ms: INT }, ['ms']) },
    { name: 'upload', description: 'Put one of the Files into a file input (number from File inputs).', params: obj({ file: STR, input: INT }, ['file', 'input']) },
    { name: 'ask_user', description: 'Hand the browser to the person: only for CAPTCHAs, verification codes, signing in, or a decision only they can make.', params: obj({ reason: STR }, ['reason']) },
    { name: 'done', description: 'The task is complete.', params: obj({ summary: STR, note: NOTE }, ['summary']) },
    { name: 'fail', description: 'The task cannot be done from here.', params: obj({ reason: STR, note: NOTE }, ['reason']) }
  ]
  var BUILTIN_NAMES = BUILTIN.map(function (t) { return t.name })

  function clip(s, n) {
    s = String(s == null ? '' : s)
    return s.length > n ? s.slice(0, n) + '…' : s
  }
  function norm(s) {
    return String(s || '').replace(/\s+/g, '').toLowerCase()
  }
  function describe(e) {
    var p = ['[' + e.id + '] ' + e.tag]
    if (e.role) p.push('role=' + e.role)
    if (e.type) p.push('type=' + e.type)
    if (e.field) p.push('field=' + JSON.stringify(e.field) + (e.required ? '*' : ''))
    else if (e.required) p.push('required')
    if (e.label) p.push('label=' + JSON.stringify(e.label))
    if (e.text) p.push('text=' + JSON.stringify(e.text))
    if (e.hint) p.push('hint=' + JSON.stringify(e.hint))
    if (e.editable) p.push('value=' + JSON.stringify(e.value || ''))
    if (e.rich) p.push('rich')
    if (e.checked) p.push('checked')
    if (e.hover) p.push('shows-on-hover')
    if (e.dialog) p.push('in-dialog')
    if (e.disabled) p.push('disabled')
    return p.join(' ')
  }
  // 取回复里第一个完整的 JSON 对象（模型有时一次回好几个，把后面几轮也「预演」了：只认第一个）
  function firstObject(s) {
    var start = s.indexOf('{')
    if (start < 0) return ''
    var depth = 0, str = false, esc = false
    for (var i = start; i < s.length; i++) {
      var c = s[i]
      if (str) {
        if (esc) esc = false
        else if (c === '\\') esc = true
        else if (c === '"') str = false
      } else if (c === '"') str = true
      else if (c === '{') depth++
      else if (c === '}' && --depth === 0) return s.slice(start, i + 1)
    }
    return ''
  }
  function parse(out) {
    var s = firstObject(String(out || ''))
    if (!s) return null
    try {
      var v = JSON.parse(s)
      if (v && v.do) v = { thought: '', actions: [v] }
      if (v && v.action && !v.actions) v = { thought: v.thought || '', actions: [v.action] }
      return v && Array.isArray(v.actions) ? v : null
    } catch (e) {
      return null
    }
  }
  function today() {
    var d = new Date()
    return d.getFullYear() + '-' + ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2)
  }
  function signature(page) {
    return page.url + '|' + page.list.map(function (e) { return e.text + '/' + e.label + '/' + (e.value || '') }).join('|') + '|' + page.dialogs.join('|')
  }

  return async function agent(b, task, opts) {
    opts = opts || {}
    var zh = b.__locale() !== 'en'
    var tools = opts.tools || {}
    var files = opts.files || {}
    var avoid = (opts.avoid || []).map(norm).filter(Boolean)
    var key = opts.notes || ''
    var maxSteps = Math.min(opts.maxSteps || 30, 80)
    var deadline = Date.now() + (opts.timeoutMs || 20 * 60000)
    var notes = key ? await b.__notes(key) : { seed: '', mine: '' }
    var history = []
    var seen = {} // 防兜圈：每批动作 + 页面出现过几次
    var bad = 0
    var page = null

    var toolLines = Object.keys(tools).map(function (n) {
      var t = tools[n]
      return '- ' + n + ': ' + (typeof t === 'function' ? '' : t.description || '')
    })
    var fileLines = Object.keys(files).filter(function (n) { return files[n] }).map(function (n) { return '- ' + n })

    // 函数调用用的工具：内置动作 + 插件的工具（参数 schema 由插件给，没给就是任意对象）
    var useTools = true
    var toolDefs = BUILTIN.filter(function (t) { return t.name !== 'upload' || fileLines.length }).map(function (t) {
      return t.name === 'upload' ? { name: t.name, description: t.description + ' Files: ' + Object.keys(files).join(', '), params: t.params } : t
    })
    Object.keys(tools).forEach(function (n) {
      if (BUILTIN_NAMES.indexOf(n) >= 0) throw new Error('b.agent: tool name ' + n + ' is reserved')
      var t = tools[n]
      toolDefs.push({ name: n, description: (typeof t === 'function' ? '' : t.description || '') || n, params: (t && t.params) || { type: 'object', properties: {}, additionalProperties: true } })
    })
    function toAction(c) {
      var a = {}
      for (var k in c.args || {}) a[k] = c.args[k]
      if (BUILTIN_NAMES.indexOf(c.name) >= 0) a.do = c.name
      else a = { do: 'tool', name: c.name, args: c.args || {} }
      return a
    }

    function avoided(e) {
      return !!e && avoid.some(function (a) { return norm(e.text) === a || norm(e.label) === a })
    }
    function find(id) {
      id = Number(id) // 函数调用的参数可能是字符串 "3"
      for (var i = 0; page && i < page.list.length; i++) if (page.list[i].id === id) return page.list[i]
      return null
    }
    async function saveNote(text) {
      if (!key || !text || !String(text).trim()) return false
      if (norm(text) === norm(notes.mine)) return false
      await b.__saveNotes(key, String(text))
      notes.mine = String(text)
      return true
    }
    // 只有人能做的（验证码、登录…）：把窗口调到前台，等页面变了并且稳定下来再接着走
    async function handOff(reason) {
      if (typeof b.show !== 'function') return false
      try {
        await b.show()
      } catch (e) {
        return false
      }
      b.__progress(zh ? '需要你在浏览器里处理：' + reason + '。处理完会自动继续' : 'Needs you in the browser: ' + reason + '. It continues on its own when you are done')
      var before = signature(page), last = '', same = 0
      for (var end = Date.now() + 10 * 60000; Date.now() < end; ) {
        await b.__sleep(2000)
        var now = signature(await b.observe())
        if (now === before) continue
        same = now === last ? same + 1 : 0
        last = now
        if (same >= 2) return true
      }
      return false
    }
    // 工具直接结束时模型没机会写笔记：再问一次「这次有什么值得记下的」，有就改写项目的笔记
    async function reflect() {
      if (!key) return false
      try {
        var ask = [
          'Task (finished):\n' + task,
          'Today: ' + today(),
          notes.seed ? 'Notes that came with the script:\n' + clip(notes.seed, 4000) : '',
          notes.mine ? 'Notes learned in earlier runs here:\n' + clip(notes.mine, 4000) : '',
          'What was done this run:\n' + history.slice(-40).join('\n'),
          'The task is finished. If this run taught something lasting that the notes do not say (follow the rules for "note"), give the complete updated notes; otherwise say nothing is new.'
        ].filter(Boolean).join('\n\n')
        if (useTools) {
          var rep = await b.__llmTools(SYSTEM_TOOLS, ask, [
            { name: 'save_note', description: 'Save the complete updated notes for this page.', params: obj({ note: STR }, ['note']) },
            { name: 'nothing_new', description: 'This run taught nothing the notes do not already say.', params: obj({}) }
          ])
          addUsage(rep.usage)
          if (!rep.unsupported) {
            b.__debug('reflect\n--- reply ---\n' + JSON.stringify(rep.calls))
            var c = (rep.calls || []).filter(function (x) { return x.name === 'save_note' })[0]
            return c ? await saveNote(c.args.note) : false
          }
        }
        var out = await b.__llm(SYSTEM, ask + '\nReply with one JSON object: {"note": "<complete updated notes>"} or {}.')
        b.__debug('reflect\n--- reply ---\n' + out)
        var v = JSON.parse(firstObject(String(out || '')) || '{}')
        return await saveNote(v.note)
      } catch (e) {
        return false
      }
    }
    async function run(a) {
      var e
      switch (a.do) {
        case 'click':
          e = find(a.id)
          if (!e) throw new Error('no element ' + a.id)
          if (avoided(e)) throw new Error('element ' + a.id + ' is forbidden; use the tool for it')
          await b.__clickId(e.id, !!e.hover)
          return 'clicked ' + describe(e)
        case 'type':
          e = find(a.id)
          if (!e) throw new Error('no element ' + a.id)
          if (!e.editable) throw new Error('element ' + a.id + ' is not a text field')
          if (e.rich && !opts.richType) throw new Error('element ' + a.id + ' is a rich-text editor; use a tool to fill it')
          await b.type('[data-annulo-act="' + e.id + '"]', String(a.text == null ? '' : a.text), { clear: a.clear !== false })
          return 'typed into [' + a.id + ']'
        case 'hover':
          e = find(a.id)
          if (!e) throw new Error('no element ' + a.id)
          // 真的把鼠标移上去：菜单、按钮常靠 CSS :hover 才显示，派发事件触发不了（2026-10-19 B站 稿件管理的「⋮」）
          await b.__hover(e.id)
          await settle()
          return 'hovered ' + describe(e)
        case 'select':
          e = find(a.id)
          if (!e) throw new Error('no element ' + a.id)
          if (avoided(e)) throw new Error('element ' + a.id + ' is forbidden')
          var want = String(a.option == null ? '' : a.option)
          if (!want) throw new Error('select needs the option text')
          // 点开下拉：字段本身就是选项所在的地方（有的下拉一直展开）就不用点
          await b.__clickId(e.id, !!e.hover)
          await settle()
          // 按文字找选项：看得见、文字正好是它、不是字段本身；优先弹出层、列表里的
          var picked = await b.eval('(() => {' +
            'document.querySelectorAll("[data-annulo-pick]").forEach(x => x.removeAttribute("data-annulo-pick"));' +
            'const want = ' + JSON.stringify(norm(want)) + ';' +
            'const field = document.querySelector(\'[data-annulo-act="' + Number(e.id) + '"]\');' +
            'const vis = x => { const r = x.getBoundingClientRect(); const st = getComputedStyle(x); return r.width > 1 && r.height > 1 && st.visibility !== "hidden" && st.display !== "none" };' +
            'const hits = [...document.querySelectorAll("li,[role=option],[role=menuitem],div,span,a,label")].filter(x => vis(x) && (x.innerText || "").replace(/\\s+/g, "").toLowerCase() === want && !(field && (x === field || x.contains(field) || field.contains(x))));' +
            'const leaf = hits.filter(x => !hits.some(y => y !== x && x.contains(y)));' +
            'const pop = leaf.filter(x => x.closest("[role=listbox],[role=menu],[class*=dropdown i],[class*=select i],[class*=option i],[class*=popper i],[class*=popover i],[class*=menu i]"));' +
            'const hit = (pop.length ? pop : leaf)[0];' +
            'if (!hit) return "";' +
            'const target = hit.closest("li,[role=option],[role=menuitem]") || hit;' +
            'target.setAttribute("data-annulo-pick", "1"); target.scrollIntoView({ block: "center" }); return "ok" })()')
          if (picked !== 'ok') throw new Error('option "' + want + '" not found after opening element ' + a.id + '; look at the page and choose from what is there')
          try {
            await b.click('[data-annulo-pick="1"]', { timeout: 3000 })
          } catch (err) {
            await b.eval('(() => { const x = document.querySelector("[data-annulo-pick]"); if (x) { for (const t of ["mousedown", "mouseup", "click"]) x.dispatchEvent(new MouseEvent(t, { bubbles: true, cancelable: true, view: window })) } })()')
          }
          await settle()
          var now = await b.eval('(() => { const f = document.querySelector(\'[data-annulo-act="' + Number(e.id) + '"]\'); return f ? String(f.value || f.innerText || "") : "" })()')
          return 'selected "' + want + '" in [' + e.id + ']; the field now shows ' + JSON.stringify(clip(now, 80)) + (norm(now).indexOf(norm(want)) < 0 ? ' (it may show the choice elsewhere, e.g. as a tag; if the dropdown is still open, press Escape)' : '')
        case 'press':
          if (a.key === 'Enter' || a.key === ' ' || a.key === 'Space') {
            var focused = await b.eval('(document.activeElement && (document.activeElement.innerText || document.activeElement.value || "")) || ""')
            if (avoid.indexOf(norm(focused)) >= 0) throw new Error('the focused element is forbidden')
          }
          await b.press(String(a.key))
          return 'pressed ' + a.key
        case 'scroll':
          if (a.id) await b.eval('(() => { const e = document.querySelector(\'[data-annulo-act="' + Number(a.id) + '"]\'); if (e) e.scrollIntoView({ block: "center" }) })()')
          else await b.eval('window.scrollBy(0, ' + Number(a.dy || 600) + ')')
          return 'scrolled'
        case 'wait':
          await b.__sleep(Math.min(Math.max(Number(a.ms) || 1000, 300), 10000))
          return 'waited'
        case 'upload':
          if (!files[a.file]) throw new Error('no file named ' + a.file + '; files: ' + Object.keys(files).join(', '))
          await b.upload('[data-annulo-file="' + Number(a.input) + '"]', [files[a.file]], { timeout: 10 * 60000 })
          return 'uploaded ' + a.file + ' into file input ' + a.input
        case 'tool':
          var t = tools[a.name]
          if (!t) throw new Error('no tool ' + a.name)
          var r = await (typeof t === 'function' ? t : t.run)(a.args || {})
          if (r && typeof r === 'object' && r.finish) return { finish: String(r.finish) }
          return 'tool ' + a.name + ' → ' + clip(typeof r === 'string' ? r : JSON.stringify(r), 800)
        default:
          throw new Error('unknown action ' + a.do)
      }
    }

    // 等页面稳定下来再看：点完、传完以后页面常在变（下拉展开、弹窗关闭的动画），马上看会看到一半的样子，
    // 模型以为按钮不见了、去滚动找（2026-10-19 B站 封面弹窗）。DOM 连续 400 毫秒没变化就算稳定，最多等 3 秒
    async function settle() {
      await b.eval('(() => { window.__annuloMut = 0; if (!window.__annuloObs) { window.__annuloObs = new MutationObserver(() => { window.__annuloMut++ }); window.__annuloObs.observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true }) } })()')
      var last = 0
      for (var end = Date.now() + 3000; Date.now() < end; ) {
        await b.__sleep(400)
        var n = await b.eval('window.__annuloMut')
        if (n === last) return
        last = n
      }
    }

    // 耗时：写进进度（发布卡片上看得到）、结束时汇总进日志和返回值（timing）
    var T = { start: Date.now(), model: 0, look: 0, act: 0, slow: [] }
    var U = { model: '', input: 0, output: 0, cache_read: 0, cache_write: 0 } // 用量：每轮函数调用返回的累加
    function sec(ms) { return (ms / 1000).toFixed(1) }
    function addUsage(u) {
      if (!u) return
      if (u.model) U.model = u.model
      U.input += u.input || 0
      U.output += u.output || 0
      U.cache_read += u.cache_read || 0
      U.cache_write += u.cache_write || 0
    }
    function end(r) {
      var total = Date.now() - T.start
      var slow = T.slow.sort(function (x, y) { return y.ms - x.ms }).slice(0, 4).map(function (x) { return x.what + ' ' + sec(x.ms) + (zh ? ' 秒' : 's') }).join('、')
      var line = zh
        ? '共 ' + r.steps + ' 步 ' + sec(total) + ' 秒：问模型 ' + sec(T.model) + ' 秒 · 看页面 ' + sec(T.look) + ' 秒 · 浏览器操作 ' + sec(T.act) + ' 秒' + (slow ? '（慢的：' + slow + '）' : '')
        : r.steps + ' steps in ' + sec(total) + 's: model ' + sec(T.model) + 's · reading the page ' + sec(T.look) + 's · browser actions ' + sec(T.act) + 's' + (slow ? ' (slowest: ' + slow + ')' : '')
      var usage = U.input || U.output ? (zh ? ' · token 输入 ' : ' · tokens in ') + U.input + (zh ? ' 输出 ' : ' out ') + U.output + (zh ? ' 缓存 ' : ' cached ') + U.cache_read + (U.model ? ' · ' + U.model : '') : ''
      // 页面单独显示汇总（总耗时、token），所以正文就是一句「做完了 / 没做完」，数字放在 summary 里
      b.__progress(r.ok ? (zh ? '浏览器助手做完了' : 'Browser assistant finished') : (zh ? '浏览器助手没做完' : "Browser assistant didn't finish"), {
        summary: { total_ms: total, steps: r.steps, model_ms: T.model, look_ms: T.look, act_ms: T.act, model: U.model, input: U.input, output: U.output, cache_read: U.cache_read, cache_write: U.cache_write },
      })
      b.__log(line + usage)
      r.timing = { total_ms: total, model_ms: T.model, look_ms: T.look, act_ms: T.act, summary: line + usage }
      r.usage = { model: U.model, input: U.input, output: U.output, cache_read: U.cache_read, cache_write: U.cache_write }
      return r
    }

    for (var step = 1; step <= maxSteps; step++) {
      if (Date.now() > deadline) return end({ ok: false, reason: zh ? '浏览器助手超时了' : 'The browser assistant ran out of time', steps: step - 1, noted: false })
      var t0 = Date.now()
      if (step > 1) await settle()
      page = await b.observe()
      T.look += Date.now() - t0
      var prompt = [
        'Task:\n' + task,
        'Today: ' + today(),
        notes.seed ? 'Notes that came with the script:\n' + clip(notes.seed, 4000) : '',
        notes.mine ? 'Notes learned in earlier runs here (newer; prefer them):\n' + clip(notes.mine, 4000) : '',
        toolLines.length ? 'Tools:\n' + toolLines.join('\n') : 'Tools: none',
        fileLines.length ? 'Files you can upload:\n' + fileLines.join('\n') : '',
        avoid.length ? 'Forbidden to click: ' + (opts.avoid || []).join(', ') : '',
        history.length ? 'Done so far:\n' + history.slice(-24).join('\n') : '',
        'Page: ' + page.title + ' — ' + page.url + ' (scrolled ' + page.scroll.y + ' of ' + page.scroll.height + ', view ' + page.scroll.view + ')',
        page.dialogs.length ? 'Open dialogs:\n' + page.dialogs.map(function (d) { return '- ' + d }).join('\n') : '',
        page.notices.length ? 'Notices on the page:\n' + page.notices.map(function (d) { return '- ' + d }).join('\n') : '',
        page.files.length ? 'File inputs:\n' + page.files.map(function (f) { return '[' + f.id + '] accept=' + JSON.stringify(f.accept) + (f.multiple ? ' multiple' : '') + ' near=' + JSON.stringify(f.near) }).join('\n') : '',
        'Elements:\n' + page.list.map(describe).join('\n')
      ].filter(Boolean).join('\n\n')
      var reply = null, out = ''
      var tm = Date.now()
      try {
        if (useTools) {
          var rep = await b.__llmTools(SYSTEM_TOOLS, prompt, toolDefs)
          addUsage(rep.usage)
          if (rep.unsupported) useTools = false
          else {
            out = (rep.text || '') + '\n' + JSON.stringify(rep.calls)
            reply = { thought: rep.text || '', actions: rep.calls.map(toAction) }
            if (!reply.actions.length) reply = null
          }
        }
        if (!useTools) {
          out = await b.__llm(SYSTEM, prompt)
          reply = parse(out)
        }
      } catch (err) {
        // 两次都没回：记进历史，下一轮接着问（连续三次就放弃，和回得不对一样算）
        history.push(step + '. (the model did not answer: ' + clip(err && err.message ? err.message : err, 120) + ')')
        T.model += Date.now() - tm
        if (++bad >= 3) return end({ ok: false, reason: zh ? '浏览器助手问模型一直没回' : "The browser assistant's model kept not answering", steps: step, noted: false })
        continue
      }
      var thinkMs = Date.now() - tm
      T.model += thinkMs
      b.__debug('step ' + step + (useTools ? ' (tool calls)' : ' (json)') + '\n--- prompt ---\n' + prompt + '\n--- reply ---\n' + out)
      if (!reply) {
        history.push(step + (useTools ? '. (you made no tool calls; call at least one tool)' : '. (your reply was not a JSON object with "actions"; reply with one JSON object)'))
        if (++bad >= 3) return end({ ok: false, reason: zh ? '浏览器助手没给出能执行的动作' : 'The browser assistant gave no usable actions', steps: step, noted: false })
        continue
      }
      bad = 0
      // 防兜圈：同样的一批动作、同样的页面出现第 3 次，就是在原地打转，停下来交给人（2026-10-19 B站 选错创作声明后来回改了十几轮）
      var sig = JSON.stringify(reply.actions) + '|' + signature(page)
      seen[sig] = (seen[sig] || 0) + 1
      if (seen[sig] >= 3) return end({ ok: false, reason: zh ? '浏览器助手在原地打转（同样的操作做了 3 次，页面没变）：' + clip(reply.thought || JSON.stringify(reply.actions), 80) : 'The browser assistant was going in circles (the same actions 3 times, page unchanged): ' + clip(reply.thought || JSON.stringify(reply.actions), 80), steps: step, noted: false })
      // 进度只放 AI 的那句话（每一步用了多久，页面按两条进度的间隔算）
      b.__progress(reply.thought ? clip(reply.thought, 160) : (zh ? '第 ' + step + ' 步' : 'Step ' + step))
      for (var k = 0; k < Math.min(reply.actions.length, 12); k++) {
        var a = reply.actions[k] || {}
        if (a.do === 'done' || a.do === 'fail') {
          var noted = await saveNote(a.note)
          b.__log((a.do === 'done' ? (zh ? '浏览器助手完成：' : 'Browser assistant done: ') + (a.summary || '') : (zh ? '浏览器助手做不到：' : 'Browser assistant failed: ') + (a.reason || '')) + (noted ? (zh ? '（更新了笔记）' : ' (notes updated)') : ''))
          return end(a.do === 'done'
            ? { ok: true, summary: String(a.summary || ''), steps: step, noted: noted }
            : { ok: false, reason: String(a.reason || ''), steps: step, noted: noted })
        }
        if (a.do === 'ask_user') {
          var handled = await handOff(String(a.reason || ''))
          if (!handled) return end({ ok: false, reason: String(a.reason || ''), ask_user: true, steps: step, noted: false })
          history.push(step + '. asked the person (' + a.reason + '); they handled it, continue')
          break
        }
        try {
          var t1 = Date.now()
          var did = await run(a)
          var ms = Date.now() - t1
          T.act += ms
          if (ms > 2000) T.slow.push({ what: a.do === 'tool' ? a.name : a.do, ms: ms })
          // 工具说已经完成（发布成功、演练核对完）：直接结束；这一轮学到的东西已经在前面的笔记里，这里不再问模型
          if (did && typeof did === 'object') {
            history.push(step + '. ' + a.name + ' finished: ' + did.finish)
            var noted2 = await reflect()
            b.__log((zh ? '浏览器助手完成：' : 'Browser assistant done: ') + did.finish + (noted2 ? (zh ? '（更新了笔记）' : ' (notes updated)') : ''))
            return end({ ok: true, summary: did.finish, steps: step, noted: noted2 })
          }
          history.push(step + '. ' + did)
        } catch (err) {
          T.act += Date.now() - t1
          history.push(step + '. ' + JSON.stringify(a) + ' failed: ' + clip(err && err.message ? err.message : err, 300))
          break
        }
      }
    }
    return end({ ok: false, reason: zh ? '浏览器助手 ' + maxSteps + ' 步内没完成' : 'The browser assistant did not finish within ' + maxSteps + ' steps', steps: maxSteps, noted: false })
  }
})()
