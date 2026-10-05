import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, ExternalLink, Loader2, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge, Field, Input, Switch, Textarea } from '@/components/ui/controls'
import { getJSON, post, type MCPServerConfig, type MCPServerStatus, type MCPState } from '@/lib/api'
import { fmtTokens } from '@/lib/format'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

type Config = { mcpServers: Record<string, MCPServerConfig> }

const PRESETS: { key: string; label: string; hint: string; name: string; cfg: MCPServerConfig }[] = [
  { key: 'notion', label: 'Notion', hint: '官方远程服务，保存后去浏览器授权', name: 'notion', cfg: { type: 'http', url: 'https://mcp.notion.com/mcp' } },
  { key: 'playwright', label: 'Playwright 浏览器', hint: '让 agent 操作浏览器，需要本机有 Node.js', name: 'playwright', cfg: { command: 'npx', args: ['@playwright/mcp@latest'] } },
  { key: 'custom-http', label: '自定义远程', hint: '填 MCP 地址，需要 OAuth 的会提示授权', name: '', cfg: { type: 'http', url: '' } },
  { key: 'custom-stdio', label: '自定义本地命令', hint: '填启动命令，比如 npx -y some-server', name: '', cfg: { command: '' } },
]

const STATUS: Record<MCPServerStatus['status'], { text: string; dot: string; ink: string }> = {
  connecting: { text: '连接中', dot: 'bg-amber-500', ink: 'text-amber-700 dark:text-amber-400' },
  needs_auth: { text: '需要授权', dot: 'bg-amber-500', ink: 'text-amber-700 dark:text-amber-400' },
  connected: { text: '已连接', dot: 'bg-emerald-500', ink: 'text-emerald-700 dark:text-emerald-400' },
  failed: { text: '连接失败', dot: 'bg-destructive', ink: 'text-destructive' },
  disabled: { text: '已停用', dot: 'bg-muted-foreground/50', ink: 'text-muted-foreground' },
}

function parse(text: string): Config {
  try {
    const c = JSON.parse(text)
    return { mcpServers: c?.mcpServers ?? {} }
  } catch {
    return { mcpServers: {} }
  }
}

