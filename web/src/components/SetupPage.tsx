import { useState } from 'react'
import { Cloud, ExternalLink, FolderOpen, LayoutTemplate, Link2, Loader2, MonitorSmartphone, Sparkles, X } from 'lucide-react'
import { useTheme } from '@/lib/theme'
import BackendPicker from '@/components/BackendPicker'
import CreghtClusterSelect from '@/components/CreghtClusterSelect'
import { Button } from '@/components/ui/button'
import { useCreghtConnect } from '@/lib/creghtConnect'
import { t } from '@/lib/i18n'

/**
 * 还没有选项目：选项目、新建项目。
 * 没连 creght 时底部只有一个「连接 creght」按钮，点开弹窗说明连上多出什么（不单独占一步，不连是常态）。
 * 连着 creght：能打开 creght 上已有的项目、选数据存云端还是本机、用 creght 的模板（BackendPicker 按连没连显示）。
 */
export default function SetupPage({ loggedIn, onDone }: { loggedIn: boolean; onDone: () => void }) {
  const [theme] = useTheme()
  const [connecting, setConnecting] = useState(false)
  return (
    <div className="flex min-h-dvh items-center justify-center overflow-y-auto bg-muted/40 p-6">
      <div className="w-full max-w-xl space-y-8">
        <div className="flex flex-col items-center space-y-4 text-center">
          <img src={theme === 'dark' ? '/_shuttle/annulo-dark.svg' : '/_shuttle/annulo-light.svg'} alt="" className="h-12" />
          <div className="space-y-2">
            <h1 className="text-xl font-bold tracking-tight">{t('选择项目')}</h1>
            <p className="text-sm leading-relaxed text-muted-foreground">{t('一个项目就是一个业务，有自己的数据、后台页面和对话历史。接着用已有的，或者新建一个。')}</p>
          </div>
        </div>
        <BackendPicker inline onDone={onDone} />
        {loggedIn ? (
          <div className="mx-auto w-56">
            <CreghtClusterSelect onChanged={() => location.reload()} className="h-8 text-xs" />
          </div>
        ) : (
          <div className="flex justify-center">
            <Button variant="ghost" size="sm" onClick={() => setConnecting(true)} className="gap-1.5 text-xs text-muted-foreground hover:text-foreground">
              <Link2 className="size-3.5" />
              {t('连接 creght')}
            </Button>
          </div>
        )}
      </div>
      {connecting && <ConnectDialog onClose={() => setConnecting(false)} />}
    </div>
  )
}

const BENEFITS = [
  { icon: Cloud, title: '数据存到云端', desc: '项目和数据存在你的 creght 账号下，换电脑也还在。不连的话都存在这台电脑上。' },
  { icon: FolderOpen, title: '打开 creght 上已有的项目', desc: '你在 creght 上建过的项目，直接拉到这台电脑上接着用。' },
  { icon: LayoutTemplate, title: 'creght 的模板', desc: '外贸助手这类做好的行业模板，选了就能用，模板更新后项目能跟着升级。' },
  { icon: Sparkles, title: '平台的模型', desc: '不用自己去申请 API key，用 creght 平台的模型。' },
  { icon: MonitorSmartphone, title: '手机上打开', desc: '在手机、别的电脑上看后台、点发布，这台电脑开着时帮你执行。' },
] as const

/** 连接 creght 的弹窗：连上多出什么、选集群、去浏览器授权（连上后整页刷新）。 */
export function ConnectDialog({ onClose }: { onClose: () => void }) {
  const { connect, waiting, url, err } = useCreghtConnect()
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px]" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div role="dialog" aria-modal="true" aria-label={t('连接 creght')} className="flex max-h-[calc(100dvh-2rem)] w-full max-w-md flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-2xl">
        <div className="flex h-12 shrink-0 items-center justify-between border-b border-border px-4">
          <span className="text-sm font-bold">{t('连接 creght')}</span>
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label={t('关闭')} className="rounded-lg text-muted-foreground hover:text-foreground">
            <X className="size-[18px]" />
          </Button>
        </div>
        <div className="space-y-4 overflow-y-auto p-5">
          <p className="text-sm text-muted-foreground">{t('Annulo 不连也能用。连上 creght 账号，可以使用以下能力：')}</p>
          <ul className="space-y-3">
            {BENEFITS.map((b) => (
              <li key={b.title} className="flex items-start gap-3">
                <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
                  <b.icon className="size-3.5" />
                </span>
                <span className="min-w-0">
                  <span className="block text-[13px] font-semibold">{t(b.title)}</span>
                  <span className="block text-xs leading-relaxed text-muted-foreground">{t(b.desc)}</span>
                </span>
              </li>
            ))}
          </ul>
          <CreghtClusterSelect onChanged={() => {}} className="h-8 text-xs" />
          {waiting && url && (
            <p className="text-xs text-muted-foreground">
              {t('浏览器没有自动打开？')}{' '}
              <a href={url} target="_blank" rel="noreferrer" className="font-semibold text-foreground hover:underline">
                {t('打开授权页')}
              </a>
            </p>
          )}
          {err && <p className="text-xs break-all text-destructive">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t('取消')}
            </Button>
            <Button size="sm" onClick={connect} disabled={waiting} className="gap-1.5">
              {waiting ? <Loader2 className="size-3.5 animate-spin" /> : <ExternalLink className="size-3.5" />}
              {waiting ? t('等你在浏览器里同意…') : t('去浏览器授权')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
