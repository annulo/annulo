import { useEffect, useState } from 'react'
import { Check, Copy, Loader, Trash2, UserPlus } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { roleLabel, type Role, type ServerUser } from '@/components/AuthPage'
import { t } from '@/lib/i18n'

type Invite = { id: string; role: Role; expires_at: string }

const roleOptions = (): { value: Role; label: string; sub: string }[] => [
  { value: 'admin', label: roleLabel('admin'), sub: t('全部：成员、设置、新建和升级项目') },
  { value: 'member', label: roleLabel('member'), sub: t('用助手、改数据、跑函数；能在服务器上执行命令') },
  { value: 'viewer', label: roleLabel('viewer'), sub: t('只看后台和数据，不能用助手、不能改') },
]

/** 设置 → 成员（服务端部署、管理员才有）：谁能用这个 Annulo、什么角色；发邀请链接加人。 */
export default function MembersSettings({ me }: { me: ServerUser }) {
  const [users, setUsers] = useState<ServerUser[] | null>(null)
  const [invites, setInvites] = useState<Invite[]>([])
  const [err, setErr] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [link, setLink] = useState('')
  const [copied, setCopied] = useState(false)
  const [busy, setBusy] = useState('')
  const [confirmDel, setConfirmDel] = useState('') // 删人点两次：第一次变成「确认删除」

  const load = () =>
    getJSON<{ users: ServerUser[]; invites: Invite[] }>('auth/users')
      .then((r) => {
        setUsers(r.users)
        setInvites(r.invites)
      })
      .catch((e: Error) => setErr(e.message))
  useEffect(() => {
    load()
  }, [])

  const act = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key)
    setErr('')
    try {
      await fn()
      await load()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }
  const invite = () =>
    act('invite', async () => {
      const r = await post('auth/invites', { role })
      setLink(((await r.json()) as { url: string }).url)
      setCopied(false)
    })

  if (!users)
    return (
      <div className="flex justify-center py-6">
        <Loader className="size-4 animate-spin text-muted-foreground" />
      </div>
    )

  return (
    <div className="space-y-5">
      <div className="space-y-3 rounded-xl border border-border bg-card p-4">
        <div className="text-sm font-semibold">{t('邀请成员')}</div>
        <p className="text-xs leading-relaxed text-muted-foreground">{t('选好角色生成一个链接发给对方，7 天内有效、只能用一次。对方打开后起名字、设密码就加进来了。')}</p>
        <div className="flex items-center gap-2">
          <Select value={role} onChange={setRole} options={roleOptions()} title={t('角色')} className="w-56" />
          <Button onClick={invite} disabled={busy === 'invite'}>
            {busy === 'invite' ? <Loader className="animate-spin" /> : <UserPlus />}
            {t('生成邀请链接')}
          </Button>
        </div>
        {link && (
          <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2">
            <code className="min-w-0 flex-1 truncate text-xs">{link}</code>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                void navigator.clipboard?.writeText(link)
                setCopied(true)
              }}
            >
              {copied ? <Check /> : <Copy />}
              {copied ? t('已复制') : t('复制')}
            </Button>
          </div>
        )}
      </div>

      <div className="divide-y divide-border rounded-xl border border-border bg-card">
        {users.map((u) => (
          <div key={u.id} className="flex items-center gap-3 px-4 py-3">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-bold text-muted-foreground">{u.name.slice(0, 1).toUpperCase()}</span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-semibold">
                {u.name}
                {u.id === me.id && <span className="ml-2 text-xs font-normal text-muted-foreground">{t('（你）')}</span>}
              </div>
            </div>
            <Select value={u.role} onChange={(r) => act('role' + u.id, () => post('auth/users/' + u.id, { role: r }, 'PATCH'))} options={roleOptions()} title={t('角色')} className="w-40" disabled={!!busy} />
            {confirmDel === u.id ? (
              <Button size="sm" variant="destructive" disabled={!!busy} onClick={() => act('del' + u.id, () => post('auth/users/' + u.id, undefined, 'DELETE')).then(() => setConfirmDel(''))}>
                {t('确认删除（马上退出登录）')}
              </Button>
            ) : (
              <Button size="icon-sm" variant="ghost" aria-label={t('删除')} disabled={u.id === me.id || !!busy} onClick={() => setConfirmDel(u.id)}>
                <Trash2 />
              </Button>
            )}
          </div>
        ))}
        {invites.map((i) => (
          <div key={i.id} className="flex items-center gap-3 px-4 py-3 text-muted-foreground">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-md border border-dashed border-border text-xs">?</span>
            <div className="min-w-0 flex-1 text-sm">
              {t('还没用的邀请（{role}），{date} 前有效', { role: roleLabel(i.role), date: new Date(i.expires_at).toLocaleDateString() })}
            </div>
            <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => act('inv' + i.id, () => post('auth/invites/' + i.id, undefined, 'DELETE'))}>
              {t('撤回')}
            </Button>
          </div>
        ))}
      </div>
      {err && <p className="text-sm text-destructive">{err}</p>}
    </div>
  )
}
