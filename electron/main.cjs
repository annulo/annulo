const { app, BrowserWindow, Menu, Tray, clipboard, dialog, ipcMain, nativeTheme, shell } = require('electron')
const { spawn, execFile } = require('node:child_process')
const { promisify } = require('node:util')
const fs = require('node:fs')
const http = require('node:http')
const os = require('node:os')
const path = require('node:path')
const { isInternalURL, externalURL, deepLinkURL, helperFilename } = require('./policy.cjs')
const { renderSplash } = require('./splash.cjs')
const { createUpdater } = require('./updater.cjs')
const { migrateDataDir } = require('./datadir.cjs')

app.setName('Annulo')

// 环境变量新旧名字都认：ANNULO_* 优先，没有再读 SHUTTLE_*（Go 那边的 internal/brand 同样）
const env = (key) => process.env['ANNULO_' + key] || process.env['SHUTTLE_' + key]
const dataDir = env('DIR') || migrateDataDir(os.homedir()) // ~/.annulo，老的 ~/.shuttle 第一次迁过来（datadir.cjs）
const logDir = path.join(dataDir, 'logs')
const panelURL = `http://127.0.0.1:${env('PORT') || '7799'}/_shuttle/`
const origin = new URL(panelURL).origin
const binDir = app.isPackaged ? path.join(process.resourcesPath, 'bin') : env('ELECTRON_BIN_DIR') || (process.platform === 'win32' ? path.join(__dirname, '../dist/electron/windows', process.arch, 'bin') : path.join(__dirname, '../dist/electron/bin'))
fs.mkdirSync(logDir, { recursive: true })
fs.mkdirSync(path.join(dataDir, 'electron-profile'), { recursive: true })
app.setPath('userData', path.join(dataDir, 'electron-profile'))
let zh = (process.env.LANG || '').startsWith('zh')
const L = (cn, en) => zh ? cn : en
let window, helper, tray, updater, quitting = false, quitPending = false, generation = 0, phase = 'loading', helperLog = '', diagnostics = '', target = panelURL

function log(message) {
  const file = path.join(logDir, 'electron.log')
  try {
    if (fs.existsSync(file) && fs.statSync(file).size > 5 * 1024 * 1024) fs.renameSync(file, file + '.previous')
    fs.appendFileSync(file, `[${new Date().toISOString()}] ${message}\n`, { mode: 0o600 })
  } catch (error) { console.error('Cannot write Electron log', error) }
}

function status() {
  return new Promise(resolve => {
    const request = http.get(new URL('api/status', panelURL), { headers: { 'X-Shuttle': '1' } }, response => {
      let body = ''
      response.on('data', chunk => { body += chunk; if (body.length > 1024 * 1024) request.destroy() })
      response.on('end', () => {
        try { resolve(response.statusCode === 200 && response.headers['x-shuttle'] ? JSON.parse(body) : null) } catch { resolve(null) }
      })
      response.on('error', () => resolve(null))
    })
    request.setTimeout(1500, () => { request.destroy(); resolve(null) })
    request.on('error', () => resolve(null))
  })
}

async function splash(title, detail, failed = false) {
  if (!window || window.isDestroyed()) return
  const run = generation
  phase = failed ? 'error' : 'loading'
  const dark = nativeTheme.shouldUseDarkColors
  const html = renderSplash({ title, detail, failed, dark, labels: { retry: L('重试', 'Retry'), copy: L('复制诊断信息', 'Copy diagnostics'), logs: L('打开日志文件夹', 'Open logs folder') } })
  try { await window.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html)) } catch (error) {
    // A newer refresh replaces this navigation. Chromium rejects the old load
    // with ERR_ABORTED; it is expected and must never become a native alert.
    if (run !== generation || quitting || error.code === 'ERR_ABORTED' || error.errno === -3) return
    log(`Could not display ${failed ? 'diagnostics' : 'startup'}: ${error.stack || error}`)
    dialog.showErrorBox(title, failed ? detail : L('启动画面未能显示，请重试或查看日志。', 'Could not display the startup screen. Please retry or check the logs.'))
  }
}

