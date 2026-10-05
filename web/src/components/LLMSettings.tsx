import { useEffect, useState } from 'react'
import { Brain, Check, CheckCircle2, Eye, Loader2, MessageSquare, Pencil, Plus, RotateCw, Trash2, XCircle } from 'lucide-react'
import { getJSON, post, priceText, THINKING_LABEL, type ModelConfig, type ModelSettings, type ProviderConfig, type Thinking } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Field, Input, Segmented, Switch } from '@/components/ui/controls'
import { Select } from '@/components/ui/select'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'
import { navigate } from '@/lib/router'

const APIS = [
  { value: 'openai-completions', label: 'OpenAI 兼容（Chat Completions）', hint: 'DeepSeek、豆包、Kimi 和各家网关都用这个。Base URL 写到 /v1（火山方舟是 /api/v3）。' },
  { value: 'anthropic-messages', label: 'Anthropic Messages', hint: 'Base URL 不带 /v1，例如 https://api.anthropic.com' },
  { value: 'openai-responses', label: 'OpenAI Responses', hint: 'OpenAI 官方的 GPT-5 系列，例如 https://api.openai.com/v1' },
]

// 常见服务商：选了就填好协议和地址
const PRESETS = [
  { name: 'DeepSeek', api: 'openai-completions', base_url: 'https://api.deepseek.com/v1', env: 'DEEPSEEK_API_KEY' },
  { name: '豆包（火山方舟）', api: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/v3', env: 'ARK_API_KEY' },
  { name: 'Kimi', api: 'openai-completions', base_url: 'https://api.moonshot.cn/v1', env: 'MOONSHOT_API_KEY' },
  { name: 'Anthropic', api: 'anthropic-messages', base_url: 'https://api.anthropic.com', env: 'ANTHROPIC_API_KEY' },
  { name: 'OpenAI', api: 'openai-responses', base_url: 'https://api.openai.com/v1', env: 'OPENAI_API_KEY' },
]

const THINKING_HINT: Record<Thinking, string> = {
  off: '不思考，回得最快、最省 token。',
  low: '简单任务够用。',
  medium: '默认。写文章、改页面这类多步任务的平衡点。',
  high: '复杂排查、大改动时用，更慢、更费 token。',
}

const hostOf = (u: string) => {
  try {
    return new URL(u).host
  } catch {
    return u
  }
}

type Editing = { kind: 'provider'; item?: ProviderConfig } | { kind: 'model'; item?: ModelConfig } | null

/** 模型设置：思考强度、服务商（内置 creght + 自己加的）、模型（每个挂在一个服务商下） */
export default function LLMSettings({ onSaved }: { onSaved: () => void }) {
  const [st, setSt] = useState<ModelSettings | null>(null)
  const [editing, setEditing] = useState<Editing>(null)
  const [err, setErr] = useState('')
  const [confirmDel, setConfirmDel] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [refreshingAgents, setRefreshingAgents] = useState(false)

  const load = (refresh = false) => getJSON<ModelSettings>(`settings/llm${refresh ? '?refresh=1' : ''}`).then(setSt)
  useEffect(() => {
    load()
  }, [])

  const act = async (fn: () => Promise<Response>) => {
    setErr('')
    try {
      const r = await fn()
      setSt(await r.json())
      onSaved()
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  const remove = (kind: 'models' | 'providers', id: string) => {
    if (confirmDel !== id) return setConfirmDel(id)
    setConfirmDel('')
    act(() => post(`settings/llm/${kind}/${id}`, undefined, 'DELETE'))
  }
  const saved = () => {
    setEditing(null)
    load()
    onSaved()
  }

  if (!st)
    return (
      <div className="flex justify-center py-8">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )

  const creght = st.providers.find((p) => p.builtin)
  // 关掉的服务商（creght 平台、本机 agent）：它的模型不出现在列表和选择菜单里，后端已经滤掉了
  const isOff = (id: string) => !!st.providers.find((p) => p.id === id)?.disabled
  const toggle = (id: string, on: boolean) => act(() => post('settings/llm', { provider: id, enabled: on }, 'PUT'))
  const own = st.providers.filter((p) => !p.builtin && !p.agent)
  const platformModels = st.models.filter((m) => m.builtin)
  const ownModels = st.models.filter((m) => !m.builtin && !m.agent)
  const active = st.models.find((m) => m.id === st.active)
  const providerName = (id: string) => st.providers.find((p) => p.id === id)?.name ?? t('（服务商已删除）')
  // 按服务商在列表里的顺序分组；服务商被删了的模型单独一组
  const ownGroups = [...new Set([...st.providers.map((p) => p.id), ...ownModels.map((m) => m.provider)])]
    .map((id) => ({ id, name: providerName(id), models: ownModels.filter((m) => m.provider === id) }))
    .filter((g) => g.models.length > 0)

  const delButton = (kind: 'models' | 'providers', id: string, label: string) => (
    <Button
      size={confirmDel === id ? 'sm' : 'icon-sm'}
      variant="ghost"
      onClick={() => remove(kind, id)}
      onBlur={() => setConfirmDel('')}
      aria-label={t('删除 {label}', { label })}
      className={cn('text-muted-foreground', confirmDel === id && 'text-destructive hover:text-destructive')}
    >
      <Trash2 />
      {confirmDel === id && t('确认删除')}
    </Button>
  )

  // 正在编辑的那一项：换底色、主色描边，和上下的行分开（不然看起来像在编辑上一行）
  const editingRow = 'border-b border-border bg-muted/40 p-5 ring-1 ring-primary/40 ring-inset last:border-b-0'

  const modelRow = (m: ModelConfig) =>
    editing?.kind === 'model' && editing.item?.id === m.id ? (
      <div key={m.id} className={editingRow}>
        <ModelEditor initial={m} providers={st.providers} onCancel={() => setEditing(null)} onSaved={saved} />
      </div>
    ) : (
      <div key={m.id} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-[13px] font-bold">{m.label}</span>
            {m.id === st.active && (
              <span className="shrink-0 rounded-md border border-primary/10 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold whitespace-nowrap text-primary-text">{t('正在使用')}</span>
            )}
            {!m.ready && <span className="rounded-md bg-destructive/10 px-2 py-0.5 text-[10px] font-semibold text-destructive">{m.error}</span>}
          </div>
          <div className="mt-0.5 flex min-w-0 items-center gap-3 text-[11px] text-muted-foreground">
            <span className="truncate font-mono">{m.model}</span>
            {m.pricing && <span className="shrink-0 tabular-nums">{priceText(m.pricing)}</span>}
            {m.images && (
              <span className="inline-flex shrink-0 items-center gap-1">
                <Eye className="size-3" /> {t('看图')}
              </span>
            )}
            {m.reasoning && (
              <span className="inline-flex shrink-0 items-center gap-1">
                <Brain className="size-3" /> {t('思考')}
              </span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {m.id !== st.active && (
            <Button size="sm" variant="outline" onClick={() => act(() => post('settings/llm', { active: m.id }, 'PUT'))} disabled={!m.ready}>
              {t('使用')}
            </Button>
          )}
          {!m.builtin && !m.agent && (
            <>
              <Button size="icon-sm" variant="ghost" onClick={() => setEditing({ kind: 'model', item: m })} aria-label={t('编辑 {name}', { name: m.label })} className="text-muted-foreground">
                <Pencil />
              </Button>
              {delButton('models', m.id, m.label)}
            </>
          )}
        </div>
      </div>
    )

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium whitespace-nowrap">{t('思考强度')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t(THINKING_HINT[st.thinking])} {t('所有模型共用，对话框底部也能随时切换。')}</p>
          </div>
          <Segmented<Thinking>
            variant="pill"
            value={st.thinking}
            onChange={(v) => act(() => post('settings/llm', { thinking: v }, 'PUT'))}
            options={st.thinking_levels.map((l) => ({ value: l, label: t(THINKING_LABEL[l]) }))}
          />
        </div>
        {active && !active.reasoning && <p className="text-xs text-muted-foreground">{t('当前模型「{name}」不支持思考，思考强度对它不生效。', { name: active.label })}</p>}
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium whitespace-nowrap">{t('对话自动起名')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('新对话发出第一句话后，让模型起一个短标题（不开思考，一次几十个 token）；关掉就用第一句话当标题。点对话标题随时可以改。')}</p>
          </div>
          <Switch checked={st.auto_title} onChange={(v) => act(() => post('settings/llm', { auto_title: v }, 'PUT'))} label={t('对话自动起名')} />
        </div>
        {st.auto_title && (
          <div className="flex flex-wrap items-center gap-3">
            <label htmlFor="title-model" className="text-xs text-muted-foreground">{t('起名用的模型')}</label>
            <Select
              id="title-model"
              className="w-72 max-w-full"
              title={t('起名用的模型')}
              value={st.models.some((m) => m.id === st.title_model) ? st.title_model : ''}
              onChange={(v) => act(() => post('settings/llm', { title_model: v }, 'PUT'))}
              options={[
                { value: '', label: t('跟对话用同一个'), sub: active ? t('现在是 {name}', { name: active.label }) : undefined, icon: <MessageSquare className="size-3.5" /> },
                ...st.models
                  .filter((m) => m.ready || m.id === st.title_model)
                  .map((m) => ({ value: m.id, label: m.label, sub: m.builtin ? `${t('creght 平台')} · ${m.model}` : m.agent ? m.model : `${providerName(m.provider)} · ${m.model}` })),
              ]}
            />
          </div>
        )}
        {st.title_model && !st.models.some((m) => m.id === st.title_model) && (
          <p className="text-xs text-muted-foreground">{t('选过的起名模型已经不在了，现在跟对话用同一个模型。')}</p>
        )}
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium whitespace-nowrap">{t('服务商')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('模型从哪里调用。creght 平台登录就能用，按 AI 积分计费；也可以加自己的 key。')}</p>
          </div>
          {editing === null && (
            <Button size="sm" variant="outline" onClick={() => setEditing({ kind: 'provider' })}>
              <Plus /> {t('添加服务商')}
            </Button>
          )}
        </div>
        <div className="overflow-hidden rounded-xl border border-border bg-background">
          {creght && (
            <div className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
              <img src="/_shuttle/creght-light-ink.svg" alt="" className="hidden size-5 shrink-0 dark:block" />
              <img src="/_shuttle/creght-dark-ink.svg" alt="" className="size-5 shrink-0 dark:hidden" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-[13px] font-bold">{t('creght 平台')}</span>
                  <span className="rounded-md bg-muted px-2 py-0.5 text-[10px] font-semibold text-muted-foreground">{t('内置')}</span>
                </div>
                <div className={cn('mt-0.5 text-[11px]', creght.ready || creght.disabled ? 'text-muted-foreground' : 'text-destructive')}>
                  {creght.disabled ? t('已关闭：平台的模型不出现在选择菜单里') : creght.ready ? t('用登录的 creght 账号，{n} 个模型，按 AI 积分计费', { n: platformModels.length }) : creght.error}
                </div>
              </div>
              {creght.needs_connect && !creght.disabled && (
                <Button size="sm" variant="outline" onClick={() => navigate('settings', '#connections')}>
                  {t('连接')}
                </Button>
              )}
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t('重新拉取平台模型')}
                title={t('重新拉取平台模型')}
                className="text-muted-foreground"
                disabled={refreshing}
                onClick={async () => {
                  setRefreshing(true)
                  await load(true).finally(() => setRefreshing(false))
                }}
              >
                <RotateCw className={cn(refreshing && 'animate-spin')} />
              </Button>
              <Switch checked={!creght.disabled} onChange={(on) => toggle(creght.id, on)} label={t('使用 creght 平台的模型')} />
            </div>
          )}
          {own.map((p) =>
            editing?.kind === 'provider' && editing.item?.id === p.id ? (
              <div key={p.id} className={editingRow}>
                <ProviderEditor initial={p} onCancel={() => setEditing(null)} onSaved={saved} />
              </div>
            ) : (
              <div key={p.id} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[13px] font-bold">{p.name}</span>
                    {!p.ready && <span className="rounded-md bg-destructive/10 px-2 py-0.5 text-[10px] font-semibold text-destructive">{p.error}</span>}
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
                    {hostOf(p.base_url)}
                    {p.api_key_env ? ` · $${p.api_key_env}` : p.key_hint ? ` · key ${p.key_hint}` : ''}
                  </div>
                </div>
                <Button size="icon-sm" variant="ghost" onClick={() => setEditing({ kind: 'provider', item: p })} aria-label={t('编辑 {name}', { name: p.name })} className="text-muted-foreground">
                  <Pencil />
                </Button>
                {delButton('providers', p.id, p.name)}
              </div>
            ),
          )}
        </div>
        {editing?.kind === 'provider' && !editing.item && (
          <div className="rounded-xl border border-border bg-background p-5">
            <ProviderEditor onCancel={() => setEditing(null)} onSaved={saved} />
          </div>
        )}
      </section>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium whitespace-nowrap">{t('模型')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('没选过的话默认用 creght 平台最便宜的模型（按 agent 的用量折算：大部分输入命中缓存）。')}</p>
          </div>
          {editing === null && (
            <Button size="sm" variant="outline" onClick={() => setEditing({ kind: 'model' })}>
              <Plus /> {t('添加模型')}
            </Button>
          )}
        </div>
        {platformModels.length > 0 && (
          <div className="space-y-1.5">
            <div className="text-[11px] font-semibold text-muted-foreground">{t('creght 平台')}</div>
            <div className="overflow-hidden rounded-xl border border-border bg-background">{platformModels.map(modelRow)}</div>
          </div>
        )}
        {/* 自己加的模型按服务商分组，组名是服务商的名字 */}
        {ownGroups.map(({ id, name, models }) => (
          <div key={id} className="space-y-1.5">
            <div className="text-[11px] font-semibold text-muted-foreground">{name}</div>
            <div className="overflow-hidden rounded-xl border border-border bg-background">{models.map(modelRow)}</div>
          </div>
        ))}
        {editing?.kind === 'model' && !editing.item && (
          <div className="rounded-xl border border-border bg-background p-5">
            <ModelEditor providers={st.providers.filter((p) => !p.agent)} onCancel={() => setEditing(null)} onSaved={saved} />
          </div>
        )}
        {err && <p className="text-sm text-destructive">{err}</p>}
      </section>

      {!!st.agents?.length && (
        <section className="space-y-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <h3 className="text-sm font-medium whitespace-nowrap">{t('本机 Agent')}</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('本机装了 Claude Code 或 Codex，可以直接让它当助手：用它自己的登录和模型（订阅或它自己的 key），不用 creght 积分。Annulo 的工具和 skill 会自动接给它。不支持中途插话，插的话等这一轮结束再发。')}
              </p>
            </div>
          </div>
          <div className="overflow-hidden rounded-xl border border-border bg-background">
            {st.agents.map((a) => {
              if (a.path)
                return (
                  <div key={a.id} className="flex items-start gap-3 border-b border-border px-4 py-3 last:border-b-0">
                    <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13px] font-bold">{a.name}</span>
                      {st.active.startsWith(a.model_id) && (
                        <span className="rounded-md border border-primary/10 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary-text">{t('正在使用')}</span>
                      )}
                      <span className="min-w-0 truncate font-mono text-[11px] text-muted-foreground">{a.path}</span>
                    </div>
                    {isOff(a.id) ? (
                      <div className="mt-1 text-[11px] text-muted-foreground">{t('已关闭：不出现在选择菜单里')}</div>
                    ) : (
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      {(a.models ?? []).map((m) => (
                        <button
                          key={m.model_id}
                          type="button"
                          onClick={() => m.model_id !== st.active && act(() => post('settings/llm', { active: m.model_id }, 'PUT'))}
                          aria-pressed={m.model_id === st.active}
                          className={cn(
                            'inline-flex h-7 cursor-pointer items-center gap-1 rounded-md border px-2.5 text-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                            m.model_id === st.active ? 'border-primary/50 bg-primary/10 font-semibold text-primary-text' : 'border-border hover:bg-accent',
                          )}
                        >
                          {m.model_id === st.active && <Check className="size-3" />}
                          {m.name}
                        </button>
                      ))}
                      {(a.models ?? []).length <= 1 && <span className="self-center text-[11px] text-muted-foreground">{t('还没读到它支持的模型，点右边的刷新')}</span>}
                    </div>
                    )}
                    </div>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t('重新读取 {name} 支持的模型', { name: a.name })}
                      title={t('重新读取 {name} 支持的模型', { name: a.name })}
                      className="shrink-0 self-center text-muted-foreground"
                      disabled={refreshingAgents}
                      onClick={async () => {
                        setRefreshingAgents(true)
                        await getJSON<ModelSettings>('settings/llm?refresh=agents').then(setSt).finally(() => setRefreshingAgents(false))
                      }}
                    >
                      <RotateCw className={cn(refreshingAgents && 'animate-spin')} />
                    </Button>
                    <div className="shrink-0 self-center">
                      <Switch checked={!isOff(a.id)} onChange={(on) => toggle(a.id, on)} label={t('使用 {name}', { name: a.name })} />
                    </div>
                  </div>
                )
              return (
                <div key={a.id} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13px] font-bold">{a.name}</span>
                      <span className="rounded-md bg-muted px-2 py-0.5 text-[10px] font-semibold text-muted-foreground">{t('没装')}</span>
                    </div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      {t('在终端里装好并登录，回到这里刷新：')} <code className="font-mono select-all">{a.install}</code>
                    </div>
                  </div>
                  <Button size="icon-sm" variant="ghost" aria-label={t('刷新')} title={t('刷新')} className="text-muted-foreground" onClick={() => load()}>
                    <RotateCw />
                  </Button>
                </div>
              )
            })}
          </div>
        </section>
      )}
    </div>
  )
}

