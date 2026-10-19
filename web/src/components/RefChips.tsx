import { useEffect, useState, type ReactNode } from 'react'
import { FileText, Film } from 'lucide-react'
import { post } from '@/lib/api'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

// 引用 @<表>:<id>#<n>（docs/references.md）：对话的输入框和消息里显示成小标签「表名 · 标题」，文件带缩略图。
// 表是项目动态声明的，标题、缩略图由 Annulo 按表声明推出来（POST refs/resolve，internal/server/refs.go）。

export const REF_RE = /@([a-z][a-z0-9_]*):([A-Za-z0-9_-]+)(?:#(\d+))?/g

export type RefInfo = { ref: string; table: string; table_name: string; id: string; n?: number; title: string; mime?: string; file?: string; thumb?: string; missing?: boolean }

const cache = new Map<string, RefInfo>()
const pending = new Set<string>()
const listeners = new Set<() => void>()

/** 文字里的引用（去重，最多 20 个） */
export const refsIn = (text: string) => [...new Set([...text.matchAll(REF_RE)].map((m) => m[0]))].slice(0, 20)

async function resolve(refs: string[]) {
  const need = refs.filter((r) => !cache.has(r) && !pending.has(r))
  if (!need.length) return
  need.forEach((r) => pending.add(r))
  try {
    const res = await post('refs/resolve', { refs: need })
    const { list } = (await res.json()) as { list: RefInfo[] }
    for (const x of list) cache.set(x.ref, x)
  } catch {
    // 解析不了（老版本、网络）：标签就显示原文
  } finally {
    need.forEach((r) => pending.delete(r))
    listeners.forEach((f) => f())
  }
}

/** 这些引用的解析结果（查过的缓存着，新出现的才去查） */
export function useRefs(refs: string[]) {
  const [, tick] = useState(0)
  const key = refs.join(' ')
  useEffect(() => {
    const f = () => tick((n) => n + 1)
    listeners.add(f)
    void resolve(refs)
    return () => void listeners.delete(f)
  }, [key])
  return (r: string) => cache.get(r)
}

export function RefChip({ ref: r, info }: { ref: string; info?: RefInfo }) {
  const label = info ? (info.missing ? t('已删除') : `${info.table_name} · ${info.title || info.id}${info.n ? ` #${info.n}${info.file ? ` ${info.file}` : ''}` : ''}`) : r
  return (
    <span
      title={r}
      className={cn(
        'mx-0.5 inline-flex max-w-full items-center gap-1 rounded-md border px-1.5 py-px align-middle text-xs whitespace-nowrap',
        info?.missing ? 'border-border text-muted-foreground line-through' : 'border-primary/30 bg-primary/10 text-primary-text',
      )}
    >
      {info?.thumb && info.mime?.startsWith('image/') ? (
        <img src={info.thumb} alt="" className="size-4 shrink-0 rounded-sm object-cover" />
      ) : info?.mime?.startsWith('video/') ? (
        <Film className="size-3.5 shrink-0" />
      ) : info && !info.missing && info.mime ? (
        <FileText className="size-3.5 shrink-0" />
      ) : null}
      <span className="min-w-0 truncate">{label}</span>
    </span>
  )
}

/** 一段文字：引用换成标签，别的原样 */
export function RefText({ text }: { text: string }) {
  const get = useRefs(refsIn(text))
  const out: ReactNode[] = []
  let last = 0
  for (const m of text.matchAll(REF_RE)) {
    const i = m.index ?? 0
    if (i > last) out.push(text.slice(last, i))
    out.push(<RefChip key={i} ref={m[0]} info={get(m[0])} />)
    last = i + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}

/** 输入框上方：这条消息里引用了哪些（输入框是纯文字，标签放在这里看） */
export function RefStrip({ text }: { text: string }) {
  const refs = refsIn(text)
  const get = useRefs(refs)
  if (!refs.length) return null
  return (
    <div className="flex flex-wrap items-center gap-1 px-1">
      {refs.map((r) => <RefChip key={r} ref={r} info={get(r)} />)}
    </div>
  )
}
