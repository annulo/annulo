import { useEffect, useMemo, useRef, useState } from 'react'
import { useChat } from '@ai-sdk/react'
import { DefaultChatTransport, type DynamicToolUIPart, type FileUIPart, type UIMessage } from 'ai'
import Markdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import {
  ArrowUp,
  ArrowUpRight,
  Brain,
  Check,
  ChevronDown,
  Clock,
  History,
  ImagePlus,
  ListPlus,
  Loader,
  MessageCircleQuestion,
  MessageSquare,
  Pencil,
  Plus,
  Square,
  Trash2,
  Wrench,
  X,
} from 'lucide-react'
import { API_HEADERS, CHAT_API, getJSON, post, type AgentMeta, type ChatDetail, type ChatSummary, type Status, type Assistant } from '@/lib/api'
import { fmtAgo, fmtDuration, fmtTokens } from '@/lib/format'
import { useTheme } from '@/lib/theme'
import { Button } from '@/components/ui/button'
import UserInputCard from '@/components/UserInputCard'
import Feedback from '@/components/Feedback'
import ChatChart from '@/components/ChatChart'
import { ZoomImage } from '@/components/ui/lightbox'
import { ACCEPT, AttachmentStrip, MessageImages, useAttachments } from '@/components/Attachments'
import { ModelMenu } from '@/components/ModelMenu'
import { Tip } from '@/components/ui/tip'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'
import { randomId } from '@/lib/id'
import { normalizeChatMessages } from '@/lib/chatMessages'
import { toast } from 'sonner'
import { msgType } from '../lib/brand'

type Msg = UIMessage<AgentMeta>
type Part = Msg['parts'][number]
type LLM = Status['llm']
// 助手忙时发的消息。steered：已经交给服务端插进这一轮，等当前这步完成后送达模型
type Queued = { id: string; text: string; files: FileUIPart[]; steered?: boolean }

// 当前对话按项目记：切回来还是上次那段
const currentKey = (ws: string) => (ws ? `shuttle.chat.${ws}` : 'shuttle.chat')
// 在后台跑完、还没回去看的对话
const unreadKey = (ws: string) => `shuttle.unread.${ws}`

type ChatStatus = 'running' | 'asking'
const WIDTH_KEY = 'shuttle.chatWidth'

const newId = () => randomId().slice(0, 24)

function store(key: string, v?: string) {
  try {
    if (v === undefined) return localStorage.getItem(key) || ''
    localStorage.setItem(key, v)
  } catch {
    // 存不了就不记
  }
  return ''
}

/**
 * 右侧 AI 面板，样式照搬 talizen 编辑器的聊天面板（RightPanel）。
 * 外壳管当前对话、历史和宽度；每段对话是一个 <ChatSession>，换对话就换 key 重新挂载。
 */
