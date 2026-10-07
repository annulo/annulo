import { useEffect, useState } from 'react'
import { ArrowUpCircle, Check, Download, Loader, Puzzle, Trash2 } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { HOW, handOff, pushText } from '@/components/TemplateCard'
import { t } from '@/lib/i18n'

// 插件（docs/plugins.md）：装进项目 plugins/<id>/ 的一包文件，有自己的版本，和模板一样三方合并升级。

type Version = { no: number; label?: string; note: string; created_at: string }
type Plugin = {
  id: string
  source: string
  name: string
  description?: string
  base?: number
  base_label?: string
  latest?: number
  latest_label?: string
  updates?: Version[]
  requires?: number
  installed: boolean
  error?: string
}
type PluginsState = {
  api: number
  installed: Plugin[]
  missing: Plugin[]
  available: Plugin[]
  merging?: boolean
  conflicts?: string[] | null
}
type UpgradeResult = {
  status: 'up_to_date' | 'merged' | 'conflict'
  from: number
  to: number
  files?: string[]
  conflicts?: string[]
}
type MergeOut = {
  id: string
  result: UpgradeResult
  push_error?: string
  push_conflicts?: string[]
  record_error?: string
}

const ver = (no?: number, label?: string) => (label || String(no ?? '')).replace(/^v/, '')

const conflictText = (id: string, files: string[]) =>
  t('插件 {id} 合并时有冲突，这些文件里有冲突标记：{files}。看 git log plugin/{id} 和 git diff 弄清插件新版和这个项目各改了什么，逐个解决。', { id, files: files.join(', ') }) + HOW()

