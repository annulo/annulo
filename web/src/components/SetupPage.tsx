import { useState } from 'react'
import { Cloud, ExternalLink, FolderOpen, LayoutTemplate, Loader2, MonitorSmartphone, Sparkles } from 'lucide-react'
import { useTheme } from '@/lib/theme'
import BackendPicker from '@/components/BackendPicker'
import CreghtClusterSelect from '@/components/CreghtClusterSelect'
import { Button } from '@/components/ui/button'
import { useCreghtConnect } from '@/lib/creghtConnect'
import { t } from '@/lib/i18n'

const SKIP_KEY = 'annulo.setup.skipConnect'

function skipped() {
  try {
    return localStorage.getItem(SKIP_KEY) === '1'
  } catch {
    return false
  }
}

/**
 * 还没有选项目。没连 creght 时先问一步要不要连（连上多出什么），能跳过；跳过或已连就是选项目、新建项目。
 * 连着 creght：能打开 creght 上已有的项目、选数据存云端还是本机、用 creght 的模板（BackendPicker 按连没连显示）。
 */
export default function SetupPage({ loggedIn, onDone }: { loggedIn: boolean; onDone: () => void }) {
  const [theme] = useTheme()
  const [step, setStep] = useState<'intro' | 'pick'>(() => (loggedIn || skipped() ? 'pick' : 'intro'))
  const logo = <img src={theme === 'dark' ? '/_shuttle/annulo-dark.svg' : '/_shuttle/annulo-light.svg'} alt="" className="h-12" />

  if (step === 'intro')
    return (
      <ConnectIntro
        logo={logo}
        onSkip={() => {
          try {
            localStorage.setItem(SKIP_KEY, '1')
          } catch {
            // 记不住就下次再问
          }
          setStep('pick')
        }}
      />
    )

  return (
    <div className="flex min-h-dvh items-center justify-center overflow-y-auto bg-muted/40 p-6">
      <div className="w-full max-w-xl space-y-8">
        <div className="flex flex-col items-center space-y-4 text-center">
          {logo}
          <div className="space-y-2">
            <h1 className="text-xl font-bold tracking-tight">{t('选择项目')}</h1>
            <p className="text-sm leading-relaxed text-muted-foreground">{t('一个项目就是一个业务，有自己的数据、后台页面和对话历史。接着用已有的，或者新建一个。')}</p>
          </div>
        </div>
        <BackendPicker onDone={onDone} />
        {loggedIn ? (
          <div className="mx-auto w-56">
            <CreghtClusterSelect onChanged={() => location.reload()} className="h-8 text-xs" />
          </div>
        ) : (
          <p className="text-center text-xs text-muted-foreground">
            <button type="button" onClick={() => setStep('intro')} className="font-semibold text-foreground hover:underline">
              {t('连接 creght')}
            </button>{' '}
            {t('后能把数据存到云端、打开 creght 上的项目、用 creght 的模板。')}
          </p>
        )}
      </div>
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

/** 第一次打开、没连 creght：说明连上多出什么，连接或者跳过。 */
function ConnectIntro({ logo, onSkip }: { logo: React.ReactNode; onSkip: () => void }) {
  const { connect, waiting, url, err } = useCreghtConnect()
  return (
    <div className="flex min-h-dvh items-center justify-center overflow-y-auto bg-muted/40 p-6">
      <div className="w-full max-w-xl space-y-7">
        <div className="flex flex-col items-center space-y-4 text-center">
          {logo}
          <div className="space-y-2">
            <h1 className="text-xl font-bold tracking-tight">{t('要连接 creght 吗？')}</h1>
            <p className="text-sm leading-relaxed text-muted-foreground">{t('Annulo 不连也能用。连上 creght 账号，多出这些：')}</p>
          </div>
        </div>
        <ul className="space-y-2 rounded-2xl border border-border bg-background p-3">
          {BENEFITS.map((b) => (
            <li key={b.title} className="flex items-start gap-3 rounded-xl px-2 py-2">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
                <b.icon className="size-4" />
              </span>
              <span className="min-w-0">
                <span className="block text-[13px] font-semibold">{t(b.title)}</span>
                <span className="block text-xs leading-relaxed text-muted-foreground">{t(b.desc)}</span>
              </span>
            </li>
          ))}
        </ul>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button onClick={connect} disabled={waiting} className="min-w-36">
              {waiting ? <Loader2 className="animate-spin" /> : <ExternalLink />}
              {waiting ? t('等你在浏览器里同意…') : t('连接 creght')}
            </Button>
            <Button variant="outline" onClick={onSkip}>
              {t('先不连，在这台电脑上用')}
            </Button>
          </div>
          {waiting && url && (
            <p className="text-center text-xs text-muted-foreground">
              {t('浏览器没有自动打开？')}{' '}
              <a href={url} target="_blank" rel="noreferrer" className="font-semibold text-foreground hover:underline">
                {t('打开授权页')}
              </a>
            </p>
          )}
          {err && <p className="text-center text-xs break-all text-destructive">{err}</p>}
          <p className="text-center text-xs leading-relaxed text-muted-foreground">{t('不连的话：项目和数据都在这台电脑上，用自己的模型 key 或这台电脑上的 Claude Code / Codex。以后随时能在 设置 → 连接 里连。')}</p>
          <div className="mx-auto w-56">
            <CreghtClusterSelect onChanged={() => {}} className="h-8 text-xs" />
          </div>
        </div>
      </div>
    </div>
  )
}
