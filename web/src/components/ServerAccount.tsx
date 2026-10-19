import { useEffect, useRef, useState, type FormEvent } from 'react'
import { KeyRound, Loader, LogOut } from 'lucide-react'
import { post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { roleLabel, type ServerUser } from '@/components/AuthPage'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

/** 顶栏右边的头像（服务端部署才有）：自己的名字和角色、改密码、退出登录。 */
export function ServerAccount({ user }: { user: ServerUser }) {
  const [open, setOpen] = useState(false)
  const [pw, setPw] = useState(false)
  const [old, setOld] = useState('')
  const [next, setNext] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const root = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => !root.current?.contains(e.target as Node) && setOpen(false)
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const logout = async () => {
    setBusy(true)
    await post('auth/logout').catch(() => {})
    location.replace('/_shuttle/')
  }
  const change = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setMsg(null)
    try {
      await post('auth/password', { old, new: next })
      setMsg({ ok: true, text: t('密码改好了') })
      setOld('')
      setNext('')
      setPw(false)
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message.replace(/ · HTTP .*$/, '') })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div ref={root} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        aria-label={t('账号')}
        aria-expanded={open}
        className={cn(
          'flex size-8 items-center justify-center rounded-full bg-muted text-xs font-bold text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50',
          open && 'text-foreground ring-2 ring-border',
        )}
      >
        {user.name.slice(0, 1).toUpperCase()}
      </button>
      {open && (
        <div role="menu" className="absolute top-full right-0 z-50 mt-2 w-64 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-lg">
          <div className="px-2.5 pt-2 pb-2.5">
            <div className="truncate font-semibold">{user.name}</div>
            <div className="text-xs text-muted-foreground">{roleLabel(user.role)}</div>
          </div>
          <div className="h-px bg-border" />
          {pw ? (
            <form onSubmit={change} className="space-y-2 px-2.5 py-2">
              <Input type="password" autoComplete="current-password" placeholder={t('原密码')} value={old} onChange={(e) => setOld(e.target.value)} />
              <Input type="password" autoComplete="new-password" placeholder={t('新密码（至少 8 位）')} value={next} onChange={(e) => setNext(e.target.value)} />
              <div className="flex justify-end gap-2">
                <Button type="button" size="sm" variant="ghost" onClick={() => setPw(false)}>
                  {t('取消')}
                </Button>
                <Button type="submit" size="sm" disabled={busy || !old || next.length < 8}>
                  {t('保存')}
                </Button>
              </div>
            </form>
          ) : (
            <button role="menuitem" onClick={() => setPw(true)} className="mt-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent">
              <KeyRound className="size-4" />
              {t('改密码')}
            </button>
          )}
          <button role="menuitem" onClick={logout} disabled={busy} className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent disabled:opacity-60">
            {busy ? <Loader className="size-4 animate-spin" /> : <LogOut className="size-4" />}
            {t('退出登录')}
          </button>
          {msg && <p className={cn('px-2.5 pt-1 pb-1.5 text-xs', msg.ok ? 'text-muted-foreground' : 'text-destructive')}>{msg.text}</p>}
        </div>
      )}
    </div>
  )
}
