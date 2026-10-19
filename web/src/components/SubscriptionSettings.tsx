import { useEffect, useState } from 'react'
import { Loader2, RefreshCw } from 'lucide-react'
import { getJSON } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { subscribeLink } from '@/components/QuotaNotice'
import { t } from '@/lib/i18n'
import { cn } from '@/lib/utils'

// Annulo 订阅（docs/billing.md）：就是 creght 账号的会员。这里只读平台的权益接口、原样显示，买、付钱都在 creght 网页上。

type Num = number | undefined
type Ent = {
  plan: { id: number; name: string; free: boolean }
  status: 'active' | 'grace' | 'expired' | 'closed' | 'free' | string
  expires_at?: string | null
  grace_until?: string | null
  quotas: {
    ai_credits?: { left: number }
    tunnel?: { machines: Num; machines_online: Num; devices: Num; used: Num; traffic_gb: Num; used_traffic_gb: Num }
    inbox_relay?: { events_per_month: Num; used: Num; storage_gb: Num; used_gb: Num }
    backup?: { gb: Num; used_gb: Num }
    assets?: { gb: Num; used_gb: Num; traffic_gb: Num; used_traffic_gb: Num }
  }
  templates?: 'all' | string[]
  subscribe_url?: string
}
type State = { available: boolean; entitlements?: Ent; error?: string }

export async function subscriptionAvailable() {
  try {
    return (await getJSON<State>('settings/subscription')).available
  } catch {
    return false
  }
}

const STATUS: Record<string, { label: string; tone: string }> = {
  active: { label: '生效中', tone: 'bg-ok/15 text-ok' },
  grace: { label: '已过期，宽限期内', tone: 'bg-warn/15 text-warn' },
  expired: { label: '已过期', tone: 'bg-destructive/15 text-destructive' },
  closed: { label: '已关闭', tone: 'bg-destructive/15 text-destructive' },
  free: { label: '免费档', tone: 'bg-muted text-muted-foreground' },
}

const fmt = (n: number) => (Number.isInteger(n) ? n.toLocaleString() : n.toFixed(n < 1 ? 2 : 1))

function Bar({ label, used, total, unit = '' }: { label: string; used?: number; total?: number; unit?: string }) {
  if (total === undefined) return null
  const unlimited = total < 0
  const pct = unlimited || !total ? 0 : Math.min(100, ((used ?? 0) / total) * 100)
  return (
    <div className="space-y-1">
      <div className="flex justify-between gap-3 text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums">
          {total === 0 ? t('不包含') : unlimited ? t('不限') : `${fmt(used ?? 0)}${unit} / ${fmt(total)}${unit}`}
        </span>
      </div>
      {total > 0 && (
        <div className="h-1.5 overflow-hidden rounded-full bg-muted">
          <div className={cn('h-full rounded-full', pct >= 90 ? 'bg-destructive' : pct >= 70 ? 'bg-warn' : 'bg-primary')} style={{ width: `${pct}%` }} />
        </div>
      )}
    </div>
  )
}

function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2.5 px-4 py-3">
      <div className="text-[13px] font-semibold">{title}</div>
      {children}
    </div>
  )
}

const date = (s?: string | null) => (s ? new Date(s).toLocaleDateString() : '')

