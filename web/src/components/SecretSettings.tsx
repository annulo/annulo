import { useEffect, useState } from 'react'
import { Check, KeyRound, Loader2, Plus, Trash2 } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

type Secret = { name: string; hint: string; source: 'shuttle' | 'env' }
type State = { list: Secret[]; needed: string[] }

/** 本机密钥：只给本机函数（ctx.secrets.get）用，只存在这台电脑上，不给 AI 助手 */
export default function SecretSettings() {
  const [st, setSt] = useState<State | null>(null)
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirmDel, setConfirmDel] = useState('')

  useEffect(() => {
    getJSON<State>('settings/secrets').then(setSt)
  }, [])

  const save = async (n = name) => {
    setBusy(true)
    setErr('')
    try {
      const r = await post('settings/secrets', { name: n.trim(), value: value.trim() }, 'PUT')
      setSt({ ...(await r.json()), needed: st?.needed ?? [] })
      setName('')
      setValue('')
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const remove = async (n: string) => {
    if (confirmDel !== n) return setConfirmDel(n)
    setConfirmDel('')
    try {
      const r = await post(`settings/secrets/${n}`, undefined, 'DELETE')
      setSt({ ...(await r.json()), needed: st?.needed ?? [] })
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  if (!st)
    return (
      <div className="flex justify-center py-8">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )

  const have = new Set(st.list.map((s) => s.name))
  const missing = st.needed.filter((n) => !have.has(n))

  return (
    <div className="space-y-6">
      {missing.length > 0 && (
        <div className="rounded-xl border border-warn/30 bg-warn/10 px-4 py-3 text-sm">
          <div className="font-medium">{t('运营后台的本机函数要用这些密钥，还没配：')}</div>
          <div className="mt-2 flex flex-wrap gap-2">
            {missing.map((n) => (
              <button
                key={n}
                type="button"
                onClick={() => setName(n)}
                className={cn('rounded-md border border-border bg-background px-2 py-1 font-mono text-xs hover:bg-accent', name === n && 'border-primary/50 text-primary-text')}
              >
                {n}
              </button>
            ))}
          </div>
        </div>
      )}

      {st.list.length > 0 && (
        <div className="overflow-hidden rounded-xl border border-border bg-background">
          {st.list.map((s) => (
            <div key={s.name} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
              <KeyRound className="size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-[13px] font-semibold">{s.name}</div>
                <div className="mt-0.5 text-[11px] text-muted-foreground">
                  {s.hint} · {s.source === 'env' ? t('来自终端的环境变量（没存在 Annulo 里）') : t('存在本机 ~/.annulo/secrets.json')}
                </div>
              </div>
              {s.source === 'shuttle' && (
                <Button
                  size={confirmDel === s.name ? 'sm' : 'icon-sm'}
                  variant="ghost"
                  onClick={() => remove(s.name)}
                  onBlur={() => setConfirmDel('')}
                  aria-label={t('删除 {label}', { label: s.name })}
                  className={cn('text-muted-foreground', confirmDel === s.name && 'text-destructive hover:text-destructive')}
                >
                  <Trash2 />
                  {confirmDel === s.name && '确认删除'}
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      <form
        className="space-y-3 rounded-xl border border-border bg-background p-5"
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
      >
        <div className="text-sm font-medium">{have.has(name.trim()) ? t('更新 {name}', { name: name.trim() }) : t('添加密钥')}</div>
        <div className="grid gap-3 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
          <Input value={name} onChange={(e) => setName(e.target.value.toUpperCase())} placeholder="PERPLEXITY_API_KEY" className="font-mono" aria-label={t('密钥名')} />
          <Input type="password" value={value} onChange={(e) => setValue(e.target.value)} placeholder={t('值（保存后只显示尾号）')} autoComplete="off" className="font-mono" aria-label={t('密钥的值')} />
        </div>
        {err && <p className="text-sm text-destructive">{err}</p>}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-xs text-muted-foreground">{t('密钥存储在本机，不会上传，也不会被 AI 助手读取。')}</p>
          <Button type="submit" size="sm" disabled={busy || !name.trim() || !value.trim()}>
            {busy ? <Loader2 className="animate-spin" /> : have.has(name.trim()) ? <Check /> : <Plus />}
            {t('保存')}
          </Button>
        </div>
      </form>
    </div>
  )
}
