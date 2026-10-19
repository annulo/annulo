import { t } from '@/lib/i18n'
// 所有接口都在 /_shuttle/api 下，必须带 X-Shuttle 头（服务端靠它挡跨站请求）。
const BASE = '/_shuttle/api/'
const H = { 'X-Shuttle': '1' }

export type Status = {
  version: string
  /** 能力版本（本机函数能用的原语每加一样 +1）；project_requires 是当前项目要求的、比它高时才有 */
  api?: number
  project_requires?: number
  setup_required?: boolean
  creght: { api_host: string; logged_in: boolean; user?: { id: string; username: string; email: string } }
  /** 用户选了不登录、离线使用 */
  offline_mode?: boolean
  /** 本机能不能用 git（模板、插件、项目历史；Windows 上助手跑命令也要它的 bash）；windows 决定提示怎么装 */
  git?: { ok: boolean; error: string; windows: boolean }
  /** offline：离线项目（数据只在本机，没有 creght 站点） */
  backend?: { project_id: string; site_id: string; dir: string; preview_url: string; offline?: boolean; name?: string }
  llm: { ready: boolean; model: string; error: string; reasoning: boolean; thinking: Thinking; thinking_levels?: Thinking[]; provider: string }
  /** 设置里的界面语言（auto 跟随系统）和生效的语言；外壳、左侧后台、助手都按 locale */
  language?: 'auto' | 'zh' | 'en'
  locale?: 'zh' | 'en'
  /** 助手面板的名字、介绍、示例问题：项目的 annulo.json / shuttle.json 里写的，没写是通用的 */
  assistant?: Assistant
  /** 服务端部署（ANNULO_MODE=server）：登录的账号；只读角色收起助手和设置 */
  server?: boolean
  /** 经隧道远程打开的（手机上）：收起只在电脑上能用的东西（docs/mobile-remote.md「远程访问：隧道」） */
  remote?: boolean
  /** 服务端部署时能用个人访问令牌连 creght（平台的能力清单里有 pat） */
  creght_pat?: boolean
  /** 能不能用 creght 账号授权（OAuth）连：本机版总能；服务端部署要 https 域名或 localhost */
  creght_oauth?: boolean
  user?: { id: string; name: string; role: 'admin' | 'member' | 'viewer' } | null
  /** 服务端部署给管理员的证书提醒：还没绑域名（IP + 明文）、证书签不下来、快到期 */
  /** 服务端部署给管理员：官网有新版本 */
  update?: { current: string; latest: string } | null
  cert?: { kind: 'no_domain' | 'error' | 'expiring'; domain?: string; message?: string; days_left?: number } | null
}

export type Assistant = { name: string; intro: string; suggestions: string[] }

/** 从失败的响应里取出能看懂的错误：优先 JSON 的 error 字段，不是 JSON 就给原文开头。 */
async function errorOf(r: Response, path: string): Promise<Error> {
  const text = await r.text().catch(() => '')
  let msg = ''
  try {
    const b = JSON.parse(text)
    msg = typeof b.error === 'string' ? b.error : b.error ? JSON.stringify(b.error) : ''
  } catch {
    msg = text.trim().slice(0, 300)
  }
  return new Error(`${msg || t('（响应里没有错误信息）')} · HTTP ${r.status} ${path}`)
}

/** 服务端部署时会话过期（被删了、改了密码）：接口回 401，回到登录页 */
function signedOut(r: Response) {
  if (r.status === 401 && !location.search.includes('invite=')) location.replace('/_shuttle/')
}

export async function getJSON<T>(path: string): Promise<T> {
  const r = await fetch(BASE + path, { headers: H })
  signedOut(r)
  if (!r.ok) throw await errorOf(r, path)
  return (await r.json()) as T
}

