import { useEffect, useRef, useState } from 'react'
import { Smartphone } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tip } from '@/components/ui/tip'
import { getJSON, post } from '@/lib/api'
import { navigate } from '@/lib/router'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

// 顶栏右侧：远程访问（docs/mobile-remote.md「远程访问：隧道」）。
// 有新设备在等确认时弹出来让用户点允许 / 拒绝；有设备正在远程用这台电脑时一直亮着，点开能撤销。只在电脑上显示（远程打开时 status.remote 不渲染它）。

export type RemoteDevice = { id: string; name: string; user?: string; status: 'pending' | 'approved'; created: number; last_seen: number; active: boolean }

export async function approveDevice(id: string) {
  await post(`remote/devices/${encodeURIComponent(id)}/approve`, {})
}
export async function removeDevice(id: string) {
  await post(`remote/devices/${encodeURIComponent(id)}`, {}, 'DELETE')
}

export default function RemoteIndicator() {
  const [list, setList] = useState<RemoteDevice[]>([])
  const [enabled, setEnabled] = useState(false)
  const [open, setOpen] = useState(false)
  const [err, setErr] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const load = () =>
    getJSON<{ enabled: boolean; list: RemoteDevice[] }>('remote/devices')
      .then((r) => {
        setEnabled(r.enabled)
        setList(r.list ?? [])
      })
      .catch(() => {})
  useEffect(() => {
    void load()
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible' || Math.random() < 0.3) void load() }, 3000)
    return () => clearInterval(timer)
  }, [])
  const pending = enabled ? list.filter((d) => d.status === 'pending') : []
  const active = enabled ? list.filter((d) => d.active) : []
  // 有新设备等确认：自动弹开
  const pendingKey = pending.map((d) => d.id).join(',')
  useEffect(() => { if (pendingKey) setOpen(true) }, [pendingKey])
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', esc)
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc) }
  }, [open])
  if (!pending.length && !active.length) return null
  const act = async (fn: () => Promise<void>) => {
    setErr('')
    try {
      await fn()
      await load()
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  const label = pending.length ? t('有设备想远程打开这台电脑的 Annulo') : t('正在远程使用这台电脑')
  return (
    <div ref={ref} className="relative">
      <Tip label={label}>
        <Button variant="ghost" size="icon-sm" onClick={() => setOpen((v) => !v)} aria-label={label} aria-expanded={open} className={cn('relative rounded-lg', pending.length ? 'text-primary' : 'text-emerald-600 dark:text-emerald-400')}>
          <Smartphone className="size-[18px]" />
          <span className={cn('absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] leading-none font-semibold tabular-nums', pending.length ? 'bg-primary text-primary-foreground' : 'bg-emerald-500 text-white')}>
            {pending.length || active.length}
          </span>
        </Button>
      </Tip>
      {open && (
        <div role="dialog" className="absolute top-full right-0 z-50 mt-2 w-80 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-xl">
          {pending.map((d) => (
            <div key={d.id} className="space-y-2 rounded-lg px-2.5 py-2">
              <div>
                <div className="font-medium">{t('允许 {name} 远程打开 Annulo？', { name: d.name })}</div>
                <div className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{t('允许后，这台设备能看后台、用助手，等于能在这台电脑上执行命令。不是你自己的设备就拒绝。')}</div>
              </div>
              <div className="flex gap-2">
                <Button size="sm" onClick={() => void act(() => approveDevice(d.id))}>{t('允许')}</Button>
                <Button size="sm" variant="outline" onClick={() => void act(() => removeDevice(d.id))}>{t('拒绝')}</Button>
              </div>
            </div>
          ))}
          {active.length > 0 && (
            <>
              <div className="px-2.5 pt-1.5 pb-1 text-xs text-muted-foreground">{t('正在远程使用这台电脑')}</div>
              {active.map((d) => (
                <div key={d.id} className="flex items-center gap-2.5 rounded-lg px-2.5 py-2 hover:bg-accent">
                  <Smartphone className="size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate font-medium">{d.name}</span>
                  <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => void act(() => removeDevice(d.id))}>{t('撤销')}</Button>
                </div>
              ))}
            </>
          )}
          <button type="button" onClick={() => { setOpen(false); navigate('settings', '#remote') }} className="block w-full cursor-pointer rounded-lg px-2.5 py-2 text-left text-xs text-muted-foreground hover:bg-accent hover:text-foreground">
            {t('管理远程访问')}
          </button>
          {err && <p className="px-2.5 py-1.5 text-xs text-destructive">{err}</p>}
        </div>
      )}
    </div>
  )
}
