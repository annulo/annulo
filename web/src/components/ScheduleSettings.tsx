import { useEffect, useState } from 'react'
import { ChevronRight, Loader2, MessageSquare, Play } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { StatusDot, Switch } from '@/components/ui/controls'
import { fmtAgo, fmtDuration } from '@/lib/format'
import { t } from '@/lib/i18n'
import { cn } from '@/lib/utils'

type Run = { started_at: string; ms: number; ok: boolean; error?: string; result?: string; chat_id?: string }
type Job = { id: string; chat_id?: string; fn?: string; prompt?: string; task?: string; name: string; every?: string; at?: string; disabled: boolean; running: boolean; next_at?: string; last?: Run; last_ok?: string; fails: number; only_when_open?: boolean }
type Group = { project_id: string; name: string; offline?: boolean; current?: boolean; list: Job[]; error?: string }
type State = { list: Job[]; error: string; file: string; projects?: Group[] }

const EVERY: Record<string, string> = { m: '分钟', h: '小时', d: '天' }
function freq(j: Job) {
  if (j.at) return t('每天 {at}', { at: j.at })
  const m = j.every?.match(/^(\d+)([mhd])$/)
  return m ? t(`每 {n} ${EVERY[m[2]]}`, { n: m[1] }) : t('每 {v}', { v: j.every ?? '' })
}
function when(iso: string) {
  const dt = new Date(iso)
  const d = dt.getTime() - Date.now()
  if (d <= 60_000) return t('马上')
  const hm = dt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return dt.toDateString() === new Date().toDateString() ? t('今天 {hm}', { hm }) : `${dt.getMonth() + 1}/${dt.getDate()} ${hm}`
}

// 在跑的看这一次的对话（chat_id），没在跑的看上一次的；关掉设置回到后台，在右侧打开
function openChat(id: string) {
  window.postMessage({ type: 'shuttle:navigate', view: 'backend' }, location.origin)
  window.dispatchEvent(new CustomEvent('shuttle:open-chat', { detail: id }))
}