export async function post(path: string, body?: unknown, method = 'POST') {
  const r = await fetch(BASE + path, { method, headers: { ...H, 'content-type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  if (!path.startsWith('auth/')) signedOut(r) // 登录页自己处理密码错
  if (!r.ok) throw await errorOf(r, path)
  return r
}

/** 每个项目一个地址（<项目>.localhost:<端口>，服务端 view.go）：响应里带了别的项目的地址（project_url）就跳过去，返回 true；
 *  没带或者就是这个项目，返回 false，调用方照旧原地刷新。 */
export async function followProject(r: Response): Promise<boolean> {
  let url = ''
  try {
    url = ((await r.clone().json()) as { project_url?: string }).project_url ?? ''
  } catch {
    return false
  }
  if (!url) return false
  const u = new URL(url)
  if (u.origin === location.origin) return false
  location.assign(u.origin + '/_shuttle/' + carryTheme())
  return true
}

/** 跳到别的项目时带上选过的主题（每个项目一个源，localStorage 不共用；index.html 读到后记下） */
function carryTheme() {
  try {
    const v = localStorage.getItem('shuttle.theme')
    return v === 'light' || v === 'dark' ? '?theme=' + v : ''
  } catch {
    return ''
  }
}

/** 项目自己的地址（在新窗口打开用）：分源时才有（地址是 <项目>.localhost），否则 null */
export function projectURL(pid: string): string | null {
  const h = location.hostname
  if (h !== 'localhost' && !h.endsWith('.localhost')) return null
  return `${location.protocol}//${pid}.localhost${location.port ? ':' + location.port : ''}/_shuttle/` + carryTheme()
}

/** 对话里的图片先传到本机，返回界面引用它的地址（/_shuttle/uploads/<name>） */
export async function uploadImage(file: Blob): Promise<string> {
  const r = await fetch(BASE + 'agent/uploads', { method: 'POST', headers: { ...H, 'content-type': file.type || 'application/octet-stream' }, body: file })
  if (!r.ok) throw await errorOf(r, 'agent/uploads')
  return ((await r.json()) as { url: string }).url
}

// ---- agent 对话（Vercel AI SDK UI Message Stream 协议）----

export type RunStats = {
  model: string
  durationMs: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cost?: number
  contextTokens: number
  contextWindow: number
}

/** 助手消息的 metadata：start 时带 startedAt，finish 时补上 stats */
export type AgentMeta = { startedAt?: number; stats?: RunStats; error?: string; aborted?: boolean; live?: { contextTokens: number; contextWindow: number } }

export type ChatSummary = { id: string; title: string; updated_at: number; messages: number }

export type ChatDetail = {
  id: string
  title: string
  messages: unknown[]
  /** 消息 id → 用户点的赞 / 踩 */
  feedback?: Record<string, 'up' | 'down'>
}

export const CHAT_API = BASE + 'agent/chat'
export const API_HEADERS = H

export type ProviderConfig = {
  id: string
  name: string
  builtin: boolean // 内置的 creght 平台：用登录的 creght 账号，不用 key
  api: string
  base_url: string
  api_key_env: string
  has_key: boolean
  key_hint: string
  ready: boolean
  error: string
  agent?: boolean // 本机的外部 agent（Claude Code / Codex）
  disabled?: boolean // 用户关掉了：它的模型不出现在列表和选择菜单里
}

export type ModelConfig = {
  id: string
  name: string
  label: string
  provider: string
  builtin: boolean // creght 平台的模型：列表来自平台，不能改
  pricing?: { input: number; cached_input: number; output: number } // 每百万 token 积分（平台模型）
  model: string
  context_window: number
  images: boolean
  reasoning: boolean
  ready: boolean
  error: string
  agent?: boolean // 本机的外部 agent：用它自己的登录和模型
  /** 这个模型支持的思考档位（从低到高）；来源：custom 自己配的 / catalog 模型资料 / default 通用 / agent 本机 agent */
  thinking_levels?: Thinking[]
  thinking_source?: 'custom' | 'catalog' | 'default' | 'agent'
  /** 自己配的档位：档 → 发给接口的值（空 = 发档名） */
  thinking_custom?: Partial<Record<Thinking, string>>
}

/** 本机能当助手的外部 agent：装了的有 path */
export type LocalAgent = { id: string; model_id: string; name: string; path?: string; install: string; models?: { model_id: string; name: string }[] }

/** 思考档位，从低到高；每个模型只支持其中一部分（ModelConfig.thinking_levels） */
export type Thinking = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export type ModelSettings = {
  providers: ProviderConfig[]
  models: ModelConfig[]
  active: string
  /** 用户选的档 */
  thinking: Thinking
  /** 当前模型实际用的档：它没有选的那档时是最接近的 */
  thinking_effective?: Thinking
  thinking_levels: Thinking[]
  /** 新对话自动起名；title_model 是起名用的模型，空 = 跟对话用同一个 */
  auto_title: boolean
  title_model: string
  /** 浏览器助手（本机函数里的 b.act：脚本卡住的一步让模型帮忙）用的模型，空 = 跟对话用同一个 */
  browser_model?: string
  /** 起名、浏览器助手调模型时的思考强度，空 = 不思考 */
  title_thinking?: Thinking | ''
  browser_thinking?: Thinking | ''
  /** 浏览器助手把每一步发给模型的内容、回复和耗时写进运行日志（调试用） */
  browser_debug?: boolean
  agents?: LocalAgent[]
}

/** settings/llm 的返回补齐：列表字段缺了或是 null（老版本后端、没连 creght 也没加服务商）都当空数组，界面直接 .filter / .map */
export function normModelSettings(s: ModelSettings): ModelSettings {
  return { ...s, providers: s.providers ?? [], models: s.models ?? [], thinking_levels: s.thinking_levels ?? [], agents: s.agents ?? [] }
}

// 价格简写：输入 / 缓存 / 输出，每百万 token 的积分
export function priceText(p?: { input: number; cached_input: number; output: number }, short = false) {
  if (!p) return ''
  const n = (v: number) => (v >= 1000 ? `${+(v / 1000).toFixed(1)}k` : `${+v.toFixed(1)}`)
  const v = { i: n(p.input), c: n(p.cached_input || p.input), o: n(p.output) }
  return short ? t('输入 {i} · 缓存 {c} · 输出 {o}', v) : t('输入 {i} · 缓存 {c} · 输出 {o} 积分/百万 token', v)
}

export const THINKING_LABEL: Record<Thinking, string> = { off: '关', minimal: '最低', low: '低', medium: '中', high: '高', xhigh: '很高', max: '最高' }

export type MCPServerStatus = {
  name: string
  transport: 'stdio' | 'http' | 'sse'
  status: 'connecting' | 'needs_auth' | 'connected' | 'failed' | 'disabled'
  error?: string
  auth_url?: string
  authed: boolean
  /** Annulo 内置的（creght）：跟着 设置 → 连接 走，不能删、不用授权，只能停用、启用 */
  managed?: boolean
  tools: MCPToolStatus[]
}

/** tokens 是工具定义大约占的 token 数，每次请求都要带上 */
export type MCPToolStatus = { name: string; enabled: boolean; tokens: number }

export type MCPState = { config: string; file: string; servers: MCPServerStatus[] }

export type MCPServerConfig = {
  type?: 'stdio' | 'http' | 'sse'
  command?: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  headers?: Record<string, string>
  disabled?: boolean
  excludeTools?: string[]
}

export type UsageBucket = {
  requests: number
  input: number
  output: number
  cache_read: number
  cache_write: number
  total: number
  cost: number
}
export type UsageDay = UsageBucket & { day: string }
export type UsageReport = {
  days: number
  totals: UsageBucket
  today: UsageBucket
  daily: UsageDay[]
  heatmap: UsageDay[]
  models: (UsageBucket & { model: string })[]
  hours: number[]
}

export type SkillInfo = {
  name: string
  description: string
  source: 'builtin' | 'workspace' | 'installed' | 'local'
  path: string
  enabled: boolean
  origin?: string
  linked?: boolean
}
export type SkillsState = { skills: SkillInfo[]; local: SkillInfo[] }
