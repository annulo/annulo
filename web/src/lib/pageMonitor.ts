import { useEffect, useRef, useState } from 'react'
import { getJSON, post } from '@/lib/api'

// 左侧运营后台（iframe，同源）的运行状态：每次加载后收集报错报给 Annulo（page_errors 工具来查），
// 并每 2 秒问一次 Annulo 有没有「刷新 / 切到某个页面」的指令（助手查报错前会要求刷新），这也是心跳。
//
// 报错来源：平台运行时从页面一开始就接管了 window.error / unhandledrejection / console.error，
// 记在 window.__TALIZEN_RENDER_ERRORS__（{ level, source, message, detail }）；没有它时自己挂监听兜底。

export type PageError = { message: string; stack?: string; source: string; at: string }

type TalizenItem = { level?: string; source?: string; message?: string; detail?: string }

const MAX = 20

const symbolicate = (text: string) => post('ui/symbolicate', { text }).then((r) => r.json() as Promise<{ text: string }>).then((j) => j.text)

/** frame：当前标签的 iframe（换标签就改监视它）；project：当前项目 id，iframe 在登录、选完项目后才出现，换项目也要重新挂 */
export function usePageMonitor(frame: HTMLIFrameElement | null, reload: () => void, project?: string) {
  const [state, setState] = useState<{ loadId: string; path: string; errors: PageError[] }>({ loadId: '', path: '', errors: [] })
  const reloadRef = useRef(reload)
  reloadRef.current = reload

  useEffect(() => {
    const f = frame
    if (!f || !project) return
    let loadId = ''
    let path = ''
    let errors: PageError[] = []
    let stop = () => {}

    const report = () => {
      setState({ loadId, path, errors: [...errors] })
      post('ui/page', { load_id: loadId, path, errors }).catch(() => {})
    }
    const add = (e: PageError) => {
      if (errors.length >= MAX || errors.some((x) => x.message === e.message)) return
      errors.push(e)
      report()
      // 本机渲染的页面，报错里是编译产物 /_client/m/<文件>?v=… 的行列：换成源码位置再显示（提示条、交给助手的都是源码位置）
      if (`${e.message}\n${e.stack ?? ''}`.includes('/_client/m/')) {
        const loaded = loadId
        Promise.all([symbolicate(e.message), e.stack ? symbolicate(e.stack) : Promise.resolve(undefined)])
          .then(([message, stack]) => {
            if (loaded !== loadId) return
            Object.assign(e, { message, stack })
            report()
          })
          .catch(() => {})
      }
    }
    const fresh = () => {
      loadId = Math.random().toString(36).slice(2)
      errors = []
    }

    const onLoad = () => {
      stop()
      fresh()
      let w: (Window & { __TALIZEN_RENDER_ERRORS__?: TalizenItem[] }) | null
      try {
        w = f.contentWindow as typeof w
        path = w!.location.pathname + w!.location.search
      } catch {
        return
      }
      if (!w) return
      const now = () => new Date().toISOString()
      let seen = 0
      const own: [string, EventListener][] = []
      if (!Array.isArray(w.__TALIZEN_RENDER_ERRORS__)) {
        const onErr = (ev: Event) => {
          const e = ev as ErrorEvent
          add({ message: e.message || String(e.error), stack: e.error?.stack, source: 'window.error', at: now() })
        }
        const onRej = (ev: Event) => {
          const r = (ev as PromiseRejectionEvent).reason
          add({ message: r?.message ? `${r.name || 'Error'}: ${r.message}` : String(r), stack: r?.stack, source: 'unhandledrejection', at: now() })
        }
        own.push(['error', onErr], ['unhandledrejection', onRej])
        for (const [t, h] of own) w.addEventListener(t, h)
      }
      const timer = window.setInterval(() => {
        try {
          // 后台内部换了页面（单页应用）：之前的报错属于上一个页面，重新算
          const p = w!.location.pathname + w!.location.search
          if (p !== path) {
            path = p
            fresh()
            seen = w!.__TALIZEN_RENDER_ERRORS__?.length ?? 0
            report()
          }
          const list = w!.__TALIZEN_RENDER_ERRORS__
          if (Array.isArray(list) && list.length > seen) {
            for (const it of list.slice(seen)) {
              if ((it.level ?? 'error') !== 'error') continue
              add({ message: it.message ?? '', stack: it.detail || undefined, source: it.source ?? 'render', at: now() })
            }
            seen = list.length
          }
        } catch {
          // 页面正在跳转
        }
      }, 1000)
      stop = () => {
        clearInterval(timer)
        try {
          for (const [t, h] of own) w!.removeEventListener(t, h)
        } catch {
          // 已经卸载
        }
      }
      report()
    }

    f.addEventListener('load', onLoad)
    if (f.contentDocument?.readyState === 'complete') onLoad()

    // 心跳 + 领指令：第一次只记下当前指令号，不执行打开之前留下的
    let seq = -1
    const tick = () =>
      getJSON<{ cmd_seq: number; cmd_path: string }>('ui/page')
        .then((r) => {
          if (seq >= 0 && r.cmd_seq > seq) {
            if (r.cmd_path) f.src = r.cmd_path
            else reloadRef.current()
          }
          seq = r.cmd_seq
        })
        .catch(() => {})
    tick()
    const hb = window.setInterval(tick, 2000)

    return () => {
      f.removeEventListener('load', onLoad)
      clearInterval(hb)
      stop()
    }
  }, [frame, project])

  return state
}
