import { useEffect, useState } from 'react'
import { ExternalLink, Loader2 } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/controls'
import { getLocale, t } from '@/lib/i18n'

/** 一个装了 Annulo 插件、正连着的 Chrome profile。name / email 是 Chrome 里这个 profile 的名字和登录的账号（对得上时才有） */
type Profile = { id: string; name?: string; email?: string; version: string; sites: string[]; last_active: number }
type State = { profiles: Profile[]; chrome: boolean }

/**
 * 插件上架 Chrome 应用商店了没有。没上架时不放安装按钮和帮助链接，只说正在上架。
 * 2026-10-14 打开：商店 id 已加进 internal/extbridge 的 ExtensionIDs，官网 /help/browser 已发布（商店页审核通过前打不开）
 */
const EXTENSION_LISTED = true

/** 官网的帮助：插件有什么用、怎么装 */
function helpURL() {
  return getLocale() === 'en' ? 'https://annulo.dev/en/help/browser' : 'https://annulo.dev/help/browser'
}

/** https://www.doubao.com/* → www.doubao.com */
function siteName(pattern: string) {
  return pattern.replace(/^[a-z*]+:\/\//, '').replace(/\/\*$/, '')
}

/**
 * 浏览器：用户自己的 Chrome 里的 Annulo 插件（docs/browser-extension.md）。
 * 本机函数写 ctx.browser.open({ profile: '@user' }) 时在这里列出的 Chrome 里操作；每个 Chrome profile 允许了哪些网站，能撤销。
 */
export default function BrowserSettings() {
  const [st, setSt] = useState<State | null>(null)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const load = () =>
    getJSON<State>('settings/browser')
      .then(setSt)
      .catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
    // 插件连上、断开（Chrome 开关、装插件）、在插件菜单里移除网站都会变
    const timer = setInterval(load, 3000)
    return () => clearInterval(timer)
  }, [])

  const revoke = async (p: Profile, site: string) => {
    setBusy(p.id + site)
    setErr('')
    try {
      setSt((await (await post('settings/browser/revoke', { id: p.id, site })).json()) as State)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const openChrome = async () => {
    setBusy('chrome')
    setErr('')
    try {
      await post('settings/browser/chrome', {})
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (!st) return err ? <p className="text-sm text-destructive">{err}</p> : null
  const help = (
    <a href={helpURL()} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary-text hover:underline">
      {t('插件有什么用、怎么装')}
      <ExternalLink className="size-3" />
    </a>
  )

  if (!st.profiles.length) {
    return (
      <section className="space-y-3 rounded-xl border border-border bg-background p-4">
        <div>
          <h3 className="text-sm font-medium">{t('没有连上你自己的 Chrome')}</h3>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            {t('想让本机函数用你自己的 Chrome（用你已经登录好的账号，豆包、DeepSeek 这类网站不用再登录一遍），在 Chrome 里装上 Annulo 插件，Chrome 开着时会自动连上。不装也没关系：本机函数照样能用 Annulo 自己开的浏览器操作网页。')}
          </p>
        </div>
        <p className="text-xs text-muted-foreground">
          <StatusDot tone="idle">{t('未连接')}</StatusDot>
        </p>
        {EXTENSION_LISTED ? (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" asChild>
                <a href={helpURL()} target="_blank" rel="noreferrer">
                  {t('安装插件')}
                  <ExternalLink />
                </a>
              </Button>
              {st.chrome ? (
                <Button size="sm" variant="outline" onClick={openChrome} disabled={busy === 'chrome'}>
                  {busy === 'chrome' && <Loader2 className="animate-spin" />}
                  {t('打开 Chrome')}
                </Button>
              ) : (
                <span className="text-xs text-muted-foreground">{t('这台电脑上没找到 Chrome：先安装 Google Chrome。')}</span>
              )}
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">{t('装好了还是没连上：确认 Annulo 插件在 chrome://extensions 里是打开的，再重新打开一次 Chrome。')}</p>
          </>
        ) : (
          <p className="rounded-lg bg-muted/60 px-3 py-2 text-xs leading-relaxed text-muted-foreground">{t('Annulo 插件正在上架 Chrome 应用商店，上架后这里可以直接安装。')}</p>
        )}
        {err && <p className="text-sm text-destructive">{err}</p>}
      </section>
    )
  }

  return (
    <>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {t('Annulo 插件只在你允许的网站上、在它自己打开的窗口里操作。')} {EXTENSION_LISTED && help}
      </p>
      {st.profiles.map((p) => (
        <section key={p.id} className="space-y-3 rounded-xl border border-border bg-background p-4">
          <div className="flex items-center gap-2.5">
            <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-bold text-muted-foreground">{(p.name || 'C').slice(0, 1).toUpperCase()}</span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px] font-semibold">{p.name || t('Chrome 里的一个用户')}</div>
              {p.email && p.email !== p.name && <div className="truncate text-xs text-muted-foreground">{p.email}</div>}
            </div>
            <span className="shrink-0 text-xs text-muted-foreground">
              <StatusDot tone="ok">{t('已连接')}</StatusDot>
            </span>
          </div>
          <div>
            <div className="text-xs font-medium text-muted-foreground">{t('允许 Annulo 操作的网站')}</div>
            {p.sites.length ? (
              <ul className="mt-1.5 divide-y divide-border">
                {p.sites.map((s) => (
                  <li key={s} className="flex items-center justify-between gap-3 py-1.5">
                    <span className="truncate font-mono text-xs">{siteName(s)}</span>
                    <Button size="xs" variant="ghost" onClick={() => revoke(p, s)} disabled={busy === p.id + s}>
                      {busy === p.id + s && <Loader2 className="animate-spin" />}
                      {t('撤销')}
                    </Button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="mt-1.5 text-xs leading-relaxed text-muted-foreground">{t('还没有。本机函数第一次要在某个网站上操作时，Chrome 里会弹窗问你。')}</p>
            )}
          </div>
        </section>
      ))}
      {err && <p className="text-sm text-destructive">{err}</p>}
    </>
  )
}
