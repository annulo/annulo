import { useEffect, useRef, useState } from 'react'
import { BarChart3, ChevronDown, Info, Link2, Moon, Settings, Sun } from 'lucide-react'
import { ConnectDialog } from '@/components/SetupPage'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'
import { navigate } from '@/lib/router'

/** 左上角 logo：Annulo 自己的菜单（设置、用量、深浅色、关于），连不连 creght 都在；没连时多一项「连接 creght」。
 *  设置和用量主要从运营后台里的入口打开（弹窗），这里是兜底：模板还没升级、没有那个入口时也进得去 */
export function AppMenu({ theme, onToggleTheme, loggedIn }: { theme: 'dark' | 'light'; onToggleTheme: () => void; loggedIn: boolean }) {
  const [open, setOpen] = useState(false)
  const [connecting, setConnecting] = useState(false)
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

  const mac = /Mac/.test(navigator.platform)
  const items = [
    { label: t('设置'), icon: Settings, go: () => navigate('settings'), hint: mac ? '⌘,' : 'Ctrl+,' },
    { label: t('用量'), icon: BarChart3, go: () => navigate('settings', '#usage') },
    { label: theme === 'dark' ? t('切换到浅色') : t('切换到深色'), icon: theme === 'dark' ? Sun : Moon, go: onToggleTheme },
    { label: t('关于 Annulo'), icon: Info, go: () => navigate('settings', '#about') },
  ]

  return (
    <div ref={root} className="relative shrink-0">
      <button
        onClick={() => setOpen((v) => !v)}
        aria-label={t('Annulo 菜单')}
        aria-expanded={open}
        aria-haspopup="menu"
        className={cn(
          'flex h-9 items-center gap-2 rounded-lg px-1.5 outline-none transition-colors hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
          open && 'bg-accent',
        )}
      >
        <img src={theme === 'dark' ? '/_shuttle/annulo-dark.svg' : '/_shuttle/annulo-light.svg'} alt="" className="h-4" />
        <span className="text-xs font-bold">Annulo</span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </button>
      {open && (
        <div role="menu" className="absolute top-full left-0 z-50 mt-1 w-60 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-lg">
          {items.map((it) => (
            <button
              key={it.label}
              role="menuitem"
              onClick={() => {
                it.go()
                setOpen(false)
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
            >
              <it.icon className="size-4" />
              <span className="flex-1">{it.label}</span>
              {it.hint && <span className="text-xs text-muted-foreground">{it.hint}</span>}
            </button>
          ))}
          {!loggedIn && (
            <>
              <div className="my-1 h-px bg-border" />
              <button
                role="menuitem"
                onClick={() => {
                  setOpen(false)
                  setConnecting(true)
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
              >
                <Link2 className="size-4" />
                {t('连接 creght')}
              </button>
            </>
          )}
        </div>
      )}
      {connecting && <ConnectDialog onClose={() => setConnecting(false)} />}
    </div>
  )
}
