import { useEffect, useState } from 'react'
import { ArrowUpRight, Check, GitBranch, HardDrive, Loader, Pencil, Plus, Trash2, X } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

export type Backend = { project_id: string; site_id: string; name: string; created_at: string; local: boolean; current: boolean; template_base?: number; template_base_label?: string; template?: string; template_name?: string; template_latest?: number; offline?: boolean }
type Package = { id: number; name: string; price_by_month: number; price_by_year: number; currency: string }
/** paid / entitled：付费模板、当前账号有没有权限 */
type Template = { key: string; id: string; name: string; description: string; preview_url: string; source?: string; removable?: boolean; version?: number; paid?: boolean; entitled?: boolean; packages?: Package[]; subscribe_url?: string }

/** 套餐的最低价：¥69/月 */
function priceText(p: Package) {
  const sym = p.currency === 'USD' ? '$' : '¥'
  return p.price_by_month ? t('{p}/月', { p: sym + p.price_by_month / 100 }) : t('{p}/年', { p: sym + p.price_by_year / 100 })
}
export type SetupState = { ready: boolean; step: string; error: string; logged_in: boolean; offline_mode?: boolean; api_host: string; dir: string; backends: Backend[]; templates: Template[] }

/** 项目基于模板哪个版本、能不能升级（升级在当前项目的模板卡片里点） */
function TemplateTag({ b }: { b: Backend }) {
  const latest = b.template_latest
  const base = (b.template_base_label || String(b.template_base ?? '')).replace(/^v/, '')
  if (!b.local) return b.template_name ? <span>{b.template_name}</span> : null
  if (!b.template_base) return <span>{b.template_name ? `${b.template_name} · ` : ''}{t('模板版本未记录')}{latest ? ` · ${t('可升级到 v{v}', { v: latest })}` : ''}</span>
  if (latest && latest > b.template_base) return <span className="font-medium text-primary-text">{b.template_name ? `${b.template_name} ` : ''}{t('模板 v{v}', { v: base })} · {t('可升级到 v{v}', { v: latest })}</span>
  return <span>{b.template_name ? `${b.template_name} ` : ''}{t('模板 v{v}', { v: base })} · {t('最新')}</span>
}

const fmtDate = (s: string) => {
  const d = new Date(s)
  return isNaN(d.getTime()) ? '' : `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${t('创建')}`
}