export default function ChatPanel(props: { workspace: string; onTurnDone: (m: Msg) => void; llm?: LLM; onOpenSettings: () => void; onLLMChanged: () => void; assistant?: Assistant }) {
  const [chat, setChat] = useState<{ id: string; initial: Msg[]; feedback?: Record<string, 'up' | 'down'> } | null>(null)
  // 当前对话的标题：第一句话发出后模型起一个短名字（随这一轮推过来），用户也可以点标题改
  const [title, setTitle] = useState('')
  // 历史弹层用 fixed 定位浮在运营后台 iframe 上面，面板拖窄了也不会被裁；锚点是历史按钮
  const [historyAt, setHistoryAt] = useState<DOMRect | null>(null)
  const historyOpen = !!historyAt
  // 当前对话的上下文占用（ChatSession 算好报上来），顶栏画成进度圈，点开看详情
  const [ctx, setCtx] = useState<{ used: number; total: number } | null>(null)
  const [ctxAt, setCtxAt] = useState<DOMRect | null>(null)
  const [width, setWidth] = useState(() => Number(store(WIDTH_KEY)) || 380)

  // 多段对话可以同时跑：轮询哪些在跑，离开时还在跑、后来跑完了的记成「有新回复」
  const [statuses, setStatuses] = useState<Record<string, ChatStatus>>({})
  const [unread, setUnread] = useState<string[]>(() => {
    try {
      return JSON.parse(store(unreadKey(props.workspace)) || '[]')
    } catch {
      return []
    }
  })
  const chatRef = useRef(chat)
  chatRef.current = chat
  const prev = useRef<Record<string, ChatStatus>>({})
  useEffect(() => {
    let alive = true
    const tick = () =>
      getJSON<{ chats: Record<string, ChatStatus> }>('agent/running')
        .then((r) => {
          if (!alive) return
          const done = Object.keys(prev.current).filter((id) => !r.chats[id] && id !== chatRef.current?.id)
          if (done.length) setUnread((u) => [...new Set([...u, ...done])])
          prev.current = r.chats
          setStatuses(r.chats)
        })
        .catch(() => {})
    tick()
    const t = setInterval(tick, 2500)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [])
  useEffect(() => {
    store(unreadKey(props.workspace), JSON.stringify(unread))
  }, [unread])
  useEffect(() => {
    if (chat) setUnread((u) => (u.includes(chat.id) ? u.filter((x) => x !== chat.id) : u))
  }, [chat?.id])
  // 「有新回复」只算还在的对话：删掉的（别处删的、清过的）从记录里去掉。打开时、每次列历史时对一遍
  const pruneUnread = (ids: string[]) => setUnread((u) => (u.every((x) => ids.includes(x)) ? u : u.filter((x) => ids.includes(x))))
  useEffect(() => {
    getJSON<{ list: ChatSummary[] }>('agent/chats')
      .then((r) => pruneUnread(r.list.map((c) => c.id)))
      .catch(() => {})
  }, [])
  const others = Object.keys(statuses).filter((id) => id !== chat?.id)
  // 和历史列表一致：在跑、在等回答的不算「有新回复」
  const newReplies = unread.filter((id) => !statuses[id]).length
  const othersAsking = others.some((id) => statuses[id] === 'asking')

  const open = async (id: string) => {
    try {
      const c = await getJSON<ChatDetail>(`agent/chats/${id}`)
      setChat({ id, initial: normalizeChatMessages<Msg>(c.messages), feedback: c.feedback })
      setTitle(c.title)
    } catch {
      setChat({ id: newId(), initial: [] })
      setTitle('')
    }
  }
  // 别处要打开某段对话：外壳里（定时任务的「查看对话」）发 shuttle:open-chat 事件，
  // 运营后台页面（iframe）postMessage({ type: 'shuttle:open-chat', chat_id })
  // 打开后弹吐司告诉用户结果；找不到（被删了）只提示，不动当前对话
  useEffect(() => {
    const openExternal = async (id: string) => {
      try {
        const c = await getJSON<ChatDetail>(`agent/chats/${id}`)
        setChat({ id, initial: normalizeChatMessages<Msg>(c.messages), feedback: c.feedback })
        setTitle(c.title)
        toast.success(t('已在右侧打开这段对话'))
      } catch {
        toast.error(t('找不到这段对话，可能已经删除了'))
      }
    }
    const on = (e: Event) => {
      const id = (e as CustomEvent<string>).detail
      if (id) openExternal(id)
    }
    const onMsg = (e: MessageEvent) => {
      if (e.origin !== location.origin || msgType(e.data) !== 'shuttle:open-chat' || typeof e.data.chat_id !== 'string') return
      openExternal(e.data.chat_id)
    }
    window.addEventListener('shuttle:open-chat', on)
    window.addEventListener('message', onMsg)
    return () => {
      window.removeEventListener('shuttle:open-chat', on)
      window.removeEventListener('message', onMsg)
    }
  }, [])
  const fresh = () => {
    setChat({ id: newId(), initial: [] })
    setTitle('')
  }

  useEffect(() => {
    const cur = store(currentKey(props.workspace))
    if (cur) open(cur)
    else fresh()
  }, [])
  useEffect(() => {
    if (chat) store(currentKey(props.workspace), chat.id)
  }, [chat?.id])

  // 左边缘拖动调宽度。左边是运营后台 iframe，鼠标进了 iframe 事件就到不了外层，所以把指针捕获在拖动条上
  const startResize = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault()
    const el = e.currentTarget
    const id = e.pointerId
    el.setPointerCapture(id)
    const x0 = e.clientX
    const w0 = width
    const move = (ev: PointerEvent) => setWidth(Math.min(560, Math.max(300, w0 + x0 - ev.clientX)))
    const up = () => {
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', up)
      el.removeEventListener('pointercancel', up)
      if (el.hasPointerCapture(id)) el.releasePointerCapture(id)
      document.body.style.cursor = ''
      setWidth((w) => {
        store(WIDTH_KEY, String(w))
        return w
      })
    }
    document.body.style.cursor = 'col-resize'
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }

  return (
    <aside className="relative flex h-full min-w-0 shrink-0 flex-col overflow-hidden border-l border-border bg-background" style={{ width }}>
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label={t('调整对话面板宽度')}
        onPointerDown={startResize}
        className="absolute inset-y-0 left-0 z-20 w-1 cursor-col-resize transition-colors hover:bg-primary/40"
      />
      <div className="flex h-12 shrink-0 items-center justify-between gap-2 border-b border-border bg-background px-3 text-xs">
        <ChatTitle key={chat?.id} chatId={chat?.id} title={title} fallback={props.assistant?.name || t('助手')} onRenamed={setTitle} />
        <div className="flex shrink-0 items-center gap-1">
          {ctx && (
            <Tip label={ctxAt ? '' : t('上下文 {pct}%', { pct: ctxPct(ctx) })}>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={(e) => setCtxAt(ctxAt ? null : e.currentTarget.getBoundingClientRect())}
                aria-label={t('上下文')}
                data-context-toggle
                aria-expanded={!!ctxAt}
                className={cn('text-muted-foreground hover:bg-accent', ctxAt && 'bg-accent')}
              >
                <ContextRing {...ctx} />
              </Button>
            </Tip>
          )}
          <Tip label={t('新对话')}>
            <Button variant="ghost" size="icon-sm" onClick={fresh} aria-label={t('新对话')} className="text-muted-foreground hover:bg-accent">
              <Plus className="size-3.5" />
            </Button>
          </Tip>
          <Tip label={historyOpen ? '' : others.length ? (othersAsking ? t('历史对话（另有 {n} 段在运行，有在等你回答的）', { n: others.length }) : t('历史对话（另有 {n} 段在运行）', { n: others.length })) : newReplies ? t('历史对话（{n} 段有新回复）', { n: newReplies }) : t('历史对话')}>
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={(e) => setHistoryAt(historyAt ? null : e.currentTarget.getBoundingClientRect())}
              aria-label={t('历史对话')}
              data-history-toggle
              aria-expanded={historyOpen}
              className={cn('text-muted-foreground hover:bg-accent', historyOpen && 'bg-accent text-foreground')}
            >
              <span className="relative">
                <History className="size-3.5" />
                {(others.length > 0 || newReplies > 0) && (
                  <span
                    aria-hidden
                    className={cn('absolute -top-1 -right-1 size-2 rounded-full ring-2 ring-background', othersAsking ? 'bg-warn' : others.length ? 'animate-pulse bg-primary' : 'bg-primary')}
                  />
                )}
              </span>
            </Button>
          </Tip>
        </div>
      </div>
      {ctxAt && ctx && <ContextPopover anchor={ctxAt} {...ctx} onClose={() => setCtxAt(null)} />}
      {historyAt && (
        <HistoryPopover
          anchor={historyAt}
          current={chat?.id}
          statuses={statuses}
          unread={unread}
          onClose={() => setHistoryAt(null)}
          onPick={(id) => {
            setHistoryAt(null)
            if (id !== chat?.id) open(id)
          }}
          onLoaded={pruneUnread}
          onDeleted={(id) => {
            setUnread((u) => u.filter((x) => x !== id))
            if (id === chat?.id) fresh()
          }}
        />
      )}
      {chat && <ChatSession key={chat.id} chatId={chat.id} initial={chat.initial} initialFeedback={chat.feedback} onTitle={setTitle} onContext={setCtx} {...props} />}
    </aside>
  )
}

