import * as React from 'react'

/**
 * 轻量提示：悬停 / 聚焦时在元素下方显示一行说明。
 * 用 fixed 定位，不会被 overflow 容器裁掉；不接管鼠标（和平台 tooltip 一致）。
 * 以元素为中心，靠近窗口边缘时往里挪，不伸出窗口。label 为空时不显示（比如按钮打开的弹层正开着）。
 */
export function Tip({ label, children, side = 'bottom' }: { label: React.ReactNode; children: React.ReactElement; side?: 'bottom' | 'top' }) {
  const [pos, setPos] = React.useState<{ x: number; y: number } | null>(null)
  const [left, setLeft] = React.useState<number | null>(null)
  const ref = React.useRef<HTMLSpanElement>(null)
  const tip = React.useRef<HTMLSpanElement>(null)
  React.useLayoutEffect(() => {
    const w = tip.current?.offsetWidth
    if (!pos || !w) return setLeft(null)
    setLeft(Math.min(Math.max(pos.x - w / 2, 8), window.innerWidth - w - 8))
  }, [pos, label])
  const show = () => {
    const r = ref.current?.getBoundingClientRect()
    if (r) setPos({ x: r.left + r.width / 2, y: side === 'bottom' ? r.bottom + 6 : r.top - 6 })
  }
  return (
    <span ref={ref} className="inline-flex" onMouseEnter={show} onMouseLeave={() => setPos(null)} onFocus={show} onBlur={() => setPos(null)}>
      {children}
      {pos && label && (
        <span
          role="tooltip"
          ref={tip}
          style={{ left: left ?? pos.x, top: pos.y, visibility: left === null ? 'hidden' : undefined }}
          className={`pointer-events-none fixed z-50 w-fit rounded-md border border-border bg-popover px-3 py-1.5 text-xs whitespace-nowrap text-foreground shadow-md select-none ${side === 'top' ? '-translate-y-full' : ''}`}
        >
          {label}
        </span>
      )}
    </span>
  )
}
