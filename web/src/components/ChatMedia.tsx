import { useEffect, useState, type ReactNode } from 'react'
import type { Element, ElementContent, Root, RootContent } from 'hast'
import { defaultUrlTransform } from 'react-markdown'
import { AlertCircle, ExternalLink, FileText, FolderOpen, Loader } from 'lucide-react'
import { toast } from 'sonner'
import { canOpenLocal } from '@/lib/runtime'
import { post } from '@/lib/api'
import { Lightbox, ZoomImage } from '@/components/ui/lightbox'
import { t } from '@/lib/i18n'

// 回答里的图片、视频、音频、PDF：助手写 ![说明](/本机/绝对路径.png)，本机文件经服务端发的地址显示（chatmedia.go），
// 下面一行按钮用系统程序打开、在文件夹里显示。网上的视频、音频地址（https://…/a.mp4、a.mp3）直接用 <video>、<audio> 播。

const VIDEO = /\.(mp4|m4v|mov|webm)$/i
const AUDIO = /\.(mp3|m4a|wav|ogg|oga|opus|aac|flac)$/i
const PDF = /\.pdf$/i
const MEDIA = /\.(png|jpe?g|gif|webp|bmp|avif|svg|mp4|m4v|mov|webm|mp3|m4a|wav|ogg|oga|opus|aac|flac|pdf)$/i

