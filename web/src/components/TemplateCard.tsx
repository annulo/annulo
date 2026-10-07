import { useEffect, useState } from 'react'
import { ArrowUpCircle, Check, Loader, Repeat, RotateCcw, Sparkles, TriangleAlert, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { t } from '@/lib/i18n'

/** label：git 模板的 tag（v1.2.0）；creght 模板没有，显示版本号 */
type Version = { no: number; label?: string; note: string; created_at: string }

/** 显示用的版本：去掉 tag 前面的 v（文案里自己带 v） */
const ver = (no?: number, label?: string) => (label || String(no ?? '')).replace(/^v/, '')
type TemplateOption = { key: string; name: string; description: string }
type TemplateState = {
  from_template: boolean
  name: string
  key?: string
  templates?: TemplateOption[]
  base?: number
  base_label?: string
  latest?: number
  latest_label?: string
  updates?: Version[]
  /** 离线项目没连 creght：查不到新版本 */
  versions_error?: string
  /** 付费模板、这个账号没订阅或过期了：项目照常用，拿不到新版本 */
  subscription_required?: boolean
  template?: { subscribe_url?: string }
  merging?: boolean
  conflicts?: string[] | null
  /** 推到预览时撞了远端改动、还没解决的文件（<<<<<<< local / >>>>>>> remote） */
  push_conflicts?: string[] | null
  /** 这台 Annulo 的能力版本、最新那一版模板要求的能力版本（shuttle.json 的 min_shuttle_api） */
  api?: number
  requires?: number
  /** 项目里和模板不一样的模板文件（改过的、删掉的），「恢复模板文件」用 */
  modified?: { path: string; deleted?: boolean }[]
}
type UpgradeResult = { status: 'up_to_date' | 'merged' | 'conflict'; from: number; to: number; files?: string[]; conflicts?: string[] }
type MergeOut = { result: UpgradeResult; push_error?: string; push_conflicts?: string[]; record_error?: string }
type RestoreOut = { commit: string; files: string[]; result?: UpgradeResult; upgrade_error?: string; push_error?: string; push_conflicts?: string[] }

// 交给助手的话里共用的解法（助手的 annulo skill 的 git.md 里有同样的规则）：小改动两边都保留，整页改了用途、合不到一起的先问用户
export const HOW = () =>
  t('两边各改了一小处、互不矛盾的，合在一起、两边都保留；一边把整个页面或文件改成了别的用途（比如模板的后台页面被改成了网站），或者两边的改法合在一起跑不起来的，不要自己选，用 request_user_input 把两种选法和各自的后果摆给我选。改完确认没有 <<<<<<< / >>>>>>> 标记，再用 annulo push 推到预览。')

/** git 合并（升级、换模板）的冲突 */
const mergeText = (what: string, files: string[]) =>
  t('{what}合并时有冲突，这些文件里有冲突标记：{files}。看 git log template 和 git diff 弄清模板和这个项目各改了什么，逐个解决。', { what, files: files.join(', ') }) + HOW()

/** 推到预览时和远端（编辑器、别的电脑）改动的冲突 */
export const pushText = (files: string[]) =>
  t('推到预览时，远端（creght 编辑器或别的电脑）也改过这些文件，合并有冲突：{files}。标记里 local 是本机的、remote 是远端的，逐个解决。', { files: files.join(', ') }) + HOW()

/**
 * 交给助手：新开一段对话在后台跑，右侧打开它（和运营后台里的按钮一样，不塞进用户正在聊的对话）。
 */
export async function handOff(text: string, title: string) {
  try {
    const r = await post('local/ask', { text, title })
    const { chat_id } = (await r.json()) as { chat_id: string }
    window.postMessage({ type: 'shuttle:navigate', view: 'backend' }, location.origin)
    window.dispatchEvent(new CustomEvent('shuttle:open-chat', { detail: chat_id }))
    toast.success(t('已交给助手，在右侧看过程'))
  } catch (e) {
    toast.error((e as Error).message)
  }
}

/** 升级 / 换模板的结果：合并冲突、推送冲突都交给助手 */
function handleResult(out: MergeOut, what: string) {
  if (out.result.status === 'conflict') handOff(mergeText(what, out.result.conflicts ?? []), t('解决模板合并的冲突'))
  else if (out.push_conflicts?.length) handOff(pushText(out.push_conflicts), t('解决推送的冲突'))
}

export default function TemplateCard() {
  const [st, setSt] = useState<TemplateState | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<UpgradeResult | null>(null)

  const load = () =>
    getJSON<TemplateState>('template')
      .then(setSt)
      .catch((e) => setError((e as Error).message))
  useEffect(() => {
    load()
  }, [])

  const upgrade = async () => {
    setBusy(true)
    setError('')
    setDone(null)
    try {
      const r = await post('template/upgrade', {})
      const out = (await r.json()) as MergeOut
      setDone(out.result)
      if (out.push_error && !out.push_conflicts?.length) setError(t('已合并到本机，但推到预览失败：') + out.push_error)
      handleResult(out, t('模板升级到 v{v} ', { v: out.result.to }))
      // 合并进来了新代码：让外壳整页刷新左侧后台（App 监听 shuttle:reload-backend），不用用户自己去刷新
      if (out.result.status === 'merged') window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  // 换模板：选一个别的模板，确认后本机三方合并（项目自己的改动保留），推到预览，平台上的来源模板一起改
  const [switchTo, setSwitchTo] = useState('')
  const [switching, setSwitching] = useState(false)
  const doSwitch = async () => {
    setSwitching(true)
    setError('')
    setDone(null)
    try {
      const r = await post('template/switch', { template: switchTo })
      const out = (await r.json()) as MergeOut
      setDone(out.result)
      setSwitchTo('')
      if (out.push_error && !out.push_conflicts?.length) setError(t('已合并到本机，但推到预览失败：') + out.push_error)
      if (out.record_error) toast.error(t('平台上的来源模板没改成（这台电脑上已经按新模板了）：') + out.record_error)
      handleResult(out, t('换模板'))
      if (out.result.status === 'merged') window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSwitching(false)
    }
  }

  // 恢复模板文件：项目改坏了的逃生口。勾选的文件换回模板（有新版本就是最新版），能撤销
  const [restoreOpen, setRestoreOpen] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [restoring, setRestoring] = useState(false)
  const [restored, setRestored] = useState<{ commit: string; files: string[]; pushed: boolean } | null>(null)
  const restore = async () => {
    setRestoring(true)
    setError('')
    setDone(null)
    try {
      const r = await post('template/restore', { files: [...picked] })
      const out = (await r.json()) as RestoreOut
      setRestored({ commit: out.commit, files: out.files, pushed: !out.push_error && out.result?.status !== 'conflict' })
      setRestoreOpen(false)
      setPicked(new Set())
      if (out.result && out.result.status !== 'up_to_date') setDone(out.result)
      if (out.upgrade_error) setError(t('文件已恢复成项目当前基于的版本，但没升级到最新：') + out.upgrade_error)
      else if (out.push_error && !out.push_conflicts?.length) setError(t('已恢复到本机，但推到预览失败：') + out.push_error)
      if (out.result) handleResult({ result: out.result, push_conflicts: out.push_conflicts }, t('模板升级到 v{v} ', { v: out.result.to }))
      window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRestoring(false)
    }
  }
  const undoRestore = async () => {
    if (!restored) return
    setRestoring(true)
    setError('')
    try {
      const r = await post('template/restore/undo', { commit: restored.commit })
      const out = (await r.json()) as { files: string[]; push_error?: string; push_conflicts?: string[] }
      setRestored(null)
      toast.success(t('已撤销，{n} 个文件改回了恢复前的样子', { n: out.files.length }))
      if (out.push_conflicts?.length) handOff(pushText(out.push_conflicts), t('解决推送的冲突'))
      else if (out.push_error) setError(t('已撤销到本机，但推到预览失败：') + out.push_error)
      window.postMessage({ type: 'shuttle:reload-backend' }, location.origin)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRestoring(false)
    }
  }
  const toggle = (p: string) =>
    setPicked((old) => {
      const next = new Set(old)
      if (next.has(p)) next.delete(p)
      else next.add(p)
      return next
    })

  if (!st?.from_template) return null
  const updates = st.updates ?? []
  const pending = st.merging && (st.conflicts?.length ?? 0) > 0
  const pushPending = !pending && (st.push_conflicts?.length ?? 0) > 0
  const others = (st.templates ?? []).filter((o) => o.key !== st.key)
  const target = others.find((o) => o.key === switchTo)
  const tooOld = !!(st.requires && st.api && st.requires > st.api)
  const modified = st.modified ?? []
  return (
    <div className="space-y-3 rounded-xl border border-border bg-background px-4 py-3">
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-bold">{t('模板：')}{st.name}</div>
          <div className="mt-0.5 text-[11px] text-muted-foreground">
            {st.base ? t('当前基于 v{v}', { v: ver(st.base, st.base_label) }) : t('还没记录基于哪个版本（第一次升级时自动识别）')}
            {st.latest ? ` · ${t('最新 v{v}', { v: ver(st.latest, st.latest_label) })}` : ''}
          </div>
        </div>
        {pending ? null : st.subscription_required ? (
          <span className="flex max-w-72 flex-col items-end gap-1 text-right text-xs text-muted-foreground">
            {t('付费模板：订阅还没开通或已经过期，开通后才能拿到新版本。项目照常能用。')}
            {st.template?.subscribe_url && (
              <a href={st.template.subscribe_url} target="_blank" rel="noreferrer" className="font-semibold text-primary-text hover:underline">
                {t('去开通')}
              </a>
            )}
          </span>
        ) : st.versions_error ? (
          <span className="max-w-56 text-right text-xs text-muted-foreground">{st.versions_error}</span>
        ) : updates.length > 0 && tooOld ? (
          <span className="text-xs font-medium text-amber-600 dark:text-amber-400">{t('先更新 Annulo 才能升级')}</span>
        ) : updates.length > 0 ? (
          <Button size="sm" disabled={busy} onClick={upgrade}>
            {busy ? <Loader className="animate-spin" /> : <ArrowUpCircle />}
            {busy ? t('升级中…') : t('升级到 v{v}', { v: ver(updates[0].no, updates[0].label) })}
          </Button>
        ) : (
          <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
            <Check className="size-3.5" /> {t('已是最新')}
          </span>
        )}
      </div>

      {!pending && updates.length > 0 && (
        <ul className="space-y-1 text-xs text-muted-foreground">
          {updates.slice(0, 5).map((v) => (
            <li key={v.no}>
              <span className="font-mono text-foreground">v{ver(v.no, v.label)}</span> {v.note || t('（没有说明）')}
            </li>
          ))}
        </ul>
      )}
      {!pending && updates.length > 0 && tooOld && (
        <p className="text-xs leading-relaxed text-amber-600 dark:text-amber-400">
          {t('模板 v{v} 用到了新版 Annulo 才有的能力（需要能力版本 {need}，这台是 {have}）。先更新 Annulo（重新安装最新的 App），再回来升级。', { v: ver(updates[0].no, updates[0].label), need: st.requires ?? 0, have: st.api ?? 0 })}
        </p>
      )}
      {!pending && updates.length > 0 && (
        <p className="text-[11px] leading-relaxed text-muted-foreground">{t('升级会把模板的改动合进这个项目，项目自己改过的地方保留；两边改了同一处的，交给助手解决。合并后推到预览，正式环境不变。')}</p>
      )}

      {pending && (
        <div className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs">
          <div className="flex items-center gap-1.5 font-semibold text-amber-600 dark:text-amber-400">
            <TriangleAlert className="size-3.5" /> {t('上次模板合并有冲突还没解决')}
          </div>
          <div className="text-muted-foreground">{st.conflicts?.join('、')}</div>
          <Button size="sm" variant="outline" onClick={() => handOff(mergeText(t('模板'), st.conflicts ?? []), t('解决模板合并的冲突'))}>
            <Sparkles /> {t('交给助手解决')}
          </Button>
        </div>
      )}
      {pushPending && (
        <div className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs">
          <div className="flex items-center gap-1.5 font-semibold text-amber-600 dark:text-amber-400">
            <TriangleAlert className="size-3.5" /> {t('推到预览时和远端的改动有冲突，还没解决')}
          </div>
          <div className="text-muted-foreground">{st.push_conflicts?.join('、')}</div>
          <Button size="sm" variant="outline" onClick={() => handOff(pushText(st.push_conflicts ?? []), t('解决推送的冲突'))}>
            <Sparkles /> {t('交给助手解决')}
          </Button>
        </div>
      )}

      {!pending && !pushPending && others.length > 0 && (
        <div className="space-y-2 border-t border-border pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-muted-foreground">{t('换成别的模板')}</span>
            <Select<string>
              value={switchTo}
              onChange={setSwitchTo}
              title={t('换成哪个模板')}
              placeholder={t('选一个模板')}
              className="w-56"
              disabled={switching}
              options={others.map((o) => ({ value: o.key, label: o.name, sub: o.description, icon: <Repeat className="size-4" /> }))}
            />
            {target && (
              <Button size="sm" variant="outline" disabled={switching} onClick={doSwitch}>
                {switching ? <Loader className="animate-spin" /> : <Repeat />}
                {switching ? t('换模板中…') : t('换成「{name}」', { name: target.name })}
              </Button>
            )}
          </div>
          {target && (
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {t('会把「{name}」合进这个项目：项目自己改过的地方保留，两个模板不一样的地方（行业文件、删掉的功能）换成新的；合不到一起的交给助手，需要你选的它会问你。合并后推到预览，之后跟着「{name}」升级。', { name: target.name })}
            </p>
          )}
        </div>
      )}

      {!pending && !pushPending && modified.length > 0 && (
        <div className="space-y-2 border-t border-border pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex-1 text-xs text-muted-foreground">{t('有 {n} 个模板文件在这个项目里改过或删掉了', { n: modified.length })}</span>
            <Button size="sm" variant="outline" disabled={restoring} onClick={() => setRestoreOpen((o) => !o)}>
              <RotateCcw /> {t('恢复模板文件')}
            </Button>
          </div>
          {restoreOpen && (
            <div className="space-y-2 rounded-lg border border-border px-3 py-2">
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                {updates.length > 0 && !tooOld
                  ? t('勾选的文件换成模板最新版 v{v} 的样子，这些文件里的改动会被覆盖（恢复后可以撤销）。模板有新版本，会一起升级到 v{v}：没勾的文件照常合并，项目自己改过的地方保留。user/ 里的定制、项目自己加的文件、表里的数据都不受影响。', { v: ver(updates[0].no, updates[0].label) })
                  : t('勾选的文件换成模板 v{v} 的样子，这些文件里的改动会被覆盖（恢复后可以撤销）。user/ 里的定制、项目自己加的文件、表里的数据都不受影响。', { v: st.base ? ver(st.base, st.base_label) : '' })}
              </p>
              <label className="flex items-center gap-2 text-xs font-medium">
                <input type="checkbox" checked={picked.size === modified.length} onChange={(e) => setPicked(e.target.checked ? new Set(modified.map((f) => f.path)) : new Set())} />
                {t('全选')}
              </label>
              <ul className="max-h-60 space-y-1 overflow-y-auto">
                {modified.map((f) => (
                  <li key={f.path}>
                    <label className="flex items-center gap-2 text-xs">
                      <input type="checkbox" checked={picked.has(f.path)} onChange={() => toggle(f.path)} />
                      <span className="min-w-0 truncate font-mono">{f.path}</span>
                      {f.deleted && <span className="shrink-0 text-[11px] text-muted-foreground">{t('已删除')}</span>}
                    </label>
                  </li>
                ))}
              </ul>
              <div className="flex items-center gap-2">
                <Button size="sm" disabled={restoring || picked.size === 0} onClick={restore}>
                  {restoring ? <Loader className="animate-spin" /> : <RotateCcw />}
                  {restoring ? t('恢复中…') : t('恢复选中的 {n} 个文件', { n: picked.size })}
                </Button>
                <Button size="sm" variant="ghost" disabled={restoring} onClick={() => setRestoreOpen(false)}>
                  {t('取消')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
      {restored && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span className="flex-1">{restored.pushed ? t('已恢复 {n} 个文件，已推到预览。', { n: restored.files.length }) : t('已恢复 {n} 个文件。', { n: restored.files.length })}</span>
          <Button size="sm" variant="outline" disabled={restoring} onClick={undoRestore}>
            {restoring ? <Loader className="animate-spin" /> : <Undo2 />} {t('撤销恢复')}
          </Button>
        </div>
      )}
      {done?.status === 'merged' && (
        <div className="text-xs text-muted-foreground">
          {t('已合并到 v{v}，改动了 {n} 个文件，已推到预览。刷新左侧后台就能看到。', { v: done.to ?? '', n: done.files?.length ?? 0 })}
        </div>
      )}
      {done?.status === 'conflict' && <div className="text-xs text-muted-foreground">{t('有 {n} 个文件冲突，已交给助手解决。', { n: done.conflicts?.length ?? 0 })}</div>}
      {error && <div className="text-xs text-destructive">{error}</div>}
    </div>
  )
}
