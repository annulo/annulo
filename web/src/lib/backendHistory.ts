// 后台独立的路由记录：浏览器的联合 history 还包含外壳设置、用量和启动页，
// 鼠标前进/后退不能沿着那些记录走。保留 URL 和 router state，触发 iframe 的 popstate。
export function attachBackendHistory(frame: HTMLIFrameElement, host: Window = window) {
  type Entry = { href: string; state: unknown }
  const entries: Entry[] = []
  let index = -1
  let restoring = false
  let detachFrame = () => {}
  let last: { direction: number; source: string; at: number } | undefined

  const read = (w: Window): Entry => ({ href: w.location.href, state: structuredClone(w.history.state) })
  const record = (entry: Entry, replace = false) => {
    if (restoring) return
    if (index < 0) { entries.push(entry); index = 0; return }
    if (replace || entries[index].href === entry.href) entries[index] = entry
    else { entries.splice(index + 1); entries.push(entry); index++ }
  }
  const move = (direction: number, source: string) => {
    if (direction !== -1 && direction !== 1) return
    const now = Date.now()
    // Windows 可能同时给出鼠标事件和系统 app-command，单次点击只执行一次。
    if (last?.direction === direction && last.source !== source && now - last.at < 120) return
    last = { direction, source, at: now }
    const next = index + direction
    if (next < 0 || next >= entries.length) return
    // 多个标签各挂一份：只动当前显示的那个
    if (frame.hidden) return
    const w = frame.contentWindow
    if (!w) return
    try {
      const target = entries[next]
      if (new URL(target.href).origin !== host.location.origin) return
      const oldURL = w.location.href
      // 同一页面的查询参数 / hash 走 SPA；真实页面切换才重新加载 iframe。
      if (new URL(oldURL).pathname === new URL(target.href).pathname) {
        restoring = true
        w.history.replaceState(target.state, '', target.href)
        index = next
        w.dispatchEvent(new PopStateEvent('popstate', { state: target.state }))
        if (new URL(oldURL).hash !== new URL(target.href).hash) {
          w.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: target.href }))
        }
      } else {
        index = next
        w.location.replace(target.href)
      }
    } catch {
      // iframe 尚未就绪或已跨域时不回退到外壳的 history。
    } finally {
      restoring = false
    }
  }
  const onMouse = (event: MouseEvent) => {
    if (event.button !== 3 && event.button !== 4) return
    event.preventDefault()
    event.stopPropagation()
    if (event.type === 'mousedown') move(event.button === 3 ? -1 : 1, 'mouse')
  }
  const mouseEvents = ['mousedown', 'mouseup', 'auxclick'] as const
  const attach = () => {
    detachFrame()
    const w = frame.contentWindow
    if (!w) return
    try {
      if (w.location.origin !== host.location.origin || !/^https?:$/.test(w.location.protocol)) return
      record(read(w), entries[index]?.href === w.location.href)
      const history = w.history
      const push = history.pushState
      const replace = history.replaceState
      const wrapPush: History['pushState'] = function (...args) { push.apply(history, args); record(read(w)) }
      const wrapReplace: History['replaceState'] = function (...args) { replace.apply(history, args); record(read(w), true) }
      history.pushState = wrapPush
      history.replaceState = wrapReplace
      const onPop = () => {
        if (restoring) return
        const entry = read(w)
        if (entries[index]?.href === entry.href) { entries[index] = entry; return }
        const found = entries.reduce((best, item, at) => item.href === entry.href && (best < 0 || Math.abs(at - index) < Math.abs(best - index)) ? at : best, -1)
        if (found >= 0) { index = found; entries[index] = entry }
        else record(entry)
      }
      w.addEventListener('popstate', onPop)
      w.addEventListener('hashchange', onPop)
      for (const type of mouseEvents) w.addEventListener(type, onMouse, { capture: true })
      detachFrame = () => {
        try {
          if (history.pushState === wrapPush) history.pushState = push
          if (history.replaceState === wrapReplace) history.replaceState = replace
          w.removeEventListener('popstate', onPop)
          w.removeEventListener('hashchange', onPop)
          for (const type of mouseEvents) w.removeEventListener(type, onMouse, true)
        } catch { /* 已经换了文档 */ }
      }
    } catch { /* 页面尚未加载或跨域 */ }
  }
  const onNative = (event: Event) => move((event as CustomEvent<number>).detail, 'native')
  for (const type of mouseEvents) host.addEventListener(type, onMouse, { capture: true })
  host.addEventListener('shuttle:backend-history', onNative)
  frame.addEventListener('load', attach)
  attach()
  return () => {
    detachFrame()
    frame.removeEventListener('load', attach)
    host.removeEventListener('shuttle:backend-history', onNative)
    for (const type of mouseEvents) host.removeEventListener(type, onMouse, true)
  }
}