function fail(title, error) {
  if (quitting) return
  generation++
  diagnostics = `${title}\n\n${error?.stack || error}\n\nAnnulo ${app.getVersion()}\nElectron ${process.versions.electron} / Chromium ${process.versions.chrome}\nOS: ${process.platform} ${os.release()} / ${process.arch}\nURL: ${target}\nApp: ${app.getAppPath()}\nLogs: ${logDir}\n\n${helperLog}`
  log(diagnostics)
  void splash(title, diagnostics, true)
}

function startHelper() {
  if (helper && helper.exitCode === null) return
  helperLog = ''
  // Windows 服务用 stdin 管道的 EOF 收尾退出，不能传 ignore（否则它一启动就退出）。
  const child = spawn(path.join(binDir, helperFilename(process.platform)), ['--app'], { env: { ...process.env, ANNULO_DIR: dataDir, SHUTTLE_DIR: dataDir }, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
  helper = child
  const record = chunk => { helperLog = (helperLog + chunk.toString()).slice(-8000); log(chunk.toString().trimEnd()) }
  child.stdout.on('data', record)
  child.stderr.on('data', record)
  child.on('error', error => { if (helper === child) helper = null; fail(L('后台程序启动失败', 'Could not start the service'), error) })
  child.on('exit', async (code, signal) => {
    if (helper === child) helper = null
    if (!quitting && !(await status())) fail(L('后台服务已退出', 'The service exited'), `Exit ${code} / ${signal}\n${helperLog}`)
  })
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
async function startup() {
  const run = ++generation
  await splash(L('正在启动 Annulo…', 'Starting Annulo…'), L('正在准备，请稍候。', 'Getting ready. Please wait.'))
  if (run !== generation || quitting) return
  try {
    let ready = await status()
    const { stdout } = await promisify(execFile)(path.join(binDir, helperFilename(process.platform)), ['--version'], { timeout: 5000, windowsHide: true })
    if (run !== generation || quitting) return
    const expectedVersion = stdout.match(/version\s+(\S+)/)?.[1]
    if (ready && expectedVersion && ready.version !== expectedVersion) throw new Error(L(`另一个 Annulo 正在运行（${ready.version}），此安装包是 ${expectedVersion}。请先退出另一个 App，再点重试，以免加载旧版界面。`, `Another Annulo is running (${ready.version}); this package contains ${expectedVersion}. Quit the other app, then Retry so the new interface can load.`))
    if (!ready) startHelper()
    const deadline = Date.now() + 30000
    while (!ready && Date.now() < deadline && run === generation && !quitting) { await sleep(250); ready = await status() }
    if (run !== generation || quitting) return
    if (!ready) throw new Error(L('本机服务在 30 秒内没有应答。', 'The local service did not respond within 30 seconds.'))
    phase = 'app'
    await window.loadURL(target)
    const renderDeadline = Date.now() + 20000
    while (Date.now() < renderDeadline && run === generation && !quitting) {
      const state = await window.webContents.executeJavaScript(`(() => { const fatal = document.querySelector('[data-shuttle-fatal],#shuttle-startup-error'); return { ready: Boolean(document.getElementById('root')?.childElementCount), fatal: fatal?.innerText || '' } })()`)
      if (state.fatal) throw new Error(state.fatal)
      if (state.ready) { log(`Interface ready: ${target}`); return }
      await sleep(250)
    }
    if (run === generation && !quitting) throw new Error(L('网页已加载，但界面在 20 秒内没有显示。', 'The page loaded but no interface appeared within 20 seconds.'))
  } catch (error) { if (run === generation && !quitting) fail(L('界面启动失败', 'Interface startup failed'), error) }
}

function showWindow() {
  if (!window || window.isDestroyed()) { buildWindow(); void startup() }
  app.dock?.show()
  window.show(); window.focus()
}

function openExternal(value) { const url = externalURL(value); if (url) void shell.openExternal(url) }
function buildWindow() {
  window = new BrowserWindow({ title: 'Annulo', icon: path.join(__dirname, 'icons', 'Annulo.png'), width: 1440, height: 900, minWidth: 900, minHeight: 600, backgroundColor: nativeTheme.shouldUseDarkColors ? '#111111' : '#ffffff', webPreferences: { preload: path.join(__dirname, 'preload.cjs'), nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true } })
  window.on('close', event => { if (!quitting) { event.preventDefault(); window.hide(); app.dock?.hide() } })
  window.on('app-command', (_event, command) => {
    const direction = command === 'browser-backward' ? -1 : command === 'browser-forward' ? 1 : 0
    if (direction && phase === 'app') {
      void window.webContents.executeJavaScript(`window.dispatchEvent(new CustomEvent('shuttle:backend-history', {detail: ${direction}}))`).catch(error => log(`Backend history: ${error.message}`))
    }
  })
  window.webContents.on('will-navigate', (event, url) => { if (!isInternalURL(url, origin)) { event.preventDefault(); openExternal(url) } })
  // ⌘ 点击后台里的链接、target=_blank 打开的本机地址：交给界面开一个运营后台标签页；站外地址用系统浏览器
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (isInternalURL(url, origin)) { if (phase === 'app') void window.webContents.executeJavaScript(`window.dispatchEvent(new CustomEvent('shuttle:open-tab', {detail: ${JSON.stringify(url)}}))`).catch(error => log(`Open tab: ${error.message}`)) }
    else openExternal(url)
    return { action: 'deny' }
  })
  window.webContents.on('will-attach-webview', event => event.preventDefault())
  window.webContents.on('render-process-gone', (_event, details) => fail(L('网页进程已退出', 'The renderer process exited'), JSON.stringify(details)))
  window.webContents.on('did-fail-load', (_event, code, description, url, isMainFrame) => { if (isMainFrame && code !== -3 && phase === 'app') fail(L('界面加载失败', 'Interface load failed'), `${code} ${description}\n${url}`) })
  window.webContents.on('console-message', (_event, details, oldMessage, line, source) => {
    if (typeof details === 'object') { if (details.level === 'error' || details.level === 3) log(`JavaScript: ${details.message} (${details.sourceId}:${details.lineNumber})`) }
    else if (details === 3) log(`JavaScript: ${oldMessage} (${source}:${line})`)
  })
  window.webContents.session.setPermissionRequestHandler((contents, permission, callback) => callback(permission === 'clipboard-sanitized-write' && isInternalURL(contents.getURL(), origin)))
}

async function stopHelper() {
  const child = helper
  helper = null
  if (!child || child.exitCode !== null) return
  await new Promise(resolve => {
    const timeout = setTimeout(() => { child.kill('SIGKILL'); resolve() }, 8000)
    child.once('exit', () => { clearTimeout(timeout); resolve() })
    if (process.platform === 'win32') child.stdin.end()
    else child.kill('SIGTERM')
  })
}

// 还有对话在跑时问一句；返回 false 表示用户取消
async function confirmStopChats(detail, action) {
  if (!helper) return true
  const current = await status()
  const count = current?.agent?.running?.length || 0
  if (count === 0) return true
  const answer = await dialog.showMessageBox(window, { type: 'warning', message: L(`还有 ${count} 段对话在运行`, `${count} chats are still running`), detail, buttons: [L('取消', 'Cancel'), action], defaultId: 0, cancelId: 0 })
  return answer.response !== 0
}

async function requestQuit() {
  if (quitting || quitPending) return
  quitPending = true
  if (!(await confirmStopChats(L('退出会停止这些对话。关闭窗口可以让它们在后台继续。', 'Quitting stops these chats. Close the window to keep them running in the background.'), L('退出', 'Quit')))) { quitPending = false; return }
  quitting = true; generation++
  await stopHelper()
  app.quit()
}

// 「安装并重启」（下载好之后用户在更新弹窗里点）：停掉本机服务再装（Mac 换 App 包、Windows 交给安装器），装好重启。装不上就把服务起回来，照常用
async function installUpdate() {
  if (quitting || quitPending || updater?.state.state !== 'ready') return
  quitPending = true
  if (!(await confirmStopChats(L('重启更新会停止这些对话。可以等它们做完再更新。', 'Restarting to update stops these chats. You can update after they finish.'), L('重启更新', 'Restart to update')))) { quitPending = false; sendUpdateState(); return }
  // 停服务、换 App 要 5-10 秒：先告诉界面，按钮显示「正在安装…」
  updater.installing(true)
  quitting = true; generation++
  await stopHelper()
  try {
    const installed = await updater.install()
    log(`Restarting into ${updater.state.version}`)
    // 从 Shuttle.app 升级的，新的装在 Annulo.app：按新位置启动
    if (process.platform === 'darwin') app.relaunch(installed?.execPath ? { execPath: installed.execPath } : undefined)
    app.exit(0)
  } catch (error) {
    log(`Update install failed: ${error.stack || error}`)
    updater.installing(false)
    quitting = false; quitPending = false
    void startup()
    const manual = await dialog.showMessageBox(window, { type: 'error', message: L('更新没装上', 'Could not install the update'), detail: `${error.message || error}\n\n${L('可以打开下载好的安装包手动安装。', 'You can open the downloaded installer and install it manually.')}`, buttons: [L('打开安装包', 'Open installer'), L('关闭', 'Close')], defaultId: 0, cancelId: 1 })
    if (manual.response === 0) void shell.openPath(updater.state.file)
  }
}

// 菜单里的「检查更新…」：有新版本就打开界面里的更新弹窗（下载、安装都在那里点）；没有或出错弹个系统对话框
async function checkForUpdates() {
  const s = await updater.check()
  if (s.state !== 'idle') {
    showWindow()
    if (phase === 'app') void window.webContents.executeJavaScript("window.dispatchEvent(new CustomEvent('shuttle:open-update'))").catch(error => log(`Open update: ${error.message}`))
    return
  }
  const failed = Boolean(s.error)
  await dialog.showMessageBox(window, { type: failed ? 'warning' : 'info', message: failed ? L('检查更新失败', 'Update check failed') : L('已经是最新版本', 'Annulo is up to date'), detail: failed ? s.error : L(`当前是 ${s.current}。`, `You're on ${s.current}.`), buttons: [L('好', 'OK')] })
}

function sendUpdateState() {
  if (phase === 'app' && window && !window.isDestroyed()) window.webContents.send('shuttle:update', updater.state)
}

// ⌘W：运营后台开着多个标签时关当前标签（界面 preventDefault 表示关了），否则照常关窗口
async function closeTabOrWindow() {
  const focused = BrowserWindow.getFocusedWindow()
  if (!focused) return
  if (focused === window && phase === 'app') {
    const handled = await window.webContents.executeJavaScript(`!window.dispatchEvent(new CustomEvent('shuttle:close-tab', {cancelable: true}))`).catch(() => false)
    if (handled) return
  }
  focused.close()
}

// ⌘R：只刷新左侧运营后台当前标签的 iframe（界面 preventDefault 表示刷了）；还没进界面、或界面里没有后台（新建项目页）就整个重新加载
async function reloadSite() {
  if (phase === 'app') {
    const handled = await window.webContents.executeJavaScript(`!window.dispatchEvent(new CustomEvent('shuttle:reload-site', {cancelable: true}))`).catch(() => false)
    if (handled) return
  }
  void startup()
}

function buildMenu() {
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: 'Annulo', submenu: [{ role: 'about' }, { label: L('检查更新…', 'Check for Updates…'), click: () => void checkForUpdates() }, { type: 'separator' }, { label: L('设置…', 'Settings…'), accelerator: 'CmdOrCtrl+,', click: () => { showWindow(); if (phase === 'app') void window.webContents.executeJavaScript("window.dispatchEvent(new CustomEvent('shuttle:open-settings'))") } }, { label: L('打开数据目录', 'Open data folder'), click: () => void shell.openPath(dataDir) }, { label: L('查看日志', 'Show logs'), click: () => void shell.openPath(logDir) }, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { label: L('退出', 'Quit'), accelerator: 'CmdOrCtrl+Q', click: requestQuit }] },
    { role: 'editMenu' },
    { label: L('显示', 'View'), submenu: [{ label: L('刷新运营后台', 'Reload back office'), accelerator: 'CmdOrCtrl+R', click: () => void reloadSite() }, { label: L('重新加载 Annulo', 'Reload Annulo'), accelerator: 'Shift+CmdOrCtrl+R', click: () => void startup() }, { role: 'toggleDevTools' }, { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }] },
    { role: 'windowMenu', submenu: [{ role: 'minimize' }, { role: 'zoom' }, { label: L('显示窗口', 'Show window'), click: showWindow }, { label: L('关闭', 'Close'), accelerator: 'CmdOrCtrl+W', click: closeTabOrWindow }] },
  ]))
}