function HistoryPopover({
  anchor,
  current,
  statuses,
  unread,
  onPick,
  onLoaded,
  onDeleted,
  onClose,
}: {
  anchor: DOMRect
  current?: string
  statuses: Record<string, ChatStatus>
  unread: string[]
  onPick: (id: string) => void
  onLoaded: (ids: string[]) => void
  onDeleted: (id: string) => void
  onClose: () => void
}) {
  const [list, setList] = useState<ChatSummary[] | null>(null)
  const ref = useRef<HTMLDivElement>(null)
  const load = () =>
    getJSON<{ list: ChatSummary[] }>('agent/chats').then((r) => {
      setList(r.list)
      onLoaded(r.list.map((c) => c.id))
    })
  // 删除要点两次：第一次按钮变成「删除？」，再点才删；移开或 3 秒不点就恢复
  const [confirming, setConfirming] = useState('')
  useEffect(() => {
    if (!confirming) return
    const t = setTimeout(() => setConfirming(''), 3000)
    return () => clearTimeout(t)
  }, [confirming])
  useEffect(() => {
    load()
    const f = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node) && !(e.target as HTMLElement).closest('[data-history-toggle]')) onClose()
    }
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    // 点进 iframe 时外层收不到 mousedown，但窗口会失焦；窗口尺寸变了锚点就不准了，一并关掉
    const b = () => setTimeout(() => document.activeElement instanceof HTMLIFrameElement && onClose())
    document.addEventListener('mousedown', f)
    document.addEventListener('keydown', k)
    window.addEventListener('blur', b)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', f)
      document.removeEventListener('keydown', k)
      window.removeEventListener('blur', b)
      window.removeEventListener('resize', onClose)
    }
  }, [])
  return (
    <div
      ref={ref}
      style={{ top: anchor.bottom + 6, right: Math.max(window.innerWidth - anchor.right, 12) }}
      className="scroll-thin fixed z-50 max-h-[min(60vh,560px)] w-80 max-w-[calc(100vw-1.5rem)] overflow-y-auto rounded-xl border border-border bg-popover p-2 shadow-xl"
    >
      <div className="px-2 pt-1 pb-2 text-[10px] font-semibold tracking-wide text-muted-foreground">{t('历史对话')}</div>
      {list === null ? (
        <div className="flex justify-center py-6">
          <Loader className="size-3.5 animate-spin text-muted-foreground" />
        </div>
      ) : list.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('还没有历史对话')}</div>
      ) : (
        list.map((c) => (
          <div key={c.id} onMouseLeave={() => confirming === c.id && setConfirming('')} className={cn('group flex items-center gap-2 rounded-lg px-2.5 py-2 hover:bg-accent', c.id === current && 'bg-accent')}>
            <button onClick={() => onPick(c.id)} className="min-w-0 flex-1 text-left">
              <div className="flex min-w-0 items-center gap-1.5">
                {unread.includes(c.id) && !statuses[c.id] && <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-label={t('有新回复')} />}
                <span className="truncate text-[13px] font-medium">{c.title || t('（无标题）')}</span>
              </div>
              <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-muted-foreground">
                {statuses[c.id] === 'running' ? (
                  <span className="inline-flex items-center gap-1 font-semibold text-primary-text">
                    <Loader className="size-3 animate-spin" /> {t('运行中')}
                  </span>
                ) : statuses[c.id] === 'asking' ? (
                  <span className="inline-flex items-center gap-1 font-semibold text-warn">
                    <MessageCircleQuestion className="size-3" /> {t('等你回答')}
                  </span>
                ) : unread.includes(c.id) ? (
                  <span className="font-semibold text-primary-text">{t('有新回复')}</span>
                ) : (
                  <span>{fmtAgo(c.updated_at)}</span>
                )}
                <span>· {t('{n} 条消息', { n: c.messages })}</span>
              </div>
            </button>
            {confirming === c.id ? (
              <button
                onClick={async () => {
                  setConfirming('')
                  await fetch(`/_shuttle/api/agent/chats/${c.id}`, {
                    method: 'DELETE',
                    headers: API_HEADERS,
                  })
                  onDeleted(c.id)
                  load()
                }}
                className="inline-flex h-7 shrink-0 items-center gap-1 rounded-md bg-destructive/10 px-2 text-[11px] font-semibold text-destructive hover:bg-destructive/20"
              >
                <Trash2 className="size-3.5" /> {t('删除？')}
              </button>
            ) : (
              <button
                onClick={() => setConfirming(c.id)}
                className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-background hover:text-destructive focus-visible:opacity-100"
                aria-label={t('删除这段对话')}
                title={t('删除这段对话')}
              >
                <Trash2 className="size-3.5" />
              </button>
            )}
          </div>
        ))
      )}
    </div>
  )
}

