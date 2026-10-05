import { useEffect, useState } from 'react'
import { getJSON, post, type Status } from '@/lib/api'
import { Segmented } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

type Lang = 'auto' | 'zh' | 'en'

/** 界面语言：跟随系统 / 中文 / English。保存后外壳、左侧后台、助手都换语言（App 按 status.locale 处理） */
export default function LanguageSettings({ onSaved }: { onSaved: () => void }) {
  const [lang, setLang] = useState<Lang | null>(null)
  const [locale, setLoc] = useState<'zh' | 'en'>('zh')
  const [err, setErr] = useState('')

  useEffect(() => {
    getJSON<Status>('status')
      .then((s) => {
        setLang(s.language ?? 'auto')
        setLoc(s.locale ?? 'zh')
      })
      .catch((e) => setErr((e as Error).message))
  }, [])

  const change = async (v: Lang) => {
    setErr('')
    try {
      const r = (await (await post('settings/language', { language: v }, 'PUT')).json()) as { language: Lang; locale: 'zh' | 'en' }
      setLang(r.language)
      setLoc(r.locale)
      onSaved()
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  if (!lang) return err ? <p className="text-sm text-destructive">{err}</p> : null
  return (
    <section className="space-y-3 rounded-xl border border-border bg-background p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium">{t('界面语言')}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {lang === 'auto' ? t('跟随系统，现在是 {lang}。', { lang: locale === 'en' ? 'English' : '中文' }) : ''}
            {t('左侧运营后台、助手的回复也跟着换（后台要模板支持多语言）。')}
          </p>
        </div>
        <Segmented<Lang>
          variant="pill"
          value={lang}
          onChange={change}
          options={[
            { value: 'auto', label: t('跟随系统') },
            { value: 'zh', label: '中文' },
            { value: 'en', label: 'English' },
          ]}
        />
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
    </section>
  )
}
