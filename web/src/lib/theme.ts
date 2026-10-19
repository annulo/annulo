import { useEffect, useState } from 'react'
import { postToPage } from './brand'

export type Theme = 'dark' | 'light'

/** 当前主题（index.html 里渲染前已经设好 data-theme），切换时记住选择 */
export function useTheme(): [Theme, () => void] {
  const [t, setT] = useState<Theme>(() => (document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'))
  useEffect(() => {
    const el = document.documentElement
    el.setAttribute('data-theme', t)
    el.style.colorScheme = t
  }, [t])
  // 同一个项目开了几个窗口：别的窗口切了主题（localStorage 变了），这里跟着换；后台页面自己也听同一个事件（模板 talizen.config.ts）
  useEffect(() => {
    const on = (e: StorageEvent) => {
      if (e.key === 'shuttle.theme' && (e.newValue === 'light' || e.newValue === 'dark')) setT(e.newValue)
    }
    window.addEventListener('storage', on)
    return () => window.removeEventListener('storage', on)
  }, [])
  const toggle = () => {
    const next = t === 'dark' ? 'light' : 'dark'
    try {
      localStorage.setItem('shuttle.theme', next) // 页面和外壳同源，页面读的是这个
      localStorage.setItem('annulo.theme', next)
    } catch {
      // 存不了就只在本次生效
    }
    setT(next)
    // 通知运营后台（iframe）跟着换
    document.querySelectorAll<HTMLIFrameElement>('iframe[data-backend]').forEach((f) => postToPage(f.contentWindow, { type: 'shuttle:theme', theme: next }))
  }
  return [t, toggle]
}
