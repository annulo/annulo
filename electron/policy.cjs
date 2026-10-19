// 本机服务的地址：origin 本身，或者同一个端口上的 127.0.0.1 / localhost / <项目>.localhost
// （每个项目一个源，见服务端 internal/server/view.go）
function isInternalURL(value, origin) {
  try {
    const url = new URL(value)
    const base = new URL(origin)
    if (url.protocol !== 'http:' || url.username || url.password || url.port !== base.port) return false
    return url.origin === base.origin || ['127.0.0.1', 'localhost'].includes(url.hostname) || /^[a-z0-9-]+\.localhost$/.test(url.hostname)
  } catch { return false }
}

function externalURL(value) {
  try {
    const url = new URL(value)
    return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : null
  } catch { return null }
}

// 带参数的链接：页面 → 认的参数。任何网页都能发 annulo:// 链接，所以参数只按白名单带过去，界面收到后先让用户确认再做
// （creght 用户中心「本机 Agent」：annulo://new 用模板新建项目、annulo://open 打开已有项目，见 docs/open-source.md「creght 客户的入口」）
const linkParams = { new: ['cluster', 'template', 'name'], open: ['cluster', 'project'] }

// annulo://<页面>#<小节>（老名字 shuttle:// 也认：已经发出去的授权完成页、链接里是它）
function deepLinkURL(value, panelURL) {
  try {
    const url = new URL(value)
    const section = url.hash.slice(1)
    if (!['annulo:', 'shuttle:'].includes(url.protocol) || !/^[a-z-]+$/.test(url.hostname) || !/^[a-z-]*$/.test(section) || url.username || url.password || url.port || !['', '/'].includes(url.pathname)) return null
    const allowed = linkParams[url.hostname] || []
    const params = new URLSearchParams()
    for (const [key, val] of url.searchParams) {
      if (!allowed.includes(key) || val.length > 300 || /[\u0000-\u001f]/.test(val)) return null
      params.set(key, val)
    }
    if (url.search && !params.size) return null
    const query = params.size ? '?' + params : ''
    return new URL(url.hostname + query + (section ? '#' + section : ''), panelURL).href
  } catch { return null }
}

// 后台程序：annulo（旁边还放着老名字 shuttle，转给 annulo，模板和助手里写的 shuttle run 照常能用）
const helperFilename = platform => platform === 'win32' ? 'annulo.exe' : 'annulo'
module.exports = { isInternalURL, externalURL, deepLinkURL, helperFilename }