function ChatSession({
  chatId,
  initial,
  initialFeedback,
  onTurnDone,
  llm,
  onOpenSettings,
  onLLMChanged,
  onTitle,
  onContext,
  assistant,
}: {
  assistant?: Assistant
  chatId: string
  initial: Msg[]
  initialFeedback?: Record<string, 'up' | 'down'>
  onTitle: (title: string) => void
  onContext: (ctx: { used: number; total: number } | null) => void
  onTurnDone: (m: Msg) => void
  llm?: LLM
  onOpenSettings: () => void
  onLLMChanged: () => void
}) {
  const transport = useMemo(
    () =>
      new DefaultChatTransport<Msg>({
        api: CHAT_API,
        headers: API_HEADERS,
        // 服务端保存了完整上下文，只发最后一条
        prepareSendMessagesRequest: ({ id, messages }) => ({
          body: { id, message: messages[messages.length - 1] },
        }),
      }),
    [],
  )
  const { messages, sendMessage, status, stop, error } = useChat<Msg>({
    id: chatId,
    messages: initial,
    transport,
    // agent 在服务端独立运行，刷新页面不会停；挂载时接上正在进行的那一轮
    resume: true,
    onFinish: ({ message }) => onTurnDone(message),
    // 服务端给新对话起好名字时推一个 data-title（transient，不进消息）
    onData: (part) => {
      const t = part.type === 'data-title' ? (part.data as { title?: string })?.title : undefined
      if (t) onTitle(t)
    },
  })
  const [input, setInput] = useState('')
  const [stuck, setStuck] = useState(true)
  // 用户给回复点的赞 / 踩：先改界面（气泡马上出来），再告诉服务端（它记进对话文件、整理摘要发给 creght）；失败不打扰用户
  const [feedback, setFeedback] = useState<Record<string, 'up' | 'down'>>(initialFeedback ?? {})
  const rate = (id: string, r: 'up' | 'down' | 'none') => {
    setFeedback((f) => {
      const n = { ...f }
      if (r === 'none') delete n[id]
      else n[id] = r
      return n
    })
    post(`agent/chats/${chatId}/feedback`, { message_id: id, rating: r }).catch(() => {})
  }
  const scroller = useRef<HTMLDivElement>(null)
  const lastTop = useRef(0)
  const box = useRef<HTMLTextAreaElement>(null)
  const picker = useRef<HTMLInputElement>(null)
  const composing = useRef(false)
  const composedAt = useRef(0)
  const atts = useAttachments()
  const [dragging, setDragging] = useState(false)
  // 助手忙的时候发的消息先排队，这一轮结束后依次发出（和 Claude Code 一样）
  const [queue, setQueue] = useState<Queued[]>([])
  const busy = status === 'submitted' || status === 'streaming'

  // 贴底时跟着滚；用户往上翻了就不打扰
  useEffect(() => {
    const el = scroller.current
    if (!el) return
    if (stuck) el.scrollTop = el.scrollHeight
    // 内容不够一屏（比如折叠了、换了短内容）就算停在底部，「下面有新内容」的提示条不该出现
    // 只认真到底了（被内容变短顶到底）；离底几像素是用户刚往上滚，不能又贴回去
    else if (el.scrollHeight - el.clientHeight - el.scrollTop < 1) setStuck(true)
  }, [messages, status, stuck])

  // 输入框自动长高，最多 12rem
  useEffect(() => {
    const el = box.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 192) + 'px'
  }, [input])

  const ctx = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      // 跑完的用最终统计；正在跑的用每次模型请求后推过来的实时占用
      const md = messages[i].metadata
      if (md?.stats?.contextWindow) return { used: md.stats.contextTokens, total: md.stats.contextWindow }
      if (md?.live?.contextWindow) return { used: md.live.contextTokens, total: md.live.contextWindow }
    }
    return null
  }, [messages])
  useEffect(() => {
    onContext(ctx)
  }, [ctx?.used, ctx?.total])
  useEffect(() => () => onContext(null), [])

  // 最后一条是在等回答的问卷：输入框提示可以直接回复（发出去就是普通消息）
  const asking = useMemo(() => {
    const last = messages[messages.length - 1]
    if (busy || last?.role !== 'assistant') return false
    return last.parts?.some((x) => x.type === 'dynamic-tool' && x.toolName === 'request_user_input' && x.state === 'output-available' && String(x.output ?? '').includes('waiting_for_user')) ?? false
  }, [messages, busy])

  const send = (text: string) => {
    const msg = text.trim()
    if (atts.uploading) return
    const files = atts.parts
    if (!msg && !files.length) return
    setInput('')
    atts.clear()
    // 新对话：模型起好名字之前先用第一句话当标题（和服务端存的一样）
    if (!messages.length) onTitle(msg ? (msg.length > 30 ? msg.slice(0, 30) + '…' : msg) : t('图片'))
    if (busy || queue.length) {
      const item: Queued = { id: newId(), text: msg, files }
      setQueue((q) => [...q, item])
      if (busy) steer(item)
      return
    }
    setStuck(true)
    sendMessage(files.length ? { text: msg, files } : { text: msg })
  }

  // 插话：助手正在跑时，消息立刻交给服务端，当前这步（工具调用）完成、下一次请求模型前送达。
  // 服务端说这段对话已经不在跑了（409），就留在队列里，这一轮结束后当普通消息发。
  const steer = (item: Queued) => {
    const parts = [...item.files, ...(item.text ? [{ type: 'text' as const, text: item.text }] : [])]
    post('agent/steer', { chat_id: chatId, message: { id: item.id, role: 'user', parts } })
      .then(() => setQueue((q) => q.map((x) => (x.id === item.id ? { ...x, steered: true } : x))))
      .catch(() => {})
  }

  // 已经送达模型的插话：出现在助手消息里的 data-steer 片段
  const delivered = useMemo(() => {
    const ids = new Set<string>()
    for (const m of messages) for (const p of m.parts) if ((p.type as string) === 'data-steer' && 'id' in p && p.id) ids.add(p.id as string)
    return ids
  }, [messages])
  useEffect(() => {
    if (queue.some((q) => delivered.has(q.id))) setQueue((q) => q.filter((x) => !delivered.has(x.id)))
  }, [delivered])

  // 一轮正常结束：没来得及插进去的（模型在写最后一段时发的）当普通消息依次发；出错了先停住，让用户决定要不要接着发
  useEffect(() => {
    const rest = queue.filter((q) => !delivered.has(q.id))
    if (status !== 'ready' || !rest.length) return
    const [next, ...others] = rest
    setQueue(others.map((q) => ({ ...q, steered: false })))
    setStuck(true)
    sendMessage(next.files.length ? { text: next.text, files: next.files } : { text: next.text })
  }, [status, queue, delivered])

  // 排队的消息拿回输入框（编辑，或者停止时全部退回）
  const unqueue = (items: Queued[]) => {
    if (!items.length) return
    const ids = new Set(items.map((q) => q.id))
    setQueue((q) => q.filter((x) => !ids.has(x.id)))
    setInput((cur) => [...items.map((q) => q.text), cur].filter(Boolean).join('\n\n'))
    atts.restore(items.flatMap((q) => q.files))
    box.current?.focus()
  }
  const hasDraft = (!!input.trim() || atts.parts.length > 0) && !atts.uploading
  // 运营后台页面上的「交给助手」按钮：postMessage 过来一句话，空闲就直接发，忙就先填进输入框
  const sendRef = useRef(send)
  sendRef.current = send
  const busyRef = useRef(busy)
  busyRef.current = busy
  useEffect(() => {
    const f = (e: MessageEvent) => {
      if (e.origin !== location.origin || msgType(e.data) !== 'shuttle:ask' || typeof e.data.text !== 'string') return
      sendRef.current(e.data.text) // 忙的时候会进队列
    }
    window.addEventListener('message', f)
    return () => window.removeEventListener('message', f)
  }, [])

  const onStop = async () => {
    // 停止时排队的消息退回输入框，不自动发出去
    unqueue(queue)
    // 断开连接不会停 agent，要先告诉服务端
    await post('agent/abort', { chat_id: chatId }).catch(() => {})
    stop()
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col p-3">
      <div
        ref={scroller}
        // 流式输出时每帧都在贴底：触控板往上一点点还在底部 40px 内，会被立刻拉回去。
        // 所以用户往上滚（滚轮向上、或 scrollTop 变小）就马上松开，回到底部附近再贴上
        onWheel={(e) => {
          if (e.deltaY < 0) setStuck(false)
        }}
        onScroll={(e) => {
          const el = e.currentTarget
          const gap = el.scrollHeight - el.scrollTop - el.clientHeight
          const up = el.scrollTop < lastTop.current && gap > 1
          lastTop.current = el.scrollTop
          setStuck(!up && gap < 40)
        }}
        className="scroll-thin relative -mr-2 min-h-0 flex-1 overflow-y-auto pr-2"
      >
        {messages.length === 0 ? (
          <EmptyState llm={llm} assistant={assistant} onOpenSettings={onOpenSettings} onPick={send} />
        ) : (
          <div className="mb-4 space-y-3">
            {messages.map((m, i) =>
              m.role === 'user' ? (
                <div key={m.id} className="flex justify-end">
                  <div className="max-w-[90%] rounded-lg bg-muted px-2 py-2 text-sm whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
                    <MessageImages parts={m.parts.filter((p): p is FileUIPart => p.type === 'file')} />
                    {m.parts.some((p) => p.type === 'text' && p.text) && (
                      <div className={cn('px-2', m.parts.some((p) => p.type === 'file') && 'pt-2')}>{m.parts.map((p) => (p.type === 'text' ? p.text : '')).join('')}</div>
                    )}
                  </div>
                </div>
              ) : (
                <AssistantMessage
                  key={m.id}
                  m={m}
                  live={busy && i === messages.length - 1}
                  onAnswer={i === messages.length - 1 && !busy ? send : undefined}
                  // 紧跟着的用户消息：问卷没填、直接在对话里回复的，问卷卡片按它给选中的选项打勾
                  reply={messages[i + 1]?.role === 'user' ? messages[i + 1].parts.map((p) => (p.type === 'text' ? p.text : '')).join('') : undefined}
                  rating={feedback[m.id]}
                  onRate={(r) => rate(m.id, r)}
                />
              ),
            )}
            {status === 'submitted' && messages[messages.length - 1]?.role !== 'assistant' && (
              <div className="px-2 pb-2">
                <Loader className="size-3.5 animate-spin text-muted-foreground" />
              </div>
            )}
            {error && <div className="w-full rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">{error.message}</div>}
          </div>
        )}
        {busy && !stuck && <div className="pointer-events-none sticky bottom-0 h-1 animate-pulse rounded-full bg-primary/90" />}
      </div>

      <div className="shrink-0">
        {/* 排队的消息、待发的图片放在输入框上面 */}
        <div className="mb-2 grid gap-2 empty:hidden">
          <QueueList
            items={queue}
            paused={status === 'error'}
            onResume={() => {
              const [next, ...rest] = queue
              setQueue(rest)
              setStuck(true)
              sendMessage(next.files.length ? { text: next.text, files: next.files } : { text: next.text })
            }}
            onEdit={(q) => unqueue([q])}
            onRemove={(id) => setQueue((xs) => xs.filter((x) => x.id !== id))}
          />
          <AttachmentStrip items={atts.items} onRemove={atts.remove} />
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            send(input)
          }}
          onDragOver={(e) => {
            if (!e.dataTransfer.types.includes('Files')) return
            e.preventDefault()
            setDragging(true)
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false)
          }}
          onDrop={(e) => {
            if (!e.dataTransfer.files.length) return
            e.preventDefault()
            setDragging(false)
            atts.add(e.dataTransfer.files)
            box.current?.focus()
          }}
          className={cn('relative grid gap-2 rounded-xl', dragging && 'ring-2 ring-primary/60 ring-offset-4 ring-offset-background')}
        >
          {dragging && (
            <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl bg-background/80 text-sm font-semibold text-foreground">{t('松开鼠标，把图片加进消息')}</div>
          )}
          <textarea
            ref={box}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onPaste={(e) => {
              // 截图、复制的图片直接贴进来；纯文字照常粘贴
              const files = [...e.clipboardData.files].filter((f) => f.type.startsWith('image/'))
              if (files.length) {
                e.preventDefault()
                atts.add(files)
              }
            }}
            onCompositionStart={() => (composing.current = true)}
            onCompositionEnd={() => {
              composing.current = false
              composedAt.current = Date.now()
            }}
            onKeyDown={(e) => {
              if (e.key !== 'Enter' || e.shiftKey) return
              // 中文输入法按回车是上屏候选词，不是发送。Safari / WKWebView（Mac App）里这次按键的 isComposing
              // 已经是 false、compositionend 还先到，所以自己记：组词中、刚结束组词、keyCode 229 都不算发送
              if (e.nativeEvent.isComposing || composing.current || e.keyCode === 229 || Date.now() - composedAt.current < 100) return
              e.preventDefault()
              send(input)
            }}
            rows={2}
            placeholder={asking ? t('回答上面的问题，或者直接在这里说') : busy ? t('接着说，助手做完手头这一步就会看到') : assistant?.name ? t('告诉{name}你想做什么', { name: assistant.name }) : t('告诉助手你想做什么')}
            aria-label={t('给{name}的消息', { name: assistant?.name || t('助手') })}
            className="max-h-48 min-h-16 w-full resize-none overflow-y-auto rounded-xl border border-input bg-muted py-3 pr-3 pl-3 text-sm shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-4 focus-visible:ring-ring/10"
          />
          <div className="flex items-center justify-between gap-2">
            <div className="flex min-w-0 items-center gap-1">
              <ModelMenu llm={llm} onChanged={onLLMChanged} onManage={onOpenSettings} />
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <Tip label={atts.full ? t('最多 8 张图片') : t('添加图片（也可以直接粘贴或拖进来）')} side="top">
                <button
                  type="button"
                  onClick={() => picker.current?.click()}
                  disabled={atts.full}
                  aria-label={t('添加图片')}
                  className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40"
                >
                  <ImagePlus className="size-4" />
                </button>
              </Tip>
              <input
                ref={picker}
                type="file"
                accept={ACCEPT}
                multiple
                hidden
                onChange={(e) => {
                  if (e.target.files) atts.add(e.target.files)
                  e.target.value = ''
                  box.current?.focus()
                }}
              />
              {busy && hasDraft && (
                <Tip label={t('插话：助手做完手头这一步就会看到（回车也可以）')} side="top">
                  <button
                    type="submit"
                    aria-label={t('插话')}
                    className="inline-flex size-8 items-center justify-center rounded-full border border-border bg-background text-foreground transition-all hover:bg-accent active:scale-95"
                  >
                    <ListPlus className="size-4" />
                  </button>
                </Tip>
              )}
              {busy ? (
                <button
                  type="button"
                  onClick={onStop}
                  aria-label={t('停止')}
                  className="relative inline-flex size-8 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-lg transition-all hover:opacity-90 active:scale-95"
                >
                  <Square className="size-3.5 fill-current" />
                  <span className="pointer-events-none absolute inset-0 animate-spin rounded-full border-2 border-white border-t-transparent border-r-transparent border-l-transparent" />
                </button>
              ) : (
                <button
                  type="submit"
                  disabled={!hasDraft}
                  aria-label={t('发送')}
                  className="inline-flex size-8 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-lg transition-all hover:scale-110 hover:opacity-90 active:scale-95 disabled:pointer-events-none disabled:opacity-40"
                >
                  <ArrowUp className="size-4" strokeWidth={3} />
                </button>
              )}
            </div>
          </div>
        </form>
      </div>
    </div>
  )
}