/** 设置 → 订阅：档位、到期、每项额度的用量；续费、升级去 creght 网页 */
export default function SubscriptionSettings({ server }: { server?: boolean }) {
  const [st, setSt] = useState<State | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = async (refresh = false) => {
    setBusy(refresh)
    try {
      setSt(await getJSON<State>('settings/subscription' + (refresh ? '?refresh=1' : '')))
      setErr('')
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  useEffect(() => {
    load()
    // 去 creght 网页开通回来：窗口拿回焦点时重读
    const onFocus = () => load(true)
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [])

  if (!st) return <div className="flex justify-center py-8">{err ? <p className="text-sm text-destructive">{err}</p> : <Loader2 className="size-4 animate-spin text-muted-foreground" />}</div>
  if (!st.available) return <p className="text-sm text-muted-foreground">{t('这个账号还用不了 Annulo 的云服务。')}</p>
  const e = st.entitlements
  if (!e) return <p className="text-sm text-destructive">{st.error}</p>
  const q = e.quotas ?? {}
  const s = STATUS[e.status] ?? { label: e.status, tone: 'bg-muted text-muted-foreground' }
  const showBackup = !!q.backup?.gb
  const showAssets = !!q.assets?.gb

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-background px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-base font-bold">{e.plan.free || !e.plan.name ? t('免费档') : e.plan.name}</span>
            <span className={cn('rounded-md px-2 py-0.5 text-[11px] font-semibold', s.tone)}>{t(s.label)}</span>
          </div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            {e.status === 'grace' && e.grace_until
              ? t('{date} 前续费：这期间只有收件箱代收照常收，别的云服务已经停了。', { date: date(e.grace_until) })
              : e.expires_at
                ? t('{date} 到期', { date: date(e.expires_at) })
                : t('按账号订阅，几个项目、几台电脑共用这一份额度。')}
          </div>
        </div>
        <Button variant="ghost" size="sm" onClick={() => load(true)} disabled={busy}>
          {busy ? <Loader2 className="animate-spin" /> : <RefreshCw />}
          {t('刷新')}
        </Button>
        {e.subscribe_url && (
          <Button size="sm" asChild>
            <a href={subscribeLink(e.subscribe_url, undefined, server)} target="_blank" rel="noreferrer">
              {e.plan.free || e.status === 'free' ? t('升级') : t('续费 / 升级')}
            </a>
          </Button>
        )}
      </div>

      <div className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-background">
        <Group title={t('平台 AI')}>
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground">{t('剩余积分')}</span>
            <span className="tabular-nums">{q.ai_credits === undefined ? '—' : q.ai_credits.left < 0 ? t('不限') : fmt(q.ai_credits.left)}</span>
          </div>
        </Group>
        {q.tunnel && (
          <Group title={t('远程访问')}>
            <Bar label={t('同时在线的电脑')} used={q.tunnel.machines_online} total={q.tunnel.machines} />
            <Bar label={t('能登录的设备')} used={q.tunnel.used} total={q.tunnel.devices} />
            <Bar label={t('本月流量')} used={q.tunnel.used_traffic_gb} total={q.tunnel.traffic_gb} unit=" GB" />
          </Group>
        )}
        {q.inbox_relay && (
          <Group title={t('收件箱代收')}>
            <Bar label={t('本月事件')} used={q.inbox_relay.used} total={q.inbox_relay.events_per_month} />
            <Bar label={t('存储')} used={q.inbox_relay.used_gb} total={q.inbox_relay.storage_gb} unit=" GB" />
          </Group>
        )}
        {showBackup && (
          <Group title={t('备份')}>
            <Bar label={t('存储')} used={q.backup?.used_gb} total={q.backup?.gb} unit=" GB" />
          </Group>
        )}
        {showAssets && (
          <Group title={t('公开素材')}>
            <Bar label={t('存储')} used={q.assets?.used_gb} total={q.assets?.gb} unit=" GB" />
            <Bar label={t('本月流量')} used={q.assets?.used_traffic_gb} total={q.assets?.traffic_gb} unit=" GB" />
          </Group>
        )}
        <Group title={t('付费模板')}>
          <div className="text-xs text-muted-foreground">{e.templates === 'all' ? t('订阅期内能升级到新版本；过期后已经升上去的版本照常用。') : t('不包含：付费模板能用当前版本，拿不到新版本。')}</div>
        </Group>
      </div>
      <p className="text-xs text-muted-foreground">{t('续费、升级、发票都在 creght 网页上，会用浏览器打开；开通好回到这里会自动刷新。')}</p>
    </div>
  )
}
