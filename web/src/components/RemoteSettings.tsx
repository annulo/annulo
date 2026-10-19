import { useEffect, useMemo, useState } from 'react'
import { Check, Copy, ExternalLink, QrCode, Smartphone } from 'lucide-react'
import { encode } from 'uqr'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/controls'
import { t } from '@/lib/i18n'
import { approveDevice, removeDevice, type RemoteDevice } from '@/components/RemoteIndicator'
import QuotaNotice from '@/components/QuotaNotice'

/** projects：能远程调到的每个项目在别处打开的地址（项目站点的预览域名），当前项目排第一；离线项目不在里面 */
type Address = { project_id: string; name: string; url: string; current: boolean }
type Remote = { enabled: boolean; online: boolean; error: string; machine: string; offline_project?: boolean; projects?: Address[] }

/** 二维码：手机扫了直接打开。总是白底黑块，深色主题下也要扫得出来 */
function QRCode({ text }: { text: string }) {
  const { size, d } = useMemo(() => {
    const qr = encode(text, { ecc: 'M', border: 2 })
    let d = ''
    qr.data.forEach((row, y) => row.forEach((on, x) => on && (d += `M${x} ${y}h1v1h-1z`)))
    return { size: qr.size, d }
  }, [text])
  return (
    <svg viewBox={`0 0 ${size} ${size}`} shapeRendering="crispEdges" className="size-28 shrink-0 rounded-lg bg-white" role="img" aria-label={text}>
      <path d={d} fill="#000" />
    </svg>
  )
}

/** 一个项目的地址：复制、在浏览器打开、展开二维码给手机扫 */
function AddressRow({ a, name }: { a: Address; name: string }) {
  const [qr, setQr] = useState(a.current)
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    await navigator.clipboard.writeText(a.url)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="space-y-3 py-3 first:pt-0 last:pb-0">
      <div className="flex items-center gap-2.5">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-bold text-muted-foreground">{name.slice(0, 1).toUpperCase()}</span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 text-[13px] font-semibold">
            <span className="truncate">{name}</span>
            {a.current && <span className="shrink-0 rounded bg-primary/10 px-1.5 py-px text-[10px] font-medium text-primary-text">{t('当前')}</span>}
          </div>
          <div className="truncate font-mono text-[11px] text-muted-foreground select-all" title={a.url}>
            {a.url}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button size="icon-sm" variant="ghost" onClick={() => setQr((v) => !v)} title={t('二维码')} aria-pressed={qr}>
            <QrCode />
          </Button>
          <Button size="icon-sm" variant="ghost" onClick={copy} title={copied ? t('已复制') : t('复制地址')}>
            {copied ? <Check /> : <Copy />}
          </Button>
          <Button size="icon-sm" variant="ghost" asChild title={t('在浏览器打开')}>
            <a href={a.url} target="_blank" rel="noreferrer">
              <ExternalLink />
            </a>
          </Button>
        </div>
      </div>
      {qr && <QRCode text={a.url} />}
    </div>
  )
}

/** 每个项目在别处打开的地址。开关是这台电脑全局的，地址是每个项目各一个，所以全列出来 */
function RemoteAddresses({ list, title, desc }: { list: Address[]; title: string; desc: string }) {
  return (
    <section className="space-y-3 rounded-xl border border-border bg-background p-4">
      <div>
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{desc}</p>
      </div>
      <div className="divide-y divide-border">
        {list.map((a) => (
          <AddressRow key={a.project_id} a={a} name={a.name || a.project_id} />
        ))}
      </div>
    </section>
  )
}

/** 平台上的订阅（GET /api/u/remote/subscription）：code 是不能用的原因 */
type Subscription = {
  active: boolean
  code?: string
  expires_at?: string
  limits: { machines: number; devices: number; monthly_traffic_bytes: number }
  usage: { traffic_bytes: number; devices: number }
  subscribe_url?: string
}
type Tunnel = {
  enabled: boolean
  online: boolean
  available: boolean
  logged_in: boolean
  error?: string
  code?: string
  subscribe_url?: string
  subscription?: Subscription
  projects?: Address[]
}

const gb = (n: number) => (n / 1024 ** 3).toFixed(n < 10 * 1024 ** 3 ? 1 : 0) + ' GB'