function EmptyState({ llm, assistant, onOpenSettings, onPick }: { llm?: LLM; assistant?: Assistant; onOpenSettings: () => void; onPick: (t: string) => void }) {
  const [theme] = useTheme()
  return (
    <div className="flex flex-col items-center justify-center space-y-8 py-8 text-center">
      <img src={theme === 'dark' ? '/_shuttle/annulo-dark.svg' : '/_shuttle/annulo-light.svg'} alt="" className="h-12" />
      <div className="space-y-2">
        <h2 className="text-xl font-bold tracking-tight">{assistant?.name ? t('你好，我是{name}', { name: assistant.name }) : t('你好，我是你的助手')}</h2>
        {assistant?.intro && <p className="text-sm leading-relaxed font-medium text-muted-foreground">{assistant.intro}</p>}
      </div>
      <div className="w-full space-y-2 pt-2">
        {llm && !llm.ready && (
          <button
            onClick={onOpenSettings}
            className="group flex w-full items-center justify-between rounded-2xl border border-destructive/30 bg-destructive/10 p-4 text-left text-xs font-bold text-destructive"
          >
            {t('先配置模型：填 Base URL、模型名和 API Key')}
            <ArrowUpRight className="size-3.5" />
          </button>
        )}
        {(assistant?.suggestions ?? []).map((s) => (
          <button
            key={s}
            onClick={() => onPick(s)}
            className="group flex w-full items-center justify-between rounded-2xl border border-border p-4 text-left text-xs font-bold text-muted-foreground transition-all hover:border-primary hover:text-foreground"
          >
            {s}
            <ArrowUpRight className="size-3.5 opacity-0 transition-opacity group-hover:opacity-100" />
          </button>
        ))}
      </div>
    </div>
  )
}

