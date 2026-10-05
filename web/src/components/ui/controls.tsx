import * as React from 'react'
import { cn } from '@/lib/utils'

// 表单控件，class 照搬平台 text-field / select / switch / tabs。

const field =
  'placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground border-input flex w-full rounded-md border bg-input-background px-3 py-1 text-sm shadow-xs transition-[color,box-shadow] outline-none ' +
  'focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 ' +
  'aria-[invalid=true]:border-destructive aria-[invalid=true]:ring-destructive/20'

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(({ className, ...p }, ref) => (
  <input ref={ref} data-slot="input" className={cn(field, 'h-9', className)} {...p} />
))
Input.displayName = 'Input'

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(({ className, ...p }, ref) => (
  <textarea ref={ref} data-slot="textarea" className={cn(field, 'min-h-16 py-2', className)} {...p} />
))
Textarea.displayName = 'Textarea'

// 下拉选择不在这里：用 ui/select.tsx 的 Select（弹出菜单，不用系统的 <select>）

export function Switch({ checked, onChange, disabled, label }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      data-checked={checked || undefined}
      className="peer inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent bg-input shadow-xs transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[checked]:bg-primary"
    >
      <span
        data-checked={checked || undefined}
        className="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-background shadow-sm transition-transform data-[checked]:translate-x-4"
      />
    </button>
  )
}

/**
 * 平台的分段切换（Tabs 那一套）：灰底圆角槽 + 选中项白色（深色下半透明）滑块。
 * variant=pill 是编辑器页面面板「PAGE | LAYERS」的胶囊样式。
 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  className,
  variant = 'default',
}: {
  value: T
  onChange: (v: T) => void
  options: { value: T; label: React.ReactNode }[]
  className?: string
  variant?: 'default' | 'pill'
}) {
  const pill = variant === 'pill'
  return (
    <div
      role="tablist"
      className={cn(
        'relative flex w-fit items-center border border-border bg-muted text-muted-foreground',
        pill ? 'h-8 rounded-full px-[2px]' : 'h-9 gap-0.5 rounded-xl px-1',
        className,
      )}
    >
      {options.map((o) => {
        const on = o.value === value
        return (
          <button
            key={o.value}
            role="tab"
            aria-selected={on}
            onClick={() => onChange(o.value)}
            className={cn(
              'relative inline-flex flex-1 items-center justify-center gap-1.5 border whitespace-nowrap transition-[color,background-color,box-shadow] duration-200 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-3.5',
              pill ? 'my-[2px] h-[calc(100%-4px)] rounded-full px-3 text-[11px] font-bold' : 'my-1 h-[calc(100%-8px)] rounded-lg px-3 text-xs font-bold',
              on
                ? 'border-transparent bg-background text-primary-text shadow-sm dark:border-input dark:bg-input-background'
                : 'border-transparent hover:text-foreground',
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

/** 时间范围切换：照搬平台「设置 → 分析」的范围按钮组 */
export function RangeToggle<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: { value: T; label: string }[] }) {
  return (
    <div role="radiogroup" className="inline-flex gap-1 rounded-lg border border-border p-1">
      {options.map((o) => {
        const on = o.value === value
        return (
          <button
            key={o.value}
            role="radio"
            aria-checked={on}
            onClick={() => onChange(o.value)}
            className={cn(
              'inline-flex h-7 items-center rounded-md px-3 text-xs font-medium transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
              on ? 'bg-primary text-primary-foreground shadow-xs' : 'text-muted-foreground hover:bg-accent hover:text-foreground dark:hover:bg-accent/50',
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

export function Badge({ children, tone = 'default', className }: { children: React.ReactNode; tone?: 'default' | 'ok' | 'warn' | 'bad' | 'primary'; className?: string }) {
  // 平台的徽标是行内 span：rounded-md border bg-*/10 px-2 py-0.5 text-[10px] font-semibold
  const tones = {
    default: 'border-border bg-muted text-muted-foreground',
    ok: 'border-ok/15 bg-ok/10 text-ok',
    warn: 'border-warn/15 bg-warn/10 text-warn',
    bad: 'border-destructive/15 bg-destructive/10 text-destructive',
    primary: 'border-primary/10 bg-primary/10 text-primary-text',
  }
  return <span className={cn('inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[10px] font-semibold whitespace-nowrap', tones[tone], className)}>{children}</span>
}

/** 状态点 + 文字：状态不只靠颜色 */
export function StatusDot({ tone, children }: { tone: 'ok' | 'warn' | 'bad' | 'idle'; children?: React.ReactNode }) {
  const c = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-destructive', idle: 'bg-muted-foreground/50' }[tone]
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn('size-1.5 shrink-0 rounded-full', c)} aria-hidden />
      {children}
    </span>
  )
}

export function Field({ label, hint, children, htmlFor }: { label: React.ReactNode; hint?: React.ReactNode; children: React.ReactNode; htmlFor?: string }) {
  return (
    <div className="grid content-start gap-2">
      <label htmlFor={htmlFor} className="text-sm leading-none font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs leading-relaxed text-muted-foreground">{hint}</p>}
    </div>
  )
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn('animate-pulse rounded-md bg-muted', className)} />
}
