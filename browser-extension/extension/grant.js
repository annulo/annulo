const zh = navigator.language.toLowerCase().startsWith('zh')
const T = zh
  ? { title: 'Annulo 想在这个网站上帮你操作', note: '只在 Annulo 自己打开的窗口里操作，用的是你在这个 Chrome 里已经登录的账号。随时可以在插件图标的菜单里取消。', allow: '允许', deny: '不允许',
      multi: '你有几个 Chrome 都装了 Annulo，每个都弹了这个窗口：在登录了这个网站的那个里点「允许」，其他的会自动关掉。', notHere: '不是这个', who: '这是 Chrome 里的', again: 'Chrome 的确认没有完成（确认框关掉了），再点一次「允许」。' }
  : { title: 'Annulo wants to work on this site for you', note: 'It only works in windows Annulo opens itself, signed in as you are in this Chrome. You can remove it any time from the extension menu.', allow: 'Allow', deny: "Don't allow",
      multi: 'Several of your Chrome profiles have Annulo, and each shows this window: click Allow in the one signed in to this site, and the others close by themselves.', notHere: 'Not this one', who: 'This is the Chrome profile', again: "Chrome's confirmation didn't finish (the prompt closed). Click Allow again." }
const q = new URLSearchParams(location.search)
const req = q.get('req')
const origin = q.get('origin')
document.getElementById('title').textContent = T.title
document.getElementById('site').textContent = origin.replace(/\/\*$/, '').replace(/^https?:\/\//, '')
document.getElementById('why').textContent = q.get('why') || ''
document.getElementById('note').textContent = T.note
const allow = document.getElementById('allow')
allow.textContent = T.allow
const multi = q.get('multi') === '1'
document.getElementById('deny').textContent = multi ? T.notHere : T.deny
if (multi) {
  const m = document.getElementById('multi')
  m.textContent = T.multi
  m.hidden = false
}
// which Chrome profile this window belongs to (Annulo knows the name; the extension doesn't)
const who = q.get('who')
if (who) {
  const w = document.getElementById('who')
  w.textContent = T.who + ' '
  const b = document.createElement('b')
  b.textContent = who
  w.append(b)
  w.hidden = false
}
allow.onclick = async () => {
  let granted = false
  try {
    granted = await chrome.permissions.request({ origins: [origin] })
  } catch (e) {
    console.warn(e)
  }
  // Chrome's own prompt also closes when the window loses focus (another profile's window popped up at the same
  // time, say). That isn't a "no": keep this window and let the user try again. Only Don't allow / closing it says no.
  if (!granted) {
    const m = document.getElementById('again')
    m.textContent = T.again
    m.hidden = false
    return
  }
  chrome.runtime.sendMessage({ type: 'grant', req, granted })
}
document.getElementById('deny').onclick = () => chrome.runtime.sendMessage({ type: 'grant', req, granted: false })
