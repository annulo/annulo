import { useEffect, useState } from 'react'
import { BarChart3, Bot, Clock, CreditCard, Globe, Inbox, Info, KeyRound, Languages, LayoutTemplate, Link2, MonitorSmartphone, Network, Plug, ShieldCheck, Sparkles, Users } from 'lucide-react'
import DomainSettings from '@/components/DomainSettings'
import NodeSettings from '@/components/NodeSettings'
import MembersSettings from '@/components/MembersSettings'
import type { ServerUser } from '@/components/AuthPage'
import LLMSettings from '@/components/LLMSettings'
import MCPSettings from '@/components/MCPSettings'
import SkillSettings from '@/components/SkillSettings'
import SecretSettings from '@/components/SecretSettings'
import ConnectionSettings from '@/components/ConnectionSettings'
import ScheduleSettings from '@/components/ScheduleSettings'
import InboxSettings from '@/components/InboxSettings'
import SubscriptionSettings, { subscriptionAvailable } from '@/components/SubscriptionSettings'
import BackendPicker from '@/components/BackendPicker'
import TemplateCard from '@/components/TemplateCard'
import PluginCard from '@/components/PluginCard'
import InstructionsCard from '@/components/InstructionsCard'
import OfflineCard from '@/components/OfflineCard'
import LanguageSettings from '@/components/LanguageSettings'
import RemoteSettings from '@/components/RemoteSettings'
import BrowserSettings from '@/components/BrowserSettings'
import AboutSettings from '@/components/AboutSettings'
import ServerVersion from '@/components/ServerVersion'
import UsagePage from '@/components/UsagePage'
import { hrefOf, navigate, useView } from '@/lib/router'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

const SECTIONS = [
  { key: 'backend', label: '项目', icon: LayoutTemplate, desc: '项目之间的后台数据、对话互相隔离' },
  { key: 'llm', label: '模型', icon: Bot, desc: '接自己的服务商（key 只存在本机 ~/.annulo/config.json），或者用这台电脑上的 Claude Code / Codex；连上 creght 也能用平台的模型，按 AI 积分计费。' },
  { key: 'subscription', label: '订阅', icon: CreditCard, desc: 'Annulo 订阅按账号算：平台 AI、远程访问、收件箱代收这些云服务的额度，几个项目、几台电脑共用一份。' },
  { key: 'usage', label: '用量', icon: BarChart3, desc: 'agent 每次调用模型的 token 消耗，只记录在这台电脑的 ~/.annulo/usage.jsonl。' },
  { key: 'skills', label: 'Skill', icon: Sparkles, desc: 'skill 是写给 agent 的做事说明，放在 ~/.annulo/skills。只有启用的 skill 会进入 agent 的上下文。' },
  { key: 'mcp', label: 'MCP', icon: Plug, desc: '接入外部工具，比如 Notion、浏览器。' },
  { key: 'secrets', label: '密钥', icon: KeyRound, desc: '外部服务的 key（比如 Perplexity），运营后台的本机函数会用到。密钥存储在本机，不会上传，也不会被 AI 助手读取。' },
  { key: 'connections', label: '连接', icon: Link2, desc: 'creght 账号和外部账号（比如 Google Search Console、Facebook）。授权只存在这台电脑上。' },
  { key: 'browser', label: '浏览器', icon: Globe, desc: 'Annulo 支持两种浏览器操作：\n1. Annulo 自己开的浏览器：每个账号一个、互相隔离，第一次在弹出的窗口里登录一次。\n2. 你自己的 Chrome：直接用你已经登录好的账号，适合豆包、DeepSeek 这类只有一个账号的网站。\n第一种无需配置，第二种需要安装插件并授权。' },
  { key: 'schedules', label: '定时任务', icon: Clock, desc: '运营后台里定时跑的任务：同步数据、拉取询盘、发布排期的内容。Annulo 开着的时候按时执行。' },
  { key: 'inboxes', label: '收件箱', icon: Inbox, desc: '外面发进来的东西（网站表单、Stripe、GitHub 这类 webhook）：收下来、发通知、交给项目里的处理函数。这里是当前项目的。' },
  { key: 'remote', label: '远程访问', icon: MonitorSmartphone, desc: '在手机、别的电脑上用这台电脑的 Annulo（用 creght 账号登录）。' },
  { key: 'domain', label: '域名', icon: ShieldCheck, desc: '给这个 Annulo 绑一个域名，Annulo 自己申请 HTTPS 证书、自动续期，不用另外配代理。' },
  { key: 'nodes', label: '节点', icon: Network, desc: '服务器上没有浏览器：服务端部署的 Annulo 借用户电脑上的浏览器做社媒发布、采集。电脑上的 Annulo 主动连到服务器，不需要公网地址。' },
  { key: 'members', label: '成员', icon: Users, desc: '谁能用这个 Annulo、是什么角色。只读的人只能看后台和数据；成员能用助手（助手能在服务器上执行命令），要谨慎给。' },
  { key: 'language', label: '语言', icon: Languages, desc: '界面用中文还是英文。默认跟随系统。' },
  { key: 'about', label: '关于', icon: Info, desc: '当前版本，检查有没有新版本。' },
] as const

