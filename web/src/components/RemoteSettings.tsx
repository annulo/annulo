import { useEffect, useMemo, useState } from 'react'
import { Check, Copy, ExternalLink, QrCode } from 'lucide-react'
import { encode } from 'uqr'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import type { SetupState } from '@/components/BackendPicker'
import { Switch } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

/** projects：能远程调到的每个项目在别处打开的地址（项目站点的预览域名），当前项目排第一；离线项目不在里面 */
type Address = { project_id: string; url: string; current: boolean }
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
function RemoteAddresses({ list }: { list: Address[] }) {
  const [names, setNames] = useState<Record<string, string>>({})
  useEffect(() => {
    getJSON<SetupState>('setup')
      .then((st) => setNames(Object.fromEntries(st.backends.map((b) => [b.project_id, b.name]))))
      .catch(() => {})
  }, [])
  return (
    <section className="space-y-3 rounded-xl border border-border bg-background p-4">
      <div>
        <h3 className="text-sm font-medium">{t('在手机或别的电脑上打开')}</h3>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {t('每个项目有自己的地址。手机扫码，或在浏览器里打开，用这台电脑登录的 creght 账号登录。看数据、改内容不用打开下面的开关。')}
        </p>
      </div>
      <div className="divide-y divide-border">
        {list.map((a) => (
          <AddressRow key={a.project_id} a={a} name={names[a.project_id] || a.project_id} />
        ))}
      </div>
    </section>
  )
}

/**
 * 远程访问：允许远程调用这台电脑（internal/relay）。默认关。
 * 打开后 Annulo 连平台，在手机、别的电脑上打开运营后台点发布、采集，转到这台电脑跑；关掉立刻断开。
 */
export default function RemoteSettings() {
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
      {!!st.projects?.length && <RemoteAddresses list={st.projects} />}
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
