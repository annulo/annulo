import { useEffect, useState } from 'react'
import { Check, Copy, Loader2 } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

type ClientState = { client_id: string; custom: boolean; redirect_uri: string }

/**
 * 服务端部署时在页面上配一种连接的 OAuth 客户端（现在是 Google）：部署者在 Google Cloud 建一个「Web 应用」客户端，
 * 回调填这里显示的地址，把 ID 和密钥填进来就能连（AGENTS.md「服务端部署：配置都在页面上」）。
 */
export default function OAuthClientForm({ provider, name, available, onSaved }: { provider: string; name: string; available: boolean; onSaved: () => void }) {
  const [st, setSt] = useState<ClientState | null>(null)
  const [open, setOpen] = useState(!available)
  const [id, setId] = useState('')
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    getJSON<ClientState>(`settings/connections/${provider}/client`)
      .then((s) => {
        setSt(s)
        if (s.custom) setId(s.client_id)
      })
      .catch((e: Error) => setErr(e.message))
  }, [provider])

  const save = async (clear = false) => {
    setBusy(true)
    setErr('')
    try {
      const r = await post(`settings/connections/${provider}/client`, clear ? { client_id: '' } : { client_id: id, client_secret: secret }, 'PUT')
      setSt((await r.json()) as ClientState)
      setSecret('')
      setOpen(false)
      onSaved()
    } catch (e) {
      setErr((e as Error).message.replace(/ · HTTP .*$/, ''))
    } finally {
      setBusy(false)
    }
  }

  if (!st) return err ? <p className="text-[11px] text-destructive">{err}</p> : null
  if (!open)
    return (
      <button className="text-[11px] text-muted-foreground underline-offset-2 hover:underline" onClick={() => setOpen(true)}>
        {st.custom ? t('{name} 客户端：{id}（点击更换）', { name, id: st.client_id }) : t('换成自己的 {name} OAuth 客户端', { name })}
      </button>
    )
  return (
    <div className="space-y-2 rounded-lg bg-muted/60 px-3 py-2.5">
      <p className="text-[11px] leading-relaxed text-muted-foreground">
        {t('在 Google Cloud 控制台建一个「Web 应用」类型的 OAuth 客户端，「已获授权的重定向 URI」填下面这个地址，再把客户端 ID 和密钥填进来。')}
      </p>
      {st.redirect_uri ? (
        <div className="flex items-center gap-2 rounded-md bg-background px-2.5 py-1.5">
          <code className="min-w-0 flex-1 truncate text-[11px]">{st.redirect_uri}</code>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              void navigator.clipboard?.writeText(st.redirect_uri)
              setCopied(true)
            }}
          >
            {copied ? <Check /> : <Copy />}
          </Button>
        </div>
      ) : (
        <p className="text-[11px] text-warn">{t('现在是用 IP 打开的：先在 设置 → 域名 绑一个域名，回调地址要用 HTTPS。')}</p>
      )}
      <Input value={id} onChange={(e) => setId(e.target.value)} placeholder={t('客户端 ID')} className="h-8 text-xs" />
      <Input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={st.custom ? t('客户端密钥（不改就留空）') : t('客户端密钥')} className="h-8 text-xs" />
      {err && <p className="text-[11px] text-destructive">{err}</p>}
      <div className="flex justify-end gap-2">
        {st.custom && (
          <Button size="sm" variant="ghost" className="text-muted-foreground" disabled={busy} onClick={() => save(true)}>
            {t('删掉')}
          </Button>
        )}
        {available && (
          <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
            {t('取消')}
          </Button>
        )}
        <Button size="sm" disabled={busy || !id.trim() || (!st.custom && !secret.trim())} onClick={() => save()}>
          {busy && <Loader2 className="animate-spin" />}
          {t('保存')}
        </Button>
      </div>
    </div>
  )
}
