import { useEffect, useMemo, useState } from 'react'
import { CheckCircle2, Download, Loader2, Search, Trash2, XCircle } from 'lucide-react'
import { getJSON, post, API_HEADERS, type SkillInfo, type SkillsState } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge, Input, Switch } from '@/components/ui/controls'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

/** Skill 设置：安装、启用 / 停用、删除，以及从本机其他 agent 导入 */
export default function SkillSettings() {
  const [state, setState] = useState<SkillsState | null>(null)
  const [source, setSource] = useState('')
  const [busy, setBusy] = useState('')
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [q, setQ] = useState('')

  const load = () => getJSON<SkillsState>('settings/skills').then(setState)
  useEffect(() => {
    load()
  }, [])

  const install = async (src: string, link = false, key = 'input') => {
    setBusy(key)
    setMsg(null)
    try {
      const r = await (await post('settings/skills/install', { source: src, link })).json()
      const names = (r.installed as SkillInfo[]).map((s) => s.name).join('、')
      setMsg({ ok: true, text: t('已安装 {names}，下一条消息开始生效。', { names }) })
      if (key === 'input') setSource('')
      load()
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message })
    } finally {
      setBusy('')
    }
  }

  const toggle = async (s: SkillInfo, on: boolean) => {
    const r = await post('settings/skills/toggle', { name: s.name, enabled: on })
    setState(await r.json())
  }

  const remove = async (s: SkillInfo) => {
    const r = await fetch(`/_shuttle/api/settings/skills?name=${encodeURIComponent(s.name)}`, { method: 'DELETE', headers: API_HEADERS })
    if (r.ok) setState(await r.json())
  }

  const local = useMemo(() => {
    const k = q.trim().toLowerCase()
    return (state?.local ?? []).filter((s) => !k || s.name.includes(k) || s.description.toLowerCase().includes(k))
  }, [state, q])

  if (!state) return <div className="text-sm text-muted-foreground">{t('加载…')}</div>
  const enabled = state.skills.filter((s) => s.enabled).length

  return (
    <div className="space-y-6 text-sm">
      <div className="space-y-3 rounded-xl border border-border bg-background p-5">
        <div>
          <div className="font-medium">{t('安装 skill')}</div>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            {t('粘贴 GitHub 链接（可以是仓库里的某个子目录）、git 地址、SKILL.md 的链接或本机路径。也可以直接让运营助手帮你装。')}
          </p>
        </div>
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (source.trim()) install(source.trim())
          }}
        >
          <Input value={source} onChange={(e) => setSource(e.target.value)} placeholder="https://github.com/owner/repo/tree/main/skills/xxx" className="font-mono" aria-label={t('skill 来源')} />
          <Button type="submit" disabled={!source.trim() || !!busy}>
            {busy === 'input' ? <Loader2 className="animate-spin" /> : <Download />}
            {t('安装')}
          </Button>
        </form>
        {msg && (
          <div className={cn('flex items-start gap-2 rounded-lg px-3 py-2 text-xs', msg.ok ? 'bg-ok/10 text-ok' : 'border border-destructive/30 bg-destructive/10 text-destructive')}>
            {msg.ok ? <CheckCircle2 className="mt-0.5 size-3.5 shrink-0" /> : <XCircle className="mt-0.5 size-3.5 shrink-0" />}
            <span className="break-all">{msg.text}</span>
          </div>
        )}
      </div>

      <div className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h4 className="text-sm font-semibold">{t('已安装')}</h4>
          <span className="text-xs text-muted-foreground">
            {t('{n} 个启用，会进入 agent 的上下文', { n: enabled })}
          </span>
        </div>
        <div className="overflow-hidden rounded-xl border border-border bg-background">
          {state.skills.map((s) => (
            <SkillRow key={s.name} s={s} onToggle={(on) => toggle(s, on)} onRemove={() => remove(s)} />
          ))}
        </div>
      </div>

      {state.local.length > 0 && (
        <div className="space-y-3">
          <div className="flex items-end justify-between gap-4">
            <div>
              <h4 className="text-sm font-semibold">{t('本机其他 agent 的 skill')}</h4>
              <p className="mt-1 text-xs text-muted-foreground">{t('来自 ~/.agents/skills 和 ~/.claude/skills。导入是软链过来，原目录更新后这里也跟着更新。')}</p>
            </div>
            <label className="relative w-56 shrink-0">
              <Search className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('搜索')} className="h-8 pl-8" aria-label={t('搜索本机 skill')} />
            </label>
          </div>
          <div className="scroll-thin max-h-[420px] overflow-y-auto rounded-xl border border-border bg-background">
            {local.length === 0 ? (
              <div className="px-4 py-8 text-center text-xs text-muted-foreground">{t('没有匹配的 skill')}</div>
            ) : (
              local.map((s) => (
                <div key={s.name} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-[13px] font-semibold">{s.name}</div>
                    <div className="mt-0.5 line-clamp-2 text-xs leading-relaxed text-muted-foreground">{s.description}</div>
                  </div>
                  <Button variant="outline" size="sm" disabled={!!busy} onClick={() => install(s.path.replace(/\/SKILL\.md$/, ''), true, s.name)}>
                    {busy === s.name && <Loader2 className="animate-spin" />}{t('导入')}
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function SkillRow({ s, onToggle, onRemove }: { s: SkillInfo; onToggle: (on: boolean) => void; onRemove: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const builtin = s.source === 'builtin'
  // 项目的 skill 是运营后台项目里的文件：可以停用，删除要改后台代码
  const workspace = s.source === 'workspace'
  return (
    <div className={cn('flex items-start gap-4 border-b border-border px-4 py-3.5 last:border-b-0', !s.enabled && 'opacity-60')}>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-[13px] font-semibold">{s.name}</span>
          {builtin ? <Badge tone="primary">{t('内置')}</Badge> : workspace ? <Badge>{t('项目')}</Badge> : s.linked ? <Badge>{t('软链')}</Badge> : null}
        </div>
        <p className="mt-1 line-clamp-2 text-xs leading-relaxed text-muted-foreground">{s.description}</p>
        {s.origin && <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground/80">{s.origin}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-2 pt-0.5">
        {!builtin && !workspace && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => (confirm ? onRemove() : setConfirm(true))}
            onMouseLeave={() => setConfirm(false)}
            aria-label={confirm ? t('再点一次删除') : t('删除 {label}', { label: s.name })}
            title={confirm ? t('再点一次删除') : t('删除')}
            className={confirm ? 'text-destructive' : 'text-muted-foreground'}
          >
            <Trash2 className="size-3.5" />
          </Button>
        )}
        <Switch checked={s.enabled} disabled={builtin} onChange={onToggle} label={`${s.enabled ? '停用' : '启用'} ${s.name}`} />
      </div>
    </div>
  )
}