/** 面板顶部的对话标题：点一下改名（Enter 保存、Esc 取消）。还没发过消息的新对话显示助手的名字（fallback）、不能改 */
function ChatTitle({ chatId, title, fallback, onRenamed }: { chatId?: string; title: string; fallback: string; onRenamed: (t: string) => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState('')
  const save = async () => {
    const t = draft.trim()
    setEditing(false)
    if (!chatId || !t || t === title) return
    try {
      await post(`agent/chats/${chatId}`, { title: t }, 'PATCH')
      onRenamed(t)
      setErr('')
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  if (editing)
    return (
      <input
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.nativeEvent.isComposing) save()
          if (e.key === 'Escape') setEditing(false)
        }}
        aria-label={t('对话标题')}
        maxLength={60}
        className="mx-1 h-7 min-w-0 flex-1 rounded-md border border-input bg-input-background px-2 text-xs font-semibold outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      />
    )
  return (
    <button
      type="button"
      disabled={!title}
      onClick={() => {
        setDraft(title)
        setEditing(true)
      }}
      title={err || (title ? t('{title}（点击改名）', { title }) : undefined)}
      className={cn(
        'flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left font-semibold outline-none enabled:cursor-pointer enabled:hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
        err && 'text-destructive',
      )}
    >
      <MessageSquare className="size-3.5 shrink-0" />
      <span className="truncate">{title || fallback}</span>
    </button>
  )
}

const ctxPct = ({ used, total }: { used: number; total: number }) => (total > 0 ? Math.min(100, Math.round((used / total) * 1000) / 10) : 0)
const ctxColor = (pct: number) => (pct > 80 ? 'text-destructive' : pct > 50 ? 'text-warn' : 'text-primary')

/** 顶栏的上下文进度圈：灰色底圈 + 按占用比例画的一段弧 */
function ContextRing(ctx: { used: number; total: number }) {
  const pct = ctxPct(ctx)
  const r = 6
  const c = 2 * Math.PI * r
  return (
    <svg viewBox="0 0 16 16" className="size-4 -rotate-90" aria-hidden>
      <circle cx="8" cy="8" r={r} fill="none" strokeWidth="2" className="stroke-foreground/15" />
      <circle cx="8" cy="8" r={r} fill="none" strokeWidth="2" strokeLinecap="round" stroke="currentColor" strokeDasharray={`${(Math.max(pct, 2) / 100) * c} ${c}`} className={ctxColor(pct)} />
    </svg>
  )
}

/** 点进度圈弹出的详情：用了多少、上限多少、满了会怎样。关法和历史弹层一样 */
function ContextPopover({ anchor, used, total, onClose }: { anchor: DOMRect; used: number; total: number; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const pct = ctxPct({ used, total })
  useEffect(() => {
    const f = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node) && !(e.target as HTMLElement).closest('[data-context-toggle]')) onClose()
    }
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    // 点进 iframe 时外层收不到 mousedown，但窗口会失焦；窗口尺寸变了锚点就不准了，一并关掉
    const b = () => setTimeout(() => document.activeElement instanceof HTMLIFrameElement && onClose())
    document.addEventListener('mousedown', f)
    document.addEventListener('keydown', k)
    window.addEventListener('blur', b)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', f)
      document.removeEventListener('keydown', k)
      window.removeEventListener('blur', b)
      window.removeEventListener('resize', onClose)
    }
  }, [])
  return (
    <div
      ref={ref}
      role="dialog"
      aria-label={t('上下文')}
      style={{ top: anchor.bottom + 6, right: Math.max(window.innerWidth - anchor.right, 12) }}
      className="fixed z-50 w-64 max-w-[calc(100vw-1.5rem)] rounded-xl border border-border bg-popover p-3 text-xs shadow-xl"
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-semibold">{t('上下文')}</span>
        <span className="font-semibold tabular-nums">{pct}%</span>
      </div>
      <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted">
        <div className={cn('h-full rounded-full bg-current', ctxColor(pct))} style={{ width: `${pct}%` }} />
      </div>
      <div className="mt-2 text-muted-foreground tabular-nums">
        {t('已用 {used} / 上限 {total} token', { used: fmtTokens(used), total: fmtTokens(total) })}
      </div>
      <p className="mt-2 leading-relaxed text-muted-foreground">{t('当前上下文大约占用的 token；接近上限时会自动压缩早先的对话')}</p>
    </div>
  )
}

// 连续的思考和工具调用归成一组步骤，文字单独成段
type Block = { kind: 'text'; text: string } | { kind: 'steps'; parts: Part[] } | { kind: 'ask'; part: DynamicToolUIPart } | { kind: 'steer'; text: string; files: FileUIPart[] }

