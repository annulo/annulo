import { useEffect, useState } from 'react'
import { Loader2, Plus, Server } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input, StatusDot } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

type Cluster = { host: string; name: string; logged_in: boolean; custom?: boolean }
type State = { current: string; clusters: Cluster[] }

/**
 * 自己部署的 creght（独立部署版）：地址 + 部署时设的管理员令牌，不走 creght 账号登录。
 * 连上以后它就是一个 creght 区域，项目、网站、MCP 和官方的一样用；它没有的功能（模板、平台模型、手机远程访问）不出现。
 */
export default function SelfhostCreght() {
  const [st, setSt] = useState<State | null>(null)
  const [open, setOpen] = useState(false)
  const [host, setHost] = useState('')
  const [name, setName] = useState('')
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const load = () => getJSON<State>('settings/creght').then(setSt).catch((e: Error) => setErr(e.message))
  useEffect(() => {
    load()
  }, [])

  const add = async () => {
    setBusy('add')
    setErr('')
    try {
      await post('settings/creght/custom', { host, name, token })
      location.reload() // 换到了这个 creght：项目、模型、MCP 都跟着变
    } catch (e) {
      setErr((e as Error).message.replace(/ · HTTP .*$/, ''))
      setBusy('')
    }
  }
  const remove = async (h: string) => {
    setBusy(h)
    setErr('')
    try {
      await post('settings/creght/custom?host=' + encodeURIComponent(h), undefined, 'DELETE')
      location.reload()
    } catch (e) {
      setErr((e as Error).message)
      setBusy('')
    }
  }

  const mine = st?.clusters.filter((c) => c.custom) ?? []
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-background">
      <div className="flex flex-wrap items-center gap-3 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[13px] font-bold">
            <Server className="size-4 text-muted-foreground" />
            {t('自己部署的 creght')}
          </div>
          <p className="mt-0.5 text-[11px] text-muted-foreground">{t('网站从 creght 导出、部署在自己服务器上的，用部署时设的管理员令牌连接（.env 里的 SELFHOST__ADMIN_TOKENS）。')}</p>
        </div>
        {!open && (
          <Button size="sm" variant="ghost" onClick={() => setOpen(true)}>
            <Plus />
            {t('添加')}
          </Button>
        )}
      </div>
      {mine.map((c) => (
        <div key={c.host} className="flex items-center gap-3 border-t border-border px-4 py-2.5 text-xs">
          <div className="min-w-0 flex-1">
            <div className="truncate font-semibold">{c.name}</div>
            <div className="truncate text-muted-foreground">{c.host}</div>
          </div>
          {c.logged_in ? <StatusDot tone="ok">{st?.current === c.host ? t('正在用') : t('已连接')}</StatusDot> : <StatusDot tone="bad">{t('令牌失效了：删掉重新添加')}</StatusDot>}
          <Button size="sm" variant="ghost" className="text-muted-foreground" disabled={!!busy} onClick={() => remove(c.host)}>
            {busy === c.host && <Loader2 className="animate-spin" />}
            {t('删除')}
          </Button>
        </div>
      ))}
      {open && (
        <div className="space-y-2.5 border-t border-border px-4 py-3">
          <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder="https://creght.example.com" />
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('名字（可以不填），比如：公司官网')} />
          <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={t('管理员令牌')} />
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
              {t('取消')}
            </Button>
            <Button size="sm" onClick={add} disabled={!host.trim() || !token.trim() || busy === 'add'}>
              {busy === 'add' && <Loader2 className="animate-spin" />}
              {t('连接并切换过去')}
            </Button>
          </div>
        </div>
      )}
      {err && <p className="px-4 pb-3 text-sm text-destructive">{err}</p>}
    </div>
  )
}
