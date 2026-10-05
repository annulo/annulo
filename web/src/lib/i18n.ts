import { useSyncExternalStore } from 'react'
import { en } from './i18n.en'

// 外壳界面的中英双语，不加依赖：界面文字直接写中文原文，用 t('中文') 包起来；中文就是 key，
// 英文在 i18n.en.ts 里按中文原文对照。没翻译的回退中文。插值写 {name}：t('每 {n} 天', { n: 3 })。
//
// 生效语言由服务端算好（status.locale：设置里选的，auto 时跟随系统），App 拿到后 setLocale。
// 拿到之前先用上次记住的（localStorage），没有就看浏览器语言，免得先闪一下中文。

export type Locale = 'zh' | 'en'

const KEY = 'shuttle.locale'

function initial(): Locale {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'zh' || v === 'en') return v
  } catch {
    // 存不了就不记
  }
  return typeof navigator !== 'undefined' && !navigator.language.toLowerCase().startsWith('zh') ? 'en' : 'zh'
}

let locale: Locale = initial()
const listeners = new Set<() => void>()

export function getLocale(): Locale {
  return locale
}

export function setLocale(l: Locale) {
  if (l === locale) return
  locale = l
  try {
    localStorage.setItem(KEY, l)
  } catch {
    // 存不了就不记
  }
  document.documentElement.lang = l === 'en' ? 'en' : 'zh-CN'
  listeners.forEach((f) => f())
}

/** 按当前语言取文字。组件里用 useT()（语言切换时会重新渲染），组件外（事件回调、工具函数）可以直接用 t */
export function t(zh: string, vars?: Record<string, string | number>): string {
  let s = locale === 'en' ? (en[zh] ?? zh) : zh
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m))
  return s
}

function subscribe(f: () => void) {
  listeners.add(f)
  return () => listeners.delete(f)
}

/** 组件里取翻译函数；语言切换时组件重新渲染 */
export function useT() {
  useSyncExternalStore(subscribe, getLocale, getLocale)
  return t
}

/** 当前语言（组件里用，切换时重新渲染） */
export function useLocale(): Locale {
  return useSyncExternalStore(subscribe, getLocale, getLocale)
}
