import { useEffect, useState } from 'react'
import { Loader2, RefreshCw, ShieldCheck } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input, StatusDot } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

type DomainState = {
  domain: string
  status: 'none' | 'issuing' | 'ok' | 'expiring' | 'error'
  email?: string
  not_after?: string
  days_left?: number
  issuer?: string
  last_error?: string
  error_at?: string
  https: boolean
}

/**
 * 设置 → 域名（服务端部署、管理员）：Annulo 自己签证书（internal/server/domain.go）。
 * 填域名 → 先自检这个域名能不能打回本机 → 当场向 Let's Encrypt 申请证书 → 之后自动续期。状态、到期、失败原因都在这里。
 */
export default function DomainSettings() {
  const [st, setSt] = useState<DomainState | null>(null)
  const [domain, setDomain] = useState('')
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [force, setForce] = useState(false)

  useEffect(() => {
    getJSON<DomainState>('settings/domain')
      .then((s) => {
        setSt(s)
        setDomain(s.domain)
        setEmail(s.email ?? '')
      })
      .catch((e: Error) => setErr(e.message))
  }, [])

  const act = async (key: string, fn: () => Promise<Response>) => {
    setBusy(key)
    setErr('')
    try {
      const s = (await (await fn()).json()) as DomainState
      setSt(s)
      // 第一次绑上、现在是用 IP 打开的：换到 https 的域名上（登录要重新来一次：cookie 跟着域名走）
      if (key === 'bind' && s.status === 'ok' && location.hostname !== s.domain) location.assign(`https://${s.domain}/_annulo/settings#domain`)
    } catch (e) {
      const msg = (e as Error).message.replace(/ · HTTP .*$/, '')
      setErr(msg)
      setForce(/80|解析|resolve|reach/.test(msg)) // 自检没过：给「跳过自检直接申请」
    } finally {
      setBusy('')
    }
  }

  if (!st) return err ? <p className="text-sm text-destructive">{err}</p> : <Loader2 className="size-4 animate-spin text-muted-foreground" />
  const tone = st.status === 'ok' ? 'ok' : st.status === 'expiring' ? 'warn' : st.status === 'error' ? 'bad' : 'idle'
  const label =
    st.status === 'ok'
      ? t('证书正常，{d} 天后到期，会自动续期', { d: st.days_left ?? 0 })
      : st.status === 'expiring'
        ? t('证书 {d} 天后到期，正在自动续期', { d: st.days_left ?? 0 })
        : st.status === 'error'
          ? t('证书签不下来')
          : st.status === 'issuing'
            ? t('正在申请证书…')
            : t('还没绑域名：现在用 IP 打开，登录密码走的是明文')
  return (
    <div className="space-y-4">
      {!st.https && <p className="rounded-lg bg-warn/10 px-3 py-2 text-xs text-warn">{t('这台服务器没开 HTTPS 端口（ANNULO_TLS_PORT），绑了域名也签不了证书。用官方镜像和 compose 部署就有。')}</p>}
      <div className="space-y-3 rounded-xl border border-border bg-card p-4">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ShieldCheck className="size-4 text-muted-foreground" />
          {st.domain || t('域名')}
        </div>
        <div className="text-xs">
          <StatusDot tone={tone}>{label}</StatusDot>
        </div>
        {st.not_after && (
          <div className="text-[11px] text-muted-foreground">
            {t('到期时间')} {new Date(st.not_after).toLocaleString()} · {st.issuer}
          </div>
        )}
        {st.last_error && <p className="rounded-lg bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">{st.last_error}</p>}
        <label className="block space-y-1.5">
          <span className="text-xs font-medium">{t('域名')}</span>
          <Input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="annulo.example.com" />
          <span className="block text-[11px] text-muted-foreground">{t('先在域名服务商那里加一条 A 记录，指向这台服务器的 IP。')}</span>
        </label>
        <label className="block space-y-1.5">
          <span className="text-xs font-medium">{t('邮箱（可以不填）')}</span>
          <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t('证书快到期、续不上时 Let\'s Encrypt 发邮件到这里')} />
        </label>
        <div className="flex flex-wrap items-center justify-end gap-2">
          {st.domain && (
            <>
              <Button size="sm" variant="ghost" className="text-muted-foreground" disabled={!!busy} onClick={() => act('unbind', () => post('settings/domain', undefined, 'DELETE'))}>
                {t('解绑')}
              </Button>
              <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => act('renew', () => post('settings/domain/renew'))}>
                {busy === 'renew' ? <Loader2 className="animate-spin" /> : <RefreshCw />}
                {t('立即续期')}
              </Button>
            </>
          )}
          {force && (
            <Button size="sm" variant="ghost" disabled={!!busy || !domain.trim()} onClick={() => act('bind', () => post('settings/domain', { domain, email, force: true }, 'PUT'))}>
              {t('跳过自检直接申请')}
            </Button>
          )}
          <Button size="sm" disabled={!!busy || !domain.trim()} onClick={() => act('bind', () => post('settings/domain', { domain, email }, 'PUT'))}>
            {busy === 'bind' && <Loader2 className="animate-spin" />}
            {st.domain && st.domain === domain.trim() ? t('重新申请证书') : t('绑定并申请证书')}
          </Button>
        </div>
      </div>
      {err && <p className="text-sm break-all text-destructive">{err}</p>}
    </div>
  )
}
