import { useEffect, useState } from 'react'
import { Brain, Check, CheckCircle2, Eye, Loader2, MessageSquare, Pencil, Plus, RotateCw, Server, Trash2, XCircle } from 'lucide-react'
import { getJSON, normModelSettings, post, priceText, THINKING_LABEL, type ModelConfig, type ModelSettings, type ProviderConfig, type Thinking } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Field, Input, Segmented, Switch } from '@/components/ui/controls'
import { Select } from '@/components/ui/select'
import { cn } from '@/lib/utils'
import { menuLabel } from '@/components/ModelMenu'
import { BrandIcon, ModelIcon } from '@/lib/modelBrand'
import { t } from '@/lib/i18n'

const APIS = [
  { value: 'openai-completions', label: 'OpenAI 兼容（Chat Completions）', hint: 'DeepSeek、豆包、Kimi 和各家网关都用这个。Base URL 写到 /v1（火山方舟是 /api/v3）。' },
  { value: 'anthropic-messages', label: 'Anthropic Messages', hint: 'Base URL 不带 /v1，例如 https://api.anthropic.com' },
  { value: 'openai-responses', label: 'OpenAI Responses', hint: 'OpenAI 官方的 GPT-5 系列，例如 https://api.openai.com/v1' },
]

// 预设里推荐的模型：拉到的列表里有就默认勾上，参数（上下文、看图、思考）按这里填；列表里没有的模型用默认参数，之后能改
type ModelMeta = { id: string; window?: number; images?: boolean; reasoning?: boolean }

