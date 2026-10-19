import { useEffect, useState } from 'react'
import { Check, ChevronRight, Copy, Loader2, RotateCcw, Send, Wrench } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/controls'
import { fmtAgo } from '@/lib/format'
import { t } from '@/lib/i18n'
import { cn } from '@/lib/utils'
import QuotaNotice, { type Quota } from '@/components/QuotaNotice'

type Source = { id: string; type: string; mode: 'push' | 'pull'; path?: string; cloud_url?: string; auth?: string }
type Box = { id: string; name: string; handler: string; sources: Source[]; notify: number; keep_days: number; today: number; pending: number; failed: number; last_at?: string; notify_error?: string }
type State = { list: Box[]; error: string; dir: string; server?: boolean; relay?: { on: boolean; error?: string; quota?: Quota } }
type Ev = { id: string; source: { id: string; type: string }; received_at: string; status: 'pending' | 'done' | 'failed'; attempts: number; error?: string; notify_error?: string; test?: boolean; via?: string; payload: unknown }

// 交给助手修：回到后台，新开一段对话，把错误和那条事件填进输入框，用户看过再发
function askFix(box: Box, ev: Ev) {
  const text = t('收件箱「{name}」（inboxes/{id}.json）的处理函数 {fn} 处理这条失败了：\n\n{err}\n\n事件：\n```json\n{ev}\n```\n\n帮我看看是哪里的问题，修好以后在 设置 → 收件箱 里点「重新处理」。', {
    name: box.name,
    id: box.id,
    fn: box.handler,
    err: ev.error ?? '',
    ev: JSON.stringify(ev, null, 2).slice(0, 4000),
  })
  window.postMessage({ type: 'shuttle:navigate', view: 'backend' }, location.origin)
  window.postMessage({ type: 'shuttle:compose', text }, location.origin)
}

function CopyURL({ url }: { url: string }) {
  const [done, setDone] = useState(false)
  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <code className="min-w-0 truncate rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]" title={url}>
        {url}
      </code>
      <button
        type="button"
        className="shrink-0 cursor-pointer rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        title={t('复制')}
        onClick={() => {
          void navigator.clipboard?.writeText(url)
          setDone(true)
          window.setTimeout(() => setDone(false), 1500)
        }}
      >
        {done ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </button>
    </div>
  )
}

const STATUS: Record<Ev['status'], { tone: 'ok' | 'warn' | 'bad'; label: string }> = {
  done: { tone: 'ok', label: '处理完了' },
  pending: { tone: 'warn', label: '等处理' },
  failed: { tone: 'bad', label: '处理失败' },
}

