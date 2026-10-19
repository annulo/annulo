import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { AppWindow, Globe, Square } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tip } from '@/components/ui/tip'
import { getJSON, post } from '@/lib/api'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

// 顶栏右侧：页面上点的操作（发布、自检、采集、登录）正开着的浏览器（GET local/browsers），数字是开着几个。
// 浏览器平时在后台（macOS 缩进 Dock、Windows 在屏幕外，见 internal/browser 的 Offscreen），点一个呼出到前台看它操作，方块停掉这次操作。

type Item = { run_id: string; fn: string; profile: string; url: string; step: string; started: string }

/** 函数名换成看得懂的事：social/social.publish → 发布 */
function what(fn: string) {
  const name = fn.split('.').pop() || fn
  const m: Record<string, string> = { publish: '发布', collect: '采集数据', probe: '自检', login: '登录', openProfile: '打开主页' }
  return m[name] ? t(m[name]) : fn
}

function host(u: string) {
  try {
    return new URL(u).host
  } catch {
    return u
  }
}

/** 弹窗右边和按钮对齐，宽 320，屏幕窄时左右都留 12px 不出屏（手机上按钮右边还有刷新、头像，只限右边距会从左边出去） */
function menuPos(r: DOMRect) {
  const vw = window.innerWidth
  const width = Math.min(320, vw - 24)
  return { top: r.bottom + 8, width, left: Math.min(Math.max(r.right - width, 12), vw - 12 - width) }
}

export default function BrowserIndicator() {
  const [list, setList] = useState<Item[]>([])
  const [tick, setTick] = useState(0) // 点了停止、呼出：马上重读一次列表
  // 打开时按钮的位置：弹窗挂到 body 上按它定位（顶栏右侧那组有 translate，里面的 fixed 不按屏幕算），手机上不会超出屏幕
  const [anchor, setAnchor] = useState<DOMRect | null>(null)
  const open = anchor !== null
  const setOpen = (v: boolean) => setAnchor(v ? (ref.current?.getBoundingClientRect() ?? null) : null)
  const [err, setErr] = useState('')
  // profile → 账号名（社媒插件的 social_accounts.browser_profile）：列表里显示「发布 · Bysir」而不是「发布 · x-1」
  const [names, setNames] = useState<Record<string, string>>({})
  const ref = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    let live = true
    const load = () => getJSON<{ list: Item[] }>('local/browsers').then((r) => live && setList(r.list ?? [])).catch(() => {})
    void load()
    // 窗口在后台时不用刷那么勤
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible' || Math.random() < 0.2) void load() }, 2000)
    return () => { live = false; clearInterval(timer) }
  }, [tick])
  useEffect(() => {
    if (!open) return
    getJSON<{ list: { name?: string; browser_profile?: string }[] }>('db/social_accounts')
      .then((r) => setNames(Object.fromEntries((r.list ?? []).filter((a) => a.browser_profile && a.name).map((a) => [a.browser_profile!, a.name!]))))
      .catch(() => {})
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node) && !menuRef.current?.contains(e.target as Node)) setOpen(false) }
    const resize = () => setOpen(false)
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', esc)
    window.addEventListener('resize', resize)
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc); window.removeEventListener('resize', resize) }
  }, [open])
  useEffect(() => { if (!list.length) { setOpen(false); setErr('') } }, [list.length])
  if (!list.length) return null
  const act = async (id: string, action: 'show' | 'abort') => {
    setErr('')
    try {
      await post(`local/runs/${encodeURIComponent(id)}/${action}`, {})
      if (action === 'show') setOpen(false)
      window.setTimeout(() => setTick((x) => x + 1), 600)
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  return (
    <div ref={ref} className="relative">
      <Tip label={t('正在操作的浏览器')}>
        <Button variant="ghost" size="icon-sm" onClick={() => setOpen(!open)} aria-label={t('正在操作的浏览器')} aria-expanded={open} className="relative rounded-lg text-muted-foreground hover:text-foreground">
          <Globe className="size-[18px]" />
          <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] leading-none font-semibold text-primary-foreground tabular-nums">{list.length}</span>
        </Button>
      </Tip>
      {anchor && createPortal(
        <div ref={menuRef} role="menu" style={menuPos(anchor)} className="fixed z-50 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-xl">
          <div className="px-2.5 pt-1 pb-2 text-xs text-muted-foreground">{t('正在操作的浏览器：点一个呼出到前台看它操作')}</div>
          {list.map((b) => (
            <div key={b.run_id + b.profile} className="group flex items-center gap-2 rounded-lg px-2.5 py-2 hover:bg-accent">
              <button type="button" role="menuitem" onClick={() => void act(b.run_id, 'show')} className="flex min-w-0 flex-1 cursor-pointer items-center gap-2.5 text-left">
                <AppWindow className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{what(b.fn)} · {names[b.profile] || b.profile}</span>
                  <span className="block truncate text-xs text-muted-foreground">{b.step || host(b.url)}</span>
                </span>
              </button>
              <button type="button" aria-label={t('停止')} title={t('停止')} onClick={() => void act(b.run_id, 'abort')} className={cn('flex size-6 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground hover:bg-destructive/10 hover:text-destructive')}>
                <Square className="size-3 fill-current" />
              </button>
            </div>
          ))}
          {err && <p className="px-2.5 py-1.5 text-xs text-destructive">{err}</p>}
        </div>,
        document.body,
      )}
    </div>
  )
}
