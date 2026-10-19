import { useEffect, useState } from 'react'
import { ArrowUpRight, ExternalLink, LayoutTemplate, Loader2, X } from 'lucide-react'
import { followProject, getJSON, post } from '@/lib/api'
import { navigate } from '@/lib/router'
import { useCreghtConnect } from '@/lib/creghtConnect'
import type { SetupState } from '@/components/BackendPicker'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

/**
 * creght 网页（用户中心「本机 Agent」）叫起的 App，Electron 按白名单换成 /_shuttle/new?… 或 /_shuttle/open?…
 * （electron/policy.cjs 的 deepLinkURL）。docs/open-source.md「creght 客户的入口」。
 * - annulo://new?cluster=creght.cn&template=<模板项目 id>&name=…：用模板新建在线项目；
 * - annulo://open?cluster=creght.cn&project=<项目 id>：打开账号下已有的项目（本机没有就拉下来）。
 * 任何网页都能发这个链接，所以先弹卡片让用户确认，点「开始」才一步步做：切集群 → 没连就连 creght（OAuth）→ 新建 / 打开。
 * 切集群、连上以后界面整页刷新，靠地址里的 go=1 接着做（go 不在 deepLinkURL 的白名单里，外面的链接带不进来）。
 */
const PATHS = ['/_shuttle/new', '/_shuttle/open']

export const isStartLink = () => PATHS.includes(location.pathname)

type ClusterState = { current: string; clusters: { host: string; name: string }[] }

const close = () => navigate('backend')
const finish = () => {
  history.replaceState(null, '', '/_shuttle/')
  location.reload()
}

