import { useEffect, useState, type FormEvent } from 'react'
import { Loader } from 'lucide-react'
import { getJSON, post } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/controls'
import { t } from '@/lib/i18n'

export type Role = 'admin' | 'member' | 'viewer'
export type ServerUser = { id: string; name: string; role: Role }
export type AuthMe = { server: boolean; setup_required?: boolean; user?: ServerUser }

export const roleLabel = (r: Role) => (r === 'admin' ? t('管理员') : r === 'member' ? t('成员') : t('只读'))

/**
 * 服务端部署的登录页（docs/server-and-multi-project.md「账号」）：第一次打开建管理员；带 ?invite= 的是邀请链接，建自己的账号；其余是登录。
 * 本机版没有这一页。
 */
export default function AuthPage({ me }: { me: AuthMe }) {
  const invite = new URLSearchParams(location.search).get('invite') ?? ''
  const mode = me.setup_required ? 'setup' : invite ? 'invite' : 'login'
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [inviteRole, setInviteRole] = useState<Role | null>(null)
  const [inviteBad, setInviteBad] = useState(false)

  useEffect(() => {
    if (mode !== 'invite') return
    getJSON<{ valid: boolean; role: Role }>('auth/invite?token=' + encodeURIComponent(invite))
      .then((r) => (r.valid ? setInviteRole(r.role) : setInviteBad(true)))
      .catch(() => setInviteBad(true))
  }, [mode, invite])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      if (mode === 'setup') await post('auth/setup', { name, password })
      else if (mode === 'invite') await post('auth/invite/accept', { token: invite, name, password })
      else await post('auth/login', { name, password })
      location.replace('/_shuttle/') // 去掉 ?invite=，重新加载进 Annulo
    } catch (e) {
      setErr((e as Error).message.replace(/ · HTTP .*$/, ''))
      setBusy(false)
    }
  }

  const title = mode === 'setup' ? t('建一个管理员账号') : mode === 'invite' ? t('加入这个 Annulo') : t('登录 Annulo')
  const desc =
    mode === 'setup'
      ? t('这是这台服务器上的第一个账号，是管理员：能管成员、模型、密钥和项目。别人用你发的邀请链接加入。')
      : mode === 'invite'
        ? inviteRole
          ? t('你被邀请成为「{role}」。起一个名字、设一个密码。', { role: roleLabel(inviteRole) })
          : ''
        : t('用管理员给你的账号登录。没有账号找管理员要一个邀请链接。')

  return (
    <div className="flex min-h-dvh items-center justify-center bg-background p-4 text-foreground">
      <form onSubmit={submit} className="w-full max-w-sm space-y-5 rounded-2xl border border-border bg-card p-6 shadow-sm">
        <div className="space-y-1.5">
          <div className="text-xs font-semibold tracking-wide text-muted-foreground">Annulo</div>
          <h1 className="text-lg font-bold">{title}</h1>
          {desc && <p className="text-sm leading-relaxed text-muted-foreground">{desc}</p>}
        </div>
        {inviteBad ? (
          <p className="text-sm text-destructive">{t('邀请链接无效或已过期，请管理员重新发一个')}</p>
        ) : (
          <>
            <label className="block space-y-1.5">
              <span className="text-xs font-medium">{t('名字')}</span>
              <Input autoFocus autoComplete="username" value={name} onChange={(e) => setName(e.target.value)} placeholder={mode === 'login' ? '' : t('不能有空格')} />
            </label>
            <label className="block space-y-1.5">
              <span className="text-xs font-medium">{t('密码')}</span>
              <Input type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} value={password} onChange={(e) => setPassword(e.target.value)} placeholder={mode === 'login' ? '' : t('至少 8 位')} />
            </label>
            {err && <p className="text-sm text-destructive">{err}</p>}
            <Button type="submit" className="w-full" disabled={busy || !name.trim() || !password || (mode === 'invite' && !inviteRole)}>
              {busy && <Loader className="animate-spin" />}
              {mode === 'login' ? t('登录') : t('创建并进入')}
            </Button>
          </>
        )}
      </form>
    </div>
  )
}
