import { useEffect } from 'react'
import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { isIOS } from '@/lib/pwa'
import { t } from '@/lib/i18n'

/** 浏览器没有安装接口（iPhone 的 Safari、火狐、Mac 的 Safari）：告诉用户在浏览器菜单里怎么把 Annulo 加到桌面 */
export function InstallHelp({ onClose }: { onClose: () => void }) {
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('keydown', esc)
    return () => document.removeEventListener('keydown', esc)
  }, [onClose])
  const ios = isIOS()
  const steps = ios
    ? [t('点 Safari 底部（iPad 是顶部）的「分享」按钮'), t('往下滑，点「添加到主屏幕」'), t('点「添加」。以后从桌面上的图标打开，全屏显示，没有地址栏')]
    : /Safari/.test(navigator.userAgent) && !/Chrome|Chromium|Edg/.test(navigator.userAgent)
      ? [t('在菜单栏点「文件」'), t('选「添加到程序坞」'), t('点「添加」。以后从程序坞打开，单独一个窗口')]
      : [t('打开浏览器的菜单（右上角的「⋮」或「…」）'), t('选「安装应用」「添加到主屏幕」或「添加到桌面」'), t('确认。以后从桌面上的图标打开，单独一个窗口')]
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-4 backdrop-blur-[2px] sm:items-center" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div role="dialog" aria-modal="true" aria-label={t('添加到桌面')} className="w-full max-w-sm rounded-2xl border border-border bg-background p-5 shadow-2xl">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">{t('添加到桌面')}</h2>
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label={t('关闭')} className="rounded-lg text-muted-foreground">
            <X className="size-[18px]" />
          </Button>
        </div>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{t('这个项目会像一个 App 一样出现在桌面上，点开直接进来。')}</p>
        <ol className="mt-4 space-y-3">
          {steps.map((s, i) => (
            <li key={i} className="flex gap-3 text-sm">
              <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary-text">{i + 1}</span>
              <span className="pt-0.5 leading-relaxed">{s}</span>
            </li>
          ))}
        </ol>
        <Button className="mt-5 w-full" onClick={onClose}>
          {t('知道了')}
        </Button>
      </div>
    </div>
  )
}