function blocksOf(parts: Part[]): Block[] {
  const out: Block[] = []
  for (const p of parts) {
    if (p.type === 'text') {
      if (p.text.trim()) out.push({ kind: 'text', text: p.text })
    } else if ((p.type as string) === 'data-steer') {
      const d = ((p as { data?: { text?: string; files?: FileUIPart[] } }).data ?? {}) as { text?: string; files?: FileUIPart[] }
      out.push({ kind: 'steer', text: d.text ?? '', files: d.files ?? [] })
    } else if (p.type === 'dynamic-tool' && p.toolName === 'request_user_input') {
      out.push({ kind: 'ask', part: p })
    } else if (p.type === 'reasoning' || p.type === 'dynamic-tool') {
      const last = out[out.length - 1]
      if (last?.kind === 'steps') last.parts.push(p)
      else out.push({ kind: 'steps', parts: [p] })
    }
  }
  return out
}

// 回答里的 ```chart 代码块画成图（格式见 ChatChart），其他代码块照常；图片可以点开。live 时没写完的块显示「正在画图」
function mdComponents(live: boolean): Components {
  return {
    pre({ node, children, ...rest }) {
      const code = node?.children[0]
      const cls = code?.type === 'element' ? code.properties.className : undefined
      if (Array.isArray(cls) && cls.includes('language-chart') && code?.type === 'element') {
        const src = code.children.map((c) => (c.type === 'text' ? c.value : '')).join('')
        return <ChatChart src={src} live={live} />
      }
      return <pre {...rest}>{children}</pre>
    },
    // 回答里的图片点开全屏看
    img({ src, alt }) {
      if (typeof src !== 'string' || !src) return null
      return <ZoomImage src={src} alt={alt || t('图片')} wrapClassName="my-1 inline-block max-w-full" className="max-h-72 max-w-full rounded-md border border-border/60" />
    },
  }
}
const liveMd = mdComponents(true)
const doneMd = mdComponents(false)

// onAnswer：这是对话的最后一条、助手没在跑时才有，上面的问卷可以作答（答案作为新消息发出）
function AssistantMessage({ m, live, onAnswer, reply, rating, onRate }: { m: Msg; live: boolean; onAnswer?: (text: string) => void; reply?: string; rating?: 'up' | 'down'; onRate: (r: 'up' | 'down' | 'none') => void }) {
  const blocks = blocksOf(m.parts)
  // 跑完、有文字回复的才能评价
  const rateable = !live && blocks.some((b) => b.kind === 'text' && b.text.trim())
  return (
    <div className="group/msg flex justify-start">
      <div className="w-full space-y-1 py-2 text-sm">
        {blocks.map((b, i) =>
          b.kind === 'text' ? (
            <div key={i} className="md px-2 py-1">
              <Markdown remarkPlugins={[remarkGfm]} components={live ? liveMd : doneMd}>
                {b.text}
              </Markdown>
            </div>
          ) : b.kind === 'ask' ? (
            <UserInputCard key={i} part={b.part} onAnswer={onAnswer} reply={reply} />
          ) : b.kind === 'steer' ? (
            // 用户在这一轮中间插的话，出现在它被送达模型的位置
            <div key={i} className="flex justify-end py-1">
              <div className="max-w-[90%] rounded-lg bg-muted px-2 py-2 text-sm whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
                <MessageImages parts={b.files} />
                {b.text && <div className={cn('px-2', b.files.length > 0 && 'pt-2')}>{b.text}</div>}
              </div>
            </div>
          ) : (
            <StepGroup key={i} parts={b.parts} live={live && i === blocks.length - 1} />
          ),
        )}
        <div className="flex items-center justify-between gap-2">
          <MessageFooter meta={m.metadata} live={live} />
          {rateable && <Feedback rating={rating} onRate={onRate} />}
        </div>
      </div>
    </div>
  )
}

const TOOL_TITLES: Record<string, string> = {
  bash: '执行命令',
  read: '读取文件',
  write: '写入文件',
  edit: '编辑文件',
  ls: '列出文件',
  grep: '搜索内容',
  find: '查找文件',
  list_channels: '查看渠道',
  channel_visits: '查看访问数据',
  db_query: '查业务表',
  db_aggregate: '统计业务表',
}

function toolTitle(name: string) {
  const m = name.match(/^mcp__(.+?)__(.+)$/)
  if (m) return `${m[1][0].toUpperCase()}${m[1].slice(1)} · ${m[2]}`
  return TOOL_TITLES[name] ? t(TOOL_TITLES[name]) : name
}

function toolArg(input: unknown) {
  const a = (input ?? {}) as Record<string, unknown>
  const v = a.command ?? a.path ?? a.file_path ?? a.pattern ?? a.query ?? a.url ?? a.id ?? a.channel_id ?? a.table
  return typeof v === 'string' ? v.replace(/^cd \S+ && /, '') : ''
}

function StepGroup({ parts, live }: { parts: Part[]; live: boolean }) {
  const [open, setOpen] = useState(false)
  const tools = parts.filter((p): p is DynamicToolUIPart => p.type === 'dynamic-tool')
  const last = tools[tools.length - 1]
  const thinking = !last && parts.some((p) => p.type === 'reasoning' && p.state === 'streaming')
  return (
    <div className="w-full py-1 pr-2 pl-2">
      {/* 展开时标题行粘在对话区顶部：往下翻长长的工具列表时，随时能点它收起 */}
      <button
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          'group flex w-full items-center gap-1.5 text-xs text-foreground opacity-80 hover:opacity-100',
          open && 'sticky top-0 z-10 -mx-2 w-[calc(100%+1rem)] bg-background px-2 py-1.5 opacity-100',
        )}
      >
        {last ? <ToolStatusIcon part={last} live={live} /> : <Brain className="size-4 p-0.5 opacity-70" />}
        <span className="shrink-0 font-medium opacity-90">{last ? toolTitle(last.toolName) : thinking ? t('思考中…') : t('思考过程')}</span>
        {last && <span className="min-w-0 truncate font-mono text-[11px] opacity-50">{toolArg(last.input)}</span>}
        <span className="ml-auto flex shrink-0 items-center gap-1 opacity-60">
          {tools.length > 1 && (
            <>
              <Wrench className="size-3" />
              <span className="tabular-nums">{tools.length}</span>
            </>
          )}
          <ChevronDown className={cn('size-3.5 transition-transform', open && 'rotate-180')} />
        </span>
      </button>
      {open && (
        <div className="mt-1.5 space-y-1.5">
          {parts.map((p, i) =>
            p.type === 'reasoning' ? <ReasoningRow key={i} text={p.text} streaming={p.state === 'streaming'} /> : p.type === 'dynamic-tool' ? <ToolCallRow key={i} part={p} live={live} /> : null,
          )}
        </div>
      )}
    </div>
  )
}

