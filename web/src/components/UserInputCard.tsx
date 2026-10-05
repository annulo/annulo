import { useMemo, useState } from 'react'
import type { DynamicToolUIPart } from 'ai'
import { Check, CircleHelp } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

type Option = { value: string; label: string; description?: string }
type Question = {
  id: string
  type: 'single_select' | 'multi_select' | 'short_text' | 'long_text'
  label: string
  description?: string
  placeholder?: string
  required?: boolean
  options?: Option[]
  allow_custom?: boolean
}
type Request = { title?: string; description?: string; questions?: Question[] }
type Answers = Record<string, string | string[]>

const CUSTOM = '__custom__'

// 服务端按界面语言出这句（internal/agent/askuser.go），两种都认
const STOPPED = ['用户没有回答就停止了', 'Stopped before the user answered']

/**
 * agent 的 request_user_input 问卷。样式照搬 talizen 平台的 RequestUserInputCard。
 * 问卷一显示，助手这一轮就结束了（结果 status: waiting_for_user）。onAnswer：问卷还是对话的最后一条、助手没在跑时才有，
 * 提交的答案作为一条新消息发给助手，它接着往下做；用户也可以不填卡片、直接在输入框回复。
 * 旧对话里被停掉的问卷（停止、退出 App、服务重启）同样可以这样接着答。
 */
