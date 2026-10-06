import { useEffect, useRef, useState } from 'react'
import { Loader, LogOut } from 'lucide-react'
import { post, type Status } from '@/lib/api'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

type User = NonNullable<Status['creght']['user']>

/** 顶栏右边的头像（连着 creght 才有）：当前 creght 账号和退出登录。设置、用量这些在左上角的 Annulo 菜单（AppMenu） */
export function AccountMenu({ user, host, onLoggedOut }: { user: User; host: string; onLoggedOut: () => void }) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const root = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    // 点进运营后台 iframe 时外层收不到 mousedown，但窗口会失焦
    const onBlur = () => setTimeout(() => document.activeElement instanceof HTMLIFrameElement && setOpen(false))
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('blur', onBlur)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('blur', onBlur)
    }
  }, [open])

  const logout = async () => {
    setBusy(true)
    setErr('')
    try {
      await post('login/logout')
      onLoggedOut()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div ref={root} className="relative">
      <button
        onClick={() => {
          setOpen((v) => !v)
          setErr('')
        }}
        aria-label={t('creght 账号')}
        aria-expanded={open}
        className={cn(
          'flex size-8 items-center justify-center rounded-full bg-muted text-xs font-bold text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50',
          open && 'text-foreground ring-2 ring-border',
        )}
      >
        {user.username.slice(0, 1).toUpperCase()}
      </button>
      {open && (
        <div role="menu" className="absolute top-full right-0 z-50 mt-2 w-64 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-lg">
          <div className="px-2.5 pt-2 pb-2.5">
            <div className="truncate font-semibold">{user.username}</div>
            {user.email && <div className="truncate text-xs text-muted-foreground">{user.email}</div>}
            <div className="mt-1 truncate text-xs text-muted-foreground">
              {t('creght 账号')} · {new URL(host).host}
            </div>
          </div>
          <div className="h-px bg-border" />
          <button
            role="menuitem"
            onClick={logout}
            disabled={busy}
            className="mt-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent disabled:opacity-60"
          >
            {busy ? <Loader className="size-4 animate-spin" /> : <LogOut className="size-4" />}
            {t('退出登录')}
          </button>
          {err ? (
            <p className="px-2.5 pt-1 pb-1.5 text-xs leading-relaxed text-destructive">{err}</p>
          ) : (
            <p className="px-2.5 pt-1 pb-1.5 text-xs leading-relaxed text-muted-foreground">{t('只断开这台电脑上的 creght 连接，平台上的项目和数据依然保留。')}</p>
          )}
        </div>
      )}
    </div>
  )
}
