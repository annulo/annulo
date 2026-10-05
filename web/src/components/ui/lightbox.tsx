import { useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

/** 全屏查看（图片、图表）：点背景、按 Esc 或右上角关闭 */
export function Lightbox({ open, onClose, label, children, className }: { open: boolean; onClose: () => void; label: string; children: ReactNode; className?: string }) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  return createPortal(
    <div role="dialog" aria-modal="true" aria-label={label} onClick={onClose} className="fixed inset-0 z-50 flex items-center justify-center bg-background/90 p-4 backdrop-blur-sm sm:p-10">
      <button
        onClick={onClose}
        aria-label="关闭"
        className="absolute top-3 right-3 inline-flex size-8 items-center justify-center rounded-full border border-border bg-background text-muted-foreground hover:text-foreground"
      >
        <X className="size-4" />
      </button>
      {/* 内容区的点击不关闭 */}
      <div onClick={(e) => e.stopPropagation()} className={cn('max-h-full max-w-full', className)}>
        {children}
      </div>
    </div>,
    document.body,
  )
}

/** 点开能全屏看的图片 */
export function ZoomImage({ src, alt, className, wrapClassName }: { src: string; alt: string; className?: string; wrapClassName?: string }) {
  const [open, setOpen] = useState(false)
  const [natural, setNatural] = useState(0)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} title={t('点击放大')} aria-label={t('放大 {alt}', { alt })} className={cn('block cursor-zoom-in', wrapClassName)}>
        <img src={src} alt={alt} className={className} />
      </button>
      <Lightbox open={open} onClose={() => setOpen(false)} label={alt}>
        {/* 显式宽度：只有 viewBox 的 SVG 没有固有宽度，不给宽度会被算成 0；小图最多放大到 2 倍 */}
        <img
          src={src}
          alt={alt}
          onLoad={(e) => setNatural(e.currentTarget.naturalWidth)}
          style={{ width: natural ? Math.min(natural * 2, 1600) : 480 }}
          className="h-auto max-h-[calc(100vh-5rem)] max-w-[calc(100vw-2rem)] rounded-lg object-contain shadow-xl"
        />
      </Lightbox>
    </>
  )
}