/** 订阅：能用时写到期和本月流量；不能用（没开通、过期、流量用完）给开通按钮 */
function SubscriptionLine({ st }: { st: Tunnel }) {
  const sub = st.subscription
  const url = sub?.subscribe_url || st.subscribe_url
  const blocked = (sub && !sub.active) || (!!st.code && st.code.startsWith('remote_') && st.code !== 'remote_replaced')
  if (blocked)
    return (
      <QuotaNotice
        notice={{
          quota: 'tunnel',
          subscribe_url: url,
          message:
            sub?.code === 'remote_traffic_exceeded' || st.code === 'remote_traffic_exceeded'
              ? t('这个月的流量用完了，下个月自动恢复；要马上用就加流量。')
              : sub?.code === 'remote_subscription_expired' || st.code === 'remote_subscription_expired'
                ? t('远程访问的订阅过期了，续费后就能用。')
                : st.code === 'remote_machine_limit'
                  ? t('能同时远程访问的电脑数满了：关掉别的电脑上的远程访问，或者加电脑数。')
                  : t('远程访问要在 creght 上开通（按账号订阅，不按项目）。'),
        }}
      />
    )
  if (!sub?.active) return null
  return (
    <p className="text-xs text-muted-foreground">
      {sub.expires_at && t('{date} 到期', { date: new Date(sub.expires_at).toLocaleDateString() })}
      {sub.expires_at && ' · '}
      {t('本月流量 {used} / {total}', { used: gb(sub.usage.traffic_bytes), total: gb(sub.limits.monthly_traffic_bytes) })}
      {' · '}
      {t('设备 {used} / {total}', { used: sub.usage.devices, total: sub.limits.devices })}
      {url && (
        <a href={url} target="_blank" rel="noreferrer" className="ml-2 text-primary-text hover:underline">
          {t('管理')}
        </a>
      )}
    </p>
  )
}

function ago(sec: number) {
  const d = Math.max(0, Date.now() / 1000 - sec)
  if (d < 90) return t('刚刚')
  if (d < 3600) return t('{n} 分钟前', { n: Math.round(d / 60) })
  if (d < 86400) return t('{n} 小时前', { n: Math.round(d / 3600) })
  return t('{n} 天前', { n: Math.round(d / 86400) })
}

/**
 * 在手机上打开这台电脑的 Annulo（internal/tunnel，docs/mobile-remote.md「远程访问：隧道」）。默认关。
 * 打开后 Annulo 连 creght 建隧道；新设备第一次打开要在这里（或顶栏）点允许，确认过的设备能撤销。
 */