function buildTray() {
  if (process.platform !== 'win32') return
  tray = new Tray(path.join(__dirname, 'icons', 'tray.png'))
  tray.setToolTip('Annulo')
  tray.on('click', showWindow)
  tray.setContextMenu(Menu.buildFromTemplate([{ label: L('显示窗口', 'Show window'), click: showWindow }, { label: L('检查更新…', 'Check for updates…'), click: () => void checkForUpdates() }, { type: 'separator' }, { label: L('退出', 'Quit'), click: requestQuit }]))
}

function splashSender(event) {
  return window && !window.isDestroyed() && event.sender === window.webContents && event.senderFrame === window.webContents.mainFrame && event.senderFrame.url.startsWith('data:text/html')
}
ipcMain.on('shuttle:retry', event => { if (splashSender(event)) void startup() })
ipcMain.on('shuttle:copy-diagnostics', event => { if (splashSender(event)) clipboard.writeText(diagnostics) })
ipcMain.on('shuttle:open-logs', event => { if (splashSender(event)) void shell.openPath(logDir) })
// 更新：只认本机界面（主框架，本机服务的地址）发来的，运营后台的 iframe 和外站都调不了
const panelSender = event => window && !window.isDestroyed() && event.sender === window.webContents && event.senderFrame === window.webContents.mainFrame && isInternalURL(event.senderFrame.url, origin)
ipcMain.handle('shuttle:update-state', event => panelSender(event) ? updater?.state ?? null : null)
ipcMain.handle('shuttle:update-check', async event => panelSender(event) && updater ? updater.check() : null) // 设置 → 关于 里的「检查更新」
ipcMain.on('shuttle:update-download', event => { if (panelSender(event)) updater?.fetchUpdate().catch(() => {}) }) // 失败写在 state.error 里，界面显示
ipcMain.on('shuttle:update-install', event => { if (panelSender(event)) void installUpdate() })
app.on('open-url', (event, url) => { event.preventDefault(); const page = deepLinkURL(url, panelURL); if (page) { target = page; if (app.isReady()) { showWindow(); void startup() } } })
app.on('before-quit', event => { if (!quitting) { event.preventDefault(); void requestQuit() } })
app.on('window-all-closed', () => {})
app.on('activate', showWindow)
process.on('uncaughtException', error => { log(error.stack || String(error)); if (app.isReady()) fail(L('应用发生错误', 'Application error'), error) })
process.on('unhandledRejection', error => { log(String(error)); if (app.isReady()) fail(L('应用发生错误', 'Application error'), error) })
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, async () => { quitting = true; generation++; await stopHelper(); app.exit() })