/** 项目：账号下已有的（切过去 / 改名）或者从模板新建。初始化页和设置页共用。 */
// inline：新建项目直接在当前页面里填，不弹窗（初始化页面用）；设置里的项目列表仍是弹窗
export default function BackendPicker({ onDone, inline = false }: { onDone: () => void; inline?: boolean }) {
  const [st, setSt] = useState<SetupState | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [newName, setNewName] = useState('')
  const [tplKey, setTplKey] = useState('')
  const [adding, setAdding] = useState(false)
  const [source, setSource] = useState('')
  const [addErr, setAddErr] = useState('')
  // 新项目的数据存在哪：creght 账号（在线）还是这台电脑（离线）。没登录只能离线
  const [storage, setStorage] = useState<'online' | 'offline'>('online')
  const [editing, setEditing] = useState<{ id: string; name: string } | null>(null)
  // 删除要把项目名完整输入一遍
  const [deleting, setDeleting] = useState<{ id: string; typed: string } | null>(null)
  // 新建项目在弹窗里填
  const [creating, setCreating] = useState(false)

  const load = () => getJSON<SetupState>('setup').then(setSt)
  useEffect(() => {
    load()
  }, [])
  useEffect(() => {
    if (!busy) return
    const t = setInterval(load, 1500)
    return () => clearInterval(t)
  }, [busy])

  const rename = async () => {
    if (!editing) return
    const name = editing.name.trim()
    if (!name) return
    setBusy('rename:' + editing.id)
    setError('')
    try {
      const r = await post('setup/rename', { project_id: editing.id, name })
      setSt(await r.json())
      setEditing(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const remove = async (b: Backend) => {
    setBusy('delete:' + b.project_id)
    setError('')
    try {
      const r = await post('setup/delete', { project_id: b.project_id, name: deleting?.typed ?? '' })
      setSt(await r.json())
      setDeleting(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const run = async (body: { action: 'new' | 'use'; project_id?: string; name?: string; template?: string; offline?: boolean }, key: string) => {
    setBusy(key)
    setError('')
    try {
      await post('setup', body)
      await load()
      onDone()
    } catch (e) {
      setError((e as Error).message)
      load()
    } finally {
      setBusy('')
    }
  }

  if (!st)
    return (
      <div className="flex justify-center py-6">
        <Loader className="size-4 animate-spin text-muted-foreground" />
      </div>
    )

  const offlineNew = !st.logged_in || storage === 'offline'
  const templates = st.templates
  const tpl = templates.find((x) => x.key === tplKey) ?? templates[0]
  const stepText = offlineNew ? t('正在新建…') : st.step === 'pulling' ? t('正在拉取到本地…') : t('正在复制模板…')
  // 新建项目的表单：弹窗里和 inline 共用
  const createBody = (
    <>
            {!tpl && <p className="text-xs text-muted-foreground">{offlineNew ? t('没有可用的模板。') : t('这个 creght 区域还没有模板，新建项目要先换到 creght.cn（设置 → creght 区域）。')}</p>}
            {tpl && (
            <>
            {st.logged_in && (
              <div className="mb-3 space-y-1.5">
                <div className="text-[13px] font-bold">{t('数据存在哪')}</div>
                <div className="grid gap-2 sm:grid-cols-2">
                  {(
                    [
                      ['online', t('creght 账号（在线）'), t('手机上也能打开后台，换电脑数据还在；表单询盘自动进来。')],
                      ['offline', t('这台电脑（离线）'), t('数据只在本机，不占 creght 空间；以后能转成在线的。')],
                    ] as const
                  ).map(([v, label, desc]) => (
                    <button
                      key={v}
                      type="button"
                      onClick={() => setStorage(v)}
                      aria-pressed={storage === v}
                      className={cn(
                        'flex cursor-pointer items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                        storage === v ? 'border-primary/50 bg-primary/5' : 'border-border hover:bg-accent',
                      )}
                    >
                      <span className={cn('mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border', storage === v ? 'border-primary bg-primary text-primary-foreground' : 'border-border')}>
                        {storage === v && <Check className="size-3" strokeWidth={3} />}
                      </span>
                      <span className="min-w-0">
                        <span className="block text-[13px] font-semibold">{label}</span>
                        <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{desc}</span>
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )}
            {!st.logged_in && <p className="mb-3 text-xs leading-relaxed text-muted-foreground">{t('新项目的数据都存在这台电脑上。')}</p>}
            <div className="text-[13px] font-bold">{t('选一个模板')}</div>
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              {templates.map((x) => {
                const on = x.key === tpl.key
                return (
                  <button
                    key={x.key}
                    type="button"
                    onClick={() => setTplKey(x.key)}
                    aria-pressed={on}
                    className={cn(
                      'flex cursor-pointer items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                      on ? 'border-primary/50 bg-primary/5' : 'border-border hover:bg-accent',
                    )}
                  >
                    <span className={cn('mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border', on ? 'border-primary bg-primary text-primary-foreground' : 'border-border')}>
                      {on && <Check className="size-3" strokeWidth={3} />}
                    </span>
                    <span className="min-w-0">
                      <span className="flex flex-wrap items-center gap-1.5 text-[13px] font-semibold">
                        {x.name}
                        {x.source && <span className="max-w-full truncate rounded-md bg-muted px-1.5 py-0.5 font-mono text-[10px] font-medium text-muted-foreground">{x.source}</span>}
                        {x.paid && <span className="rounded-md bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:text-amber-400">{x.entitled ? t('付费 · 已开通') : t('付费')}</span>}
                      </span>
                      <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{x.description}</span>
                    </span>
                  </button>
                )
              })}
              <button
                type="button"
                onClick={() => {
                  setAdding((v) => !v)
                  setAddErr('')
                }}
                aria-expanded={adding}
                className="flex cursor-pointer items-center gap-2.5 rounded-xl border border-dashed border-border px-3 py-2.5 text-left text-[13px] font-semibold text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                <Plus className="size-4 shrink-0" />
                {t('添加 git 模板')}
              </button>
            </div>
            {adding && (
              <form
                className="mt-2 space-y-2 rounded-xl border border-border bg-muted/40 p-3"
                onSubmit={async (e) => {
                  e.preventDefault()
                  setBusy('source')
                  setAddErr('')
                  try {
                    const r = (await (await post('settings/templates', { source })).json()) as { template: Template }
                    await load()
                    setTplKey(r.template.key)
                    setSource('')
                    setAdding(false)
                  } catch (err) {
                    setAddErr((err as Error).message)
                  } finally {
                    setBusy('')
                  }
                }}
              >
                <div className="flex items-center gap-2">
                  <GitBranch className="size-4 shrink-0 text-muted-foreground" />
                  <Input
                    id="template-source"
                    value={source}
                    onChange={(e) => setSource(e.target.value)}
                    placeholder="https://github.com/you/templates#my-template"
                    aria-label={t('git 模板的地址')}
                    className="h-8 flex-1 font-mono text-[12px]"
                  />
                  <Button type="submit" size="sm" disabled={busy === 'source' || !source.trim()}>
                    {busy === 'source' && <Loader className="animate-spin" />}
                    {busy === 'source' ? t('正在读取…') : t('添加')}
                  </Button>
                </div>
                <p className="text-[11px] leading-relaxed text-muted-foreground">
                  {t('仓库地址，模板在子目录里就在后面加 #目录名。版本是仓库里 v1.0.0 这样的 tag，每个 tag 上要是完整的模板；之后打新 tag，从它建的项目就能升级。私有仓库用这台电脑上 git 的登录。')}
                </p>
                {addErr && <p className="text-xs break-all text-destructive">{addErr}</p>}
              </form>
            )}
            {tpl.paid && !tpl.entitled ? (
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {t('「{name}」是付费模板，开通下面的套餐后能用：', { name: tpl.name })}
                  {(tpl.packages ?? []).map((p) => `${p.name}（${priceText(p)}）`).join(t('、'))}
                </p>
                {tpl.subscribe_url && (
                  <a href={tpl.subscribe_url} target="_blank" rel="noreferrer" className="inline-flex h-8 items-center gap-1 rounded-md bg-primary px-3 text-xs font-semibold text-primary-foreground">
                    {t('去开通')} <ArrowUpRight className="size-[13px]" />
                  </a>
                )}
                <Button type="button" size="sm" variant="outline" onClick={() => getJSON<SetupState>('setup?refresh=1').then(setSt)}>
                  {t('开通好了，刷新')}
                </Button>
              </div>
            ) : (
            <form
              className="mt-3 flex items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                run({ action: 'new', template: tpl.key, name: newName.trim(), offline: offlineNew }, 'new')
              }}
            >
              <Input
                autoFocus
                value={newName}
                maxLength={40}
                onChange={(e) => setNewName(e.target.value)}
                placeholder={t('起个名字，比如品牌名或业务线')}
                aria-label={t('新项目的名字')}
                className="h-8 flex-1 text-[13px]"
              />
              <Button type="submit" size="sm" disabled={!!busy || !newName.trim()} className={cn(busy === 'new' && 'min-w-28')}>
                {busy === 'new' && <Loader className="animate-spin" />}
                {busy === 'new' ? stepText : t('新建')}
              </Button>
            </form>
            )}
{tpl.removable && (
              <button
                type="button"
                onClick={async () => {
                  await post('settings/templates?key=' + encodeURIComponent(tpl.key), undefined, 'DELETE')
                  setTplKey('')
                  load()
                }}
                className="mt-1 mr-3 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-destructive"
              >
                <Trash2 className="size-[13px]" />
                {t('从列表里移除「{name}」', { name: tpl.name })}
              </button>
            )}
            {tpl.preview_url && (
            <a href={tpl.preview_url} target="_blank" rel="noreferrer" className="mt-1 inline-flex items-center gap-1 text-xs font-bold hover:text-primary-text">
              {t('预览「{name}」模板', { name: tpl.name })} <ArrowUpRight className="size-[13px]" />
            </a>
            )}
            </>
            )}
    </>
  )
  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <div className="text-xs font-semibold text-muted-foreground">{inline && st.backends.length === 0 ? t('新建项目') : t('你的项目')}</div>
          {st.backends.length > 0 && (
            <Button size="sm" variant="outline" onClick={() => setCreating(true)} className="gap-1">
              <Plus className="size-3.5" />
              {t('新建项目')}
            </Button>
          )}
        </div>
        {st.backends.length === 0 && inline ? (
          <div className="rounded-xl border border-border bg-background p-5">{createBody}</div>
        ) : st.backends.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-border bg-background px-4 py-8 text-center">
            <p className="text-xs text-muted-foreground">{t('还没有项目，从模板新建一个。')}</p>
            <Button size="sm" onClick={() => setCreating(true)} className="gap-1">
              <Plus className="size-3.5" />
              {t('新建项目')}
            </Button>
          </div>
        ) : (
          <div className="overflow-hidden rounded-xl border border-border bg-background">
            {st.backends.map((b) => (
              <div key={b.project_id} className="border-b border-border last:border-b-0">
              <div className="flex items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  {editing?.id === b.project_id ? (
                    <form
                      className="flex items-center gap-1.5"
                      onSubmit={(e) => {
                        e.preventDefault()
                        rename()
                      }}
                    >
                      <Input
                        autoFocus
                        value={editing.name}
                        maxLength={40}
                        onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                        onKeyDown={(e) => e.key === 'Escape' && setEditing(null)}
                        aria-label={t('项目名字')}
                        className="h-8 max-w-64 text-[13px]"
                      />
                      <Button type="submit" size="sm" disabled={!editing.name.trim() || busy === 'rename:' + b.project_id}>
                        {busy === 'rename:' + b.project_id && <Loader className="animate-spin" />}
                        {t('保存')}
                      </Button>
                      <Button type="button" variant="ghost" size="icon-sm" onClick={() => setEditing(null)} aria-label={t('取消改名')}>
                        <X />
                      </Button>
                    </form>
                  ) : (
                    <div className="group flex items-center gap-2">
                      <span className="truncate text-[13px] font-bold">{b.name}</span>
                      <button
                        type="button"
                        onClick={() => setEditing({ id: b.project_id, name: b.name })}
                        aria-label={t('给 {name} 改名', { name: b.name })}
                        title={t('改名')}
                        className="rounded p-0.5 text-muted-foreground opacity-0 outline-none group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100 focus-visible:ring-[3px] focus-visible:ring-ring/50"
                      >
                        <Pencil className="size-3" />
                      </button>
                      {b.current && <span className="rounded-md border border-primary/10 bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary-text">{t('正在使用')}</span>}
                    </div>
                  )}
                  <div className="mt-0.5 flex items-center gap-2 text-[11px] text-muted-foreground">
                    {b.offline ? (
                      <span className="rounded bg-muted px-1.5 py-px font-semibold" title={t('离线项目：数据只在这台电脑上')}>
                        {t('离线')}
                      </span>
                    ) : (
                      <span className="rounded bg-muted px-1.5 py-px font-semibold" title={t('在线项目：数据在 creght 上（{id}）', { id: b.project_id })}>
                        Creght
                      </span>
                    )}
                    <span>{fmtDate(b.created_at)}</span>
                    {b.local && !b.offline && (
                      <span className="inline-flex items-center gap-1">
                        <HardDrive className="size-3" /> {t('本机已有')}
                      </span>
                    )}
                    <TemplateTag b={b} />
                  </div>
                </div>
                {b.current ? (
                  <Check className="size-4 text-primary-text" />
                ) : (
                  <>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      disabled={!!busy}
                      onClick={() => setDeleting(deleting?.id === b.project_id ? null : { id: b.project_id, typed: '' })}
                      aria-label={t('删除 {label}', { label: b.name })}
                      title={t('删除项目')}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 />
                    </Button>
                    <Button variant="outline" size="sm" disabled={!!busy} onClick={() => run({ action: 'use', project_id: b.project_id }, b.project_id)}>
                      {busy === b.project_id && <Loader className="animate-spin" />}
                      {busy === b.project_id ? (st.step === 'pulling' ? t('拉取中…') : t('准备中…')) : t('切换')}
                    </Button>
                  </>
                )}
              </div>
              {deleting?.id === b.project_id && (
                <form
                  className="space-y-2 border-t border-border bg-destructive/5 px-4 py-3"
                  onSubmit={(e) => {
                    e.preventDefault()
                    remove(b)
                  }}
                >
                  <p className="text-xs leading-relaxed text-muted-foreground">
                    {b.offline ? t('离线项目只在这台电脑上：项目文件、业务数据和对话历史会挪到') : t('会删除 creght 上的这个项目：后台页面、业务数据、CMS 都没法恢复。本机的副本和对话历史会挪到')} <code className="font-mono">~/.annulo/trash</code>{t('。输入项目名「')}
                    <span className="font-semibold text-foreground">{b.name}</span>{t('」确认：')}
                  </p>
                  <div className="flex items-center gap-1.5">
                    <Input
                      autoFocus
                      value={deleting.typed}
                      onChange={(e) => setDeleting({ ...deleting, typed: e.target.value })}
                      onKeyDown={(e) => e.key === 'Escape' && setDeleting(null)}
                      aria-label={t('输入项目名确认删除')}
                      className="h-8 max-w-64 text-[13px]"
                    />
                    <Button type="submit" variant="destructive" size="sm" disabled={deleting.typed.trim() !== b.name || busy === 'delete:' + b.project_id}>
                      {busy === 'delete:' + b.project_id && <Loader className="animate-spin" />}
                      {t('删除')}
                    </Button>
                    <Button type="button" variant="ghost" size="sm" onClick={() => setDeleting(null)}>
                      {t('取消')}
                    </Button>
                  </div>
                </form>
              )}
              </div>
            ))}
          </div>
        )}
      </div>

      {creating && inline && st.backends.length > 0 && (
        <div className="overflow-hidden rounded-xl border border-border bg-background">
          <div className="flex h-11 items-center justify-between border-b border-border px-4">
            <span className="text-[13px] font-bold">{t('新建项目')}</span>
            <Button variant="ghost" size="icon-sm" disabled={busy === 'new'} onClick={() => setCreating(false)} aria-label={t('关闭')} className="rounded-lg text-muted-foreground hover:text-foreground">
              <X className="size-4" />
            </Button>
          </div>
          <div className="p-5">{createBody}</div>
        </div>
      )}

      {creating && !inline && (
        <div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px]"
          onMouseDown={(e) => e.target === e.currentTarget && busy !== 'new' && setCreating(false)}
        >
          {/* Esc 只关这个弹窗，不往外传（外面的设置弹窗也听 Esc） */}
          <div
            role="dialog"
            aria-modal="true"
            aria-label={t('新建项目')}
            onKeyDown={(e) => {
              if (e.key !== 'Escape') return
              e.stopPropagation()
              if (busy !== 'new') setCreating(false)
            }}
            className="flex max-h-[calc(100dvh-2rem)] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-2xl"
          >
            <div className="flex h-12 shrink-0 items-center justify-between border-b border-border px-4">
              <span className="text-sm font-bold">{t('新建项目')}</span>
              <Button variant="ghost" size="icon-sm" disabled={busy === 'new'} onClick={() => setCreating(false)} aria-label={t('关闭')} className="rounded-lg text-muted-foreground hover:text-foreground">
                <X className="size-[18px]" />
              </Button>
            </div>
          <div className="overflow-y-auto p-5">
            {createBody}
            {error && <div className="mt-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm break-all text-destructive">{error}</div>}
          </div>
          </div>
        </div>
      )}

      {(error || st.error) && <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm break-all text-destructive">{error || st.error}</div>}
    </div>
  )
}