function TunnelSettings({ onProjects }: { onProjects: (list: Address[]) => void }) {
  const [st, setSt] = useState<Tunnel | null>(null)
  const [devices, setDevices] = useState<RemoteDevice[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const load = () =>
    Promise.all([getJSON<Tunnel>('settings/remote/tunnel'), getJSON<{ list: RemoteDevice[] }>('remote/devices')])
      .then(([a, b]) => {
        setSt(a)
        onProjects(a.available && a.enabled ? (a.projects ?? []) : [])
        setDevices(b.list ?? [])
      })
      .catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
    const timer = setInterval(load, 3000)
    return () => clearInterval(timer)
  }, [])
  const change = async (enabled: boolean) => {
    setBusy(true)
    setErr('')
    try {
      setSt((await (await post('settings/remote/tunnel', { enabled }, 'PUT')).json()) as Tunnel)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const act = async (fn: () => Promise<void>) => {
    setErr('')
    try {
      await fn()
      await load()
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  if (!st) return err ? <p className="text-sm text-destructive">{err}</p> : null
  if (!st.available) return null // 这个 creght 没有远程访问（自己部署的、老版本）
  const status = !st.enabled
    ? t('已关闭')
    : st.online
      ? t('已连上：手机上能打开这台电脑的 Annulo')
      : st.error
        ? t('连不上：{err}', { err: st.error })
        : t('正在连接…')
  return (
    <section className="space-y-3 rounded-xl border border-border bg-background p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">{t('在手机上打开这台电脑的 Annulo')}</h3>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            {t('打开后，手机上用 creght 账号登录，就能用这台电脑上的 Annulo：后台、助手、项目文件夹都和电脑上一样。这台电脑要开着、Annulo 要开着。每台新设备第一次打开，要在这台电脑上点允许。')}
          </p>
        </div>
        <Switch checked={st.enabled} onChange={change} disabled={busy} label={t('在手机上打开这台电脑的 Annulo')} />
      </div>
      <p className={st.enabled && !st.online && st.error ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
        <span className={`mr-1.5 inline-block size-2 rounded-full ${st.enabled && st.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'}`} />
        {status}
      </p>
      <SubscriptionLine st={st} />
      {devices.length > 0 && (
        <div className="divide-y divide-border rounded-lg border border-border">
          {devices.map((d) => (
            <div key={d.id} className="flex items-center gap-2.5 px-3 py-2.5">
              <Smartphone className="size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5 text-[13px] font-medium">
                  <span className="truncate">{d.name}</span>
                  {d.status === 'pending' && <span className="shrink-0 rounded bg-primary/10 px-1.5 py-px text-[10px] font-medium text-primary-text">{t('等你确认')}</span>}
                  {d.active && <span className="shrink-0 rounded bg-emerald-500/10 px-1.5 py-px text-[10px] font-medium text-emerald-700 dark:text-emerald-400">{t('正在使用')}</span>}
                </div>
                <div className="text-[11px] text-muted-foreground">{t('最近一次：{when}', { when: ago(d.last_seen) })}</div>
              </div>
              {d.status === 'pending' ? (
                <div className="flex shrink-0 gap-1.5">
                  <Button size="sm" onClick={() => void act(() => approveDevice(d.id))}>{t('允许')}</Button>
                  <Button size="sm" variant="outline" onClick={() => void act(() => removeDevice(d.id))}>{t('拒绝')}</Button>
                </div>
              ) : (
                <Button size="sm" variant="ghost" className="shrink-0 text-destructive hover:text-destructive" onClick={() => void act(() => removeDevice(d.id))}>{t('撤销')}</Button>
              )}
            </div>
          ))}
        </div>
      )}
      {err && <p className="text-sm text-destructive">{err}</p>}
    </section>
  )
}

/** 隧道：开关、订阅、设备，下面是每个项目在手机上打开的地址（连上过隧道才有） */
function TunnelBlock() {
  const [projects, setProjects] = useState<Address[]>([])
  return (
    <>
      <TunnelSettings onProjects={setProjects} />
      {projects.length > 0 && (
        <RemoteAddresses
          list={projects}
          title={t('在浏览器中打开')}
          desc={t('每个项目有自己的地址，书签一直能用。在手机或别的电脑的浏览器里打开（手机可以扫码），用 creght 账号登录；第一次打开要在这台电脑上点允许。')}
        />
      )}
    </>
  )
}

/** 设置 → 远程访问：只有隧道（手机上打开这台电脑的 Annulo） */
export default function RemoteSettings() {
  return <TunnelBlock />
}

/**
 * 旧的远程访问（在线项目经中转调这台电脑，internal/relay）：2026-10-16 起界面上不显示，换成上面的隧道；服务端实现留着，以后可能再用。
 * 打开后 Annulo 连平台，在手机、别的电脑上打开运营后台点发布、采集，转到这台电脑跑；关掉立刻断开。
 */
export function LegacyRelaySettings() {
  const [st, setSt] = useState<Remote | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = () =>
    getJSON<Remote>('settings/remote')
      .then(setSt)
      .catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
    const timer = setInterval(load, 5000) // 连接状态会变（断网、重连）
    return () => clearInterval(timer)
  }, [])

  const change = async (enabled: boolean) => {
    setBusy(true)
    setErr('')
    try {
      setSt((await (await post('settings/remote', { enabled }, 'PUT')).json()) as Remote)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (!st) return err ? <p className="text-sm text-destructive">{err}</p> : null
  const status = !st.enabled ? t('已关闭：远程调不到这台电脑') : st.online ? t('已连上：可以远程调用「{name}」', { name: st.machine }) : st.error ? t('连不上平台：{err}', { err: st.error }) : t('正在连接…')
  return (
    <>
      {!!st.projects?.length && (
        <RemoteAddresses
          list={st.projects}
          title={t('在线项目：在手机或别的电脑上打开')}
          desc={t('每个项目有自己的地址。手机扫码，或在浏览器里打开，用这台电脑登录的 creght 账号登录。看数据、改内容不用打开下面的开关。')}
        />
      )}
      <section className="space-y-3 rounded-xl border border-border bg-background p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h3 className="text-sm font-medium">{t('允许远程调用这台电脑')}</h3>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              {t('打开后，在手机或别的电脑上打开运营后台，点「发布到网站」「立即采集」会转到这台电脑上执行，用的是这台电脑的浏览器登录态和密钥。只有项目所有者能调，只执行项目里声明过的函数。Annulo 要开着。')}
            </p>
          </div>
          <Switch checked={st.enabled} onChange={change} disabled={busy} label={t('允许远程调用这台电脑')} />
        </div>
        <p className={st.enabled && !st.online && st.error ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
          <span className={`mr-1.5 inline-block size-2 rounded-full ${st.enabled && st.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'}`} />
          {status}
        </p>
        <p className="text-xs leading-relaxed text-muted-foreground">{t('手机上还能用助手：看对话、发消息、跑任务，交给电脑上正在打开的项目里的助手做。')}</p>
        {st.offline_project && <p className="text-xs leading-relaxed text-amber-600 dark:text-amber-400">{t('离线项目不支持远程访问：远程访问要项目在 creght 上。在 设置 → 项目 里转成在线项目后就能用。')}</p>}
        {err && <p className="text-sm text-destructive">{err}</p>}
      </section>
    </>
  )
}
