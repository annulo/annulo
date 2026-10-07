import { useEffect, useRef, useState } from 'react'
import { Plus, X } from 'lucide-react'
import { attachBackendHistory } from '@/lib/backendHistory'
import { cn } from '@/lib/utils'
import { t } from '@/lib/i18n'

// 运营后台的多个标签页：每个标签一个 iframe，都常驻（切走再回来不重新加载），右侧助手共用一个。
const MOD = /Mac/.test(navigator.platform) ? '⌘' : 'Ctrl+'

export type BackendTab = { id: string; src: string }

/** 新标签要打开的后台地址：本机的完整 URL、外壳地址（/_shuttle/?path=…）都换成 iframe 用的路径 */
export function backendPath(value: string) {
  try {
    const url = new URL(value, location.href)
    if (url.origin !== location.origin) return null
    if (url.pathname.startsWith('/_shuttle')) return url.searchParams.get('path') || '/'
    return url.pathname + url.search + url.hash
  } catch {
    return null
  }
}

/** 一个标签的 iframe：挂鼠标前进/后退（各标签各记各的）和快捷键；不是当前标签时隐藏 */
export function BackendFrame({
  tab,
  active,
  project,
  loggedIn,
  onFrame,
  onKey,
}: {
  tab: BackendTab
  active: boolean
  project?: string
  loggedIn?: boolean
  onFrame: (id: string, el: HTMLIFrameElement | null) => void
  onKey: (e: KeyboardEvent) => void
}) {
  const ref = useRef<HTMLIFrameElement>(null)
  const key = useRef(onKey)
  key.current = onKey

  useEffect(() => {
    onFrame(tab.id, ref.current)
    return () => onFrame(tab.id, null)
  }, [tab.id, onFrame])

  useEffect(() => {
    if (!ref.current || !project) return
    return attachBackendHistory(ref.current)
  }, [project, loggedIn])

  // 焦点在 iframe 里时按键发给 iframe：每次加载完给它的窗口挂上
  useEffect(() => {
    const f = ref.current
    if (!f) return
    const onKeyDown = (e: KeyboardEvent) => key.current(e)
    let fw: Window | null = null
    const attach = () => {
      try {
        fw?.removeEventListener('keydown', onKeyDown)
        fw = f.contentWindow
        fw?.addEventListener('keydown', onKeyDown)
      } catch {
        fw = null
      }
    }
    attach()
    f.addEventListener('load', attach)
    return () => {
      f.removeEventListener('load', attach)
      try {
        fw?.removeEventListener('keydown', onKeyDown)
      } catch {
        // iframe 已经换了页面
      }
    }
  }, [])

  return <iframe ref={ref} data-backend src={tab.src} hidden={!active} title={t('运营后台')} className="size-full border-0 bg-background" />
}

