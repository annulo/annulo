import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertCircle, Loader, X } from 'lucide-react'
import type { FileUIPart } from 'ai'
import { uploadImage } from '@/lib/api'
import { cn } from '@/lib/utils'
import { ZoomImage } from '@/components/ui/lightbox'
import { t } from '@/lib/i18n'
import { randomId } from '@/lib/id'

// 对话输入框的图片附件：粘贴、拖进来、点按钮选，选中就开始上传到本机，发消息时只带引用地址。

const TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const MAX_BYTES = 20 << 20
const MAX_COUNT = 8

export const ACCEPT = TYPES.join(',')

export type Attachment = {
  key: string
  name: string
  type: string
  preview: string // 本地 object URL，上传完成前就能显示
  url?: string
  error?: string
}

export function useAttachments() {
  const [items, setItems] = useState<Attachment[]>([])
  const itemsRef = useRef(items)
  itemsRef.current = items

  const patch = (key: string, p: Partial<Attachment>) => setItems((xs) => xs.map((x) => (x.key === key ? { ...x, ...p } : x)))

  const add = useCallback((files: Iterable<File>) => {
    const room = MAX_COUNT - itemsRef.current.length
    const list = [...files].filter((f) => f.type.startsWith('image/')).slice(0, Math.max(room, 0))
    for (const f of list) {
      const a: Attachment = {
        key: randomId(),
        name: f.name || t('粘贴的图片'),
        type: f.type,
        preview: URL.createObjectURL(f),
      }
      if (!TYPES.includes(f.type)) a.error = t('只支持 PNG、JPEG、GIF、WebP')
      else if (f.size > MAX_BYTES) a.error = t('图片超过 20 MB')
      setItems((xs) => [...xs, a])
      if (a.error) continue
      uploadImage(f).then(
        (url) => patch(a.key, { url }),
        (e: Error) => patch(a.key, { error: e.message }),
      )
    }
    return list.length
  }, [])

  const remove = (key: string) =>
    setItems((xs) => {
      const x = xs.find((i) => i.key === key)
      if (x?.preview.startsWith('blob:')) URL.revokeObjectURL(x.preview)
      return xs.filter((i) => i.key !== key)
    })

  // 排队的消息拿回输入框时，把已经上传好的图片放回来
  const restore = (parts: FileUIPart[]) =>
    setItems((xs) => [...xs, ...parts.map((p) => ({ key: randomId(), name: p.filename || '图片', type: p.mediaType, preview: p.url, url: p.url }))].slice(0, MAX_COUNT))

  const clear = () => {
    itemsRef.current.forEach((x) => x.preview.startsWith('blob:') && URL.revokeObjectURL(x.preview))
    setItems([])
  }

  useEffect(() => () => itemsRef.current.forEach((x) => x.preview.startsWith('blob:') && URL.revokeObjectURL(x.preview)), [])

  const uploading = items.some((x) => !x.url && !x.error)
  const parts: FileUIPart[] = items
    .filter((x) => x.url)
    .map((x) => ({
      type: 'file',
      mediaType: x.type,
      filename: x.name,
      url: x.url!,
    }))
  return {
    items,
    add,
    remove,
    clear,
    restore,
    uploading,
    parts,
    full: items.length >= MAX_COUNT,
  }
}

/** 输入框上方的缩略图条 */
export function AttachmentStrip({ items, onRemove }: { items: Attachment[]; onRemove: (key: string) => void }) {
  if (!items.length) return null
  return (
    <ul className="flex flex-wrap gap-2" aria-label={t('附加的图片')}>
      {items.map((a) => (
        <li key={a.key} className="group relative" title={a.error ? `${a.name}：${a.error}` : a.name}>
          <img src={a.preview} alt={a.name} className={cn('size-14 rounded-lg border border-border object-cover', a.error && 'opacity-40')} />
          {!a.url && !a.error && (
            <span className="absolute inset-0 flex items-center justify-center rounded-lg bg-background/60">
              <Loader className="size-4 animate-spin text-foreground" aria-label={t('上传中')} />
            </span>
          )}
          {a.error && (
            <span className="absolute inset-0 flex items-center justify-center rounded-lg">
              <AlertCircle className="size-5 text-destructive" aria-label={a.error} />
            </span>
          )}
          <button
            type="button"
            onClick={() => onRemove(a.key)}
            aria-label={t('移除 {name}', { name: a.name })}
            className="absolute -top-1.5 -right-1.5 flex size-5 items-center justify-center rounded-full border border-border bg-popover text-muted-foreground shadow-sm hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
          >
            <X className="size-3" strokeWidth={2.5} />
          </button>
        </li>
      ))}
    </ul>
  )
}

/** 用户消息里的图片：点开全屏看 */
export function MessageImages({ parts }: { parts: FileUIPart[] }) {
  if (!parts.length) return null
  return (
    <div className={cn('grid gap-1.5', parts.length === 1 ? 'grid-cols-1' : 'grid-cols-3')}>
      {parts.map((p, i) => (
        <ZoomImage
          key={i}
          src={p.url}
          alt={p.filename || '图片'}
          wrapClassName="w-full overflow-hidden rounded-md border border-border/60"
          className={cn('w-full object-cover', parts.length === 1 ? 'max-h-56' : 'aspect-square')}
        />
      ))}
    </div>
  )
}
