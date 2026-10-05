import { useEffect, useState } from 'react'
import { Check, Loader } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

type State = { text: string; file: string; max: number }

/**
 * 给助手的说明：当前项目的 INSTRUCTIONS.md，拼进运营助手的系统提示（和项目自带的 SHUTTLE.md 冲突时以它为准）。
 * 只归用户：模板里没有这个文件，升级模板不会改它。改完从新对话开始生效。
 */
export default function InstructionsCard() {
  const [st, setSt] = useState<State | null>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    getJSON<State>('project/instructions')
      .then((s) => {
        setSt(s)
        setText(s.text)
      })
      .catch((e: Error) => setErr(e.message))
  }, [])

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      const s: State = await (await post('project/instructions', { text }, 'PUT')).json()
      setSt(s)
      setText(s.text)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const dirty = !!st && text.trim() !== st.text.trim()
  const len = [...text].length
  return (
    <div className="space-y-3 rounded-xl border border-border bg-background px-4 py-3">
      <div>
        <div className="text-[13px] font-bold">{t('给助手的说明')}</div>
        <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
          {t('写给这个项目里运营助手的要求，比如回答的风格、不能做的事、常用的做法。会放进助手的系统提示，和项目自带的说明冲突时以这里为准。')}
          {t('存在项目的 {file} 里，升级模板不会改它；改完从新对话开始生效。', { file: st?.file ?? 'INSTRUCTIONS.md' })}
        </div>
      </div>
      {!st && !err ? (
        <div className="flex justify-center py-4">
          <Loader className="size-4 animate-spin text-muted-foreground" />
        </div>
      ) : (
        <>
          <Textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={6}
            aria-label={t('给助手的说明')}
            placeholder={t('例：\n- 回答先给结论，控制在 5 句以内\n- 写小红书笔记不要用 emoji\n- 改站点页面前先问我')}
            className="text-[13px] leading-relaxed"
          />
          <div className="flex flex-wrap items-center gap-3">
            <Button size="sm" disabled={busy || !dirty || len > (st?.max ?? 8000)} onClick={save}>
              {busy ? <Loader className="animate-spin" /> : saved ? <Check /> : null}
              {saved ? t('已保存') : t('保存')}
            </Button>
            {dirty && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setText(st?.text ?? '')}>
                {t('撤销修改')}
              </Button>
            )}
            <span className={len > (st?.max ?? 8000) ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
              {t('{n} / {max} 字', { n: len, max: st?.max ?? 8000 })}
            </span>
            {err && <span className="text-xs text-destructive">{err}</span>}
          </div>
        </>
      )}
    </div>
  )
}
