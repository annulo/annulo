import { useEffect, useRef, useState } from 'react'
import { AppWindow, Check, ChevronsUpDown, Loader, Plus, Settings2, Smartphone } from 'lucide-react'
import { followProject, getJSON, post, projectURL } from '@/lib/api'
import { navigate } from '@/lib/router'
import type { SetupState } from '@/components/BackendPicker'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

/** 顶栏的项目切换。项目之间数据、后台页面、对话历史都互相隔离；每个项目一个地址，切换就是这个窗口换到那个项目的地址，
 *  别的项目照常开着、对话照常跑。「在新窗口打开」同时看几个项目。 */
export function WorkspaceSwitcher({ current, remote: remoteView = false }: { current: string; remote?: boolean }) {
  const [st, setSt] = useState<SetupState | null>(null)
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const root = useRef<HTMLDivElement>(null)
  // 菜单从按钮左边往右展开；手机上按钮在屏幕中间，放不下就往左挪，屏幕太窄就缩窄，两边各留 12px
  const [place, setPlace] = useState<{ left: number; width: number }>({ left: 0, width: 288 })
  const toggle = () => {
    if (!open && root.current) {
      const x = root.current.getBoundingClientRect().left
      const width = Math.min(288, window.innerWidth - 24)
      setPlace({ width, left: Math.min(0, window.innerWidth - 12 - x - width) })
    }
    setOpen(!open)
  }

  const load = () =>
    getJSON<SetupState>('setup')
      .then(setSt)
      .catch((e: Error) => setErr(e.message))
  useEffect(() => {
    load()
  }, [current])

  useEffect(() => {
    if (!open) return
    load()
    const onDown = (e: MouseEvent) => !root.current?.contains(e.target as Node) && setOpen(false)
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

  const cur = st?.backends.find((b) => b.project_id === current)
  const name = cur?.name || (st ? current : '')

  const use = async (pid: string) => {
    setBusy(pid)
    setErr('')
    try {
      const r = await post('setup', { action: 'use', project_id: pid })
      if (!(await followProject(r))) location.reload()
    } catch (e) {
      setErr((e as Error).message)
      setBusy('')
    }
  }
  const manage = () => {
    setOpen(false)
    navigate('settings', '#backend')
  }
  const remote = () => {
    setOpen(false)
    navigate('settings', '#remote') // 地址、二维码在 设置 → 远程访问
  }

  return (
    <div ref={root} className="relative -ml-1.5 min-w-0 max-w-64">
      <button
        onClick={toggle}
        aria-expanded={open}
        aria-haspopup="menu"
        className={cn(
          'flex max-w-full min-w-0 items-center gap-1.5 rounded-lg px-1.5 py-1 text-xs font-bold outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
          open && 'bg-accent',
        )}
        title={t('切换项目')}
      >
        <span className="truncate">{name || <span className="text-muted-foreground">{t('项目')}</span>}</span>
        <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
      </button>
      {open && (
        <div role="menu" style={place} className="absolute top-full z-50 mt-2 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-xl">
          <div className="px-2.5 pt-1.5 pb-1 text-[11px] font-semibold text-muted-foreground">{t('项目')}</div>
          {!st ? (
            <div className="flex justify-center py-4">
              <Loader className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="scroll-thin max-h-72 overflow-y-auto">
              {st.backends.map((b) => (
                <div key={b.project_id} className="group relative">
                <button
                  role="menuitemradio"
                  aria-checked={b.project_id === current}
                  disabled={!!busy}
                  onClick={() => (b.project_id === current ? setOpen(false) : use(b.project_id))}
                  className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent disabled:opacity-60"
                >
                  <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-[11px] font-bold text-muted-foreground">{b.name.slice(0, 1).toUpperCase()}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13px] font-semibold">{b.name}</span>
                    <span className="block truncate font-mono text-[11px] text-muted-foreground">{b.project_id}</span>
                  </span>
                  {busy === b.project_id ? (
                    <Loader className="size-4 shrink-0 animate-spin text-muted-foreground" />
                  ) : (
                    b.project_id === current && <Check className="size-4 shrink-0 text-primary-text group-hover:invisible" />
                  )}
                </button>
                {b.local !== false && projectURL(b.project_id) && (
                  <button
                    role="menuitem"
                    title={t('在新窗口打开')}
                    aria-label={t('在新窗口打开')}
                    onClick={() => {
                      setOpen(false)
                      window.open(projectURL(b.project_id)!, '_blank')
                    }}
                    className="absolute top-1/2 right-2 hidden size-7 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground outline-none group-hover:flex hover:bg-background hover:text-foreground focus-visible:flex"
                  >
                    <AppWindow className="size-4" />
                  </button>
                )}
                </div>
              ))}
            </div>
          )}
          {err && <p className="px-2.5 py-1.5 text-xs leading-relaxed text-destructive">{err}</p>}
          <div className="my-1 h-px bg-border" />
          {/* 远程访问（隧道）要 creght 账号：没连就不出现；离线项目也能远程打开 */}
          {st?.logged_in && cur && !remoteView && (
            <button role="menuitem" onClick={remote} className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent">
              <Smartphone className="size-4 text-muted-foreground" />
              {t('在浏览器中打开')}
            </button>
          )}
          <button role="menuitem" onClick={manage} className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent">
            <Plus className="size-4 text-muted-foreground" />
            {t('新建项目')}
          </button>
          <button role="menuitem" onClick={manage} className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent">
            <Settings2 className="size-4 text-muted-foreground" />
            {t('管理项目')}
          </button>
        </div>
      )}
    </div>
  )
}