if (!app.requestSingleInstanceLock()) app.exit()
else {
  app.on('second-instance', (_event, argv) => { const page = deepLinkURL(argv.find(value => value.startsWith('annulo:') || value.startsWith('shuttle:')) || '', panelURL); if (page) target = page; showWindow(); if (page) void startup() })
  app.whenReady().then(() => {
    zh = app.getPreferredSystemLanguages()[0]?.startsWith('zh') || zh
    if (process.platform === 'win32') app.setAppUserModelId('com.creght.shuttle')
    const page = deepLinkURL(process.argv.find(value => value.startsWith('annulo:') || value.startsWith('shuttle:')) || '', panelURL)
    if (page) target = page
    log(`Starting Electron ${process.versions.electron}, Chromium ${process.versions.chrome}, app ${app.getVersion()}`)
    updater = createUpdater({ app, dataDir, log, onChange: sendUpdateState })
    updater.cleanup()
    buildMenu(); buildWindow(); buildTray(); void startup()
    // 启动一会儿后检查一次，之后每 4 小时一次；只看有没有新版本，有就在顶栏提示，用户点了才下载、再点才安装
    setTimeout(() => void updater.check(), 30 * 1000)
    setInterval(() => void updater.check(), 4 * 60 * 60 * 1000)
  })
}
