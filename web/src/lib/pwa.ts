import { useEffect, useState } from 'react'

// 添加到桌面（PWA，清单见 internal/server/pwa.go）。
// Chrome、Edge、安卓会发 beforeinstallprompt：先存着，用户点菜单里的「添加到桌面」时再弹系统的安装框；
// iPhone、iPad 的 Safari 和别的浏览器没有这个接口，只能告诉用户在浏览器菜单里怎么点。

type InstallEvent = Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }> }

let deferred: InstallEvent | null = null
const listeners = new Set<() => void>()
const changed = () => listeners.forEach((f) => f())

// 越早听越好：事件可能在 React 渲染之前就来了（main.tsx 一开始就 import 这个文件）
window.addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault()
  deferred = e as InstallEvent
  changed()
})
window.addEventListener('appinstalled', () => {
  deferred = null
  changed()
})

/** 已经是从桌面打开的（独立窗口），不用再提示添加 */
export const standalone = () =>
  window.matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true

/** 在 Annulo 桌面 App（Electron）里：本来就是 App */
export const inDesktopApp = () => 'shuttleDesktop' in window

export const isIOS = () => /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)

export function useInstall() {
  const [, tick] = useState(0)
  useEffect(() => {
    const f = () => tick((x) => x + 1)
    listeners.add(f)
    return () => {
      listeners.delete(f)
    }
  }, [])
  return {
    /** 菜单里出不出「添加到桌面」 */
    available: !inDesktopApp() && !standalone(),
    /** 能直接弹系统的安装框 */
    canPrompt: !!deferred,
    /** 弹系统的安装框；返回 false 表示用户取消了 */
    prompt: async () => {
      if (!deferred) return false
      const e = deferred
      await e.prompt()
      const { outcome } = await e.userChoice
      deferred = null
      changed()
      return outcome === 'accepted'
    },
  }
}
