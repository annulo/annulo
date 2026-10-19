import { useEffect, useState } from 'react'
import { Check, Copy, Loader, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { getJSON } from '@/lib/api'
import { t } from '@/lib/i18n'

type VersionState = { current: string; latest?: string; published_at?: string; notes?: string; newer: boolean; error: string; upgrade: string; comparable: boolean }

/** 设置 → 关于（服务端部署）：当前版本、官网最新版本和更新说明；有新版本时给升级命令（在服务器上跑，一键升级以后再做）。 */
export default function ServerVersion() {
  const [st, setSt] = useState<VersionState | null>(null)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const load = (refresh = false) => {
    setBusy(true)
    getJSON<VersionState>('settings/version' + (refresh ? '?refresh=1' : ''))
      .then(setSt)
      .catch(() => {})
      .finally(() => setBusy(false))
  }
  useEffect(() => load(), [])

  if (!st) return <Loader className="size-4 animate-spin text-muted-foreground" />
  return (
    <section className="space-y-4 rounded-xl border border-border bg-background p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <div className="text-sm font-semibold">Annulo {st.current}</div>
          <div className="text-xs text-muted-foreground">
            {st.latest ? t('官网最新 {v}', { v: st.latest }) : st.error ? t('读不到官网的版本信息：{error}', { error: st.error }) : ''}
          </div>
        </div>
        <Button size="sm" variant="outline" onClick={() => load(true)} disabled={busy} className="shrink-0 gap-1.5">
          {busy ? <Loader className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
          {t('检查更新')}
        </Button>
      </div>
      {st.newer ? (
        <div className="space-y-2 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2.5">
          <div className="text-sm font-medium">{t('有新版本 {v}', { v: st.latest ?? '' })}</div>
          {st.notes && <p className="max-h-60 overflow-y-auto text-xs leading-relaxed whitespace-pre-wrap text-muted-foreground">{st.notes}</p>}
          <div className="space-y-1.5 border-t border-primary/20 pt-2">
            <div className="text-xs text-muted-foreground">{t('在服务器上 annulo 代码所在的目录里跑这几行就升级好了，数据不受影响（升级时会停几秒）：')}</div>
            <div className="flex items-start gap-2 rounded-md bg-background px-2.5 py-2">
              <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-[11px] leading-relaxed">{st.upgrade}</pre>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  void navigator.clipboard?.writeText(st.upgrade)
                  setCopied(true)
                }}
              >
                {copied ? <Check /> : <Copy />}
              </Button>
            </div>
          </div>
        </div>
      ) : st.comparable && st.latest ? (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Check className="size-3.5" />
          {t('已经是最新版本')}
        </p>
      ) : (
        !st.comparable && <p className="text-xs text-muted-foreground">{t('这个镜像是自己构建的开发版，没法和官网版本比较。')}</p>
      )}
    </section>
  )
}
