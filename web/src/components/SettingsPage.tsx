import { Bot, Clock, Globe, Info, KeyRound, Languages, LayoutTemplate, Link2, MonitorSmartphone, Plug, Sparkles } from 'lucide-react'
import LLMSettings from '@/components/LLMSettings'
import MCPSettings from '@/components/MCPSettings'
import SkillSettings from '@/components/SkillSettings'
import SecretSettings from '@/components/SecretSettings'
import ConnectionSettings from '@/components/ConnectionSettings'
import ScheduleSettings from '@/components/ScheduleSettings'
import BackendPicker from '@/components/BackendPicker'
import TemplateCard from '@/components/TemplateCard'
import InstructionsCard from '@/components/InstructionsCard'
import OfflineCard from '@/components/OfflineCard'
import LanguageSettings from '@/components/LanguageSettings'
import RemoteSettings from '@/components/RemoteSettings'
import CreghtClusterSelect from '@/components/CreghtClusterSelect'
import AboutSettings from '@/components/AboutSettings'
import { hrefOf, navigate, useView } from '@/lib/router'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

const SECTIONS = [
  { key: 'backend', label: '项目', icon: LayoutTemplate, desc: '一个项目就是一个业务，项目之间互相隔离：各有各的数据、后台页面和对话历史。在这里切换、改名或新建。' },
  { key: 'llm', label: '模型', icon: Bot, desc: '接自己的服务商（key 只存在本机 ~/.annulo/config.json），或者用这台电脑上的 Claude Code / Codex；连上 creght 也能用平台的模型，按 AI 积分计费。' },
  { key: 'skills', label: 'Skill', icon: Sparkles, desc: 'skill 是写给 agent 的做事说明，放在 ~/.annulo/skills。只有启用的 skill 会进入 agent 的上下文。' },
  { key: 'mcp', label: 'MCP', icon: Plug, desc: '接入外部工具，比如 Notion、浏览器。' },
  { key: 'secrets', label: '密钥', icon: KeyRound, desc: '外部服务的 key（比如 Perplexity），运营后台的本机函数会用到。密钥存储在本机，不会上传，也不会被 AI 助手读取。' },
  { key: 'connections', label: '连接', icon: Link2, desc: 'creght 账号和外部账号（比如 Google Search Console）。连上 creght 能用平台的模型和 creght 网站；外部账号授权后，运营后台的本机函数就能读它的数据。授权只存在这台电脑上。' },
  { key: 'schedules', label: '定时任务', icon: Clock, desc: '运营后台里定时跑的任务：同步数据、拉取询盘、发布排期的内容。Annulo 开着的时候按时执行。' },
  { key: 'remote', label: '远程访问', icon: MonitorSmartphone, desc: '运营后台在手机、别的电脑上也能打开（用 creght 账号登录）。看数据、改内容不需要这台电脑；发布、采集要用这台电脑的浏览器和密钥，打开下面的开关后，这台电脑开着时，在别处也能点。' },
  { key: 'language', label: '语言', icon: Languages, desc: '界面用中文还是英文。默认跟随系统。' },
  { key: 'creght', label: 'creght 区域', icon: Globe, desc: 'creght.cn、creght.com、talizen.com 是分开的：账号、项目、模板各是各的。换区域后在那个区域登录、打开或新建项目；原来的项目留在本机，切回来还能打开。' },
  { key: 'about', label: '关于', icon: Info, desc: '当前版本，检查有没有新版本。' },
] as const

/** 设置：/_shuttle/settings，#llm / #mcp 切换小节 */
export default function SettingsPage({ onSaved, creght }: { onSaved: () => void; creght: boolean }) {
  const [, hash] = useView()
  // creght 区域、远程访问只对连了 creght 的有意义（没连的在「连接」里连，docs/annulo-plan.md 第 2 步）
  const sections = SECTIONS.filter((s) => creght || (s.key !== 'creght' && s.key !== 'remote'))
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
              <p className="text-sm text-muted-foreground">{t(cur.desc)}</p>
            </div>
            {cur.key === 'backend' ? (
              <div className="space-y-5">
                {/* 新建或切换项目后关掉设置、直接进到那个项目的后台 */}
                <BackendPicker onDone={() => location.replace(hrefOf('backend'))} />
                <OfflineCard />
                <TemplateCard />
                <InstructionsCard />
              </div>
            ) : cur.key === 'llm' ? (
              <LLMSettings onSaved={onSaved} />
            ) : cur.key === 'skills' ? (
              <SkillSettings />
            ) : cur.key === 'secrets' ? (
              <SecretSettings />
            ) : cur.key === 'connections' ? (
              <ConnectionSettings />
            ) : cur.key === 'schedules' ? (
              <ScheduleSettings />
            ) : cur.key === 'remote' ? (
              <RemoteSettings />
            ) : cur.key === 'language' ? (
              <LanguageSettings onSaved={onSaved} />
            ) : cur.key === 'about' ? (
              <AboutSettings />
            ) : cur.key === 'creght' ? (
              <section className="rounded-xl border border-border bg-background p-4">
                <div className="max-w-72">
                  <CreghtClusterSelect onChanged={() => location.reload()} />
                </div>
              </section>
            ) : (
              <MCPSettings />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