function Events({ box, onChange }: { box: Box; onChange: () => void }) {
  const [list, setList] = useState<Ev[] | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const load = () =>
    getJSON<{ list: Ev[] }>(`local/inboxes/${encodeURIComponent(box.id)}/events`)
      .then((r) => setList(r.list))
      .catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
  }, [box.id, box.last_at, box.failed, box.pending])

  const retry = async (ev: Ev) => {
    setBusy(ev.id)
    setErr('')
    try {
      await post(`local/inboxes/${encodeURIComponent(box.id)}/events/${ev.id}/retry`)
      await load()
      onChange()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (!list) return <div className="py-3 text-xs text-muted-foreground">{err || <Loader2 className="size-3.5 animate-spin" />}</div>
  if (list.length === 0) return <div className="py-3 text-xs text-muted-foreground">{t('还没收到过。')}</div>
  return (
    <div className="mt-2 space-y-1.5">
      {err && <p className="text-xs text-destructive">{err}</p>}
      {list.map((ev) => (
        <details key={ev.id} className="rounded-lg border border-border bg-muted/30 px-3 py-2 text-xs">
          <summary className="flex cursor-pointer flex-wrap items-center gap-x-3 gap-y-1 select-none">
            <span className="text-muted-foreground">{fmtAgo(new Date(ev.received_at).getTime())}</span>
            <span className="font-mono text-muted-foreground">{ev.source.id}</span>
            {ev.test && <span className="rounded bg-muted px-1.5 py-px text-[10px] font-semibold">{t('测试')}</span>}
            {ev.via === 'cloud' && <span className="rounded bg-muted px-1.5 py-px text-[10px] font-semibold">{t('云端代收')}</span>}
            <StatusDot tone={STATUS[ev.status].tone}>
              {t(STATUS[ev.status].label)}
              {ev.attempts > 1 ? t('（试了 {n} 次）', { n: ev.attempts }) : ''}
            </StatusDot>
            {ev.notify_error && <StatusDot tone="bad">{t('通知没发出去')}</StatusDot>}
            {ev.status !== 'done' && (
              <span className="ml-auto flex gap-1">
                <Button variant="ghost" size="sm" disabled={!!busy} onClick={(e) => (e.preventDefault(), retry(ev))}>
                  {busy === ev.id ? <Loader2 className="animate-spin" /> : <RotateCcw />}
                  {t('重新处理')}
                </Button>
                {ev.error && (
                  <Button variant="ghost" size="sm" onClick={(e) => (e.preventDefault(), askFix(box, ev))}>
                    <Wrench />
                    {t('交给助手修')}
                  </Button>
                )}
              </span>
            )}
          </summary>
          {ev.error && <div className="mt-2 rounded-md border border-destructive/30 bg-destructive/10 px-2 py-1.5 break-all whitespace-pre-wrap text-destructive">{ev.error}</div>}
          {ev.notify_error && <div className="mt-2 rounded-md border border-destructive/30 bg-destructive/10 px-2 py-1.5 break-all whitespace-pre-wrap text-destructive">{ev.notify_error}</div>}
          <pre className="scroll-thin mt-2 max-h-64 overflow-auto rounded-md bg-background p-2 font-mono text-[11px] break-all whitespace-pre-wrap">{JSON.stringify(ev.payload, null, 2)}</pre>
        </details>
      ))}
    </div>
  )
}

/** 收件箱：项目 inboxes/ 里声明的（一个收件箱一个文件），收 webhook，交给本机函数处理，收到时发通知（docs/inbox.md） */
export default function InboxSettings() {
  const [st, setSt] = useState<State | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [testing, setTesting] = useState('')

  const load = () => getJSON<State>('local/inboxes').then(setSt).catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
    const id = window.setInterval(load, 5000)
    return () => clearInterval(id)
  }, [])

  const test = async (box: Box, source: string) => {
    setTesting(box.id + '/' + source)
    setErr('')
    try {
      await post(`local/inboxes/${encodeURIComponent(box.id)}/test`, { source })
      await load()
      setOpen((o) => ({ ...o, [box.id]: true }))
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setTesting('')
    }
  }

  if (!st)
    return (
      <div className="flex justify-center py-8">
        {err ? <p className="text-sm text-destructive">{err}</p> : <Loader2 className="size-4 animate-spin text-muted-foreground" />}
      </div>
    )

  return (
    <div className="space-y-4">
      {st.error && <div className="rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm break-all whitespace-pre-wrap text-destructive">{st.error}</div>}
      {st.relay?.quota && <QuotaNotice notice={st.relay.quota} server={st.server} />}
      {st.relay?.error && <p className="text-xs text-destructive">{st.relay.error}</p>}
      {st.list.length === 0 && !st.error ? (
        <div className="rounded-xl border border-border bg-background px-5 py-6 text-sm text-muted-foreground">
          {t('这个项目还没有收件箱。要接网站表单、Stripe、GitHub 这类发过来的 webhook，在项目的')} <code className="font-mono">{st.dir}</code> {t('里加一个文件（一个收件箱一个），或者直接告诉助手「建个收件箱接网站的询盘表单」。')}
        </div>
      ) : (
        st.list.map((b) => (
          <div key={b.id} className="overflow-hidden rounded-xl border border-border bg-background">
            <div className="space-y-2 px-4 py-3">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
                <span className="text-[13px] font-bold">{b.name}</span>
                <span className="font-mono text-[11px] text-muted-foreground">{b.handler}</span>
                <span className="text-[11px] text-muted-foreground">{t('今天 {n} 条', { n: b.today })}</span>
                {b.last_at && <span className="text-[11px] text-muted-foreground">{t('最近一条 {ago}', { ago: fmtAgo(new Date(b.last_at).getTime()) })}</span>}
                {b.pending > 0 && <StatusDot tone="warn">{t('{n} 条等处理', { n: b.pending })}</StatusDot>}
                {b.failed > 0 && <StatusDot tone="bad">{t('{n} 条处理失败', { n: b.failed })}</StatusDot>}
                {b.notify_error && <StatusDot tone="bad">{t('最近一条的通知没发出去')}</StatusDot>}
              </div>
              {b.sources.map((s) => (
                <div key={s.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                  <span className="w-28 shrink-0 truncate text-muted-foreground">
                    {s.id} · {s.type}
                    {s.auth === 'hmac' ? ' · hmac' : ''}
                  </span>
                  {s.cloud_url ? (
                    <CopyURL url={s.cloud_url} />
                  ) : s.path ? (
                    <CopyURL url={location.origin + s.path} />
                  ) : (
                    <span className="text-muted-foreground">{t('定时去读')}</span>
                  )}
                  <Button variant="ghost" size="sm" className="ml-auto" disabled={!!testing} onClick={() => test(b, s.id)}>
                    {testing === b.id + '/' + s.id ? <Loader2 className="animate-spin" /> : <Send />}
                    {t('发一条测试')}
                  </Button>
                </div>
              ))}
              {st.relay?.on && b.sources.some((s) => s.cloud_url) && (
                <p className="text-[11px] text-muted-foreground">{t('这是 creght 替你收的公网地址：电脑关着也收下来、照样发通知，打开 Annulo 后交给处理函数。签名在这台电脑上验，密钥不上传。')}</p>
              )}
              {!st.server && b.sources.some((s) => s.path && !s.cloud_url) && !st.relay?.on && (
                <p className="text-[11px] text-muted-foreground">
                  {/* 没连 creght 不提云端代收（「没连 creght 就不出现」） */}
                  {t('本机版的地址只有这台电脑上的程序调得到；要让外面的网站、平台发进来，用服务端部署。')}
                </p>
              )}
            </div>
            <div className="border-t border-border px-4 py-2">
              <button
                type="button"
                onClick={() => setOpen((o) => ({ ...o, [b.id]: !o[b.id] }))}
                aria-expanded={!!open[b.id]}
                className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ChevronRight className={cn('size-3.5 transition-transform', open[b.id] && 'rotate-90')} />
                {t('最近收到的')}
              </button>
              {open[b.id] && <Events box={b} onChange={load} />}
            </div>
          </div>
        ))
      )}
      {err && <p className="text-sm text-destructive">{err}</p>}
      <p className="text-xs text-muted-foreground">{t('收到的先发通知（如果配了），再交给处理函数；失败的每分钟重试，试 3 次还不行就停下等你处理。处理完的留 {n} 天后删掉。', { n: 7 })}</p>
    </div>
  )
}
