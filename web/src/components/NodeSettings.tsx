import { useEffect, useState } from 'react'
import { Check, Copy, Loader, Plus, Trash2 } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input, StatusDot, Switch } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

/**
 * 设置 → 节点（docs/server-and-multi-project.md「浏览器：节点」）。
 * 服务器上（server）：管理员生成节点令牌、看哪些电脑连着；服务器上的本机函数用连着的电脑的浏览器。
 * 本机版：填服务器地址和令牌，把这台电脑的浏览器借给服务器用。
 */
export default function NodeSettings({ server }: { server: boolean }) {
  return server ? <ServerNodes /> : <ConnectToServer />
}

type ServerNode = { id: string; name: string; created_at: string; online: boolean; default: boolean; machine_name?: string; capabilities?: string[] }

function ServerNodes() {
  const [nodes, setNodes] = useState<ServerNode[] | null>(null)
  const [serverURL, setServerURL] = useState('')
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState<{ token: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const load = () =>
    getJSON<{ nodes: ServerNode[]; server_url: string }>('nodes')
      .then((r) => {
        setNodes(r.nodes)
        setServerURL(r.server_url)
      })
      .catch((e: Error) => setErr(e.message))
  useEffect(() => {
    load()
    const timer = setInterval(load, 5000) // 电脑那边连上了马上能看到
    return () => clearInterval(timer)
  }, [])

  const act = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key)
    setErr('')
    try {
      await fn()
      await load()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }
  const create = () =>
    act('create', async () => {
      const r = await post('nodes/tokens', { name })
      setFresh({ token: ((await r.json()) as { token: string }).token })
      setName('')
      setCopied(false)
    })

  if (!nodes)
    return (
      <div className="flex justify-center py-6">
        <Loader className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  return (
    <div className="space-y-5">
      <div className="space-y-3 rounded-xl border border-border bg-card p-4">
        <div className="text-sm font-semibold">{t('添加一台电脑')}</div>
        <p className="text-xs leading-relaxed text-muted-foreground">
          {t('给那台电脑起个名字生成令牌，在它的 Annulo 里打开 设置 → 节点，填上服务器地址和令牌。')}
        </p>
        <div className="flex items-center gap-2">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('比如：办公室的 Mac mini')} className="max-w-64" />
          <Button onClick={create} disabled={!name.trim() || busy === 'create'}>
            {busy === 'create' ? <Loader className="animate-spin" /> : <Plus />}
            {t('生成令牌')}
          </Button>
        </div>
        {fresh && (
          <div className="space-y-2 rounded-lg bg-muted px-3 py-2.5 text-xs">
            <div className="text-muted-foreground">{t('令牌只显示这一次，复制下来填到那台电脑的 Annulo 里：')}</div>
            <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
              <span className="text-muted-foreground">{t('服务器地址')}</span>
              <code className="truncate">{serverURL}</code>
              <span className="text-muted-foreground">{t('令牌')}</span>
              <code className="truncate">{fresh.token}</code>
            </div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                void navigator.clipboard?.writeText(fresh.token)
                setCopied(true)
              }}
            >
              {copied ? <Check /> : <Copy />}
              {copied ? t('已复制') : t('复制令牌')}
            </Button>
          </div>
        )}
      </div>

      {nodes.length > 0 && (
        <div className="divide-y divide-border rounded-xl border border-border bg-card">
          {nodes.map((n) => (
            <div key={n.id} className="flex items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-semibold">{n.name}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {n.online ? (
                    <StatusDot tone="ok">{t('连着：{machine}', { machine: n.machine_name ?? '' })}</StatusDot>
                  ) : (
                    <StatusDot tone="idle">{t('没连着：那台电脑的 Annulo 开着、设置 → 节点 里打开开关才连得上')}</StatusDot>
                  )}
                </div>
              </div>
              {nodes.length > 1 &&
                (n.default ? (
                  <span className="text-xs text-muted-foreground">{t('默认')}</span>
                ) : (
                  <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => act('def' + n.id, () => post('nodes/default', { id: n.id }, 'PUT'))}>
                    {t('设为默认')}
                  </Button>
                ))}
              <Button size="icon-sm" variant="ghost" aria-label={t('删除')} disabled={!!busy} onClick={() => act('del' + n.id, () => post('nodes/tokens/' + n.id, undefined, 'DELETE'))}>
                <Trash2 />
              </Button>
            </div>
          ))}
        </div>
      )}
      {err && <p className="text-sm text-destructive">{err}</p>}
    </div>
  )
}

type NodeState = { url: string; enabled: boolean; has_token: boolean; online: boolean; error: string }

function ConnectToServer() {
  const [st, setSt] = useState<NodeState | null>(null)
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    getJSON<NodeState>('settings/node')
      .then((s) => {
        setSt(s)
        setUrl(s.url)
      })
      .catch((e: Error) => setErr(e.message))
  }, [])

  const save = async (enabled: boolean) => {
    setBusy(true)
    setErr('')
    try {
      const r = await post('settings/node', { url, enabled, ...(token ? { token } : {}) }, 'PUT')
      setSt((await r.json()) as NodeState)
      setToken('')
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (!st)
    return (
      <div className="flex justify-center py-6">
        <Loader className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  return (
    <div className="space-y-4 rounded-xl border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-semibold">{t('把这台电脑的浏览器借给服务器')}</div>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            {t('团队的 Annulo 部署在服务器上时，打开这个开关，服务器上的社媒发布、采集就用这台电脑的浏览器和登录态。只有浏览器借出去，文件、密钥、命令行都不给。')}
          </p>
        </div>
        <Switch checked={st.enabled} disabled={busy || !url || (!st.has_token && !token)} onChange={(v) => save(v)} label={t('连到服务器')} />
      </div>
      <label className="block space-y-1.5">
        <span className="text-xs font-medium">{t('服务器地址')}</span>
        <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://annulo.example.com" />
      </label>
      <label className="block space-y-1.5">
        <span className="text-xs font-medium">{t('节点令牌')}</span>
        <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={st.has_token ? t('已经填过；要换就填新的') : t('在服务器的 设置 → 节点 里生成')} />
      </label>
      <div className="flex items-center justify-between gap-3">
        <div className="text-xs">
          {st.enabled &&
            (st.online ? <StatusDot tone="ok">{t('已连上服务器')}</StatusDot> : <StatusDot tone="bad">{st.error || t('正在连…')}</StatusDot>)}
        </div>
        <Button size="sm" onClick={() => save(true)} disabled={busy || !url || (!st.has_token && !token)}>
          {busy && <Loader className="animate-spin" />}
          {t('保存并连接')}
        </Button>
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
    </div>
  )
}
