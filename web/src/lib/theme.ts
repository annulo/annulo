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