function Result({ r }: { r: { ok: boolean; text: string } | null }) {
  if (!r) return null
  return (
    <div className={`flex items-start gap-2 rounded-lg px-3 py-2.5 text-sm ${r.ok ? 'bg-ok/10 text-ok' : 'bg-destructive/10 text-destructive'}`}>
      {r.ok ? <CheckCircle2 className="mt-0.5 size-4 shrink-0" /> : <XCircle className="mt-0.5 size-4 shrink-0" />}
      <span className="break-all">{r.text}</span>
    </div>
  )
}

type KeyMode = 'direct' | 'env'

/** 新增 / 编辑服务商：协议、地址、key */
function ProviderEditor({ initial, onSaved, onCancel }: { initial?: ProviderConfig; onSaved: () => void; onCancel: () => void }) {
  const [name, setName] = useState(initial?.name ?? '')
  const [api, setApi] = useState(initial?.api || 'openai-completions')
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? '')
  const [keyMode, setKeyMode] = useState<KeyMode>(initial?.api_key_env && !initial.has_key ? 'env' : 'direct')
  const [apiKey, setApiKey] = useState('')
  const [keyEnv, setKeyEnv] = useState(initial?.api_key_env ?? '')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)

  const preset = (p: (typeof PRESETS)[number]) => {
    setApi(p.api)
    setBaseURL(p.base_url)
    setName(p.name)
    if (!keyEnv) setKeyEnv(p.env)
  }
  const save = async () => {
    setBusy(true)
    setResult(null)
    try {
      await post('settings/llm/providers', {
        id: initial?.id ?? '',
        name,
        api,
        base_url: baseURL,
        api_key: keyMode === 'direct' ? apiKey : '',
        api_key_env: keyMode === 'env' ? keyEnv : '',
      })
      onSaved()
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  const keepHint = initial && (initial.has_key || initial.api_key_env) ? t('留空则沿用当前的 key') + (initial.key_hint ? t('（尾号 {tail}）', { tail: initial.key_hint.replace('…', '') }) : '') : 'sk-…'

  return (
    <div className="space-y-5">
      <div className="text-sm font-medium">{initial ? t('编辑「{name}」', { name: initial.name }) : t('添加服务商')}</div>
      {!initial && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 text-xs text-muted-foreground">{t('常用')}</span>
          {PRESETS.map((p) => (
            <button
              key={p.name}
              type="button"
              onClick={() => preset(p)}
              className={cn(
                'rounded-md border border-border px-2 py-1 text-xs outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
                baseURL === p.base_url && 'border-primary/40 bg-primary/10 text-primary-text',
              )}
            >
              {t(p.name)}
            </button>
          ))}
        </div>
      )}
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label={t('名字')} htmlFor="p-name">
          <Input id="p-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('比如：DeepSeek')} />
        </Field>
        <Field label={t('协议')} hint={t(APIS.find((a) => a.value === api)?.hint ?? '')} htmlFor="p-api">
          <Select id="p-api" title={t('协议')} value={api} onChange={setApi} options={APIS.map((a) => ({ value: a.value, label: t(a.label) }))} />
        </Field>
      </div>
      <Field label="Base URL" htmlFor="p-base">
        <Input id="p-base" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.deepseek.com/v1" className="font-mono" />
      </Field>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium">API Key</div>
            <p className="mt-1 text-xs text-muted-foreground">{t('直接填写的 key 存在本机、权限 0600；也可以让 Annulo 读终端里的环境变量。')}</p>
          </div>
          <Segmented<KeyMode>
            variant="pill"
            value={keyMode}
            onChange={setKeyMode}
            options={[
              { value: 'direct', label: t('直接填写') },
              { value: 'env', label: t('环境变量') },
            ]}
          />
        </div>
        {keyMode === 'direct' ? (
          <Input type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={keepHint} autoComplete="off" className="font-mono" aria-label="API Key" />
        ) : (
          <Input value={keyEnv} onChange={(e) => setKeyEnv(e.target.value)} placeholder="DEEPSEEK_API_KEY" className="font-mono" aria-label={t('环境变量名')} />
        )}
      </div>
      <Result r={result} />
      <div className="flex gap-2">
        <Button onClick={save} disabled={busy}>
          {busy ? <Loader2 className="animate-spin" /> : <Check />} {t('保存')}
        </Button>
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          {t('取消')}
        </Button>
      </div>
      {!initial && <p className="text-xs text-muted-foreground">{t('保存后在下面「添加模型」里选这个服务商，填模型 id。')}</p>}
    </div>
  )
}

