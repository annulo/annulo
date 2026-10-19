import { useState } from 'react'
import { ExternalLink, Loader, TriangleAlert } from 'lucide-react'
import { getJSON, type Status } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { t } from '@/lib/i18n'

/** 本机没有 git 时的提示条：模板、插件、项目历史、助手运行命令都靠它。给装的入口和「重新检测」（装完不用重启）。 */
export function GitNotice({ git, onStatus, className = '' }: { git: Status['git']; onStatus: (s: Status) => void; className?: string }) {
  const [checking, setChecking] = useState(false)
  const [still, setStill] = useState(false)
  if (!git || git.ok) return null
  const recheck = () => {
    setChecking(true)
    getJSON<Status>('status?recheck_git=1')
      .then((s) => {
        setStill(!s.git?.ok)
        onStatus(s)
      })
      .catch(() => setStill(true))
      .finally(() => setChecking(false))
  }
  return (
    <div className={`flex flex-wrap items-center gap-x-3 gap-y-2 border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-800 dark:text-amber-300 ${className}`}>
      <TriangleAlert className="size-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="font-semibold">{t('这台电脑还没装 Git')}</div>
        <div className="text-amber-800/80 dark:text-amber-300/80">
          {git.windows
            ? t('新建项目、模板和插件升级、改动历史、助手运行命令都要用它。下载 Git for Windows 安装（选项都用默认），装完点「重新检测」。')
            : t('新建项目、模板和插件升级、改动历史都要用它。在终端运行 xcode-select --install 安装，装完点「重新检测」。')}
          {still && <span className="ml-1 font-semibold">{t('还是没找到 git。')}</span>}
        </div>
      </div>
      {git.windows && (
        <>
          <Button asChild size="sm" className="h-7 gap-1 text-xs">
            <a href="https://git-scm.com/download/win" target="_blank" rel="noreferrer">
              {t('下载 Git')}
              <ExternalLink className="size-3" />
            </a>
          </Button>
          <a href="https://registry.npmmirror.com/binary.html?path=git-for-windows/" target="_blank" rel="noreferrer" className="underline underline-offset-2 hover:no-underline">
            {t('国内镜像')}
          </a>
        </>
      )}
      <Button size="sm" variant="outline" className="h-7 gap-1 bg-background text-xs" disabled={checking} onClick={recheck}>
        {checking && <Loader className="size-3 animate-spin" />}
        {t('重新检测')}
      </Button>
    </div>
  )
}
