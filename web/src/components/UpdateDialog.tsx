import { ArrowDownToLine, Loader, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { t } from '@/lib/i18n'
import type { DesktopUpdate } from '@/lib/desktopUpdate'

const mb = (n?: number) => ((n ?? 0) / 1024 / 1024).toFixed(1)

/** 更新弹窗：下载（看进度）→ 安装并重启，每一步都要用户点。下载中可以关掉，在后台接着下，顶栏按钮上看进度 */
export function UpdateDialog({ update, onDownload, onInstall, onClose }: { update: DesktopUpdate; onDownload: () => void; onInstall: () => void; onClose: () => void }) {
  const { state, version, current, bytes, received, error, notes } = update
  const pct = bytes ? Math.min(100, Math.round(((received ?? 0) / bytes) * 100)) : 0
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px]" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div role="dialog" aria-modal="true" aria-label={t('更新 Annulo')} className="w-full max-w-md overflow-hidden rounded-2xl border border-border bg-background shadow-2xl">
        <div className="flex h-12 items-center justify-between border-b border-border px-4">
          <span className="text-sm font-bold">{t('更新 Annulo')}</span>
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label={t('关闭')} className="rounded-lg text-muted-foreground hover:text-foreground">
            <X className="size-[18px]" />
          </Button>
        </div>
        <div className="space-y-4 p-5 text-sm">
          <div>
            <div className="font-semibold">{t('新版本 {v}', { v: version ?? '' })}</div>
            <div className="mt-1 text-xs text-muted-foreground">
              {t('当前是 {current}', { current })}
              {bytes ? ` · ${t('安装包 {size} MB', { size: mb(bytes) })}` : ''}
            </div>
            {notes && <p className="mt-2 whitespace-pre-wrap text-xs text-muted-foreground">{notes}</p>}
          </div>

          {state === 'downloading' && (
            <div className="space-y-1.5">
              <div className="h-2 overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary transition-[width] duration-500" style={{ width: `${pct}%` }} />
              </div>
              <div className="flex justify-between text-xs text-muted-foreground">
                <span>{t('下载中… {pct}%', { pct })}</span>
                <span>
                  {mb(received)} / {mb(bytes)} MB
                </span>
              </div>
            </div>
          )}
          {state === 'ready' && <p className="text-xs text-muted-foreground">{t('已经下载好。安装时 Annulo 会退出再自动打开，正在运行的对话会停止。')}</p>}
          {state === 'installing' && <p className="text-xs text-muted-foreground">{t('正在安装新版本，完成后 Annulo 会自动重启，大约 10 秒。')}</p>}
          {state === 'available' && error && <p className="text-xs text-destructive">{t('下载失败：{error}', { error })}</p>}

          <div className="flex justify-end gap-2">
            {state !== 'installing' && (
              <Button variant="ghost" size="sm" onClick={onClose}>
                {state === 'downloading' ? t('后台下载') : t('稍后')}
              </Button>
            )}
            {state === 'available' && (
              <Button size="sm" onClick={onDownload} className="gap-1.5">
                <ArrowDownToLine className="size-3.5" />
                {error ? t('重新下载') : t('下载')}
              </Button>
            )}
            {state === 'ready' && (
              <Button size="sm" onClick={onInstall}>
                {t('安装并重启')}
              </Button>
            )}
            {state === 'installing' && (
              <Button size="sm" disabled className="gap-1.5">
                <Loader className="size-3.5 animate-spin" />
                {t('正在安装…')}
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
