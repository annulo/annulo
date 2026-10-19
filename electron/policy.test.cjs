const test = require('node:test')
const assert = require('node:assert/strict')
const { isInternalURL, externalURL, deepLinkURL, helperFilename } = require('./policy.cjs')
const panel = 'http://127.0.0.1:7799/_shuttle/'
test('Windows launches the bundled .exe, Mac launches the bundled Mach-O helper', () => {
  assert.equal(helperFilename('win32'), 'annulo.exe')
  assert.equal(helperFilename('darwin'), 'annulo')
})
test('only this local service may replace the desktop interface', () => {
  assert.equal(isInternalURL(panel + 'settings', 'http://127.0.0.1:7799'), true)
  for (const url of ['http://127.0.0.1:7800/', 'https://example.com/', 'file:///etc/passwd', 'http://user@127.0.0.1:7799/', 'http://p1.localhost:7800/', 'http://a.b.localhost:7799/', 'https://p1.localhost:7799/', 'http://evil.com:7799/']) assert.equal(isInternalURL(url, 'http://127.0.0.1:7799'), false)
  // 每个项目一个源：<项目>.localhost 同端口也是本机服务
  for (const url of ['http://p1.localhost:7799/_shuttle/', 'http://local-0c69304433ec.localhost:7799/', 'http://localhost:7799/_shuttle/']) assert.equal(isInternalURL(url, 'http://127.0.0.1:7799'), true)
})
test('external navigation cannot launch local files or commands', () => {
  assert.equal(externalURL('https://example.com/help'), 'https://example.com/help')
  for (const url of ['javascript:alert(1)', 'file:///Applications/Terminal.app', 'data:text/html,test', 'shuttle://settings']) assert.equal(externalURL(url), null)
})
test('OAuth links map to known local paths without remote redirects', () => {
  assert.equal(deepLinkURL('shuttle://settings#connections', panel), panel + 'settings#connections')
  assert.equal(deepLinkURL('annulo://settings#connections', panel), panel + 'settings#connections')
  for (const url of ['https://example.com/settings', 'shuttle://settings/../../evil', 'shuttle://settings?redirect=https://evil.com', 'shuttle://user@settings']) assert.equal(deepLinkURL(url, panel), null)
})
test('annulo://new carries only the start-from-template parameters', () => {
  assert.equal(deepLinkURL('annulo://new?cluster=creght.cn&template=p9ok3myl0ne6', panel), panel + 'new?cluster=creght.cn&template=p9ok3myl0ne6')
  assert.equal(deepLinkURL('annulo://new?template=p9ok3myl0ne6&name=%E5%A4%96%E8%B4%B8', panel), panel + 'new?template=p9ok3myl0ne6&name=%E5%A4%96%E8%B4%B8')
  for (const url of ['annulo://new?redirect=https://evil.com', 'annulo://new?template=a&token=x', 'annulo://settings?template=a', 'annulo://new?template=' + 'x'.repeat(301), 'annulo://new?name=a%0Ab']) assert.equal(deepLinkURL(url, panel), null)
})
test('annulo://open carries only the cluster and project', () => {
  assert.equal(deepLinkURL('annulo://open?cluster=creght.cn&project=p9kmhz32tb32', panel), panel + 'open?cluster=creght.cn&project=p9kmhz32tb32')
  for (const url of ['annulo://open?template=a', 'annulo://open?project=a&go=1']) assert.equal(deepLinkURL(url, panel), null)
})
