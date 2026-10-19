import { Component, StrictMode, type ErrorInfo, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import './lib/pwa' // 尽早听 beforeinstallprompt（添加到桌面）

const zh = navigator.language.toLowerCase().startsWith('zh')
const detailOf = (error: unknown) => error instanceof Error ? `${error.name}: ${error.message}\n${error.stack || ''}` : String(error)

function FatalError({ detail }: { detail: string }) {
  const [title, retry, copy] = zh ? ['界面启动失败', '重试', '复制错误信息'] : ['Interface failed to start', 'Retry', 'Copy error details']
  return <main data-shuttle-fatal style={{ padding: 40, maxWidth: 900, margin: 'auto', fontFamily: 'system-ui', color: 'CanvasText', background: 'Canvas' }}>
    <h1>{title}</h1>
    <p>{zh ? '请将以下信息发给开发者。完整日志可从 Annulo 菜单中的「查看日志」打开。' : 'Send these details to the developer. Open the full log from the Annulo menu.'}</p>
    <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', padding: 20, border: '1px solid #888', maxHeight: '60vh', overflow: 'auto' }}>{detail}</pre>
    <button onClick={() => location.reload()}>{retry}</button>{' '}
    <button onClick={() => { void navigator.clipboard?.writeText(detail) }}>{copy}</button>
  </main>
}

class StartupBoundary extends Component<{ children: ReactNode }, { detail: string | null }> {
  state: { detail: string | null } = { detail: null }
  static getDerivedStateFromError(error: unknown) { return { detail: detailOf(error) } }
  componentDidCatch(error: Error, info: ErrorInfo) {
    const detail = detailOf(error) + '\n' + (info.componentStack || '')
    console.error('Annulo interface error', detail)
    window.dispatchEvent(new ErrorEvent('error', { message: detail, error }))
  }
  render() { return this.state.detail ? <FatalError detail={this.state.detail} /> : this.props.children }
}

const root = createRoot(document.getElementById('root')!)
// 把应用模块的加载/解析异常也接住；静态 import 失败会发生在错误兜底运行之前。
// 服务端部署要先登录（docs/server-and-multi-project.md「账号」）：没登录显示登录页，不加载 App。本机版 auth/me 回 server: false
async function boot() {
  const me = await fetch('/_shuttle/api/auth/me', { headers: { 'X-Shuttle': '1' } }).then((r) => (r.ok ? r.json() : null)).catch(() => null)
  if (me?.server && !me.user) {
    const { default: AuthPage } = await import('./components/AuthPage')
    root.render(<StrictMode><AuthPage me={me} /></StrictMode>)
    return
  }
  const { default: App } = await import('./App')
  root.render(<StrictMode><StartupBoundary><App /></StartupBoundary></StrictMode>)
}
boot().catch((error: unknown) => {
  root.render(<FatalError detail={detailOf(error)} />)
  window.dispatchEvent(new ErrorEvent('error', { message: detailOf(error) }))
})