export default function StartLink() {
  const q = new URLSearchParams(location.search)
  const cluster = q.get('cluster') ? 'https://' + q.get('cluster')!.replace(/^https?:\/\//, '').replace(/\/+$/, '') : ''
  const opening = location.pathname === PATHS[1]
  const tplParam = opening ? '' : (q.get('template') ?? '')
  const projectParam = opening ? (q.get('project') ?? '') : ''
  const go = q.get('go') === '1'
  const isGit = tplParam.startsWith('git:')

  const [cl, setCl] = useState<ClusterState | null>(null)
  const [st, setSt] = useState<SetupState | null>(null)
  const [name, setName] = useState(q.get('name') ?? '')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [another, setAnother] = useState(false) // 已经有这个模板的项目，用户选了「再建一个」
  const { connect, waiting, url, err: connectErr } = useCreghtConnect()

  const load = () =>
    Promise.all([getJSON<ClusterState>('settings/creght' + (cluster ? '?include=' + encodeURIComponent(cluster) : '')), getJSON<SetupState>('setup')])
      .then(([c, s]) => {
        setCl(c)
        setSt(s)
      })
      .catch((e) => setErr((e as Error).message))
  useEffect(() => {
    load()
  }, [])

  const knownCluster = !cluster || !cl || cl.clusters.some((c) => c.host === cluster)
  const clusterName = cl?.clusters.find((c) => c.host === cluster)?.name ?? cluster.replace('https://', '')
  const needCluster = !!cluster && !!cl && cl.current !== cluster
  const needConnect = !isGit && !!st && !st.logged_in
  const tpl = st?.templates.find((x) => x.id === tplParam || x.key === tplParam)
  const project = opening ? st?.backends.find((b) => b.project_id === projectParam) : undefined
  const existing = tpl ? (st?.backends ?? []).filter((b) => b.template === tpl.key) : []
  const ready = !!st && !!cl && !needCluster && !needConnect

  useEffect(() => {
    if (tpl && !name) setName(tpl.name)
  }, [tpl?.key])

  // 用户点过「开始」（go=1）：切集群、连接后整页刷新回来，接着做下一步
  const step = async () => {
    setErr('')
    if (!knownCluster) return
    if (needCluster) {
      setBusy('cluster')
      try {
        await post('settings/creght', { host: cluster }, 'PUT')
        location.reload()
      } catch (e) {
        setErr((e as Error).message)
        setBusy('')
      }
      return
    }
    if (needConnect) {
      if (!waiting) connect()
    }
  }
  useEffect(() => {
    if (go && st && cl) step()
  }, [go, !!st, !!cl])

  const start = () => {
    if (!go) {
      const next = new URLSearchParams(location.search)
      next.set('go', '1')
      history.replaceState(null, '', location.pathname + '?' + next)
    }
    step()
  }

  const create = async () => {
    if (!tpl) return
    setBusy('new')
    setErr('')
    try {
      const r = await post('setup', { action: 'new', template: tpl.key, name: name.trim() })
      if (!(await followProject(r))) finish()
    } catch (e) {
      setErr((e as Error).message)
      setBusy('')
    }
  }
  const open = async (projectID: string) => {
    setBusy(projectID)
    setErr('')
    try {
      const r = await post('setup', { action: 'use', project_id: projectID })
      if (!(await followProject(r))) finish()
    } catch (e) {
      setErr((e as Error).message)
      setBusy('')
    }
  }

  let body
  if (!st || !cl) {
    body = err ? null : (
      <div className="flex justify-center py-4">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  } else if (!knownCluster) {
    body = <p className="text-sm text-muted-foreground">{t('不认识链接里的 creght 区域：{host}。可以在 设置 → 连接 里手动选区域。', { host: cluster.replace('https://', '') })}</p>
  } else if (!ready) {
    // 还没切到链接里的集群 / 还没连 creght：说明要做什么，点「开始」一步步做
    body = (
      <>
        <p className="text-sm leading-relaxed text-muted-foreground">
          {opening ? t('creght 网页请你在 Annulo 里打开你的一个项目。点「开始」后会：') : t('creght 网页请你用模板新建一个项目，项目和数据存在你的 creght 账号下。点「开始」后会：')}
        </p>
        <ol className="list-decimal space-y-1 pl-5 text-[13px]">
          {needCluster && <li>{t('切换到 {name}', { name: clusterName })}</li>}
          {needConnect && <li>{t('连接你的 creght 账号（浏览器里点一下授权）')}</li>}
          <li>{opening ? t('打开这个项目（本机没有就先拉下来）') : t('用模板新建项目并打开')}</li>
        </ol>
        {waiting && url && (
          <p className="text-xs text-muted-foreground">
            {t('浏览器没有自动打开？')}{' '}
            <a href={url} target="_blank" rel="noreferrer" className="font-semibold text-foreground hover:underline">
              {t('打开授权页')}
            </a>
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={close}>
            {t('取消')}
          </Button>
          <Button size="sm" onClick={start} disabled={!!busy || waiting} className="gap-1.5">
            {busy || waiting ? <Loader2 className="size-3.5 animate-spin" /> : <ExternalLink className="size-3.5" />}
            {busy === 'cluster' ? t('正在切换…') : waiting ? t('等你在浏览器里同意…') : t('开始')}
          </Button>
        </div>
      </>
    )
  } else if (opening) {
    body = !project ? (
      <>
        <p className="text-sm leading-relaxed text-muted-foreground">
          {t('你连着的 creght 账号（{host}）下没有这个项目，可能是用别的账号建的。可以在 设置 → 连接 里断开，换那个账号再连。', { host: st.api_host.replace('https://', '') })}
        </p>
        <div className="flex justify-end">
          <Button size="sm" variant="outline" onClick={() => navigate('settings', '#connections')}>
            {t('去设置')}
          </Button>
        </div>
      </>
    ) : project.current ? (
      <>
        <p className="text-sm text-muted-foreground">{t('「{name}」已经打开着。', { name: project.name })}</p>
        <div className="flex justify-end">
          <Button size="sm" onClick={close}>
            {t('好')}
          </Button>
        </div>
      </>
    ) : (
      <>
        <p className="text-sm leading-relaxed text-muted-foreground">
          {project.local ? t('切换到「{name}」？', { name: project.name }) : t('打开「{name}」？这台电脑上还没有它，会先拉下来。', { name: project.name })}
        </p>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={close} disabled={!!busy}>
            {t('取消')}
          </Button>
          <Button size="sm" onClick={() => open(project.project_id)} disabled={!!busy} className="gap-1.5">
            {busy && <Loader2 className="size-3.5 animate-spin" />}
            {busy ? t('准备中…') : t('打开')}
          </Button>
        </div>
      </>
    )
  } else if (!tpl) {
    body = <p className="text-sm text-muted-foreground">{t('{host} 上没有找到链接里的模板，可能已经下线。可以在项目列表里选别的模板新建。', { host: clusterName || st.api_host.replace('https://', '') })}</p>
  } else if (tpl.paid && !tpl.entitled) {
    body = (
      <>
        <p className="text-sm leading-relaxed text-muted-foreground">{t('「{name}」是付费模板，开通套餐后才能用。', { name: tpl.name })}</p>
        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" size="sm" variant="outline" onClick={() => getJSON<SetupState>('setup?refresh=1').then(setSt)}>
            {t('开通好了，刷新')}
          </Button>
          {tpl.subscribe_url && (
            <a href={tpl.subscribe_url} target="_blank" rel="noreferrer" className="inline-flex h-8 items-center gap-1 rounded-md bg-primary px-3 text-xs font-semibold text-primary-foreground">
              {t('去开通')} <ArrowUpRight className="size-[13px]" />
            </a>
          )}
        </div>
      </>
    )
  } else if (existing.length > 0 && !another) {
    // 装完回网页又点了一次之类：已经有这个模板的项目，先给「打开它」，免得多建一个
    body = (
      <>
        <p className="text-sm leading-relaxed text-muted-foreground">{t('你已经有用「{name}」建的项目：', { name: tpl.name })}</p>
        <ul className="space-y-2">
          {existing.map((b) => (
            <li key={b.project_id} className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2">
              <span className="min-w-0 truncate text-[13px] font-semibold">{b.name}</span>
              <Button size="sm" variant="outline" disabled={!!busy} onClick={() => open(b.project_id)} className="shrink-0">
                {busy === b.project_id && <Loader2 className="size-3.5 animate-spin" />}
                {busy === b.project_id ? t('准备中…') : t('打开')}
              </Button>
            </li>
          ))}
        </ul>
        <div className="flex justify-end">
          <Button size="sm" variant="ghost" onClick={() => setAnother(true)} disabled={!!busy}>
            {t('再建一个')}
          </Button>
        </div>
      </>
    )
  } else {
    body = (
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
      >
        <p className="text-sm leading-relaxed text-muted-foreground">
          {isGit || !st.logged_in ? t('用「{name}」新建项目，项目和数据存在这台电脑上。', { name: tpl.name }) : t('用「{name}」新建项目，项目和数据存在你的 creght 账号下。', { name: tpl.name })}
        </p>
        <Input autoFocus value={name} maxLength={40} onChange={(e) => setName(e.target.value)} placeholder={t('起个名字，比如品牌名或业务线')} aria-label={t('新项目的名字')} className="h-8 text-[13px]" />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={close} disabled={!!busy}>
            {t('取消')}
          </Button>
          <Button type="submit" size="sm" disabled={!!busy || !name.trim()} className="gap-1.5">
            {busy === 'new' && <Loader2 className="size-3.5 animate-spin" />}
            {busy === 'new' ? t('正在新建…') : t('新建')}
          </Button>
        </div>
      </form>
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px]" onMouseDown={(e) => e.target === e.currentTarget && !busy && close()}>
      <div role="dialog" aria-modal="true" aria-label={opening ? t('在 Annulo 中打开项目') : t('用模板新建项目')} className="flex max-h-[calc(100dvh-2rem)] w-full max-w-md flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-2xl">
        <div className="flex h-12 shrink-0 items-center justify-between border-b border-border px-4">
          <span className="flex items-center gap-2 text-sm font-bold">
            <LayoutTemplate className="size-4" />
            {opening ? t('在 Annulo 中打开项目') : tpl ? t('用「{name}」新建项目', { name: tpl.name }) : t('用模板新建项目')}
          </span>
          <Button variant="ghost" size="icon-sm" onClick={close} disabled={!!busy} aria-label={t('关闭')} className="rounded-lg text-muted-foreground hover:text-foreground">
            <X className="size-[18px]" />
          </Button>
        </div>
        <div className="space-y-4 overflow-y-auto p-5">
          {body}
          {(err || connectErr) && <p className="text-xs break-all text-destructive">{err || connectErr}</p>}
        </div>
      </div>
    </div>
  )
}