// 常见服务商：选了就填好协议和地址，带上推荐的模型
type Preset = { name: string; api: string; base_url: string; env: string; models: ModelMeta[] }
const PRESETS: Preset[] = [
  {
    name: 'DeepSeek',
    api: 'openai-completions',
    base_url: 'https://api.deepseek.com/v1',
    env: 'DEEPSEEK_API_KEY',
    models: [
      { id: 'deepseek-chat', window: 128000, images: false, reasoning: false },
      { id: 'deepseek-reasoner', window: 128000, images: false, reasoning: true },
    ],
  },
  { name: '豆包（火山方舟）', api: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/v3', env: 'ARK_API_KEY', models: [] },
  { name: 'Kimi', api: 'openai-completions', base_url: 'https://api.moonshot.cn/v1', env: 'MOONSHOT_API_KEY', models: [] },
  {
    name: 'Anthropic',
    api: 'anthropic-messages',
    base_url: 'https://api.anthropic.com',
    env: 'ANTHROPIC_API_KEY',
    models: [
      { id: 'claude-sonnet-5-5', window: 200000, images: true, reasoning: true },
      { id: 'claude-opus-5-5', window: 200000, images: true, reasoning: true },
      { id: 'claude-haiku-4-5-20251001', window: 200000, images: true, reasoning: true },
    ],
  },
  { name: 'OpenAI', api: 'openai-responses', base_url: 'https://api.openai.com/v1', env: 'OPENAI_API_KEY', models: [{ id: 'gpt-5', window: 400000, images: true, reasoning: true }] },
]
const presetOf = (baseURL: string) => PRESETS.find((p) => p.base_url === baseURL.replace(/\/+$/, ''))

const THINKING_HINT: Record<Thinking, string> = {
  off: '不思考，回得最快、最省 token。',
  minimal: '只想一点点，比「低」还快。',
  low: '简单任务够用。',
  medium: '默认。写文章、改页面这类多步任务的平衡点。',
  high: '复杂排查、大改动时用，更慢、更费 token。',
  xhigh: '比「高」想得更久，难题用。',
  max: '想得最久、最费 token，最难的问题用。',
}

// 档位列表的写法：低 / 高 / 最高
const levelNames = (ls: Thinking[]) => ls.map((l) => t(THINKING_LABEL[l])).join(' / ')

const hostOf = (u: string) => {
  try {
    return new URL(u).host
  } catch {
    return u
  }
}

// add：新服务商（填地址和 key → 选模型，一次存好）；provider：改服务商；models：给已有服务商加模型；model：改一个模型
type Editing = { kind: 'add'; preset?: Preset } | { kind: 'provider'; item: ProviderConfig } | { kind: 'models'; provider: ProviderConfig } | { kind: 'model'; item: ModelConfig } | null

/** 模型设置：思考强度、服务商（内置 creght + 自己加的）、模型（每个挂在一个服务商下） */
export default function LLMSettings({ onSaved }: { onSaved: () => void }) {
  const [st, setSt] = useState<ModelSettings | null>(null)
  const [editing, setEditing] = useState<Editing>(null)
  const [err, setErr] = useState('')
  const [confirmDel, setConfirmDel] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [refreshingAgents, setRefreshingAgents] = useState(false)
  const [managing, setManaging] = useState('') // 展开成列表（能改、能删模型）的服务商

  const load = (refresh = false) => getJSON<ModelSettings>(`settings/llm${refresh ? '?refresh=1' : ''}`).then((s) => setSt(normModelSettings(s)))
  useEffect(() => {
    load()
  }, [])

  const act = async (fn: () => Promise<Response>) => {
    setErr('')
    try {
      const r = await fn()
      setSt(normModelSettings(await r.json()))
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
  // 思考档位只列当前模型有的；选的档它没有时显示实际用的那档
  const activeLevels = active?.reasoning ? (active.thinking_levels ?? st.thinking_levels) : []
  const effective = st.thinking_effective ?? st.thinking
  const providerName = (id: string) => st.providers.find((p) => p.id === id)?.name ?? t('（服务商已删除）')
  // 服务商被删了还留着的模型：单独一张卡片，能删
  const orphans = ownModels.filter((m) => !st.providers.some((p) => p.id === m.provider))
  const use = (id: string) => id !== st.active && act(() => post('settings/llm', { active: id }, 'PUT'))

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

  // nested：服务商卡片里的模型（缩进到服务商名字下面、字小一号），和卡片头分得开
  const modelRow = (m: ModelConfig, nested = false) =>
    editing?.kind === 'model' && editing.item?.id === m.id ? (
      <div key={m.id} className={editingRow}>
        <ModelEditor initial={m} providers={st.providers} onCancel={() => setEditing(null)} onSaved={saved} />
      </div>
    ) : (
      <div key={m.id} className={cn('group flex items-center gap-3 border-b border-border last:border-b-0', nested ? 'py-2 pr-4 pl-[60px]' : 'px-4 py-3')}>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className={cn('truncate', nested ? 'text-xs font-medium' : 'text-[13px] font-bold')}>{m.label}</span>
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
          {/* 「使用」鼠标移到这一行才出现，不然一列按钮太抢眼 */}
          {m.id !== st.active && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => act(() => post('settings/llm', { active: m.id }, 'PUT'))}
              disabled={!m.ready}
              className="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
            >
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
            <p className="mt-1 text-xs text-muted-foreground">{t(THINKING_HINT[effective])} {t('每个模型的档位不一样，这里只列当前模型有的；对话框底部也能随时切换。')}</p>
          </div>
          {activeLevels.length > 1 && (
            <Segmented<Thinking>
              variant="pill"
              value={effective}
              onChange={(v) => act(() => post('settings/llm', { thinking: v }, 'PUT'))}
              options={activeLevels.map((l) => ({ value: l, label: t(THINKING_LABEL[l]) }))}
            />
          )}
        </div>
        {active && !active.reasoning && <p className="text-xs text-muted-foreground">{t('当前模型「{name}」不支持思考，思考强度对它不生效。', { name: active.label })}</p>}
        {active?.reasoning && effective !== st.thinking && (
          <p className="text-xs text-muted-foreground">
            {t('你选的「{want}」当前模型「{name}」没有，现在用最接近的「{used}」；换到有这档的模型时还是「{want}」。', { want: t(THINKING_LABEL[st.thinking]), name: active.label, used: t(THINKING_LABEL[effective]) })}
          </p>
        )}
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
                  // 大字是模型名，小字是服务商（模型 id 和名字不一样时补在后面；本机 agent 的 id 是别名，不显示）
                  .map((m) => {
                    const label = menuLabel(m)
                    const provider = m.builtin ? t('creght 平台') : providerName(m.provider)
                    return { value: m.id, label, sub: m.agent || m.model === label ? provider : `${provider} · ${m.model}`, icon: ModelIcon({ m }) }
                  }),
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
            <h3 className="text-sm font-medium whitespace-nowrap">{t('服务商和模型')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {creght ? t('creght 平台登录就能用，按 AI 积分计费；也可以接自己的 key。点模型就切换，对话框底部也能切。') : t('接自己的 key：选一个服务商，填 key，勾要用的模型。点模型就切换，对话框底部也能切。')}
            </p>
          </div>
          {editing === null && (creght || own.length > 0) && (
            <Button size="sm" variant="outline" onClick={() => setEditing({ kind: 'add' })}>
              <Plus /> {t('添加服务商')}
            </Button>
          )}
        </div>

        {/* 一个服务商都没有：直接给常用的，点了就进添加流程 */}
        {!creght && own.length === 0 && editing?.kind !== 'add' && (
          <div className="space-y-3 rounded-xl border border-dashed border-border p-5">
            <p className="text-sm">{t('还没有接模型服务商。选一个，填上 API Key 就能用：')}</p>
            <div className="flex flex-wrap gap-2">
              {PRESETS.map((p) => (
                <Button key={p.name} size="sm" variant="outline" onClick={() => setEditing({ kind: 'add', preset: p })}>
                  {t(p.name)}
                </Button>
              ))}
              <Button size="sm" variant="ghost" onClick={() => setEditing({ kind: 'add' })}>
                <Plus /> {t('其他（自定义地址）')}
              </Button>
            </div>
            {!!st.agents?.some((a) => a.path) && <p className="text-xs text-muted-foreground">{t('这台电脑装了 Claude Code / Codex 的，也可以直接用下面的「本机 Agent」。')}</p>}
          </div>
        )}

        {editing?.kind === 'add' && (
          <div className="rounded-xl border border-border bg-background p-5 ring-1 ring-primary/40 ring-inset">
            <AddProvider preset={editing.preset} activeReady={!!active?.ready} onCancel={() => setEditing(null)} onSaved={saved} />
          </div>
        )}

        {creght && (
          <div className="overflow-hidden rounded-xl border border-border bg-background">
            <div className="flex items-center gap-3 bg-muted/40 px-4 py-3">
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
            {!creght.disabled && platformModels.length > 0 && (
              <div className="flex flex-wrap gap-1.5 border-t border-border py-3 pr-4 pl-12">
                <ModelChips models={platformModels} active={st.active} onUse={use} />
              </div>
            )}
          </div>
        )}

        {own.map((p) => {
          const models = ownModels.filter((m) => m.provider === p.id)
          if (editing?.kind === 'provider' && editing.item.id === p.id)
            return (
              <div key={p.id} className="rounded-xl border border-border bg-background p-5 ring-1 ring-primary/40 ring-inset">
                <ProviderEditor initial={p} onCancel={() => setEditing(null)} onSaved={saved} />
              </div>
            )
          return (
            <div key={p.id} className="overflow-hidden rounded-xl border border-border bg-background">
              <div className="flex items-center gap-3 bg-muted/40 px-4 py-3">
                <div className="flex size-8 shrink-0 items-center justify-center rounded-lg border border-border bg-background text-muted-foreground">
                  <Server className="size-4" />
                </div>
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
                {editing === null && (
                  <Button size="sm" variant="ghost" onClick={() => setEditing({ kind: 'models', provider: p })} className="text-muted-foreground">
                    <Plus /> {t('模型')}
                  </Button>
                )}
                <Button size="icon-sm" variant="ghost" onClick={() => setEditing({ kind: 'provider', item: p })} aria-label={t('编辑 {name}', { name: p.name })} className="text-muted-foreground">
                  <Pencil />
                </Button>
                {delButton('providers', p.id, p.name)}
              </div>
              {editing?.kind === 'models' && editing.provider.id === p.id ? (
                <div className="border-t border-border bg-muted/40 p-5">
                  <AddModels provider={p} exclude={models.map((m) => m.model)} activeReady={!!active?.ready} onCancel={() => setEditing(null)} onSaved={saved} />
                </div>
              ) : managing === p.id || (editing?.kind === 'model' && models.some((m) => m.id === editing.item.id)) ? (
                <div className="border-t border-border">
                  <div className="flex items-center justify-between pt-2 pr-4 pl-[60px]">
                    <span className="text-[11px] font-semibold text-muted-foreground">{t('模型 · {n} 个', { n: models.length })}</span>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 text-xs text-muted-foreground"
                      onClick={() => {
                        setManaging('')
                        if (editing?.kind === 'model') setEditing(null) // 正在改的模型也收起来（不然它撑着列表不让收）
                      }}
                    >
                      {t('收起')}
                    </Button>
                  </div>
                  {models.map((m) => modelRow(m, true))}
                </div>
              ) : (
                <div className="flex flex-wrap items-center gap-1.5 border-t border-border py-3 pr-4 pl-[60px]">
                  {models.length ? (
                    <>
                      <ModelChips models={models} active={st.active} onUse={use} />
                      <Button size="sm" variant="ghost" className="h-7 text-xs text-muted-foreground" onClick={() => setManaging(p.id)}>
                        {t('管理模型')}
                      </Button>
                    </>
                  ) : (
                    <span className="text-[11px] text-muted-foreground">{t('还没有模型，点右上的「+ 模型」添加')}</span>
                  )}
                </div>
              )}
            </div>
          )
        })}

        {orphans.length > 0 && (
          <div className="space-y-1.5">
            <div className="text-[11px] font-semibold text-muted-foreground">{t('（服务商已删除）')}</div>
            <div className="overflow-hidden rounded-xl border border-border bg-background">{orphans.map((m) => modelRow(m))}</div>
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
                        await getJSON<ModelSettings>('settings/llm?refresh=agents').then((s) => setSt(normModelSettings(s))).finally(() => setRefreshingAgents(false))
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

type ProviderInput = { id: string; name: string; api: string; base_url: string; api_key: string; api_key_env: string }

/** 服务商的表单（协议、地址、key）：新增和编辑共用；新增时上面一排常用服务商 */
function useProviderForm(initial?: ProviderConfig, preset?: Preset) {
  const [name, setName] = useState(initial?.name ?? (preset ? t(preset.name) : ''))
  const [api, setApi] = useState(initial?.api || preset?.api || 'openai-completions')
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? preset?.base_url ?? '')
  const [keyMode, setKeyMode] = useState<KeyMode>(initial?.api_key_env && !initial.has_key ? 'env' : 'direct')
  const [apiKey, setApiKey] = useState('')
  const [keyEnv, setKeyEnv] = useState(initial?.api_key_env ?? preset?.env ?? '')
  const choose = (p: Preset) => {
    setApi(p.api)
    setBaseURL(p.base_url)
    setName(t(p.name))
    setKeyEnv(p.env)
  }
  const input = (): ProviderInput => ({
    id: initial?.id ?? '',
    name,
    api,
    base_url: baseURL,
    api_key: keyMode === 'direct' ? apiKey : '',
    api_key_env: keyMode === 'env' ? keyEnv : '',
  })
  const keepHint = initial && (initial.has_key || initial.api_key_env) ? t('留空则沿用当前的 key') + (initial.key_hint ? t('（尾号 {tail}）', { tail: initial.key_hint.replace('…', '') }) : '') : 'sk-…'
  const fields = (
    <>
      {!initial && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 text-xs text-muted-foreground">{t('常用')}</span>
          {PRESETS.map((p) => (
            <button
              key={p.name}
              type="button"
              onClick={() => choose(p)}
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
          <Select id="p-api" title={t('协议')} value={api} onChange={setApi} options={APIS.map((a) => ({ value: a.value, label: t(a.label), icon: BrandIcon({ of: [a.value] }) }))} />
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
    </>
  )
  return { input, fields, baseURL }
}

/** 编辑服务商：协议、地址、key */
function ProviderEditor({ initial, onSaved, onCancel }: { initial: ProviderConfig; onSaved: () => void; onCancel: () => void }) {
  const form = useProviderForm(initial)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const save = async () => {
    setBusy(true)
    setResult(null)
    try {
      await post('settings/llm/providers', form.input())
      onSaved()
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-5">
      <div className="text-sm font-medium">{t('编辑「{name}」', { name: initial.name })}</div>
      {form.fields}
      <Result r={result} />
      <div className="flex gap-2">
        <Button size="sm" onClick={save} disabled={busy}>
          {busy ? <Loader2 className="animate-spin" /> : <Check />} {t('保存')}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>
          {t('取消')}
        </Button>
      </div>
    </div>
  )
}

/** 一排模型标签，点了就切换成当前模型（和「本机 Agent」一个样子） */
function ModelChips({ models, active, onUse }: { models: ModelConfig[]; active: string; onUse: (id: string) => void }) {
  return (
    <>
      {models.map((m) => (
        <button
          key={m.id}
          type="button"
          onClick={() => m.ready && onUse(m.id)}
          aria-pressed={m.id === active}
          disabled={!m.ready}
          title={!m.ready ? m.error : m.pricing ? priceText(m.pricing) : m.model}
          className={cn(
            'inline-flex h-7 cursor-pointer items-center gap-1 rounded-md border px-2.5 text-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50',
            m.id === active ? 'border-primary/50 bg-primary/10 font-semibold text-primary-text' : 'border-border hover:bg-accent',
          )}
        >
          {m.id === active && <Check className="size-3" />}
          {m.label}
        </button>
      ))}
    </>
  )
}

// 拉服务商的模型列表；unsupported 是服务商不提供列表（让用户手填），key 不对、连不上直接抛出
const listModels = async (provider: ProviderInput) => (await (await post('settings/llm/provider-models', { provider })).json()) as { models: string[]; unsupported?: string }

/** 加模型时每个模型的参数：默认按模型资料（查不到用预设写的或通用的），保存前能改 */
type ModelParams = {
  window: string
  images: boolean
  reasoning: boolean
  custom: Partial<Record<Thinking, string>> | null
  source?: ModelConfig['thinking_source']
  levels?: Thinking[]
}
type ModelDefaults = { context_window: number; images: boolean; reasoning: boolean; catalog: boolean; thinking_levels: Thinking[]; thinking_source: ModelConfig['thinking_source'] }

/** 把勾选的模型存到服务商下；还没有能用的当前模型时，第一个设成当前模型 */
async function saveModels(provider: string, ids: string[], params: Record<string, ModelParams>, activeReady: boolean) {
  let first = ''
  for (const id of ids) {
    const p = params[id]
    const body = p
      ? { context_window: Number(p.window) || 0, images: p.images, reasoning: p.reasoning, thinking_levels: p.reasoning && p.custom ? p.custom : {} }
      : { auto: true, images: true, reasoning: true } // 参数还没读到：后端按资料填
    const r = await post('settings/llm/models', { id: '', name: '', provider, model: id, ...body })
    first ||= ((await r.json()) as ModelConfig).id
  }
  if (first && !activeReady) await post('settings/llm', { active: first }, 'PUT')
}

const windowText = (w: string) => {
  const n = Number(w) || 512000
  return n >= 1000000 ? `${+(n / 1000000).toFixed(1)}M` : `${Math.round(n / 1000)}k`
}

/** 选模型：从服务商拉到的列表里勾，可以搜；拉不到或者列表里没有的，手填模型 id。勾上的模型显示参数摘要，点「调整」能改 */
function ModelPicker({ ids, listError, recommended, api, baseURL, exclude = [], busy, onSave, onBack, onCancel }: {
  ids: string[]
  listError: string
  recommended: string[]
  api: string
  baseURL: string
  exclude?: string[]
  busy: boolean
  onSave: (ids: string[], params: Record<string, ModelParams>) => void
  onBack?: () => void
  onCancel: () => void
}) {
  const [extra, setExtra] = useState<string[]>([])
  const all = [...new Set([...recommended.filter((id) => ids.includes(id)), ...ids, ...extra])].filter((id) => !exclude.includes(id))
  const [picked, setPicked] = useState<string[]>(() => recommended.filter((id) => ids.includes(id) && !exclude.includes(id)))
  const [params, setParams] = useState<Record<string, ModelParams>>({})
  const [open, setOpen] = useState('') // 正在调整参数的模型
  const [q, setQ] = useState('')
  const [manual, setManual] = useState('')
  const [manualErr, setManualErr] = useState('')
  const shown = all.filter((id) => !q.trim() || id.toLowerCase().includes(q.trim().toLowerCase()))

  // 勾上的模型还没有参数：按模型资料取默认（资料里没有，用预设写的，再没有用通用的）
  useEffect(() => {
    const need = picked.filter((id) => !params[id])
    if (!need.length) return
    const meta = presetOf(baseURL)?.models ?? []
    post('settings/llm/model-defaults', { api, models: need })
      .then((r) => r.json() as Promise<{ models: Record<string, ModelDefaults> }>)
      .then(({ models }) =>
        setParams((cur) => {
          const next = { ...cur }
          for (const id of need) {
            const d = models[id]
            const m = meta.find((x) => x.id === id)
            const fromPreset = !d?.catalog && m
            next[id] = next[id] ?? {
              window: String((fromPreset ? m.window : d?.context_window) || ''),
              images: fromPreset ? (m.images ?? true) : (d?.images ?? true),
              reasoning: fromPreset ? (m.reasoning ?? true) : (d?.reasoning ?? true),
              custom: null,
              source: d?.thinking_source,
              levels: d?.thinking_levels,
            }
          }
          return next
        }),
      )
      .catch(() => {}) // 取不到就不显示摘要，保存时后端按资料填
  }, [picked, params, api, baseURL])

  const flip = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  const set = (id: string, patch: Partial<ModelParams>) => setParams((cur) => ({ ...cur, [id]: { ...cur[id], ...patch } }))
  // 手填的填错了：拿回输入框改，或者直接去掉
  const drop = (id: string) => {
    setExtra((e) => e.filter((x) => x !== id))
    setPicked((p) => p.filter((x) => x !== id))
  }
  const addManual = () => {
    const id = manual.trim()
    if (!id) return
    if (exclude.includes(id)) return setManualErr(t('「{id}」已经加过了，同一个服务商下不能重复。', { id }))
    setExtra((e) => (e.includes(id) ? e : [...e, id]))
    setPicked((p) => (p.includes(id) ? p : [...p, id]))
    setManual('')
  }
  const summary = (p: ModelParams) => {
    const levels = p.custom ? ALL_LEVELS.filter((l) => l in p.custom!) : p.levels
    const thinking = !p.reasoning ? t('不思考') : levels?.length ? t('思考：{levels}', { levels: levelNames(levels) }) : t('思考')
    return [t('上下文 {n}', { n: windowText(p.window) }), p.images ? t('看图') : t('不看图'), thinking].join(' · ')
  }
  return (
    <div className="space-y-4">
      {listError ? (
        <p className="text-xs text-muted-foreground">{t('没读到模型列表（{err}）。在下面手填模型 id，保存前会用第一个模型发一句话，测一下 key 和地址能不能用。', { err: listError })}</p>
      ) : (
        <p className="text-xs text-muted-foreground">{all.length ? t('勾选要用的模型（{n} 个可选），之后还能再加。', { n: all.length }) : t('这个服务商的模型都加过了；要加列表里没有的，在下面手填。')}</p>
      )}
      {all.length > 8 && <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('搜索模型')} aria-label={t('搜索模型')} />}
      {all.length > 0 && (
        <div className="max-h-[28rem] overflow-y-auto rounded-lg border border-border">
          {shown.map((id) => {
            const p = params[id]
            const on = picked.includes(id)
            return (
              <div key={id} className="border-b border-border last:border-b-0">
                <div className="flex items-center gap-2.5 px-3 py-2 hover:bg-accent/50">
                  <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2.5">
                    <input type="checkbox" checked={on} onChange={() => flip(id)} className="size-4 shrink-0 accent-primary" />
                    <span className="min-w-0">
                      <span className="block truncate font-mono text-xs">{id}</span>
                      {on && p && <span className="block truncate text-[11px] text-muted-foreground">{summary(p)}</span>}
                    </span>
                  </label>
                  {recommended.includes(id) && <span className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 text-[10px] font-semibold text-primary-text">{t('推荐')}</span>}
                  {on && p && (
                    <Button size="xs" variant="ghost" className="shrink-0 text-muted-foreground" onClick={() => setOpen(open === id ? '' : id)} aria-expanded={open === id}>
                      {open === id ? t('收起') : t('调整')}
                    </Button>
                  )}
                  {extra.includes(id) && !ids.includes(id) && (
                    <span className="flex shrink-0 gap-1">
                      <Button size="icon-xs" variant="ghost" aria-label={t('改 {name}', { name: id })} title={t('改')} className="text-muted-foreground" onClick={() => { drop(id); setManual(id) }}>
                        <Pencil />
                      </Button>
                      <Button size="icon-xs" variant="ghost" aria-label={t('删除 {label}', { label: id })} title={t('删除')} className="text-muted-foreground" onClick={() => drop(id)}>
                        <Trash2 />
                      </Button>
                    </span>
                  )}
                </div>
                {on && p && open === id && (
                  <div className="space-y-3 border-t border-border bg-muted/30 px-3 py-3">
                    <Field label={t('上下文窗口')} hint={t('单位 token，可不填，默认 512k。')} htmlFor={`w-${id}`}>
                      <Input id={`w-${id}`} value={p.window} onChange={(e) => set(id, { window: e.target.value.replace(/[^0-9]/g, '') })} placeholder="512000" inputMode="numeric" className="tabular-nums" />
                    </Field>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <label className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2">
                        <span className="text-sm">{t('支持看图')}</span>
                        <Switch checked={p.images} onChange={(v) => set(id, { images: v })} label={t('支持看图')} />
                      </label>
                      <label className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2">
                        <span className="text-sm">{t('支持思考')}</span>
                        <Switch checked={p.reasoning} onChange={(v) => set(id, { reasoning: v })} label={t('支持思考')} />
                      </label>
                    </div>
                    {p.reasoning && <ThinkingConfig custom={p.custom} onChange={(c) => set(id, { custom: c })} source={p.source} levels={p.levels} />}
                  </div>
                )}
              </div>
            )
          })}
          {!shown.length && <p className="px-3 py-2 text-xs text-muted-foreground">{t('没有匹配的模型')}</p>}
        </div>
      )}
      <div className="space-y-1.5">
        <div className="flex gap-2">
          <Input
            value={manual}
            onChange={(e) => {
              setManual(e.target.value)
              setManualErr('')
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                addManual()
              }
            }}
            placeholder={t('其他模型 id，比如 deepseek-chat')}
            className="font-mono"
            aria-label={t('模型 id')}
            aria-invalid={!!manualErr}
          />
          <Button variant="outline" onClick={addManual} disabled={!manual.trim()}>
            <Plus /> {t('添加')}
          </Button>
        </div>
        {manualErr && <p className="text-xs text-destructive">{manualErr}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => onSave(picked, params)} disabled={busy || !picked.length}>
          {busy ? <Loader2 className="animate-spin" /> : <Check />} {picked.length ? t('保存（{n} 个模型）', { n: picked.length }) : t('保存')}
        </Button>
        {onBack && (
          <Button size="sm" variant="outline" onClick={onBack} disabled={busy}>
            {t('上一步')}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>
          {t('取消')}
        </Button>
      </div>
    </div>
  )
}

/** 添加服务商：填地址和 key → 拉模型列表（顺便验证 key）→ 勾模型，服务商和模型一次存好 */
function AddProvider({ preset, activeReady, onSaved, onCancel }: { preset?: Preset; activeReady: boolean; onSaved: () => void; onCancel: () => void }) {
  const form = useProviderForm(undefined, preset)
  const [step, setStep] = useState<'form' | 'pick'>('form')
  const [ids, setIds] = useState<string[]>([])
  const [listError, setListError] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const next = async () => {
    setBusy(true)
    setResult(null)
    try {
      const r = await listModels(form.input())
      setIds(r.models)
      setListError(r.unsupported ?? '')
      setStep('pick')
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message }) // key 不对、连不上：停在这一步改
    } finally {
      setBusy(false)
    }
  }
  const save = async (picked: string[], params: Record<string, ModelParams>) => {
    setBusy(true)
    setResult(null)
    try {
      // 没拉到列表就没验证过 key：先用第一个模型真发一句，不通不存
      if (listError)
        await post('settings/llm/test', { model: { model: picked[0], images: true, reasoning: false }, provider: form.input() }).catch((e: Error) => {
          throw new Error(t('用 {model} 测试没通过：{err}', { model: picked[0], err: e.message }))
        })
      const p = (await (await post('settings/llm/providers', form.input())).json()) as ProviderConfig
      await saveModels(p.id, picked, params, activeReady)
      onSaved()
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-5">
      <div className="text-sm font-medium">{step === 'form' ? t('添加服务商') : t('选模型')}</div>
      {step === 'form' ? (
        <>
          {form.fields}
          <Result r={result} />
          <div className="flex gap-2">
            <Button size="sm" onClick={next} disabled={busy}>
              {busy && <Loader2 className="animate-spin" />} {t('下一步：选模型')}
            </Button>
            <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>
              {t('取消')}
            </Button>
          </div>
        </>
      ) : (
        <>
          <ModelPicker ids={ids} listError={listError} recommended={(presetOf(form.baseURL)?.models ?? []).map((m) => m.id)} api={form.input().api} baseURL={form.baseURL} busy={busy} onSave={save} onBack={() => setStep('form')} onCancel={onCancel} />
          <Result r={result} />
        </>
      )}
    </div>
  )
}

/** 给已有的服务商加模型：用存着的 key 拉列表 */
function AddModels({ provider, exclude, activeReady, onSaved, onCancel }: { provider: ProviderConfig; exclude: string[]; activeReady: boolean; onSaved: () => void; onCancel: () => void }) {
  const [ids, setIds] = useState<string[] | null>(null)
  const [listError, setListError] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  useEffect(() => {
    listModels({ id: provider.id, name: provider.name, api: provider.api, base_url: provider.base_url, api_key: '', api_key_env: '' })
      .then((r) => {
        setIds(r.models)
        setListError(r.unsupported ?? '')
      })
      .catch((e: Error) => {
        setListError(e.message)
        setIds([])
      })
  }, [provider])
  const save = async (picked: string[], params: Record<string, ModelParams>) => {
    setBusy(true)
    setResult(null)
    try {
      await saveModels(provider.id, picked, params, activeReady)
      onSaved()
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-4">
      <div className="text-sm font-medium">{t('给「{name}」加模型', { name: provider.name })}</div>
      {ids === null ? (
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" /> {t('正在读取模型列表…')}
        </p>
      ) : (
        <ModelPicker ids={ids} listError={listError} recommended={(presetOf(provider.base_url)?.models ?? []).map((m) => m.id)} api={provider.api} baseURL={provider.base_url} exclude={exclude} busy={busy} onSave={save} onCancel={onCancel} />
      )}
      <Result r={result} />
    </div>
  )
}

const ALL_LEVELS: Thinking[] = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

/** 思考档位：默认按模型资料（查不到按通用的 关 / 低 / 中 / 高）；可以自己设置，勾支持哪些档、每档发给接口什么值 */
function ThinkingConfig({ custom, onChange, source, levels }: {
  custom: Partial<Record<Thinking, string>> | null
  onChange: (c: Partial<Record<Thinking, string>> | null) => void
  source?: ModelConfig['thinking_source']
  levels?: Thinking[]
}) {
  const start = () => onChange(Object.fromEntries((levels?.length ? levels : (['off', 'low', 'medium', 'high'] as Thinking[])).map((l) => [l, ''])))
  const flip = (l: Thinking) => {
    const next = { ...custom }
    if (l in next) delete next[l]
    else next[l] = ''
    onChange(next)
  }
  return (
    <div className="space-y-3 rounded-lg border border-border px-3 py-2.5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <span>
          <span className="block text-sm">{t('思考档位')}</span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            {custom
              ? t('勾上这个模型支持的档；值不填就发档名（low、high…），接口要别的写法就填上。')
              : source === 'catalog' && levels
                ? t('按模型资料：{levels}。', { levels: levelNames(levels) })
                : t('没查到这个模型的资料，按通用的 关 / 低 / 中 / 高 发。档位不对的话自己设置。')}
          </span>
        </span>
        <Button size="sm" variant="outline" onClick={() => (custom ? onChange(null) : start())}>
          {custom ? t('恢复默认') : t('自己设置')}
        </Button>
      </div>
      {custom && (
        <div className="space-y-1.5">
          {ALL_LEVELS.map((l) => (
            <div key={l} className="flex items-center gap-3">
              <label className="flex w-24 shrink-0 cursor-pointer items-center gap-2 text-sm">
                <input type="checkbox" checked={l in custom} onChange={() => flip(l)} className="size-4 accent-primary" />
                {t(THINKING_LABEL[l])}
              </label>
              {l in custom && (
                <Input
                  value={custom[l] ?? ''}
                  onChange={(e) => onChange({ ...custom, [l]: e.target.value })}
                  placeholder={l}
                  className="h-8 font-mono text-xs"
                  aria-label={t('「{level}」发的值', { level: t(THINKING_LABEL[l]) })}
                />
              )}
            </div>
          ))}
          {!Object.keys(custom).length && <p className="text-xs text-destructive">{t('至少勾一档；要关掉思考，把上面的「支持思考」关掉。')}</p>}
        </div>
      )}
    </div>
  )
}

/** 编辑模型：服务商、模型 id、参数；可以先测试连接再保存 */
function ModelEditor({ initial, providers, onSaved, onCancel }: { initial?: ModelConfig; providers: ProviderConfig[]; onSaved: () => void; onCancel: () => void }) {
  const firstOwn = providers.find((p) => !p.builtin)?.id ?? 'creght'
  const [provider, setProvider] = useState(initial?.provider || firstOwn)
  const [name, setName] = useState(initial?.name ?? '')
  const [model, setModel] = useState(initial?.model ?? '')
  const [win, setWin] = useState(initial?.context_window ? String(initial.context_window) : '')
  const [images, setImages] = useState(initial?.images ?? true)
  const [reasoning, setReasoning] = useState(initial?.reasoning ?? true)
  // 自己配的思考档位：null = 不自己配（按模型资料 / 通用）；档 → 发给接口的值（空 = 发档名）
  const [custom, setCustom] = useState<Partial<Record<Thinking, string>> | null>(initial?.thinking_custom && Object.keys(initial.thinking_custom).length ? initial.thinking_custom : null)
  const [busy, setBusy] = useState<'' | 'test' | 'save'>('')
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)

  const p = providers.find((x) => x.id === provider)
  const body = () => ({ id: initial?.id ?? '', name, provider, model, context_window: Number(win) || 0, images, reasoning, thinking_levels: reasoning && custom ? custom : {} })
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
      {reasoning && (
        <ThinkingConfig
          custom={custom}
          onChange={setCustom}
          source={initial?.model === model ? initial?.thinking_source : undefined}
          levels={initial?.model === model ? initial?.thinking_levels : undefined}
        />
      )}
      <Result r={result} />
      <div className="flex gap-2">
        <Button size="sm" onClick={() => run('save')} disabled={!!busy}>
          {busy === 'save' ? <Loader2 className="animate-spin" /> : <Check />} {t('保存')}
        </Button>
        <Button size="sm" variant="outline" onClick={() => run('test')} disabled={!!busy}>
          {busy === 'test' && <Loader2 className="animate-spin" />} {t('测试连接')}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={!!busy}>
          {t('取消')}
        </Button>
      </div>
    </div>
  )
}