/** 设置：/_shuttle/settings，#llm / #mcp 切换小节 */
export default function SettingsPage({ onSaved, creght, user, remote }: { onSaved: () => void; creght: boolean; user?: ServerUser; remote?: boolean }) {
  const [, hash] = useView()
  // 订阅：连着 creght、这个账号能用 Annulo 的云服务（内测期间不在名单里的不显示）才有这一节
  const [subOK, setSubOK] = useState(false)
  useEffect(() => {
    if (creght) subscriptionAvailable().then(setSubOK)
  }, [creght])
  // 远程访问只对连了 creght 的有意义（没连的在「连接」里连，docs/annulo-plan.md 第 2 步）；creght 区域在「连接」的 creght 卡片里选
  // 服务端部署（有 user）：成员只给管理员看；没有用户自己的浏览器、不用远程访问（本来就在服务器上）
  const sections = SECTIONS.filter((s) => {
    if (s.key === 'members' || s.key === 'domain') return user?.role === 'admin' // 服务端部署才有，只给管理员
    if (s.key === 'subscription') return creght && subOK && (!user || user.role === 'admin') // 服务端部署：订阅是管理员连的账号的
    if (s.key === 'nodes') return !user || user.role === 'admin' // 服务器上只给管理员；本机版都有
    if (user && (s.key === 'browser' || s.key === 'remote')) return false
    if (remote && s.key === 'remote') return false // 远程打开的：开关隧道、确认设备只能在电脑上做
    return creght || s.key !== 'remote'
  })
  const cur = sections.find((s) => '#' + s.key === hash) ?? sections[0] // 没指定就是「项目」
  return (
    // 按自己的宽度排版（对话面板拉宽、窗口变窄时主区域会很窄）：窄了导航变成顶上一排
    <div className="@container flex h-full min-h-0 w-full overflow-hidden bg-background">
      <div className="flex min-h-0 w-full flex-col @3xl:flex-row">
        <nav className="scroll-thin flex shrink-0 gap-1 overflow-x-auto border-b border-border px-3 py-2 @3xl:block @3xl:w-56 @3xl:space-y-1 @3xl:overflow-visible @3xl:border-r @3xl:border-b-0 @3xl:px-4 @3xl:py-3.5">
          {sections.map((s) => (
            <button
              key={s.key}
              onClick={() => navigate('settings', '#' + s.key)}
              className={cn(
                'flex h-9 shrink-0 items-center justify-start gap-2 rounded-lg border px-2.5 py-2 text-sm whitespace-nowrap transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 @3xl:w-full',
                s.key === cur.key ? 'border-sidebar-border/50 bg-sidebar-accent text-foreground' : 'border-transparent text-muted-foreground hover:bg-sidebar-accent/70 hover:text-foreground',
              )}
            >
              <s.icon className="size-[18px]" />
              {t(s.label)}
            </button>
          ))}
        </nav>
        <div className="scroll-thin min-h-0 min-w-0 flex-1 overflow-y-auto bg-muted/40 p-4 @3xl:p-6">
          <div className="mx-auto w-full max-w-3xl space-y-6">
            <div className="space-y-2">
              <h3 className="text-base font-semibold tracking-tight">{t(cur.label)}</h3>
              <p className="text-sm whitespace-pre-line text-muted-foreground">
                {/* 服务端部署：授权存在服务器上，不是「这台电脑」 */}
                {user && cur.key === 'connections' ? t('creght 账号和外部账号（比如 Google Search Console、Facebook）。授权存在这台服务器上。') : t(cur.desc)}
              </p>
            </div>
            {cur.key === 'backend' ? (
              <div className="space-y-5">
                {/* 新建或切换项目后关掉设置、直接进到那个项目的后台 */}
                <BackendPicker onDone={() => location.replace(hrefOf('backend'))} />
                <OfflineCard />
                <TemplateCard />
                <PluginCard />
                <InstructionsCard />
              </div>
            ) : cur.key === 'domain' ? (
              <DomainSettings />
            ) : cur.key === 'nodes' ? (
              <NodeSettings server={!!user} />
            ) : cur.key === 'members' && user ? (
              <MembersSettings me={user} />
            ) : cur.key === 'llm' ? (
              <LLMSettings onSaved={onSaved} />
            ) : cur.key === 'usage' ? (
              <UsagePage />
            ) : cur.key === 'skills' ? (
              <SkillSettings />
            ) : cur.key === 'secrets' ? (
              <SecretSettings />
            ) : cur.key === 'connections' ? (
              <ConnectionSettings />
            ) : cur.key === 'browser' ? (
              <BrowserSettings />
            ) : cur.key === 'schedules' ? (
              <ScheduleSettings />
            ) : cur.key === 'subscription' ? (
              <SubscriptionSettings server={!!user} />
            ) : cur.key === 'inboxes' ? (
              <InboxSettings />
            ) : cur.key === 'remote' ? (
              <RemoteSettings />
            ) : cur.key === 'language' ? (
              <LanguageSettings onSaved={onSaved} />
            ) : cur.key === 'about' ? (
              user ? <ServerVersion /> : <AboutSettings />
            ) : (
              <MCPSettings />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