/** 定时任务：运营后台 schedules/ 里声明的（一个任务一个文件），Annulo 开着时按时跑本机函数，或者把一段话交给助手 */
export default function ScheduleSettings() {
  const [st, setSt] = useState<State | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({}) // 别的项目默认收起

  const load = () => getJSON<State>('local/schedules').then(setSt).catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
    const t = window.setInterval(load, 5000)
    return () => clearInterval(t)
  }, [])

  const act = async (project: string, id: string, action: string, body?: unknown) => {
    setErr('')
    try {
      setSt(await (await post(`local/schedules/${encodeURIComponent(id)}/${action}?project=${encodeURIComponent(project)}`, body)).json())
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  if (!st)
    return (
      <div className="flex justify-center py-8">
        {err ? <p className="text-sm text-destructive">{err}</p> : <Loader2 className="size-4 animate-spin text-muted-foreground" />}
      </div>
    )

  // 每个项目一组，当前项目在最前（老版本的接口没有 projects，就只有当前项目一组）
  const groups: Group[] = st.projects ?? [{ project_id: '', name: t('当前项目'), current: true, list: st.list, error: st.error }]
  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <div key={g.project_id} className="space-y-2">
          <button
            type="button"
            disabled={g.current}
            onClick={() => setOpen((o) => ({ ...o, [g.project_id]: !o[g.project_id] }))}
            aria-expanded={g.current || !!open[g.project_id]}
            className="flex w-full flex-wrap items-center gap-2 text-left text-xs text-muted-foreground enabled:cursor-pointer"
          >
            {!g.current && <ChevronRight className={cn('size-3.5 transition-transform', open[g.project_id] && 'rotate-90')} />}
            <span className="text-[13px] font-semibold text-foreground">{g.name}</span>
            {g.offline && <span className="rounded bg-muted px-1.5 py-px text-[11px] font-semibold">{t('离线')}</span>}
            {g.current && <span className="rounded-md border border-primary/10 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary-text">{t('当前项目')}</span>}
            {!g.current && <span>{t('{n} 个任务', { n: g.list.length })}</span>}
            {!g.current && g.list.some((j) => j.last && !j.last.ok) && (
              <StatusDot tone="bad">{t('{n} 个上次失败', { n: g.list.filter((j) => j.last && !j.last.ok).length })}</StatusDot>
            )}
          </button>
          {(g.current || open[g.project_id]) && (<>
      {g.error && <div className="rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">{g.error}</div>}
      {g.list.length === 0 && !g.error ? (
        <div className="rounded-xl border border-border bg-background px-5 py-6 text-sm text-muted-foreground">
          {t('运营后台还没有定时任务。要让某个本机函数定时跑、或者定时让助手做一件事（比如每周写周报），在项目的')} <code className="font-mono">{st.file}</code> {t('里加一个文件（一个任务一个），或者直接告诉助手「每天早上同步一次数据」。')}
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border border-border bg-background">
          {g.list.map((j) => (
            <div key={j.id} className="border-b border-border px-4 py-3 last:border-b-0">
              <div className="flex flex-wrap items-center gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-[13px] font-bold">{j.name || j.fn || j.id}</span>
                    <span className="text-[11px] text-muted-foreground">{freq(j)}</span>
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                    {j.task ? (
                      <span className="font-mono">{t('任务：')}tasks/{j.task}.md</span>
                    ) : j.prompt ? (
                      <span className="max-w-80 truncate" title={j.prompt}>
                        {t('交给助手：')}{j.prompt}
                      </span>
                    ) : (
                      <span className="font-mono">{j.fn}</span>
                    )}
                    {j.running ? (
                      <StatusDot tone="warn">{t('正在跑')}</StatusDot>
                    ) : j.last ? (
                      <StatusDot tone={j.last.ok ? 'ok' : 'bad'}>
                        {fmtAgo(new Date(j.last.started_at).getTime())}
                        {j.last.ok ? t('成功') : j.fails > 1 ? t('失败（连续 {n} 次）', { n: j.fails }) : t('失败')} · {fmtDuration(j.last.ms)}
                      </StatusDot>
                    ) : (
                      <span>{t('还没跑过')}</span>
                    )}
                    {!j.disabled && j.next_at && !j.running && <span>{t('下次 {when}', { when: when(j.next_at) })}</span>}
                    {j.only_when_open && <span>{t('交给助手的，打开这个项目时才跑')}</span>}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {(j.chat_id || j.last?.chat_id) && (
                    <Button variant="ghost" size="sm" onClick={() => openChat((j.chat_id || j.last?.chat_id)!)}>
                      <MessageSquare />
                      {t('查看对话')}
                    </Button>
                  )}
                  <Button variant="ghost" size="sm" onClick={() => act(g.project_id, j.id, 'run')} disabled={j.running || j.only_when_open}>
                    {j.running ? <Loader2 className="animate-spin" /> : <Play />}
                    {t('立即运行')}
                  </Button>
                  <Switch checked={!j.disabled} onChange={(v) => act(g.project_id, j.id, 'toggle', { disabled: !v })} label={j.disabled ? t('已暂停') : t('开启')} />
                </div>
              </div>
              {j.last && !j.last.ok && j.last.error && (
                <div className="mt-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">{j.last.error}</div>
              )}
            </div>
          ))}
        </div>
      )}
          </>)}
        </div>
      ))}
      {err && <p className="text-sm text-destructive">{err}</p>}
      <p className="text-xs text-muted-foreground">{t('只在 Annulo 开着的时候跑；电脑关机或退出 Annulo 期间错过的，下次打开时补跑一次。本机所有项目的本机函数都会按时跑（在各自的项目里）；交给助手的任务只在打开那个项目时跑，会开一段新对话，跑完在对话列表里标成有新回复。')}</p>
    </div>
  )
}
