import { useEffect, useRef, useState } from 'react'
import { Brain, Check, ChevronDown, Loader, Settings2 } from 'lucide-react'
import { getJSON, normModelSettings, post, priceText, THINKING_LABEL, type ModelConfig, type ModelSettings, type Status, type Thinking } from '@/lib/api'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

/**
 * 对话框底部的模型菜单：切换模型、思考强度。改完下一条消息生效，当前对话的上下文不丢。
 * 用 fixed 定位向上弹出，浮在运营后台上面，不受面板宽度限制。
 */
export function ModelMenu({ llm, onChanged, onManage }: { llm?: Status['llm']; onChanged: () => void; onManage: () => void }) {
  const [at, setAt] = useState<DOMRect | null>(null)
  const [st, setSt] = useState<ModelSettings | null>(null)
  const [err, setErr] = useState('')
  const root = useRef<HTMLDivElement>(null)
  const btn = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!at) return
    getJSON<ModelSettings>('settings/llm')
      .then((s) => setSt(normModelSettings(s)))
      .catch((e: Error) => setErr(e.message))
    const close = () => setAt(null)
    const onDown = (e: MouseEvent) => !root.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node) && close()
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close()
    const onBlur = () => setTimeout(() => document.activeElement instanceof HTMLIFrameElement && close())
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('blur', onBlur)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('blur', onBlur)
      window.removeEventListener('resize', close)
    }
  }, [at])

  // 先在界面上改（勾马上跟过去），请求在后面跑，失败再改回来。不要在请求期间禁用整个列表：那会让整个弹层闪一下
  const update = async (body: { active?: string; thinking?: Thinking }) => {
    setErr('')
    const prev = st
    // 高亮看的是 thinking_effective（模型实际用的档）：选档时一起改，不然点了不动；换模型后它会变，存完重新读一次
    setSt((s) => s && { ...s, active: body.active ?? s.active, thinking: body.thinking ?? s.thinking, thinking_effective: body.thinking ?? s.thinking_effective })
    try {
      await post('settings/llm', body, 'PUT')
      getJSON<ModelSettings>('settings/llm').then((s) => setSt(normModelSettings(s))).catch(() => {})
      onChanged()
    } catch (e) {
      setSt(prev)
      setErr((e as Error).message)
    }
  }

  const active = st?.models.find((m) => m.id === st.active)
  // 思考档位只列当前模型有的（每个模型不一样）；选中的是它实际用的那档
  const levels = active?.thinking_levels ?? st?.thinking_levels ?? []
  const effective = st?.thinking_effective ?? st?.thinking
  const thinkingOn = llm?.reasoning && llm.thinking !== 'off'

  return (
    <>
      <button
        ref={btn}
        type="button"
        onClick={(e) => setAt(at ? null : e.currentTarget.getBoundingClientRect())}
        aria-expanded={!!at}
        aria-haspopup="menu"
        className={cn(
          'inline-flex min-w-0 items-center gap-1.5 rounded-md px-1 py-0.5 text-xs font-semibold text-foreground/80 outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50',
          at && 'text-foreground',
        )}
        title={t('切换模型和思考强度')}
      >
        {/* 只在模型用不了时亮红点（原因在悬停提示里）；能用就不显示，免得多一个看不懂的符号 */}
        {llm && !llm.ready && <span className="size-1.5 shrink-0 rounded-full bg-destructive" title={llm.error} aria-label={t('模型不可用：{err}', { err: llm.error })} />}
        <span className="truncate">{llm?.ready ? llm.model : t('未配置模型')}</span>
        {llm?.ready && thinkingOn && (
          <span className="inline-flex shrink-0 items-center gap-0.5 font-medium text-muted-foreground">
            <Brain className="size-3" />
            {t(THINKING_LABEL[llm.thinking])}
          </span>
        )}
        <ChevronDown className="size-3.5 shrink-0 opacity-60" />
      </button>
      {at && (
        <div
          ref={root}
          role="menu"
          style={{ bottom: window.innerHeight - at.top + 8, left: Math.min(at.left - 6, window.innerWidth - 300) }}
          className="fixed z-50 w-72 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-xl"
        >
          {!st ? (
            <div className="flex justify-center py-4">
              <Loader className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="scroll-thin max-h-72 overflow-y-auto">
              {/* 按服务商分组：creght 平台在前，没有模型的服务商不显示（creght 没连就不出现；连了但拉不到时显示原因，关掉了的不显示） */}
              {st.providers.map((p) => {
                const ms = st.models.filter((m) => m.provider === p.id)
                if (!ms.length && !(p.builtin && !p.ready && !p.disabled)) return null
                return (
                  <div key={p.id} role="group" aria-label={p.name}>
                    <div className="px-2.5 pt-1.5 pb-1 text-[11px] font-semibold text-muted-foreground">{p.name}</div>
                    {!ms.length && (
                      <p className="px-2.5 pb-2 text-xs text-muted-foreground">
                        {p.error}
                      </p>
                    )}
                    {ms.map((m) => (
                      <button
                        key={m.id}
                        role="menuitemradio"
                        aria-checked={m.id === st.active}
                        disabled={!m.ready}
                        onClick={() => m.id !== st.active && update({ active: m.id })}
                        title={m.ready ? undefined : m.error}
                        className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent disabled:opacity-50"
                      >
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-[13px] font-semibold">{menuLabel(m)}</span>
                          {(m.pricing || !m.ready) && (
                            <span className="block truncate text-[11px] text-muted-foreground tabular-nums">{m.ready ? priceText(m.pricing, true) : m.error}</span>
                          )}
                        </span>
                        {m.id === st.active && <Check className="size-4 shrink-0 text-primary-text" />}
                      </button>
                    ))}
                  </div>
                )
              })}
            </div>
          )}
          {st && (
            <>
              <div className="my-1 h-px bg-border" />
              <div className="px-2.5 pt-1.5 pb-2">
                <div className="flex items-center justify-between text-[11px] font-semibold text-muted-foreground">
                  <span>{t('思考强度')}</span>
                  {active && !active.reasoning && <span className="font-normal">{t('这个模型不支持')}</span>}
                </div>
                <div
                  role="radiogroup"
                  aria-label="思考强度"
                  className="mt-1.5 grid gap-1 rounded-lg bg-muted p-0.5"
                  style={{ gridTemplateColumns: `repeat(${Math.max(levels.length, 1)}, minmax(0, 1fr))` }}
                >
                  {levels.map((l) => (
                    <button
                      key={l}
                      role="radio"
                      aria-checked={effective === l}
                      disabled={!active?.reasoning}
                      onClick={() => effective !== l && update({ thinking: l })}
                      className={cn(
                        'rounded-md py-1 text-xs font-semibold whitespace-nowrap outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50',
                        effective === l ? 'bg-background text-foreground shadow-xs' : 'text-muted-foreground hover:text-foreground',
                      )}
                    >
                      {t(THINKING_LABEL[l])}
                    </button>
                  ))}
                </div>
              </div>
            </>
          )}
          {err && <p className="px-2.5 py-1.5 text-xs leading-relaxed text-destructive">{err}</p>}
          <div className="my-1 h-px bg-border" />
          <button
            role="menuitem"
            onClick={() => {
              setAt(null)
              onManage()
            }}
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
          >
            <Settings2 className="size-4 text-muted-foreground" />
            {t('管理模型')}
          </button>
        </div>
      )}
    </>
  )
}

/** 菜单里本机 Agent 的模型已经在「Claude Code（本机）」这类分组下面：只写模型名，没选具体模型的是「默认」（底部按钮仍显示完整名字） */
export function menuLabel(m: ModelConfig) {
  if (!m.agent) return m.label
  const rest = m.label.split(' · ').slice(1).join(' · ')
  if (!m.id.includes('/')) return rest || t('默认（跟它自己的设置）')
  return rest || m.label
}
