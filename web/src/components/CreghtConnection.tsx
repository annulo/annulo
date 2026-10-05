import { useEffect, useState } from 'react'
import { ExternalLink, Loader2 } from 'lucide-react'
import { getJSON, post, type Status } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/controls'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'
import { useCreghtConnect } from '@/lib/creghtConnect'

/**
 * 连接里的 creght 账号：平台的 OAuth 授权（浏览器里同意，跳回本机）。token 只存在这台电脑上，助手在命令行里用 creght 时也用它。
 * 离线使用时在这里连上，就能用平台的模型、creght MCP、绑定 creght 网站，也能把离线项目转成在线的；断开就回到离线使用。
 */
export default function CreghtConnection() {
  const [st, setSt] = useState<Status | null>(null)
  const { connect, waiting, url, err, setErr } = useCreghtConnect()
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)

  const load = () => getJSON<Status>('status').then(setSt)
  useEffect(() => {
    load()
  }, [])

  const disconnect = async () => {
    if (!confirm) return setConfirm(true)
    setConfirm(false)
    setBusy(true)
    setErr('')
    try {
      // 断开就是回到离线使用：不弹登录页
      await post('settings/offline', { enabled: true }, 'PUT')
      await post('login/logout')
      location.reload()
    } catch (e) {
      setErr((e as Error).message)
      setBusy(false)
    }
  }

  if (!st) return null
  const cr = st.creght
  const host = cr.api_host.replace(/^https?:\/\//, '')
  const onlineProject = !!st.backend && !st.backend.offline
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-background">
      <div className="flex flex-wrap items-center gap-3 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
            <span className="text-[13px] font-bold text-foreground">creght</span>
            {cr.logged_in ? (
              <StatusDot tone="ok">
                {t('已连接')} {cr.user?.email || cr.user?.username || ''}
              </StatusDot>
            ) : waiting ? (
              <StatusDot tone="warn">{t('等你在浏览器里同意')}</StatusDot>
            ) : (
              <StatusDot tone="idle">{t('没连接')}</StatusDot>
            )}
            <span>{host}</span>
          </div>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {t('连上以后能用 creght 平台的模型、creght MCP、绑定 creght 网站当渠道，也能把离线项目转成在线的。授权只存在这台电脑上。')}
          </p>
          {cr.logged_in && confirm && onlineProject && (
            <p className="mt-0.5 text-[11px] text-warn">{t('现在打开的是在线项目：断开后它的数据读不了，要换到离线项目或重新连接。')}</p>
          )}
          {waiting && url && (
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              {t('浏览器没有自动打开？')}{' '}
              <a href={url} target="_blank" rel="noreferrer" className="font-medium text-primary-text hover:underline">
                {t('打开授权页')}
              </a>
            </p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {cr.logged_in ? (
            <>
              <Button variant="ghost" size="sm" onClick={connect} disabled={waiting || busy}>
                {waiting && <Loader2 className="animate-spin" />}
                {t('重新连接')}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={disconnect}
                onMouseLeave={() => setConfirm(false)}
                disabled={busy}
                className={cn(confirm ? 'text-destructive hover:text-destructive' : 'text-muted-foreground')}
              >
                {busy && <Loader2 className="animate-spin" />}
                {confirm ? t('确认断开') : t('断开')}
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={connect} disabled={waiting}>
              {waiting ? <Loader2 className="animate-spin" /> : <ExternalLink />}
              {t('连接 {name}', { name: 'creght' })}
            </Button>
          )}
        </div>
      </div>
      {err && <p className="px-4 pb-3 text-sm text-destructive">{err}</p>}
    </div>
  )
}