/** 顶栏的标签条：标题取后台页面的 document.title，只有一个标签时不显示关闭 */
export function TabStrip({
  tabs,
  active,
  frames,
  onSelect,
  onClose,
  onNew,
}: {
  tabs: BackendTab[]
  active: string
  frames: Map<string, HTMLIFrameElement>
  onSelect: (id: string) => void
  onClose: (id: string) => void
  onNew: () => void
}) {
  const [titles, setTitles] = useState<Record<string, string>>({})
  useEffect(() => {
    const read = () => {
      const next: Record<string, string> = {}
      for (const [id, f] of frames) {
        try {
          next[id] = f.contentDocument?.title.trim() ?? ''
        } catch {
          next[id] = ''
        }
      }
      setTitles((old) => (JSON.stringify(old) === JSON.stringify(next) ? old : next))
    }
    read()
    const timer = window.setInterval(read, 1000)
    return () => clearInterval(timer)
  }, [frames, tabs])

  // 换了当前标签（⌘ 数字、新开的）：滚到看得见的地方
  const scroller = useRef<HTMLDivElement>(null)
  const hide = useRef(0)
  useEffect(() => () => clearTimeout(hide.current), [])
  useEffect(() => {
    scroller.current?.querySelector(`[data-tab="${active}"]`)?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [active, tabs.length])

  return (
    // 像 Chrome 的标签：36px 高、贴着顶栏底边、文字居中；当前标签用页面色，和下面的后台连成一片，两个下角外面带弧。
    // 标签多了不再往窄里挤：每个最窄 112px，放不下就横向滚动，「+」固定在右边。
    // 滚动条要在标签上方（放底下会隔开当前标签和后台）：滚动区上下翻转、每个标签再翻回来；标签贴底，上面 4px 放滚动条。
    // 左右留 8px 放当前标签下角外侧的弧，不然它伸出去，只有一个标签也会出现滚动条
    <div className="flex h-10 min-w-0 flex-1 items-center self-end">
      <div
        ref={scroller}
        role="tablist"
        onScroll={(e) => {
          // 滚动时显示滚动条，停下 0.8 秒后隐藏
          const el = e.currentTarget
          el.dataset.scrolling = ''
          clearTimeout(hide.current)
          hide.current = window.setTimeout(() => delete el.dataset.scrolling, 800)
        }}
        onWheel={(e) => {
          if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) e.currentTarget.scrollLeft += e.deltaY
        }}
        className="tab-scroll flex h-full min-w-0 [transform:rotateX(180deg)] items-start px-2 overflow-x-auto overflow-y-hidden"
      >
        {tabs.map((tab, i) => {
          const title = titles[tab.id] || t('后台') // 页面没设标题（助手常忘了设）时显示「后台」，不留空白标签；页面标题怎么设见内置 annulo skill 的 pages.md
          const on = tab.id === active
          // 两个没选中的标签之间画一道竖线；挨着当前标签的不画
          const divider = !on && tabs[i + 1] && tabs[i + 1].id !== active
          return (
            <div
              key={tab.id}
              data-tab={tab.id}
              role="tab"
              aria-selected={on}
              title={title || undefined}
              onMouseDown={(e) => {
                // 中键关掉标签，和浏览器一样
                if (e.button === 1 && tabs.length > 1) {
                  e.preventDefault()
                  onClose(tab.id)
                }
              }}
              onClick={() => onSelect(tab.id)}
              className={cn(
                'group relative flex h-9 w-40 min-w-28 shrink [transform:rotateX(180deg)] cursor-pointer items-center gap-1.5 rounded-t-lg pr-2 pl-3.5 text-xs font-medium transition-colors',
                on
                  ? 'z-10 bg-background font-semibold text-foreground before:absolute before:bottom-0 before:-left-2 before:size-2 before:bg-[radial-gradient(circle_at_0_0,transparent_8px,var(--background)_8px)] after:absolute after:-right-2 after:bottom-0 after:size-2 after:bg-[radial-gradient(circle_at_100%_0,transparent_8px,var(--background)_8px)]'
                  : 'text-muted-foreground hover:text-foreground',
                divider && 'after:absolute after:top-2.5 after:right-0 after:h-4 after:w-px after:bg-border',
                tabs.length === 1 && 'pr-3',
              )}
            >
              <span className="min-w-0 flex-1 truncate">{title}</span>
              {tabs.length > 1 && (
                <button
                  type="button"
                  aria-label={t('关闭标签页')}
                  onClick={(e) => {
                    e.stopPropagation()
                    onClose(tab.id)
                  }}
                  className={cn('flex size-5 shrink-0 items-center justify-center rounded-md hover:bg-foreground/10', !on && 'opacity-0 group-hover:opacity-100')}
                >
                  <X className="size-3" />
                </button>
              )}
            </div>
          )
        })}
      </div>
      <button
        type="button"
        onClick={onNew}
        aria-label={t('新建标签页')}
        title={t('新建标签页（{mod}T，或按住 {mod} 点后台里的链接）', { mod: MOD })}
        className="mt-1 ml-1 flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <Plus className="size-4" />
      </button>
    </div>
  )
}
