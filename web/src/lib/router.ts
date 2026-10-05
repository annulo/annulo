import { useEffect, useState } from 'react'

// Annulo 界面只有几个视图，用 pathname 区分，不引路由库。
export type View = 'backend' | 'usage' | 'settings'

const BASE = '/_shuttle'

export function viewOf(path = location.pathname): View {
  if (path.startsWith(BASE + '/usage')) return 'usage'
  if (path.startsWith(BASE + '/settings')) return 'settings'
  return 'backend'
}

export function hrefOf(v: View, hash = '') {
  return (v === 'backend' ? BASE + '/' : `${BASE}/${v}`) + hash
}

export function navigate(v: View, hash = '') {
  const to = hrefOf(v, hash)
  if (location.pathname + location.hash !== to) history.pushState(null, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

export function useView(): [View, string] {
  const [s, set] = useState<[View, string]>([viewOf(), location.hash])
  useEffect(() => {
    const f = () => set([viewOf(), location.hash])
    window.addEventListener('popstate', f)
    window.addEventListener('hashchange', f)
    return () => {
      window.removeEventListener('popstate', f)
      window.removeEventListener('hashchange', f)
    }
  }, [])
  return s
}
