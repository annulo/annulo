import { useEffect, useRef, useState } from 'react'
import { ArrowDownToLine, Check, Loader, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { getJSON, type Status } from '@/lib/api'
import { useDesktopUpdate } from '@/lib/desktopUpdate'
import { t } from '@/lib/i18n'

// 设置 → 关于：当前版本、检查更新。有新版本就打开顶栏那个更新弹窗（App.tsx 听 shuttle:open-update），下载、安装都在那里点
export default function AboutSettings() {
  const update = useDesktopUpdate()
  const [service, setService] = useState('')
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState(false)
  useEffect(() => {
    getJSON<Status>('status')
      .then((s) => setService(s.version))
      .catch(() => {})
  }, [])
  const s = update.state
  const openDialog = () => window.dispatchEvent(new CustomEvent('shuttle:open-update'))
  const check = async () => {
    setChecking(true)
    try {
      const r = await update.check()
      setChecked(true)
      if (r && r.state !== 'idle') openDialog()
    } finally {
      setChecking(false)
    }
  }
  // 打开「关于」自动查一次：有新版本就在下面显示（不弹窗，弹窗只在手动点「检查更新」时开）
  const auto = useRef(false)
  useEffect(() => {
    if (!update.available || auto.current) return
    auto.current = true
    setChecking(true)
    update
      .check()
      .then(() => setChecked(true))
      .catch(() => {})
      .finally(() => setChecking(false))
  }, [update.available]) // eslint-disable-line react-hooks/exhaustive-deps
  const pct = s?.bytes ? Math.round(((s.received ?? 0) / s.bytes) * 100) : 0

  return (
    <section className="space-y-4 rounded-xl border border-border bg-background p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <div className="text-sm font-semibold">Annulo {s?.current ?? ''}</div>
          {service && <div className="text-xs text-muted-foreground">{t('本机服务 {v}', { v: service })}</div>}
        </div>
        {update.available && (
          <Button size="sm" variant="outline" onClick={check} disabled={checking || s?.state === 'downloading' || s?.state === 'installing'} className="shrink-0 gap-1.5">
            {checking ? <Loader className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
            {checking ? t('正在检查…') : t('检查更新')}
          </Button>
        )}
      </div>

      {!update.available ? (
        <p className="text-xs text-muted-foreground">{t('在 Annulo App 里才能检查和安装更新。')}</p>
      ) : s && s.state !== 'idle' ? (
        <div className="flex items-center justify-between gap-4 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2.5">
          <div className="min-w-0 text-sm">
            <div className="font-medium">{t('新版本 {v}', { v: s.version ?? '' })}</div>
            <div className="text-xs text-muted-foreground">
              {s.state === 'downloading'
                ? t('下载中… {pct}%', { pct })
                : s.state === 'ready'
                  ? t('已经下载好，安装时 Annulo 会自动重启')
                  : s.state === 'installing'
                    ? t('正在安装…')
                    : s.error
                      ? t('下载失败：{error}', { error: s.error })
                      : t('有新版本可以更新')}
            </div>
          </div>
          <Button size="sm" onClick={openDialog} className="shrink-0 gap-1.5">
            <ArrowDownToLine className="size-3.5" />
            {s.state === 'ready' ? t('安装更新') : s.state === 'available' ? t('下载') : t('查看')}
          </Button>
        </div>
      ) : checked && s?.error ? (
        <p className="text-xs text-destructive">{t('检查更新失败：{error}', { error: s.error })}</p>
      ) : checked ? (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Check className="size-3.5" />
          {t('已经是最新版本')}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">{t('Annulo 开着时每 4 小时检查一次，有新版本会在顶栏提示；下载和安装都要你点了才做。')}</p>
      )}
    </section>
  )
}
