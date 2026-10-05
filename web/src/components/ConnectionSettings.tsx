import { useEffect, useRef, useState } from 'react'
import { ExternalLink, Loader2, Plus } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/controls'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'
import CreghtConnection from '@/components/CreghtConnection'

/** accounts：连上的账号，最早连的在前（本机函数不指定账号时用它）。一种连接可以连多个账号 */
type Account = { account: string; connected_at: string; missing_scopes?: string[] }
type Conn = { key: string; name: string; desc: string; scopes: string[]; available: boolean; connected: boolean; accounts: Account[]; pending: boolean }

/** 连接：授权外部账号（Google…），本机函数用 ctx.oauth('google', { account }) 拿 token。一种连接可以连多个账号 */
export default function ConnectionSettings() {
  const [list, setList] = useState<Conn[] | null>(null)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [confirm, setConfirm] = useState('')
  const timer = useRef<number>(0)

  const load = () => getJSON<{ list: Conn[] }>('settings/connections').then((r) => setList(r.list))
  useEffect(() => {
    load()
    return () => clearInterval(timer.current)
  }, [])
  // 打开授权页以后轮询，用户在浏览器里同意完这里自动变成已连接
  useEffect(() => {
    clearInterval(timer.current)
    if (list?.some((c) => c.pending)) timer.current = window.setInterval(load, 2000)
  }, [list])

  const connect = async (key: string) => {
    setBusy(key)
    setErr('')
    try {
      const r = (await (await post(`settings/connections/${key}/connect`)).json()) as { auth_url: string }
      window.open(r.auth_url, '_blank', 'noopener')
      await load()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }
  const disconnect = async (key: string, account: string) => {
    const id = key + '/' + account
    if (confirm !== id) return setConfirm(id)
    setConfirm('')
    try {
      setList(((await (await post(`settings/connections/${key}?account=${encodeURIComponent(account)}`, undefined, 'DELETE')).json()) as { list: Conn[] }).list)
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  if (!list)
    return (
      <div className="flex justify-center py-8">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )

  return (
    <div className="space-y-4">
      <CreghtConnection />
      <div className="overflow-hidden rounded-xl border border-border bg-background">
        {list.map((c) => (
          <div key={c.key} className="space-y-2.5 border-b border-border px-4 py-3 last:border-b-0">
            <div className="flex flex-wrap items-center gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                  <span className="text-[13px] font-bold text-foreground">{c.name}</span>
                  {c.pending ? (
                    <StatusDot tone="warn">{t('等你在浏览器里同意')}</StatusDot>
                  ) : (
                    !c.connected && <StatusDot tone="idle">{t('没连接')}</StatusDot>
                  )}
                </div>
                <p className="mt-0.5 text-[11px] text-muted-foreground">{c.desc}</p>
                {!c.available && <p className="mt-0.5 text-[11px] text-warn">{t('这个版本的 Annulo 没有带 {name} 的授权客户端，暂时连不了。', { name: c.name })}</p>}
              </div>
              <Button size="sm" variant={c.connected ? 'outline' : 'default'} onClick={() => connect(c.key)} disabled={!c.available || busy === c.key} className="shrink-0">
                {busy === c.key ? <Loader2 className="animate-spin" /> : c.connected ? <Plus /> : <ExternalLink />}
                {c.connected ? t('再连一个账号') : t('连接 {name}', { name: c.name })}
              </Button>
            </div>
            {c.accounts.length > 0 && (
              <div className="divide-y divide-border rounded-lg border border-border">
                {c.accounts.map((a, i) => {
                  const id = c.key + '/' + a.account
                  return (
                    <div key={id} className="flex flex-wrap items-center gap-2 px-3 py-2">
                      <div className="min-w-0 flex-1 text-[12px]">
                        <StatusDot tone={a.missing_scopes?.length ? 'warn' : 'ok'}>
                          <span className="font-medium text-foreground">{a.account || t('已连接')}</span>
                          {c.accounts.length > 1 && i === 0 && <span className="ml-1.5 text-muted-foreground">{t('默认')}</span>}
                        </StatusDot>
                        {!!a.missing_scopes?.length && <p className="mt-0.5 text-[11px] text-warn">{t('加了新的权限（比如 GA4），重新连接一次才能用。')}</p>}
                      </div>
                      <div className="flex shrink-0 items-center gap-1">
                        {!!a.missing_scopes?.length && (
                          <Button size="sm" onClick={() => connect(c.key)} disabled={busy === c.key}>
                            {t('重新连接')}
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => disconnect(c.key, a.account)}
                          onMouseLeave={() => setConfirm('')}
                          className={cn(confirm === id ? 'text-destructive hover:text-destructive' : 'text-muted-foreground')}
                        >
                          {confirm === id ? t('确认断开') : t('断开')}
                        </Button>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
            {c.accounts.length > 1 && <p className="text-[11px] text-muted-foreground">{t('连了多个账号时，每个网站用哪个账号由后台记住（比如关联 Search Console 时）；没指定的用默认（最早连的）那个。')}</p>}
          </div>
        ))}
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
      <p className="text-xs text-muted-foreground">{t('授权只存在这台电脑的 ~/.annulo/connections 里，不会上传。断开只删本机的授权；要彻底撤销，到对方的账号安全页里移除 Annulo。')}</p>
    </div>
  )
}
