import { useEffect, useState } from 'react'

// App 内更新（electron/updater.cjs）：主进程在后台只检查有没有新版本；有就顶栏出「更新到 <版本>」，
// 用户在更新弹窗里点「下载」才下载（弹窗里看进度），下好再点「安装并重启」才安装（installing：停服务、换 App，5-10 秒）。
// 不在 Electron 里（浏览器直接开、命令行版）时没有 shuttleDesktop，什么都不显示。

export type DesktopUpdate = {
  state: 'idle' | 'available' | 'downloading' | 'ready' | 'installing'
  current: string
  version?: string
  notes?: string
  received?: number
  bytes?: number
  error?: string
}

type Bridge = {
  update?: {
    state: () => Promise<DesktopUpdate | null>
    check: () => Promise<DesktopUpdate | null>
    download: () => void
    install: () => void
    onChange: (cb: (s: DesktopUpdate) => void) => () => void
  }
}

const bridge = () => (window as unknown as { shuttleDesktop?: Bridge }).shuttleDesktop?.update

export function useDesktopUpdate() {
  const [state, setState] = useState<DesktopUpdate | null>(null)
  // 点了「安装并重启」到主进程回 installing 之间（可能先弹「还有对话在跑」的确认）：按钮先转起来，收到主进程的状态就以它为准
  const [pending, setPending] = useState(false)
  useEffect(() => {
    const b = bridge()
    if (!b) return
    b.state().then((s) => s && setState(s)).catch(() => {})
    return b.onChange((s) => {
      setPending(false)
      setState(s)
    })
  }, [])
  const shown = state && pending && state.state === 'ready' ? { ...state, state: 'installing' as const } : state
  return {
    /** 在 Annulo App 里（有更新功能）；浏览器直接打开的没有 */
    available: !!bridge(),
    state: shown,
    /** 立即检查一次（设置 → 关于）；结果也会经 onChange 推过来 */
    check: async () => {
      const s = await bridge()?.check()
      if (s) setState(s)
      return s ?? null
    },
    download: () => bridge()?.download(),
    install: () => {
      setPending(true)
      bridge()?.install()
    },
  }
}
