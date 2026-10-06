import { useEffect, useRef, useState } from 'react'
import { getJSON, post } from '@/lib/api'
import { t } from '@/lib/i18n'

/**
 * 连接 creght（平台的 OAuth）：在浏览器里打开授权页，等它跳回本机；连上以后整页刷新（顶栏、模型、项目、模板都跟着变）。
 * 设置 → 连接 和选择项目页的连接弹窗（SetupPage）共用。
 */
export function useCreghtConnect() {
  const [waiting, setWaiting] = useState(false)
  const [url, setUrl] = useState('')
  const [err, setErr] = useState('')
  const timer = useRef<number>(0)
  useEffect(() => () => clearInterval(timer.current), [])

  const connect = async () => {
    setErr('')
    try {
      const r = (await (await post('login')).json()) as { verify_url: string }
      setUrl(r.verify_url)
      window.open(r.verify_url, '_blank', 'noopener')
      setWaiting(true)
      clearInterval(timer.current)
      timer.current = window.setInterval(async () => {
        try {
          const s = await getJSON<{ status: string; error?: string }>('login')
          if (s.status === 'pending') return
          clearInterval(timer.current)
          setWaiting(false)
          if (s.status === 'approved') location.reload()
          else setErr(s.status === 'error' && s.error ? s.error : t('授权已过期，请重新连接'))
        } catch (e) {
          clearInterval(timer.current)
          setWaiting(false)
          setErr((e as Error).message)
        }
      }, 2000)
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  return { connect, waiting, url, err, setErr }
}