export default function PluginCard() {
  const [st, setSt] = useState<PluginsState | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  // 正在装、升级、卸载的插件（id 或来源）
  const [busy, setBusy] = useState('')
  const [removing, setRemoving] = useState('')
  const [source, setSource] = useState('')

  const load = () =>
    getJSON<PluginsState>('plugins')
      .then(setSt)
      .catch((e) => setError((e as Error).message))
  useEffect(() => {
    load()
  }, [])

  const merge = async (key: string, path: string, body: object) => {
    setBusy(key)
    setError('')
    setNotice('')
    try {
      const r = await post(path, body)
      const out = (await r.json()) as MergeOut
      if (out.result.status === 'conflict') {
        handOff(conflictText(out.id, out.result.conflicts ?? []), t('解决插件合并的冲突'))
        setNotice(
          t('有 {n} 个文件冲突，已交给助手解决。', {
            n: out.result.conflicts?.length ?? 0,
          }),
        )
      } else if (out.result.status === 'merged') {
        setNotice(
          t('插件 {id} 已合并到 v{v}。', {
            id: out.id,
            v: VersionLabel(out.result.to),
          }),
        )
        window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      }
      if (out.push_conflicts?.length) handOff(pushText(out.push_conflicts), t('解决推送的冲突'))
      else if (out.push_error) setError(t('已合并到本机，但推到预览失败：') + out.push_error)
      if (out.record_error) setError(t('插件装好了，但没记进 user/annulo.json：') + out.record_error)
      if (path === 'plugins/install') setSource('')
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const remove = async (id: string) => {
    setBusy(id)
    setError('')
    setNotice('')
    try {
      const r = await post('plugins/remove', { id })
      const out = (await r.json()) as {
        push_error?: string
        push_conflicts?: string[]
      }
      setNotice(t('已卸载插件 {id}。表里的数据和你的定制（user/plugins/{id}/）还在，重新装上就能接着用。', { id }))
      if (out.push_conflicts?.length) handOff(pushText(out.push_conflicts), t('解决推送的冲突'))
      else if (out.push_error) setError(t('已卸载，但推到预览失败：') + out.push_error)
      window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy('')
      setRemoving('')
    }
  }

  if (!st) return error ? <div className="text-xs text-destructive">{error}</div> : null
  const merging = !!(st.merging && (st.conflicts?.length ?? 0) > 0)
  const locked = merging || !!busy
  const tooOld = (p: Plugin) => !!(p.requires && p.requires > st.api)

  const installButton = (p: Plugin) =>
    tooOld(p) ? (
      <span className="text-xs font-medium text-amber-600 dark:text-amber-400">{t('先更新 Annulo 才能装')}</span>
    ) : p.error ? (
      <span className="max-w-56 text-right text-xs text-muted-foreground">{p.error}</span>
    ) : (
      <Button size="sm" variant="outline" disabled={locked} onClick={() => merge(p.source, 'plugins/install', { source: p.source })}>
        {busy === p.source ? <Loader className="animate-spin" /> : <Download />}
        {busy === p.source ? t('安装中…') : t('安装')}
      </Button>
    )

  return (
    <div className="space-y-3 rounded-xl border border-border bg-background px-4 py-3">
      <div>
        <div className="text-[13px] font-bold">{t('插件')}</div>
        <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
          {t('插件是装进这个项目的一组功能（比如社媒发布），和模板分开升级，任何模板的项目都能装。装在 plugins/ 下，助手能直接改。')}
        </div>
      </div>

      {st.installed.map((p) => {
        const updates = p.updates ?? []
        return (
          <Row key={p.id} p={p} sub={p.base ? t('v{v}', { v: ver(p.base, p.base_label) }) + (p.latest ? ` · ${t('最新 v{v}', { v: ver(p.latest, p.latest_label) })}` : '') : p.source}>
            {removing === p.id ? (
              <>
                <Button size="sm" variant="destructive" disabled={locked} onClick={() => remove(p.id)}>
                  {busy === p.id ? <Loader className="animate-spin" /> : <Trash2 />} {t('确认卸载')}
                </Button>
                <Button size="sm" variant="ghost" disabled={locked} onClick={() => setRemoving('')}>
                  {t('取消')}
                </Button>
              </>
            ) : (
              <>
                {p.error ? (
                  <span className="max-w-56 text-right text-xs text-muted-foreground">{p.error}</span>
                ) : updates.length > 0 && tooOld(p) ? (
                  <span className="text-xs font-medium text-amber-600 dark:text-amber-400">{t('先更新 Annulo 才能升级')}</span>
                ) : updates.length > 0 ? (
                  <Button size="sm" disabled={locked} onClick={() => merge(p.id, 'plugins/upgrade', { id: p.id })}>
                    {busy === p.id ? <Loader className="animate-spin" /> : <ArrowUpCircle />}
                    {busy === p.id
                      ? t('升级中…')
                      : t('升级到 v{v}', {
                          v: ver(updates[0].no, updates[0].label),
                        })}
                  </Button>
                ) : p.source ? (
                  <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                    <Check className="size-3.5" /> {t('已是最新')}
                  </span>
                ) : null}
                <Button size="icon-sm" variant="ghost" disabled={locked} onClick={() => setRemoving(p.id)} title={t('卸载')}>
                  <Trash2 />
                </Button>
              </>
            )}
            {updates.length > 0 && (
              <ul className="w-full space-y-1 pt-1 text-xs text-muted-foreground">
                {updates.slice(0, 5).map((v) => (
                  <li key={v.no}>
                    <span className="font-mono text-foreground">v{ver(v.no, v.label)}</span> {v.note || t('（没有说明）')}
                  </li>
                ))}
              </ul>
            )}
          </Row>
        )
      })}

      {st.missing.map((p) => (
        <Row key={p.id} p={p} sub={t('这个项目的模板要用它，还没装')} warn>
          {installButton(p)}
        </Row>
      ))}

      {st.available.length > 0 && (
        <div className="space-y-2 border-t border-border pt-3">
          <div className="text-xs text-muted-foreground">{t('可以装的插件')}</div>
          {st.available.map((p) => (
            <Row key={p.id} p={p} sub={p.latest ? t('最新 v{v}', { v: ver(p.latest, p.latest_label) }) : p.source}>
              {installButton(p)}
            </Row>
          ))}
        </div>
      )}

      <div className="space-y-2 border-t border-border pt-3">
        <label htmlFor="plugin-source" className="block text-xs text-muted-foreground">
          {t('从 git 安装')}
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <Input id="plugin-source" value={source} onChange={(e) => setSource(e.target.value)} placeholder="https://github.com/annulo/plugins#social" className="h-8 min-w-0 flex-1 font-mono text-xs" disabled={locked} />
          <Button size="sm" variant="outline" disabled={locked || !source.trim()} onClick={() => merge(source.trim(), 'plugins/install', { source: source.trim() })}>
            {busy === source.trim() && busy ? <Loader className="animate-spin" /> : <Download />} {t('安装')}
          </Button>
        </div>
        <p className="text-[11px] text-muted-foreground">{t('填 <仓库>#<目录>：仓库地址，# 后面是插件所在的目录。')}</p>
      </div>
      {merging && <div className="text-xs text-amber-600 dark:text-amber-400">{t('上次合并的冲突还没解决（见上面的模板一栏），解决后才能装、升级插件。')}</div>}
      {notice && <div className="text-xs text-muted-foreground">{notice}</div>}
      {error && <div className="text-xs text-destructive">{error}</div>}
    </div>
  )
}

/** 插件版本号（semver 换算成整数存的）换回 1.2.3 */
function VersionLabel(no: number) {
  return `${Math.floor(no / 1_000_000)}.${Math.floor(no / 1_000) % 1_000}.${no % 1_000}`
}

function Row({ p, sub, warn, children }: { p: Plugin; sub: string; warn?: boolean; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <div className={`flex size-8 shrink-0 items-center justify-center rounded-lg ${warn ? 'bg-amber-500/10 text-amber-600 dark:text-amber-400' : 'bg-muted text-muted-foreground'}`}>
        <Puzzle className="size-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-semibold">
          {p.name} <span className="font-mono text-[11px] font-normal text-muted-foreground">{p.id}</span>
        </div>
        <div className="truncate text-[11px] text-muted-foreground">{p.description || sub}</div>
        {p.description && <div className="truncate text-[11px] text-muted-foreground">{sub}</div>}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2">{children}</div>
    </div>
  )
}
