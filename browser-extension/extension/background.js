// Annulo browser bridge.
//
// Annulo (the desktop app) talks to this extension through native messaging: Chrome starts the
// annulo program registered as "com.annulo.browser" and relays JSON both ways. Annulo asks to open a
// tab on a site, then drives that tab with DevTools commands sent through chrome.debugger.
//
// chrome.debugger is not limited by host permissions: attached to any tab it could read every
// cookie in this Chrome profile. So the limits live here, in code that ships with the extension:
//   - only tabs this extension opened for Annulo are ever attached;
//   - a tab may only be on a site the user allowed (optional host permissions, asked per site);
//     navigating anywhere else detaches the debugger and fails every later command;
//   - only allow-listed DevTools commands are forwarded; cookie, storage, target and browser-wide
//     commands never are.

const HOST = 'com.annulo.browser'
const VERSION = chrome.runtime.getManifest().version

let port = null
let lastActive = 0
// tabId -> { window, blocked: url | null }, mirrored to storage.session so a worker restart keeps it
const owned = new Map()
// grant request key -> { resolve, tab }
const grants = new Map()

// ---------- DevTools allow-list ----------

const DOMAINS = new Set(['Page', 'Runtime', 'DOM', 'Input', 'Emulation', 'Log', 'Inspector', 'CSS', 'Network', 'Accessibility', 'Overlay', 'DOMSnapshot', 'Performance'])
const DENIED = new Set([
  // cookies and other sites' data
  'Network.getCookies', 'Network.getAllCookies', 'Network.setCookie', 'Network.setCookies', 'Network.deleteCookies',
  'Network.clearBrowserCookies', 'Network.clearBrowserCache', 'Network.setCookieControls', 'Network.loadNetworkResource',
  'Network.replayXHR', 'Network.setRequestInterception', 'Network.continueInterceptedRequest',
  'Network.takeResponseBodyForInterceptionAsStream', 'Page.getCookies', 'Page.deleteCookie',
  // the tab's lifetime and downloads belong to the extension
  'Page.close', 'Page.crash', 'Page.setDownloadBehavior', 'Page.setBypassCSP',
])

function commandAllowed(method) {
  return DOMAINS.has(method.split('.')[0]) && !DENIED.has(method)
}

// ---------- sites ----------

function sitePattern(url) {
  const u = new URL(url)
  if (u.protocol !== 'https:' && u.protocol !== 'http:') throw new Error('only http and https pages can be opened: ' + url)
  return `${u.protocol}//${u.hostname}/*`
}

async function siteAllowed(url) {
  if (!url || url === 'about:blank') return true
  let pattern
  try {
    pattern = sitePattern(url)
  } catch {
    return false
  }
  return chrome.permissions.contains({ origins: [pattern] })
}

async function sites() {
  const p = await chrome.permissions.getAll()
  return (p.origins || []).filter((o) => o !== 'https://*/*' && o !== 'http://*/*')
}

// ---------- connection to Annulo ----------

async function profileId() {
  let { id } = await chrome.storage.local.get('id')
  if (!id) {
    id = 'annulo-' + crypto.randomUUID()
    await chrome.storage.local.set({ id })
  }
  return id
}

function send(msg) {
  if (!port) return
  try {
    port.postMessage(msg)
  } catch (e) {
    console.warn('annulo: send failed', e)
  }
}

async function hello() {
  return { id: await profileId(), version: VERSION, ua: navigator.userAgent, sites: await sites(), lastActive }
}

function connect() {
  if (port) return
  try {
    port = chrome.runtime.connectNative(HOST)
  } catch (e) {
    console.warn('annulo: connectNative', e)
    return
  }
  port.onMessage.addListener(onRequest)
  port.onDisconnect.addListener(() => {
    // Annulo isn't running (or isn't installed): try again from the alarm
    console.info('annulo: disconnected', chrome.runtime.lastError?.message)
    port = null
    chrome.storage.session.set({ connected: false })
  })
  chrome.storage.session.set({ connected: true })
  hello().then((h) => send({ event: 'hello', ...h }))
}

async function restore() {
  const { owned: saved } = await chrome.storage.session.get('owned')
  for (const [tab, info] of Object.entries(saved || {})) owned.set(Number(tab), info)
}

function persist() {
  chrome.storage.session.set({ owned: Object.fromEntries(owned) })
}

chrome.runtime.onStartup.addListener(connect)
chrome.runtime.onInstalled.addListener(connect)
chrome.alarms.create('connect', { periodInMinutes: 0.5 })
chrome.alarms.onAlarm.addListener((a) => a.name === 'connect' && connect())
restore().then(connect)

// ---------- events ----------

chrome.windows.onFocusChanged.addListener((windowId) => {
  if (windowId === chrome.windows.WINDOW_ID_NONE) return
  lastActive = Date.now()
  send({ event: 'active', at: lastActive })
})

chrome.debugger.onEvent.addListener((source, method, params) => {
  if (owned.has(source.tabId)) send({ event: 'cdp', tab: source.tabId, method, params })
})

chrome.debugger.onDetach.addListener((source, reason) => {
  if (!owned.has(source.tabId)) return
  // canceled_by_user: the user clicked "Cancel" on Chrome's debugging bar
  send({ event: 'detached', tab: source.tabId, reason })
})

chrome.tabs.onRemoved.addListener((tabId) => {
  if (owned.delete(tabId)) {
    persist()
    send({ event: 'closed', tab: tabId })
  }
  for (const [id, g] of grants) if (g.tab === tabId) finishGrant(id, false)
})