/** 回答里写的是不是本机文件路径：/Users/…、~/…、file://…、C:\…；/_shuttle/、/_annulo/ 是本服务的地址，不算 */
export function localPath(src: unknown): string | null {
  if (typeof src !== 'string' || !src) return null
  let s = src.trim()
  try {
    s = decodeURI(s) // markdown 里的空格、中文可能被编码
  } catch {
    // 不是合法编码就用原样
  }
  if (/^file:\/\//i.test(s) || /^~[/\\]/.test(s) || /^[a-zA-Z]:[/\\]/.test(s)) return s
  if (s.startsWith('/') && !s.startsWith('//') && !/^\/_(annulo|shuttle)\//.test(s)) return s
  return null
}

/** 本机媒体文件的路径（扩展名是图片、视频、音频、PDF 的）；回答里的链接指向它时也显示成按钮 */
export function localMediaPath(src: unknown): string | null {
  const p = localPath(src)
  return p && MEDIA.test(p.split(/[?#]/)[0]) ? p : null
}

/** react-markdown 默认会去掉 file://、C:\ 这类地址；本机媒体路径原样留着，其他照默认 */
export function urlTransform(url: string): string {
  return localMediaPath(url) ? url : defaultUrlTransform(url)
}

type Media = { url: string; name: string; kind: 'image' | 'video' | 'audio' | 'pdf'; path: string }

const isMac = typeof navigator !== 'undefined' && /Mac/.test(navigator.platform)

function openLocal(path: string, reveal: boolean) {
  post('local/open', { path, reveal }).catch((e: Error) => toast.error(e.message))
}

/** 打开、在文件夹里显示两个按钮；回答里指向本机媒体文件的链接也用它 */
export function LocalFileActions({ path, name }: { path: string; name?: string }) {
  // 远程访问、服务端部署：用户不在这台电脑前面，打开了也看不到，只留文件名
  if (!canOpenLocal()) return name ? <span className="min-w-0 truncate text-xs text-muted-foreground" title={path}>{name}</span> : null
  return (
    <span className="inline-flex max-w-full items-center gap-1 align-middle text-xs text-muted-foreground">
      {name && <span className="min-w-0 truncate" title={path}>{name}</span>}
      <button
        type="button"
        onClick={() => openLocal(path, false)}
        title={t('用系统默认程序打开')}
        className="inline-flex h-6 shrink-0 cursor-pointer items-center gap-1 rounded-md px-1.5 hover:bg-muted hover:text-foreground"
      >
        <ExternalLink className="size-3.5" />
        {t('打开')}
      </button>
      <button
        type="button"
        onClick={() => openLocal(path, true)}
        title={path}
        className="inline-flex h-6 shrink-0 cursor-pointer items-center gap-1 rounded-md px-1.5 hover:bg-muted hover:text-foreground"
      >
        <FolderOpen className="size-3.5" />
        {isMac ? t('在访达中显示') : t('在文件夹中显示')}
      </button>
    </span>
  )
}

/** 本机文件换成能显示的地址（chatmedia.go 发的 token 地址） */
function useLocalMedia(path: string | null) {
  const [media, setMedia] = useState<Media | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!path) return
    let live = true
    setMedia(null)
    setError('')
    post('local/media', { path })
      .then((r) => r.json())
      .then((m: Media) => live && setMedia(m))
      .catch((e: Error) => live && setError(e.message.replace(/ · HTTP .*$/, '')))
    return () => {
      live = false
    }
  }, [path])
  return { media, error }
}

/** 回答里的一张图、一段视频或音频、一份 PDF */
export function ChatMedia({ src, alt }: { src: string; alt?: string }) {
  const path = localPath(src)
  if (!path) {
    if (VIDEO.test(src.split(/[?#]/)[0])) return <video src={src} controls preload="metadata" className="my-1 max-h-80 max-w-full rounded-md border border-border/60" />
    if (AUDIO.test(src.split(/[?#]/)[0])) return <audio src={src} controls preload="metadata" className="my-1 w-80 max-w-full" />
    if (PDF.test(src.split(/[?#]/)[0])) return <PdfCard url={src} name={alt || src.split(/[?#]/)[0].split('/').pop() || 'PDF'} />
    return <ZoomImage src={src} alt={alt || t('图片')} wrapClassName="my-1 inline-block max-w-full" className="max-h-60 max-w-[min(20rem,100%)] rounded-md border border-border/60" />
  }
  return <LocalMedia path={path} alt={alt} />
}

function MediaError({ path, error }: { path: string; error: string }) {
  return (
    <span className="my-1 inline-flex max-w-full items-center gap-1.5 rounded-md border border-border/60 px-2 py-1 align-middle text-xs text-muted-foreground">
      <AlertCircle className="size-3.5 shrink-0 text-destructive" />
      <span className="min-w-0 truncate" title={path}>{error}</span>
    </span>
  )
}

function LocalMedia({ path, alt }: { path: string; alt?: string }) {
  const { media, error } = useLocalMedia(path)
  if (error) return <MediaError path={path} error={error} />
  if (!media)
    return (
      <span className="my-1 inline-flex size-16 items-center justify-center rounded-md border border-border/60 align-middle">
        <Loader className="size-4 animate-spin text-muted-foreground" aria-label={t('加载中')} />
      </span>
    )
  return (
    <span className="my-1 inline-flex max-w-full flex-col items-start gap-0.5 align-top">
      {media.kind === 'video' ? (
        <video src={media.url} controls preload="metadata" className="max-h-80 max-w-full rounded-md border border-border/60" />
      ) : media.kind === 'audio' ? (
        <audio src={media.url} controls preload="metadata" className="w-80 max-w-full" />
      ) : media.kind === 'pdf' ? (
        <PdfCard url={media.url} name={alt || media.name} />
      ) : (
        // 文件可能被覆盖（重新生成的二维码），地址不变，靠 no-cache 每次重新验证
        <ZoomImage src={media.url} alt={alt || media.name} wrapClassName="inline-block max-w-full" className="max-h-60 max-w-[min(20rem,100%)] rounded-md border border-border/60" />
      )}
      <LocalFileActions path={media.path} name={media.name} />
    </span>
  )
}

/** PDF：一张卡片，点了在新标签页打开（手机浏览器嵌在页面里显示不了） */
function PdfCard({ url, name }: { url: string; name: string }) {
  return (
    <a href={url} target="_blank" rel="noreferrer" title={t('打开')} className="inline-flex max-w-full items-center gap-2 rounded-md border border-border/60 px-3 py-2 text-sm text-foreground no-underline hover:bg-muted">
      <FileText className="size-4 shrink-0 text-destructive" />
      <span className="min-w-0 truncate">{name}</span>
      <ExternalLink className="size-3.5 shrink-0 text-muted-foreground" />
    </a>
  )
}

// ---- 多张图排成网格 ----
// 助手把几张图连着写（每行一张），markdown 里是同一段；中间空了行就是挨着的几段。
// rehypeImageGroups 把挨着的「只有图」的段落合成一段，ChatPanel 的 p 组件再用 imageGroup 判断：两张以上、都是图片（不含视频、音频、PDF）就画成网格。

const isBlank = (n: ElementContent) => (n.type === 'text' && !n.value.trim()) || (n.type === 'element' && n.tagName === 'br')
const isImageOnly = (n: ElementContent | RootContent): boolean =>
  n.type === 'element' && n.tagName === 'p' && n.children.some((c) => c.type === 'element' && c.tagName === 'img') && n.children.every((c) => isBlank(c) || (c.type === 'element' && c.tagName === 'img'))

export function rehypeImageGroups() {
  const merge = (parent: Root | Element) => {
    const out: typeof parent.children = []
    let group: Element | null = null
    for (const c of parent.children) {
      if (isImageOnly(c)) {
        const kids = (c as Element).children.filter((x) => !isBlank(x))
        if (group) group.children.push(...kids)
        else {
          group = { ...(c as Element), children: kids }
          out.push(group)
        }
        continue
      }
      if (group && c.type === 'text' && !c.value.trim()) continue // 段落之间的换行
      group = null
      if (c.type === 'element') merge(c)
      out.push(c as never)
    }
    parent.children = out as never
  }
  return (tree: Root) => merge(tree)
}

/** 段落里两张以上图片、没有别的（也没有视频、音频、PDF）时，返回这些图的 src 和说明 */
export function imageGroup(node: Element | undefined): { src: string; alt: string }[] | null {
  if (!node || !isImageOnly(node)) return null
  const imgs = node.children.filter((c): c is Element => c.type === 'element' && c.tagName === 'img')
  const list = imgs.map((i) => ({ src: String(i.properties.src ?? ''), alt: String(i.properties.alt ?? '') }))
  if (list.length < 2 || list.some((i) => !i.src || VIDEO.test(i.src.split(/[?#]/)[0]) || AUDIO.test(i.src.split(/[?#]/)[0]) || PDF.test(i.src.split(/[?#]/)[0]))) return null
  return list
}

/** 多张图：固定大小的正方形缩略图（112px），按面板宽度换行；点开看大图，本机文件在大图下面和鼠标移上去时有打开按钮 */
export function MediaGrid({ items }: { items: { src: string; alt: string }[] }) {
  return (
    <div className="my-1 flex flex-wrap gap-1.5">
      {items.map((it, i) => (
        <GridItem key={i} src={it.src} alt={it.alt} />
      ))}
    </div>
  )
}

function GridItem({ src, alt }: { src: string; alt: string }) {
  const path = localPath(src)
  const { media, error } = useLocalMedia(path)
  const [open, setOpen] = useState(false)
  const url = path ? media?.url : src
  const name = media?.name || alt || t('图片')
  if (error)
    return (
      <div title={`${path}：${error}`} className="flex size-28 flex-col items-center justify-center gap-1 rounded-md border border-border/60 p-2 text-center text-[11px] text-muted-foreground">
        <AlertCircle className="size-4 text-destructive" />
        <span className="line-clamp-3 break-all">{error}</span>
      </div>
    )
  if (!url)
    return (
      <div className="flex size-28 items-center justify-center rounded-md border border-border/60">
        <Loader className="size-4 animate-spin text-muted-foreground" aria-label={t('加载中')} />
      </div>
    )
  return (
    <div className="group/cell relative size-28 overflow-hidden rounded-md border border-border/60">
      <button type="button" onClick={() => setOpen(true)} title={t('点击放大')} aria-label={t('放大 {alt}', { alt: name })} className="block size-full cursor-zoom-in">
        <img src={url} alt={name} className="size-full object-cover" />
      </button>
      {path && (
        <div className="absolute inset-x-1 bottom-1 hidden justify-end gap-1 group-hover/cell:flex">
          <IconAction onClick={() => openLocal(path, false)} label={t('用系统默认程序打开')} icon={<ExternalLink className="size-3.5" />} />
          <IconAction onClick={() => openLocal(path, true)} label={isMac ? t('在访达中显示') : t('在文件夹中显示')} icon={<FolderOpen className="size-3.5" />} />
        </div>
      )}
      <Lightbox open={open} onClose={() => setOpen(false)} label={name}>
        <div className="flex flex-col items-center gap-2">
          <img src={url} alt={name} className="h-auto max-h-[calc(100vh-7rem)] max-w-[calc(100vw-2rem)] rounded-lg object-contain shadow-xl" />
          {path && <LocalFileActions path={path} name={name} />}
        </div>
      </Lightbox>
    </div>
  )
}

function IconAction({ onClick, label, icon }: { onClick: () => void; label: string; icon: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      className="inline-flex size-6 cursor-pointer items-center justify-center rounded-md border border-border/60 bg-background/85 text-muted-foreground shadow-sm backdrop-blur hover:text-foreground"
    >
      {icon}
    </button>
  )
}