/** 新增 / 编辑模型：选服务商、填模型 id；可以先测试连接再保存 */
function ModelEditor({ initial, providers, onSaved, onCancel }: { initial?: ModelConfig; providers: ProviderConfig[]; onSaved: () => void; onCancel: () => void }) {
  const firstOwn = providers.find((p) => !p.builtin)?.id ?? 'creght'
  const [provider, setProvider] = useState(initial?.provider || firstOwn)
  const [name, setName] = useState(initial?.name ?? '')
  const [model, setModel] = useState(initial?.model ?? '')
  const [win, setWin] = useState(initial?.context_window ? String(initial.context_window) : '')
  const [images, setImages] = useState(initial?.images ?? true)
  const [reasoning, setReasoning] = useState(initial?.reasoning ?? true)
  const [busy, setBusy] = useState<'' | 'test' | 'save'>('')
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)

  const p = providers.find((x) => x.id === provider)
  const body = () => ({ id: initial?.id ?? '', name, provider, model, context_window: Number(win) || 0, images, reasoning })
  const run = async (kind: 'test' | 'save') => {
    setBusy(kind)
    setResult(null)
    try {
      if (kind === 'test') {
        const d = await (await post('settings/llm/test', { model: body() })).json()
        setResult({ ok: true, text: t('连接成功，用时 {s} 秒。模型回复：{reply}', { s: (d.ms / 1000).toFixed(1), reply: d.reply }) })
      } else {
        await post('settings/llm/models', body())
        onSaved()
      }
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message })
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="space-y-5">
      <div className="text-sm font-medium">{initial ? t('编辑「{name}」', { name: initial.label }) : t('添加模型')}</div>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field label={t('服务商')} hint={p && !p.ready ? p.error : undefined} htmlFor="m-provider">
          <Select
            id="m-provider"
            title={t('服务商')}
            value={provider}
            onChange={setProvider}
            options={providers.map((x) => ({ value: x.id, label: x.name, sub: x.base_url || undefined }))}
          />
        </Field>
        <Field label={t('模型 id')} hint={p?.base_url.includes('volces.com') ? t('火山方舟填模型 id（如 doubao-seed-1-6-250615）或推理接入点 id（ep-…）。') : undefined} htmlFor="m-model">
          <Input id="m-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder="deepseek-chat" className="font-mono" />
        </Field>
        <Field label={t('名字')} hint={t('可不填，默认显示模型 id。')} htmlFor="m-name">
          <Input id="m-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('比如：豆包 Seed')} />
        </Field>
        <Field label={t('上下文窗口')} hint={t('单位 token，可不填，默认 512k。')} htmlFor="m-win">
          <Input id="m-win" value={win} onChange={(e) => setWin(e.target.value.replace(/[^0-9]/g, ''))} placeholder="512000" inputMode="numeric" className="tabular-nums" />
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="flex items-start justify-between gap-3 rounded-lg border border-border px-3 py-2.5">
          <span>
            <span className="block text-sm">{t('支持看图')}</span>
            <span className="mt-0.5 block text-xs text-muted-foreground">{t('关掉后，对话里的图片只把本机路径告诉模型。')}</span>
          </span>
          <Switch checked={images} onChange={setImages} label={t('支持看图')} />
        </label>
        <label className="flex items-start justify-between gap-3 rounded-lg border border-border px-3 py-2.5">
          <span>
            <span className="block text-sm">{t('支持思考')}</span>
            <span className="mt-0.5 block text-xs text-muted-foreground">{t('打开后，思考强度会发给模型。不认这个参数的模型会报错，关掉即可。')}</span>
          </span>
          <Switch checked={reasoning} onChange={setReasoning} label={t('支持思考')} />
        </label>
      </div>
      <Result r={result} />
      <div className="flex gap-2">
        <Button onClick={() => run('save')} disabled={!!busy}>
          {busy === 'save' ? <Loader2 className="animate-spin" /> : <Check />} {t('保存')}
        </Button>
        <Button variant="outline" onClick={() => run('test')} disabled={!!busy}>
          {busy === 'test' && <Loader2 className="animate-spin" />} {t('测试连接')}
        </Button>
        <Button variant="ghost" onClick={onCancel} disabled={!!busy}>
          {t('取消')}
        </Button>
      </div>
    </div>
  )
}
