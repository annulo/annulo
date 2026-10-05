function isInternalURL(value, origin) {
  try {
    const url = new URL(value)
    return url.protocol === 'http:' && url.origin === origin && !url.username && !url.password
  } catch { return false }
}

function externalURL(value) {
  try {
    const url = new URL(value)
    return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : null
  } catch { return null }
}

// annulo://<页面>#<小节>（老名字 shuttle:// 也认：已经发出去的授权完成页、链接里是它）
function deepLinkURL(value, panelURL) {
  try {
    const url = new URL(value)
    const section = url.hash.slice(1)
    if (!['annulo:', 'shuttle:'].includes(url.protocol) || !/^[a-z-]+$/.test(url.hostname) || !/^[a-z-]*$/.test(section) || url.username || url.password || url.port || url.search || !['', '/'].includes(url.pathname)) return null
    return new URL(url.hostname + (section ? '#' + section : ''), panelURL).href
  } catch { return null }
}

// 后台程序：annulo（旁边还放着老名字 shuttle，转给 annulo，模板和助手里写的 shuttle run 照常能用）
const helperFilename = platform => platform === 'win32' ? 'annulo.exe' : 'annulo'
module.exports = { isInternalURL, externalURL, deepLinkURL, helperFilename }
