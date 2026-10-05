import { useEffect, useRef, useState } from 'react'
import { Heart, ThumbsDown, ThumbsUp } from 'lucide-react'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

type Rating = 'up' | 'down'

/**
 * AI 回复底部的点赞 / 点踩。不打扰：平时很淡，鼠标移到这条回复上才明显，不弹表单。
 * 点了之后按钮保持选中，再点一次取消；旁边冒出一个小气泡（带小动画）两三秒后自己消失。
 * 评价由 ChatPanel 发给服务端：记进对话文件，整理成摘要发给 creght（internal/server/feedback.go）。
 */
export default function Feedback({ rating, onRate }: { rating?: Rating; onRate: (r: Rating | 'none') => void }) {
  const [bubble, setBubble] = useState<{ kind: Rating; key: number; leaving: boolean } | null>(null)
  const timers = useRef<number[]>([])
  useEffect(() => () => timers.current.forEach(clearTimeout), [])

  const click = (r: Rating) => {
    const next = rating === r ? 'none' : r
    onRate(next)
    timers.current.forEach(clearTimeout)
    if (next === 'none') {
      setBubble(null)
      return
    }
    const key = Date.now()
    setBubble({ kind: r, key, leaving: false })
    timers.current = [
      window.setTimeout(() => setBubble((b) => (b && b.key === key ? { ...b, leaving: true } : b)), 2600),
      window.setTimeout(() => setBubble((b) => (b && b.key === key ? null : b)), 2900),
    ]
  }

  const btn = (r: Rating, Icon: typeof ThumbsUp, label: string) => (
    <button
      type="button"
      onClick={() => click(r)}
      aria-label={label}
      aria-pressed={rating === r}
      title={label}
      className={cn(
        'flex size-6 items-center justify-center rounded-md outline-none transition-[opacity,color,background-color] hover:bg-accent focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50',
        rating === r ? 'text-primary-text opacity-100' : 'text-muted-foreground opacity-30 group-hover/msg:opacity-70 hover:!opacity-100',
      )}
    >
      <Icon className={cn('size-3.5', rating === r && 'fill-current/20')} />
    </button>
  )

  return (
    <div className="relative flex shrink-0 items-center gap-0.5 pr-1">
      {btn('up', ThumbsUp, t('有帮助'))}
      {btn('down', ThumbsDown, t('没帮上忙'))}
      {bubble && <Bubble key={bubble.key} kind={bubble.kind} leaving={bubble.leaving} />}
    </div>
  )
}

function Bubble({ kind, leaving }: { kind: Rating; leaving: boolean }) {
  return (
    <div
      role="status"
      className="absolute right-0 bottom-full z-20 mb-2 flex w-64 items-center gap-3 rounded-2xl border border-border bg-popover px-3 py-2.5 text-popover-foreground shadow-lg"
      style={{ animation: leaving ? 'fb-out .3s ease-in forwards' : 'fb-pop .28s cubic-bezier(.2,.9,.3,1.3)' }}
    >
      {kind === 'up' ? <Cheer /> : <Cry />}
      <p className="text-[13px] leading-snug">{kind === 'up' ? t('感谢你的认可，我们会做得越来越好') : t('我们已经收到你的反馈，下次再来试试，说不定我们会做得更好')}</p>
      {/* 小尾巴，指向按钮 */}
      <span className="absolute -bottom-1.5 right-5 size-3 rotate-45 border-r border-b border-border bg-popover" />
    </div>
  )
}

/** 点赞：大拇指弹一下，冒出三颗小爱心 */
function Cheer() {
  return (
    <span className="relative flex size-10 shrink-0 items-center justify-center rounded-full bg-primary/12 text-primary-text">
      <ThumbsUp className="size-5" style={{ animation: 'fb-bounce .9s ease-in-out .1s' }} />
      {[
        { dx: '-14px', delay: '.15s', left: '10%' },
        { dx: '2px', delay: '.35s', left: '45%' },
        { dx: '14px', delay: '.55s', left: '75%' },
      ].map((h, i) => (
        <Heart
          key={i}
          className="absolute top-1 size-2.5 fill-rose-500 text-rose-500 opacity-0"
          style={{ left: h.left, ['--dx' as string]: h.dx, animation: `fb-heart 1.1s ease-out ${h.delay} forwards` }}
        />
      ))}
    </span>
  )
}

/** 点踩：一张哭脸，两颗眼泪往下掉 */
function Cry() {
  return (
    <span className="relative flex size-10 shrink-0 items-center justify-center">
      <svg viewBox="0 0 40 40" className="size-10" style={{ animation: 'fb-sob .5s ease-in-out 3' }} aria-hidden="true">
        <circle cx="20" cy="20" r="17" className="fill-amber-300 dark:fill-amber-400" />
        {/* 闭着的眼睛（往下弯）和撇下去的嘴 */}
        <path d="M11.5 16.5 q3 2.6 6 0 M22.5 16.5 q3 2.6 6 0" className="fill-none stroke-amber-900" strokeWidth="1.8" strokeLinecap="round" />
        <path d="M14 28.5 q6 -5 12 0" className="fill-none stroke-amber-900" strokeWidth="1.8" strokeLinecap="round" />
        {/* 眼泪 */}
        <path d="M13 19.5 q-1.6 2.6 0 3.6 q1.6 -1 0 -3.6z" className="fill-sky-400" style={{ transformOrigin: '13px 19.5px', animation: 'fb-tear 1s ease-in .2s infinite' }} />
        <path d="M27 19.5 q-1.6 2.6 0 3.6 q1.6 -1 0 -3.6z" className="fill-sky-400" style={{ transformOrigin: '27px 19.5px', animation: 'fb-tear 1s ease-in .7s infinite' }} />
      </svg>
    </span>
  )
}