chrome.tabs.onUpdated.addListener(async (tabId, change) => {
  const info = owned.get(tabId)
  if (!info || !change.url) return
  if (await siteAllowed(change.url)) {
    if (info.blocked) {
      info.blocked = null
      persist()
    }
    return
  }
  info.blocked = change.url
  persist()
  chrome.debugger.detach({ tabId }).catch(() => {})
  send({ event: 'blocked', tab: tabId, url: change.url })
})

chrome.permissions.onAdded.addListener(async () => send({ event: 'sites', sites: await sites() }))
chrome.permissions.onRemoved.addListener(async () => send({ event: 'sites', sites: await sites() }))

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (sender.id !== chrome.runtime.id) return
  if (msg?.type === 'grant') finishGrant(msg.req, !!msg.granted)
  if (msg?.type === 'status') reply({ connected: !!port })
})

function finishGrant(key, granted) {
  const g = grants.get(key)
  if (!g) return
  grants.delete(key)
  chrome.tabs.remove(g.tab).catch(() => {})
  g.resolve({ granted })
}

// ---------- requests from Annulo ----------

async function onRequest(msg) {
  const { id, op, args = {} } = msg || {}
  try {
    const result = await handle(op, args)
    send({ id, ok: true, result: result ?? null })
  } catch (e) {
    send({ id, ok: false, error: String(e?.message || e), code: e?.code })
  }
}

function fail(message, code) {
  const e = new Error(message)
  e.code = code
  return e
}

async function ownedTab(tab) {
  const info = owned.get(tab)
  if (!info) throw fail('not a tab Annulo opened: ' + tab, 'not_owned')
  if (info.blocked) throw fail('the page left the allowed sites: ' + info.blocked, 'blocked')
  const t = await chrome.tabs.get(tab).catch(() => null)
  if (!t) throw fail('the tab was closed', 'closed')
  const url = t.pendingUrl || t.url
  if (!(await siteAllowed(url))) throw fail('the page left the allowed sites: ' + url, 'blocked')
  return { info, tab: t }
}

async function handle(op, a) {
  switch (op) {
    case 'hello':
      return hello()

    case 'sites':
      return sites()

    case 'grant': {
      // chrome.permissions.request needs a click: open our own page with an Allow button.
      // multi: Annulo asks every connected Chrome profile at once; the user allows it in the one signed in to the site,
      // and Annulo cancels the others (cancel_grant).
      const pattern = sitePattern(a.url)
      if (await chrome.permissions.contains({ origins: [pattern] })) return { granted: true }
      const key = a.key || crypto.randomUUID()
      const q = new URLSearchParams({ req: key, origin: pattern, why: a.why || '', multi: a.multi ? '1' : '', who: a.who || '' })
      const win = await chrome.windows.create({ url: chrome.runtime.getURL('grant.html?' + q), type: 'popup', width: 520, height: a.multi ? 470 : 420, focused: true })
      return new Promise((resolve) => grants.set(key, { resolve, tab: win.tabs[0].id }))
    }

    case 'cancel_grant':
      finishGrant(a.key, false)
      return null

    case 'revoke':
      return chrome.permissions.remove({ origins: [a.origin] })

    case 'open': {
      if (!(await siteAllowed(a.url))) throw fail('Annulo is not allowed on ' + new URL(a.url).hostname + ' yet', 'not_granted')
      const win = await chrome.windows.create({ url: 'about:blank', type: 'normal', focused: !!a.focused, width: a.width || 1280, height: a.height || 900 })
      const tab = win.tabs[0].id
      owned.set(tab, { window: win.id, blocked: null })
      persist()
      try {
        await chrome.debugger.attach({ tabId: tab }, '1.3')
      } catch (e) {
        owned.delete(tab)
        persist()
        chrome.windows.remove(win.id).catch(() => {})
        throw e
      }
      if (a.url && a.url !== 'about:blank') await chrome.tabs.update(tab, { url: a.url })
      return { tab, window: win.id }
    }

    case 'close': {
      const info = owned.get(a.tab)
      if (!info) return null
      owned.delete(a.tab)
      persist()
      await chrome.debugger.detach({ tabId: a.tab }).catch(() => {})
      const t = await chrome.tabs.get(a.tab).catch(() => null)
      if (!t) return null
      // close the window we opened; if the user dragged other tabs into it, close only ours
      const tabs = await chrome.tabs.query({ windowId: t.windowId })
      if (tabs.length <= 1) await chrome.windows.remove(t.windowId).catch(() => {})
      else await chrome.tabs.remove(a.tab).catch(() => {})
      return null
    }

    case 'release': {
      // hand the window to the user (a failed run they were watching): stop debugging it and forget it,
      // so it becomes an ordinary window of theirs
      if (!owned.delete(a.tab)) return null
      persist()
      await chrome.debugger.detach({ tabId: a.tab }).catch(() => {})
      return null
    }

    case 'focus': {
      const { tab } = await ownedTab(a.tab)
      await chrome.windows.update(tab.windowId, { focused: true, state: 'normal' })
      await chrome.tabs.update(tab.id, { active: true })
      return null
    }

    case 'window': {
      const { tab } = await ownedTab(a.tab)
      return { window: tab.windowId }
    }

    case 'cdp': {
      if (!commandAllowed(a.method)) throw fail('not allowed: ' + a.method, 'denied')
      await ownedTab(a.tab)
      if (a.method === 'Page.navigate' && !(await siteAllowed(a.params?.url))) {
        throw fail('Annulo is not allowed on ' + a.params?.url, 'not_granted')
      }
      return chrome.debugger.sendCommand({ tabId: a.tab }, a.method, a.params || {})
    }

    default:
      throw fail('unknown op ' + op, 'unknown')
  }
}
