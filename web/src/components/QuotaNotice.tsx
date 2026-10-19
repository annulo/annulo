import { Button } from '@/components/ui/button'
import { t } from '@/lib/i18n'

// 额度提示（docs/billing.md）：哪项云服务因为额度、订阅被平台拒了，就在用户正在操作的地方显示它。
// 平台的拒绝是同一种格式（4021 + quota + subscribe_url），这里只管显示：说清楚是哪项、为什么，加一个去开通页的按钮。
// 买、付钱都在 creght 网页上（系统浏览器打开），回来时页面自己重读权益。

export type Quota = { quota?: string; reason?: string; message: string; subscribe_url?: string }

// 开通页的地址带上是哪项额度引过来的、回跳地址（平台只认本机地址，服务端部署不带）
export function subscribeLink(url: string, quota?: string, server?: boolean) {
  try {
    const u = new URL(url)
    if (quota && !u.searchParams.has('quota')) u.searchParams.set('quota', quota)
    if (!server && location.port) u.searchParams.set('return', `http://127.0.0.1:${location.port}/_shuttle/settings#subscription`)
    return u.toString()
  } catch {
    return url
  }
}

/** canSubscribe 是 false（服务端部署的成员、只读）时不给按钮，提示找管理员 */
export default function QuotaNotice({ notice, canSubscribe = true, server, action }: { notice: Quota; canSubscribe?: boolean; server?: boolean; action?: string }) {
  return (
    <div className="flex items-center gap-3 rounded-lg bg-primary/5 px-3 py-2.5">
      <p className="min-w-0 flex-1 text-xs leading-relaxed">
        {notice.message}
        {!canSubscribe && <span className="text-muted-foreground"> {t('找管理员开通。')}</span>}
      </p>
      {canSubscribe && notice.subscribe_url && (
        <Button size="sm" asChild>
          <a href={subscribeLink(notice.subscribe_url, notice.quota, server)} target="_blank" rel="noreferrer">
            {action ?? t('去开通')}
          </a>
        </Button>
      )}
    </div>
  )
}