export default function UserInputCard({ part, onAnswer }: { part: DynamicToolUIPart; onAnswer?: (text: string) => void }) {
  const req = (part.input ?? {}) as Request
  // option 的 value 模型常漏写，缺了就用 label（服务端校验也是这么补的）
  const questions = (req.questions ?? []).map((q) => ({ ...q, options: q.options?.map((o) => ({ ...o, value: o.value || o.label })) }))
  const [answers, setAnswers] = useState<Answers>({})
  const [custom, setCustom] = useState<Record<string, string>>({})
  const [sent, setSent] = useState(false)

  const result = useMemo(() => {
    if (part.state !== 'output-available') return null
    try {
      return JSON.parse(String(part.output ?? '{}')) as { answers?: Answers; status?: string; reply?: string; ok?: boolean; error?: string }
    } catch {
      return null
    }
  }, [part])

  // 参数不合法被退回（我们的校验，或者 pi 的参数校验）：平台的做法是不显示空卡片，模型会自己改了重发
  const errorText = part.state === 'output-error' ? String(part.errorText ?? '') : ''
  const stopped = part.state === 'output-error' && STOPPED.some((x) => errorText.includes(x))
  if (result?.ok === false || (part.state === 'output-error' && !stopped)) return null
  const waiting = result?.status === 'waiting_for_user'
  const open = (waiting || stopped) && !!onAnswer

  const valueOf = (q: Question): string | string[] | undefined => {
    const v = answers[q.id]
    if (q.type === 'single_select' && v === CUSTOM) return custom[q.id]?.trim() || undefined
    if (q.type === 'multi_select' && Array.isArray(v)) {
      const list = v.filter((x) => x !== CUSTOM)
      if (v.includes(CUSTOM) && custom[q.id]?.trim()) list.push(custom[q.id].trim())
      return list.length ? list : undefined
    }
    return typeof v === 'string' && v.trim() ? v.trim() : undefined
  }
  const missing = questions.filter((q) => q.required && valueOf(q) === undefined)

  // 答案写成一条消息发给助手
  const submit = () => {
    const lines = questions.flatMap((q) => {
      const v = valueOf(q)
      if (v === undefined) return []
      return [`- ${q.label}：${(Array.isArray(v) ? v : [v]).map((x) => labelOf(q, x)).join('、')}`]
    })
    onAnswer!(`${t('（回答问卷「{title}」）', { title: req.title || t('需要你确认几件事') })}\n${lines.join('\n')}`)
    setSent(true)
  }


  // 还在等回答（或者被停掉了）但已经不是最后一条：用户在后面的对话里回复过了
  const done = !open || sent
  const labelOf = (q: Question, v: string) => q.options?.find((o) => o.value === v)?.label ?? v

  return (
    <div className="mx-2 rounded-xl border border-border bg-muted/40 p-3 text-sm">
      <div className="flex items-start gap-2">
        <CircleHelp className="mt-0.5 size-4 shrink-0 text-primary-text" />
        <div className="min-w-0">
          <div className="font-semibold">{req.title || t('需要你确认几件事')}</div>
          {req.description && <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{req.description}</p>}
        </div>
      </div>

      {done ? (
        <div className="mt-3 space-y-2 border-t border-border pt-3 text-xs">
          {part.state === 'output-error' && !sent ? (
            <p className="text-muted-foreground">{t('没有回答就停止了。')}</p>
          ) : waiting && !sent ? (
            <p className="text-muted-foreground">{t('已在下面的对话里回复。')}</p>
          ) : part.state !== 'output-available' && !sent ? (
            <p className="text-muted-foreground">{t('正在显示问卷…')}</p>
          ) : result?.status === 'continued_in_chat' ? (
            <p className="text-muted-foreground">{t('你在对话里回复了：')}{result.reply}</p>
          ) : (
            questions.map((q) => {
              const v = result?.answers?.[q.id] ?? (sent ? valueOf(q) : undefined)
              return (
                <div key={q.id}>
                  <div className="text-muted-foreground">{q.label}</div>
                  <div className="mt-0.5 font-medium">{v === undefined ? t('（未回答）') : Array.isArray(v) ? v.map((x) => labelOf(q, x)).join(t('、')) : labelOf(q, v)}</div>
                </div>
              )
            })
          )}
        </div>
      ) : (
        <div className="mt-3 space-y-4">
          {questions.map((q) => (
            <div key={q.id} className="space-y-2">
              <div>
                <div className="text-[13px] font-medium">
                  {q.label}
                  {q.required && <span className="ml-0.5 text-destructive">*</span>}
                </div>
                {q.description && <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{q.description}</p>}
              </div>
              {q.type === 'single_select' || q.type === 'multi_select' ? (
                <div className="space-y-1.5">
                  {[...(q.options ?? []), ...(q.allow_custom ? [{ value: CUSTOM, label: t('其他'), description: undefined }] : [])].map((o) => {
                    const multi = q.type === 'multi_select'
                    const cur = answers[q.id]
                    const on = multi ? Array.isArray(cur) && cur.includes(o.value) : cur === o.value
                    return (
                      <button
                        key={o.value}
                        type="button"
                        role={multi ? 'checkbox' : 'radio'}
                        aria-checked={on}
                        onClick={() =>
                          setAnswers((a) => {
                            if (!multi) return { ...a, [q.id]: o.value }
                            const list = Array.isArray(a[q.id]) ? (a[q.id] as string[]) : []
                            return { ...a, [q.id]: on ? list.filter((x) => x !== o.value) : [...list, o.value] }
                          })
                        }
                        className={cn(
                          'flex w-full items-start gap-2 rounded-lg border px-2.5 py-2 text-left transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
                          on ? 'border-primary bg-primary/10' : 'border-border bg-background hover:bg-accent',
                        )}
                      >
                        <span
                          className={cn(
                            'mt-0.5 flex size-4 shrink-0 items-center justify-center border',
                            multi ? 'rounded' : 'rounded-full',
                            on ? 'border-primary bg-primary text-primary-foreground' : 'border-input',
                          )}
                        >
                          {on && <Check className="size-3" strokeWidth={3} />}
                        </span>
                        <span className="min-w-0">
                          <span className="block text-[13px] font-medium">{o.label}</span>
                          {o.description && <span className="block text-[11px] leading-relaxed text-muted-foreground">{o.description}</span>}
                        </span>
                      </button>
                    )
                  })}
                  {(answers[q.id] === CUSTOM || (Array.isArray(answers[q.id]) && (answers[q.id] as string[]).includes(CUSTOM))) && (
                    <input
                      autoFocus
                      value={custom[q.id] ?? ''}
                      onChange={(e) => setCustom((c) => ({ ...c, [q.id]: e.target.value }))}
                      placeholder={q.placeholder || t('写下你的答案')}
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                    />
                  )}
                </div>
              ) : q.type === 'long_text' ? (
                <textarea
                  value={(answers[q.id] as string) ?? ''}
                  onChange={(e) => setAnswers((a) => ({ ...a, [q.id]: e.target.value }))}
                  rows={3}
                  placeholder={q.placeholder}
                  className="flex min-h-16 w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                />
              ) : (
                <input
                  value={(answers[q.id] as string) ?? ''}
                  onChange={(e) => setAnswers((a) => ({ ...a, [q.id]: e.target.value }))}
                  placeholder={q.placeholder}
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                />
              )}
            </div>
          ))}
          <div className="flex items-center justify-between gap-2">
            <span className="text-[11px] text-muted-foreground">
              {missing.length ? t('还有 {n} 个必答题', { n: missing.length }) : stopped ? t('这份问卷在回答前被停止了，提交后答案会作为新消息发给助手') : t('也可以直接在下面的输入框回复')}
            </span>
            <Button size="sm" onClick={submit} disabled={missing.length > 0}>
              {t('提交')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
