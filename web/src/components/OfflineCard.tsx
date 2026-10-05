import { useEffect, useState } from 'react'
import { CloudUpload, HardDrive, Loader } from 'lucide-react'
import { getJSON, post, type Status } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { t } from '@/lib/i18n'
import { navigate } from '@/lib/router'

/**
 * 当前项目是离线的：说明数据在哪、哪些用不了，给「转成在线项目」的入口（要先登录 creght）。
 * 转的时候在 creght 账号下建一个新项目，把本机的数据、上传的文件迁过去，之后这个项目就是在线的（不能转回离线）。
 */
export default function OfflineCard() {
  const [st, setSt] = useState<Status | null>(null)
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    getJSON<Status>('status').then(setSt).catch(() => {})
  }, [])
  if (!st?.backend?.offline) return null

  const goOnline = async () => {
    setBusy(true)
    setErr('')
    try {
      await post('setup/online', {})
      location.reload()
    } catch (e) {
      setErr((e as Error).message)
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3 rounded-xl border border-border bg-background px-4 py-3">
      <div className="flex items-start gap-3">
        <HardDrive className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0">
          <div className="text-[13px] font-bold">{t('这是离线项目')}</div>
          <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            {t('后台页面、业务数据和上传的文件都只在这台电脑上。本机函数、社媒发布和采集、AI 助手、定时任务照常能用；手机上打开后台、远程访问、表单询盘自动进来（webhook）要转成在线项目才有。')}
          </div>
        </div>
      </div>
      {confirm ? (
        <div className="space-y-2 rounded-lg bg-muted/60 px-3 py-2.5">
          <p className="text-xs leading-relaxed text-muted-foreground">
            {t('会在你的 creght 账号下建一个新项目，把后台页面、全部业务数据和上传的文件迁过去，对话历史留在本机跟着走。转完以后这个项目就是在线的，不能再转回离线。数据多的话要几分钟。')}
          </p>
          <div className="flex items-center gap-2">
            <Button size="sm" disabled={busy} onClick={goOnline}>
              {busy && <Loader className="animate-spin" />}
              {busy ? t('正在迁移…') : t('确认转成在线项目')}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirm(false)}>
              {t('取消')}
            </Button>
          </div>
        </div>
      ) : st.creght.logged_in ? (
        <Button size="sm" variant="outline" onClick={() => setConfirm(true)}>
          <CloudUpload /> {t('转成在线项目')}
        </Button>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => navigate('settings', '#connections')}>
            {t('连接 creght 账号')}
          </Button>
          <span className="text-[11px] text-muted-foreground">{t('登录后可以转成在线项目')}</span>
        </div>
      )}
      {err && <p className="text-xs break-all text-destructive">{err}</p>}
    </div>
  )
}