/** MCP 设置（设置页的一节）：server 列表与状态、添加、授权、原始 JSON 编辑。 */
export default function MCPSettings() {
  const [state, setState] = useState<MCPState | null>(null)
  const [error, setError] = useState('')
  const [adding, setAdding] = useState(false)
  const [rawOpen, setRawOpen] = useState(false)

  const load = useCallback(() => getJSON<MCPState>('settings/mcp').then(setState).catch((e) => setError(e.message)), [])
  useEffect(() => {
    load()
  }, [load])

  // 有 server 在连接或等授权时轮询状态
  const pending = state?.servers.some((s) => s.status === 'connecting' || s.status === 'needs_auth')
  useEffect(() => {
    if (!pending) return
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [pending, load])

  const save = async (cfg: Config) => {
    setError('')
    try {
      const r = await post('settings/mcp', { config: JSON.stringify(cfg, null, 2) + '\n' }, 'PUT')
      setState(await r.json())
      return true
    } catch (e) {
      setError((e as Error).message)
      return false
    }
  }

  const action = async (path: string, name: string) => {
    setError('')
    try {
      const r = await post(`settings/mcp/${path}?name=${encodeURIComponent(name)}`)
      setState(await r.json())
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const toggleTools = async (name: string, tools: string[], enabled: boolean) => {
    setError('')
    try {
      const r = await post('settings/mcp/tool', { name, tools, enabled })
      setState(await r.json())
    } catch (e) {
      setError((e as Error).message)
    }
  }

  if (!state) return <div className="text-sm text-faint">{t('加载…')}</div>
  const cfg = parse(state.config)

  return (
    <div className="space-y-4 text-sm">
      {error && <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">{error}</div>}

      {state.servers.length === 0 && !adding && (
        <div className="rounded-lg border border-dashed border-border bg-muted/40 px-4 py-10 text-center text-sm text-muted-foreground">
          {t('还没有接入 MCP server。点下面的「添加 MCP server」，可以一键接入 Notion。')}
        </div>
      )}

      <div className={cn(state.servers.length > 0 && 'overflow-hidden rounded-xl border border-border bg-background')}>
        {state.servers.map((s) => (
          <ServerCard
            key={s.name}
            s={s}
            cfg={cfg.mcpServers[s.name]}
            onAction={action}
            onToggleTools={(tools, enabled) => toggleTools(s.name, tools, enabled)}
            onToggle={() => {
              const next = structuredClone(cfg)
              const c = next.mcpServers[s.name]
              if (c.disabled) delete c.disabled
              else c.disabled = true
              save(next)
            }}
            onRemove={() => {
              const next = structuredClone(cfg)
              delete next.mcpServers[s.name]
              save(next)
            }}
          />
        ))}
      </div>

      <p className="text-xs text-muted-foreground">
        {t('配置格式和 Claude Code / Cursor 通用，存在')} <code className="font-mono">{state.file}</code>{t('，可以直接复制过来。')}
      </p>

      {adding ? (
        <AddServer
          existing={Object.keys(cfg.mcpServers)}
          onCancel={() => setAdding(false)}
          onAdd={async (name, c) => {
            const next = structuredClone(cfg)
            next.mcpServers[name] = c
            if (await save(next)) setAdding(false)
          }}
        />
      ) : (
        <div className="flex gap-2">
          <Button onClick={() => setAdding(true)}>
            <Plus /> {t('添加 MCP server')}
          </Button>
          <Button variant="outline" onClick={() => setRawOpen((v) => !v)}>
            {rawOpen ? t('收起 JSON') : t('编辑 JSON')}
          </Button>
        </div>
      )}

      {rawOpen && <RawEditor text={state.config} onSave={async (t) => {
        setError('')
        try {
          const r = await post('settings/mcp', { config: t }, 'PUT')
          setState(await r.json())
        } catch (e) {
          setError((e as Error).message)
        }
      }} />}
    </div>
  )
}

function ServerCard({
  s,
  cfg,
  onAction,
  onToggleTools,
  onToggle,
  onRemove,
}: {
  s: MCPServerStatus
  cfg?: MCPServerConfig
  onAction: (path: string, name: string) => void
  onToggleTools: (tools: string[], enabled: boolean) => void
  onToggle: () => void
  onRemove: () => void
}) {
  const [open, setOpen] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const st = STATUS[s.status]
  const on = s.tools.filter((t) => t.enabled)
  const onTokens = on.reduce((n, t) => n + t.tokens, 0)
  const target = cfg?.url ?? [cfg?.command, ...(cfg?.args ?? [])].filter(Boolean).join(' ')
  return (
    <div className="border-b border-border last:border-b-0">
      <div className="flex items-center gap-3 px-4 py-3">
        <button onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex min-w-0 flex-1 items-center gap-2 text-left">
          <ChevronRight className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
          <span className="truncate text-[13px] font-bold">{s.name}</span>
          <Badge>{s.transport === 'stdio' ? t('本地') : t('远程')}</Badge>
          <span className={cn('inline-flex items-center gap-1.5 text-[11px]', st.ink)}>
            {s.status === 'connecting' ? <Loader2 className="size-3 animate-spin" /> : <span className={cn('size-1.5 rounded-full', st.dot)} />}
            {t(st.text)}
          </span>
          {s.status === 'connected' && (
            <span className="text-[11px] text-muted-foreground tabular-nums">
              {on.length === s.tools.length ? t('{n} 个工具', { n: s.tools.length }) : t('{on}/{n} 个工具', { on: on.length, n: s.tools.length })} · {t('约 {n} token', { n: fmtTokens(onTokens) })}
            </span>
          )}
        </button>
        <div className="flex shrink-0 items-center gap-1">
          {s.auth_url && (
            <Button size="sm" asChild>
              <a href={s.auth_url} target="_blank" rel="noreferrer">
                {t('去授权')} <ExternalLink />
              </a>
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => onAction('reconnect', s.name)}>
            {t('重连')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onToggle}>
            {s.status === 'disabled' ? t('启用') : t('停用')}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => (confirm ? onRemove() : setConfirm(true))}
            onMouseLeave={() => setConfirm(false)}
            className={confirm ? 'text-destructive hover:text-destructive' : 'text-muted-foreground'}
          >
            {confirm ? t('确认删除') : t('删除')}
          </Button>
        </div>
      </div>
      {s.error && s.status === 'failed' && <div className="mx-4 mb-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">{s.error}</div>}
      {s.status === 'needs_auth' && (
        <div className="mx-4 mb-3 rounded-lg border border-input bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          {t('点「去授权」在浏览器里登录并同意，完成后这里会自动变成已连接。授权链接 10 分钟内有效，过期了点「重连」。')}
        </div>
      )}
      {open && (
        <div className="space-y-3 px-4 pb-3 pl-[40px] text-xs">
          <div className="rounded-lg border border-input bg-muted/40 px-3 py-2 font-mono break-all text-muted-foreground">{target}</div>
          {s.authed && (
            <div className="flex items-center gap-2 text-muted-foreground">
              {t('已保存 OAuth 授权')}
              <button onClick={() => onAction('logout', s.name)} className="font-medium text-primary-text hover:underline">
                {t('退出授权')}
              </button>
            </div>
          )}
          {s.tools.length > 0 && (
            <div className="space-y-2">
              <div className="flex items-center gap-3 text-muted-foreground">
                <span className="flex-1">{t('工具定义每次请求都会带上，用不到的关掉能省上下文和费用。')}</span>
                <button onClick={() => onToggleTools(s.tools.map((t) => t.name), true)} className="font-medium text-primary-text hover:underline">
                  {t('全部开启')}
                </button>
                <button onClick={() => onToggleTools(s.tools.map((t) => t.name), false)} className="font-medium text-primary-text hover:underline">
                  {t('全部关闭')}
                </button>
              </div>
              <div className="divide-y divide-border rounded-lg border border-border">
                {s.tools.map((tool) => (
                  <div key={tool.name} className="flex items-center gap-3 px-3 py-1.5">
                    <span className={cn('min-w-0 flex-1 truncate font-mono text-[11px]', !tool.enabled && 'text-muted-foreground line-through')}>{tool.name}</span>
                    <span className={cn('shrink-0 text-[11px] tabular-nums', tool.tokens >= 2000 ? 'text-amber-700 dark:text-amber-400' : 'text-muted-foreground')}>
                      {t('约 {n}', { n: fmtTokens(tool.tokens) })}
                    </span>
                    <Switch checked={tool.enabled} onChange={(v) => onToggleTools([tool.name], v)} label={tool.enabled ? t('关闭 {name}', { name: tool.name }) : t('开启 {name}', { name: tool.name })} />
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function lines(text: string) {
  return text.split('\n').map((l) => l.trim()).filter(Boolean)
}

function kv(text: string, sep: string) {
  const out: Record<string, string> = {}
  for (const l of lines(text)) {
    const i = l.indexOf(sep)
    if (i > 0) out[l.slice(0, i).trim()] = l.slice(i + 1).trim()
  }
  return out
}

function AddServer({ existing, onCancel, onAdd }: { existing: string[]; onCancel: () => void; onAdd: (name: string, cfg: MCPServerConfig) => void }) {
  const [preset, setPreset] = useState(PRESETS[0].key)
  const p = PRESETS.find((x) => x.key === preset)!
  const [name, setName] = useState(p.name)
  const [kind, setKind] = useState<'http' | 'stdio'>(p.cfg.command !== undefined ? 'stdio' : 'http')
  const [url, setUrl] = useState(p.cfg.url ?? '')
  const [headers, setHeaders] = useState('')
  const [command, setCommand] = useState([p.cfg.command, ...(p.cfg.args ?? [])].filter(Boolean).join(' '))
  const [env, setEnv] = useState('')
  const [err, setErr] = useState('')

  const pick = (key: string) => {
    const x = PRESETS.find((y) => y.key === key)!
    setPreset(key)
    setName(x.name)
    setKind(x.cfg.command !== undefined ? 'stdio' : 'http')
    setUrl(x.cfg.url ?? '')
    setCommand([x.cfg.command, ...(x.cfg.args ?? [])].filter(Boolean).join(' '))
  }

  const submit = () => {
    const n = name.trim()
    if (!/^[A-Za-z0-9_-]{1,32}$/.test(n)) return setErr(t('名称只能用字母、数字、_ 和 -'))
    if (existing.includes(n)) return setErr(t('已经有叫 {n} 的 server 了', { n }))
    let cfg: MCPServerConfig
    if (kind === 'http') {
      if (!url.trim()) return setErr(t('要填 URL'))
      cfg = { type: 'http', url: url.trim() }
      const h = kv(headers, ':')
      if (Object.keys(h).length) cfg.headers = h
    } else {
      const parts = command.trim().split(/\s+/).filter(Boolean)
      if (!parts.length) return setErr(t('要填启动命令'))
      cfg = { command: parts[0], args: parts.slice(1) }
      const e = kv(env, '=')
      if (Object.keys(e).length) cfg.env = e
    }
    onAdd(n, cfg)
  }

  return (
    <div className="space-y-5 rounded-xl border border-border bg-background p-5">
      <div className="grid grid-cols-2 gap-2">
        {PRESETS.map((x) => (
          <button
            key={x.key}
            onClick={() => pick(x.key)}
            className={cn(
              'rounded-xl border-[1.5px] px-3 py-2.5 text-left transition-all',
              preset === x.key ? 'border-primary bg-background' : 'border-border hover:bg-accent',
            )}
          >
            <div className="text-[13px] font-bold">{t(x.label)}</div>
            <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">{t(x.hint)}</div>
          </button>
        ))}
      </div>
      <Field label={t('名称')} htmlFor="mcp-name">
        <Input id="mcp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('比如 notion')} className="font-mono" />
      </Field>
      {kind === 'http' ? (
        <>
          <Field label="URL" htmlFor="mcp-url">
            <Input id="mcp-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://…/mcp" className="font-mono" />
          </Field>
          <Field label={t('请求头（可选）')} hint={t('每行一个 Key: Value。填了 Authorization 就不走 OAuth；${VAR} 会用 shuttle 进程的环境变量替换。')} htmlFor="mcp-headers">
            <Textarea id="mcp-headers" value={headers} onChange={(e) => setHeaders(e.target.value)} rows={2} placeholder="Authorization: Bearer ${MY_TOKEN}" className="font-mono" />
          </Field>
        </>
      ) : (
        <>
          <Field label={t('启动命令')} htmlFor="mcp-cmd">
            <Input id="mcp-cmd" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx -y @notionhq/notion-mcp-server" className="font-mono" />
          </Field>
          <Field label={t('环境变量（可选）')} hint={t('每行一个 KEY=VALUE。')} htmlFor="mcp-env">
            <Textarea id="mcp-env" value={env} onChange={(e) => setEnv(e.target.value)} rows={2} placeholder="NOTION_TOKEN=${NOTION_TOKEN}" className="font-mono" />
          </Field>
        </>
      )}
      {err && <div className="text-xs text-destructive">{err}</div>}
      <div className="flex gap-2">
        <Button onClick={submit}>{t('保存并连接')}</Button>
        <Button variant="outline" onClick={onCancel}>
          {t('取消')}
        </Button>
      </div>
    </div>
  )
}

function RawEditor({ text, onSave }: { text: string; onSave: (t: string) => void }) {
  const [val, setVal] = useState(text)
  useEffect(() => setVal(text), [text])
  return (
    <div className="space-y-2">
      <Textarea value={val} onChange={(e) => setVal(e.target.value)} rows={14} spellCheck={false} className="font-mono text-xs" aria-label="mcp.json" />
      <Button onClick={() => onSave(val)}>{t('保存 JSON')}</Button>
    </div>
  )
}
