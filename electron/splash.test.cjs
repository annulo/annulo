const test = require('node:test')
const assert = require('node:assert/strict')
const { renderSplash } = require('./splash.cjs')
const labels = { retry: '重试', copy: '复制诊断信息', logs: '打开日志文件夹' }

test('normal startup is plain language without a diagnostic box or technical terms', () => {
  const html = renderSplash({ title: '正在启动 Shuttle…', detail: '正在准备，请稍候。', failed: false, dark: true, labels })
  assert.match(html, /正在准备，请稍候/)
  assert(!html.includes('<pre>'))
  assert(!html.includes('<button'))
  assert(!/Chromium|Electron|本机服务/.test(html))
})

test('failure keeps readable escaped diagnostics and recovery actions', () => {
  const html = renderSplash({ title: '启动失败', detail: 'Chromium error <script>alert(1)</script>', failed: true, dark: false, labels })
  assert.match(html, /<pre>Chromium error &lt;script&gt;/)
  assert.match(html, /复制诊断信息/)
  assert.match(html, /shuttleDesktop.retry/)
})
