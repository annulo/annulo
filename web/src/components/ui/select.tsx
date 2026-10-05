import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

export type SelectOption<T extends string> = {
  value: T
  label: string
  /** 第二行的小字（id、地址、说明） */
  sub?: string
  /** 左边的图标块；不给就用名字的第一个字 */
  icon?: ReactNode
}

/**
 * 下拉选择：按钮 + 弹出菜单，样子和顶栏的项目切换一致（标题、每项一个图标块、两行字、选中的打勾）。
 * 界面里的下拉都用它，不用系统的 <select>：各平台长得不一样，也放不了图标和第二行字。
 *
 * 菜单用 fixed 定位，放在 overflow-hidden 的卡片里也不会被裁；下面放不下就往上弹。
 * 键盘：↑↓ 移动、Enter 选、Esc 关。
 */
export function Select<T extends string>({
  value,
  onChange,
  options,
  title,
  placeholder = t('请选择'),
  id,
  ariaLabel,
  disabled,
  className,
}: {
  value: T
  onChange: (v: T) => void
  options: SelectOption<T>[]
  /** 菜单顶上的小标题 */
  title?: string
  placeholder?: string
  id?: string
  ariaLabel?: string
  disabled?: boolean
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ left: number; width: number; top?: number; bottom?: number } | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    if (!open || !btn.current) return
    const r = btn.current.getBoundingClientRect()
    const width = Math.max(r.width, 240)
    const left = Math.max(8, Math.min(r.left, window.innerWidth - width - 8))
    // 下面放不下（菜单最高约 320px）就往上弹
    setPos(window.innerHeight - r.bottom < 330 && r.top > window.innerHeight - r.bottom ? { left, width, bottom: window.innerHeight - r.top + 6 } : { left, width, top: r.bottom + 6 })
  }, [open])

  useEffect(() => {
    if (!open) return
    // 打开时把焦点放到选中的那项上
    ;(menu.current?.querySelector<HTMLElement>('[aria-checked="true"]') ?? menu.current?.querySelector<HTMLElement>('button'))?.focus({ preventScroll: true })
    const onDown = (e: MouseEvent) => !btn.current?.contains(e.target as Node) && !menu.current?.contains(e.target as Node) && setOpen(false)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        btn.current?.focus()
      }
    }
    // 滚动、改窗口大小时位置会错，直接关掉；点进运营后台 iframe 时外层收不到 mousedown，但窗口会失焦
    const onScroll = (e: Event) => !menu.current?.contains(e.target as Node) && setOpen(false)
    const close = () => setOpen(false)
    const onBlur = () => setTimeout(() => document.activeElement instanceof HTMLIFrameElement && setOpen(false))
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', close)
    window.addEventListener('blur', onBlur)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', close)
      window.removeEventListener('blur', onBlur)
    }
  }, [open, pos])

  const move = (e: React.KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const items = [...(menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]') ?? [])]
    const i = items.indexOf(document.activeElement as HTMLButtonElement)
    items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
  }

  const cur = options.find((o) => o.value === value)
  return (
    <>
      <button
        ref={btn}
        id={id}
        type="button"
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !open) {
            e.preventDefault()
            setOpen(true)
          }
        }}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={ariaLabel}
        className={cn(
          'flex h-9 w-full min-w-0 cursor-pointer items-center gap-2 rounded-md border border-input bg-input-background px-3 text-left text-sm shadow-xs outline-none transition-[color,box-shadow]',
          'hover:bg-accent/50 focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50',
          open && 'bg-accent/50',
          className,
        )}
      >
        {cur?.icon && <span className="flex shrink-0 text-muted-foreground">{cur.icon}</span>}
        <span className={cn('min-w-0 flex-1 truncate', !cur && 'text-muted-foreground')}>{cur?.label ?? placeholder}</span>
        <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>
      {open && pos && (
        <div
          ref={menu}
          role="menu"
          onKeyDown={move}
          style={{ position: 'fixed', left: pos.left, width: pos.width, top: pos.top, bottom: pos.bottom }}
          className="z-50 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-xl"
        >
          {title && <div className="px-2.5 pt-1.5 pb-1 text-[11px] font-semibold text-muted-foreground">{title}</div>}
          <div className="scroll-thin max-h-72 overflow-y-auto">
            {options.map((o) => (
              <button
                key={o.value}
                type="button"
                role="menuitemradio"
                aria-checked={o.value === value}
                onClick={() => {
                  setOpen(false)
                  btn.current?.focus()
                  if (o.value !== value) onChange(o.value)
                }}
                className="flex w-full cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
              >
                <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-[11px] font-bold text-muted-foreground">{o.icon ?? o.label.slice(0, 1).toUpperCase()}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-semibold">{o.label}</span>
                  {o.sub && <span className="block truncate text-[11px] text-muted-foreground">{o.sub}</span>}
                </span>
                {o.value === value && <Check className="size-4 shrink-0 text-primary-text" />}
              </button>
            ))}
          </div>
        </div>
      )}
    </>
  )
}