// live：这一轮还在跑。已经结束（停止、出错、服务重启）的轮次里没收到结果的工具显示「已停止」，不再转圈
function ToolStatusIcon({ part, live }: { part: DynamicToolUIPart; live: boolean }) {
  if (part.state === 'output-error')
    return (
      <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-red-500/15 text-red-500">
        <X size={11} strokeWidth={3} />
      </span>
    )
  if (part.state === 'output-available')
    return (
      <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-green-500/15 text-green-600 dark:text-green-500">
        <Check size={11} strokeWidth={3} />
      </span>
    )
  if (!live)
    return (
      <span className="flex size-4 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground" title={t('已停止，没有执行完')}>
        <Square className="size-2 fill-current" />
      </span>
    )
  return (
    <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground">
      <Loader className="size-3.5 animate-spin" />
    </span>
  )
}

function ToolCallRow({ part, live }: { part: DynamicToolUIPart; live: boolean }) {
  const [open, setOpen] = useState(false)
  const output = part.state === 'output-available' ? String(part.output ?? '') : part.state === 'output-error' ? part.errorText : ''
  return (
    <div className="min-w-0 space-y-1 text-foreground">
      <button onClick={() => setOpen((o) => !o)} className="flex w-full items-start gap-1.5 text-left text-xs">
        <ToolStatusIcon part={part} live={live} />
        <span className="shrink-0 font-medium opacity-90">{toolTitle(part.toolName)}</span>
        <span className="min-w-0 font-mono text-[11px] break-all opacity-50">{toolArg(part.input)}</span>
      </button>
      {open && (
        <pre className="scroll-thin ml-[22px] max-h-60 overflow-auto rounded-lg border border-border/30 bg-card p-2.5 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-muted-foreground">
          {JSON.stringify(part.input, null, 2)}
          {output ? '\n\n' + output : ''}
        </pre>
      )}
    </div>
  )
}

function ReasoningRow({ text, streaming }: { text: string; streaming: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button onClick={() => setOpen((o) => !o)} className="group flex w-full items-center gap-1.5 text-xs text-foreground opacity-70 hover:opacity-100">
        <Brain className="size-4 p-0.5 opacity-70" />
        {streaming ? t('思考中…') : t('思考过程')}
        <ChevronDown className={cn('size-3.5 opacity-60 transition-transform', open && 'rotate-180')} />
      </button>
      {open && <div className="md pt-1.5 pl-[22px] text-xs whitespace-pre-wrap opacity-90">{text}</div>}
    </div>
  )
}

function MessageFooter({ meta, live }: { meta?: AgentMeta; live: boolean }) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => setNow(Date.now()), 250)
    return () => clearInterval(t)
  }, [live])
  if (live && meta?.startedAt) {
    return (
      <div className="flex items-center gap-1.5 px-2 pt-1 text-xs text-muted-foreground">
        {/* 和上面工具行的状态图标同样大小的框，图标上下对齐 */}
        <span className="flex size-4 shrink-0 items-center justify-center">
          <Loader className="size-3.5 animate-spin" />
        </span>
        <span className="tabular-nums">{fmtDuration(now - meta.startedAt)}</span>
      </div>
    )
  }
  const st = meta?.stats
  if (!st) return meta?.aborted ? <div className="px-2 pt-1 text-[11px] text-muted-foreground">{t('已停止')}</div> : null
  const cached = st.cacheReadTokens + st.cacheWriteTokens
  return (
    <div
      className="flex flex-wrap gap-x-2.5 px-2 pt-1 text-[11px] text-muted-foreground tabular-nums"
      title={t('输入 {i} · 输出 {o} · 缓存读 {cr} · 缓存写 {cw}', { i: st.inputTokens, o: st.outputTokens, cr: st.cacheReadTokens, cw: st.cacheWriteTokens })}
    >
      <span>{fmtDuration(st.durationMs)}</span>
      <span>↑ {fmtTokens(st.inputTokens)}</span>
      <span>↓ {fmtTokens(st.outputTokens)}</span>
      {cached > 0 && <span>{t('缓存 {n}', { n: fmtTokens(cached) })}</span>}
      {st.cost ? <span>${st.cost.toFixed(4)}</span> : null}
      {meta?.aborted && <span>{t('已停止')}</span>}
    </div>
  )
}

/** 输入框上方的排队消息：这一轮结束后按顺序发出 */
function QueueList({ items, paused, onResume, onEdit, onRemove }: { items: Queued[]; paused: boolean; onResume: () => void; onEdit: (q: Queued) => void; onRemove: (id: string) => void }) {
  if (!items.length) return null
  return (
    <div className="rounded-xl border border-border bg-background/60 p-1.5">
      <div className="flex items-center justify-between gap-2 px-1.5 pt-0.5 pb-1 text-[11px] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5 font-semibold">
          <Clock className="size-3" />
          {paused
            ? t('上一轮出错了，{n} 条排队的消息还没发', { n: items.length })
            : items.every((q) => q.steered)
              ? t('{n} 条插话，助手做完手头这一步就会看到', { n: items.length })
              : t('{n} 条待发，这一轮结束后依次发出', { n: items.length })}
        </span>
        {paused && (
          <button type="button" onClick={onResume} className="rounded px-1.5 py-0.5 font-semibold text-primary-text outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50">
            {t('继续发送')}
          </button>
        )}
      </div>
      <ul className="space-y-0.5">
        {items.map((q) => (
          <li key={q.id} className="group flex items-start gap-2 rounded-lg px-1.5 py-1 hover:bg-accent/60">
            <span className="line-clamp-2 min-w-0 flex-1 text-[13px] leading-snug whitespace-pre-wrap text-foreground/90 [overflow-wrap:anywhere]">
              {q.text || <span className="text-muted-foreground">{t('（只有图片）')}</span>}
              {q.files.length > 0 && <span className="ml-1.5 text-[11px] text-muted-foreground">{t('+{n} 张图', { n: q.files.length })}</span>}
            </span>
            {q.steered ? (
              <span className="shrink-0 pt-0.5 text-[11px] text-muted-foreground">{t('等待插入')}</span>
            ) : (
              <span className="flex shrink-0 items-center opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                <button
                  type="button"
                  onClick={() => onEdit(q)}
                  aria-label={t('拿回输入框编辑')}
                  title={t('拿回输入框编辑')}
                  className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-background hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  <Pencil className="size-3" />
                </button>
                <button
                  type="button"
                  onClick={() => onRemove(q.id)}
                  aria-label={t('移出队列')}
                  title={t('移出队列')}
                  className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-background hover:text-destructive focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  <X className="size-3.5" />
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}
