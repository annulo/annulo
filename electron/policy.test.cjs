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
  for (const url of ['http://127.0.0.1:7800/', 'https://example.com/', 'file:///etc/passwd', 'http://user@127.0.0.1:7799/']) assert.equal(isInternalURL(url, 'http://127.0.0.1:7799'), false)
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
