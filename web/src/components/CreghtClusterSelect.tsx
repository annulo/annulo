import { useEffect, useState } from 'react'
import { Globe } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Select } from '@/components/ui/select'
import { t } from '@/lib/i18n'

type Cluster = { host: string; name: string; logged_in: boolean; templates: number }
type State = { current: string; clusters: Cluster[] }

/**
 * 选 creght 集群（creght.cn / creght.com）。几个集群的账号、项目、模板各是各的：
 * 换了之后正在用的项目不在新集群就会放下，onChanged 里让界面回到登录 / 选项目。
 */
export default function CreghtClusterSelect({ onChanged, className }: { onChanged: () => void; className?: string }) {
  const [st, setSt] = useState<State | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    getJSON<State>('settings/creght')
      .then(setSt)
      .catch((e) => setErr((e as Error).message))
  }, [])

  const change = async (host: string) => {
    setErr('')
    setBusy(true)
    try {
      setSt((await (await post('settings/creght', { host }, 'PUT')).json()) as State)
      onChanged()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (!st) return err ? <p className="text-sm text-destructive">{err}</p> : null
  return (
    <div className="space-y-1.5">
      <Select
        value={st.current}
        onChange={change}
        disabled={busy}
        title={t('creght 区域')}
        ariaLabel={t('creght 区域')}
        className={className}
        options={st.clusters.map((c) => ({
          value: c.host,
          label: c.name,
          icon: <Globe className="size-3.5" />,
          sub: [c.logged_in ? t('已登录') : t('未登录'), c.templates ? '' : t('还没有模板，只能打开已有项目')].filter(Boolean).join(' · '),
        }))}
      />
      {err && <p className="text-sm text-destructive">{err}</p>}
    </div>
  )
}
