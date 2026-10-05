import { getLocale, t } from '@/lib/i18n'
export function fmtTokens(n: number) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(n >= 10_000 ? 0 : 1) + 'k'
  return String(n)
}

export function fmtDuration(ms: number) {
  const s = ms / 1000
  if (s < 60) return s.toFixed(s < 10 ? 1 : 0) + 's'
  return `${Math.floor(s / 60)}m${Math.round(s % 60)}s`
}

export function fmtAgo(ts: number) {
  const d = (Date.now() - ts) / 1000
  if (d < 60) return t('刚刚')
  if (d < 3600) return t('{n} 分钟前', { n: Math.floor(d / 60) })
  if (d < 86400) return t('{n} 小时前', { n: Math.floor(d / 3600) })
  return new Date(ts).toLocaleDateString(getLocale() === 'en' ? 'en-US' : 'zh-CN')
}
