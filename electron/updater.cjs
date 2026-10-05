// App 内更新。Mac 包是 ad-hoc 签名，Electron 自带的 autoUpdater（Squirrel.Mac）要正式签名，用不了，所以自己装：
//   发布脚本在官网写 update.json（版本号 + 各平台安装包的地址、大小、sha256，scripts/release.py）；
//   App 启动一会儿后、之后每 4 小时读一次，只看有没有新版本，不下载；有就告诉界面（顶栏出「更新到 <版本>」）；
//   用户点了才下载到 ~/.shuttle/updates/ 并核对 sha256，下好就装、重启：
//     Mac：挂载 DMG，把新的 Annulo.app 拷到当前 App 旁边，和当前的改名互换（从 Shuttle.app 升级的装成 Annulo.app），重启；
//     Windows：静默运行新的安装器（/S），装完它自己启动 App（--force-run）。
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const { execFile, spawn } = require('node:child_process')
const { promisify } = require('node:util')

const run = promisify(execFile)
const DEFAULT_FEED = 'https://shuttle.site.creght.cn/update.json'

// 版本号是日期 + 当天第几次：2026.9.29、2026.9.29-1……「-N」是同一天更晚的版本，不是 SemVer 的预发布版
function parseVersion(v) {
  const m = /^(\d+)\.(\d+)\.(\d+)(?:-(\d+))?$/.exec(String(v || '').trim())
  return m ? [Number(m[1]), Number(m[2]), Number(m[3]), Number(m[4] || 0)] : null
}

function newer(candidate, current) {
  const a = parseVersion(candidate), b = parseVersion(current)
  if (!a || !b) return false
  for (let i = 0; i < 4; i++) if (a[i] !== b[i]) return a[i] > b[i]
  return false
}

// update.json 里 downloads 的 key，和 release.py 的 TARGETS 一致
function assetKey(platform, arch) {
  if (platform === 'darwin') return arch === 'arm64' ? 'macArm64' : 'macIntel'
  if (platform === 'win32') return arch === 'arm64' ? 'windowsArm64' : 'windowsAmd64'
  return null
}

// 更新地址只接受 https；本机测试可以用 http://127.0.0.1
function allowedURL(value) {
  try {
    const u = new URL(value)
    return u.protocol === 'https:' || (u.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(u.hostname))
  } catch { return false }
}

// 从 update.json 里挑这台电脑要的安装包；没有新版本、格式不对返回 null
function pickUpdate(feed, current, platform, arch) {
  if (!feed || !newer(feed.version, current)) return null
  const asset = feed.downloads?.[assetKey(platform, arch)]
  if (!asset || !allowedURL(asset.url) || !/^[0-9a-f]{64}$/.test(asset.sha256 || '') || !(asset.bytes > 0)) return null
  return { version: feed.version, notes: String(feed.notes || ''), url: asset.url, sha256: asset.sha256, bytes: asset.bytes }
}

// 正在运行的 .app 的路径（Contents/MacOS/<名字> 往上三层）
function macAppBundle(execPath) {
  const bundle = path.posix.resolve(execPath, '../../..') // 只在 Mac 上用；写成 posix，Windows 上跑测试也一样
  return bundle.endsWith('.app') ? bundle : null
}

function createUpdater({ app, dataDir, log, onChange, fetchImpl = fetch }) {
  const feedURL = process.env.ANNULO_UPDATE_URL || process.env.SHUTTLE_UPDATE_URL || DEFAULT_FEED
  const dir = path.join(dataDir, 'updates')
  // state：idle 没有新版本 / available 有新版本、等用户点 / downloading 下载中 / ready 下好了 / installing 正在装（停服务、换 App，5-10 秒）
  // 有新版本时 update 里是它（下载失败也留着，用户可以再点一次）
  let state = { state: 'idle', current: app.getVersion() }
  let update = null
  let checking = null, downloading = null
  const set = next => { state = { ...next, current: app.getVersion() }; onChange?.(state) }

  async function download(update) {
    fs.mkdirSync(dir, { recursive: true })
    const file = path.join(dir, `Annulo-${update.version}${path.extname(new URL(update.url).pathname) || (process.platform === 'win32' ? '.exe' : '.dmg')}`)
    if (fs.existsSync(file) && fs.statSync(file).size === update.bytes && await sha256(file) === update.sha256) return file
    // 旧版本的安装包都删掉，只留这一份
    for (const name of fs.readdirSync(dir)) if (name !== path.basename(file)) fs.rmSync(path.join(dir, name), { recursive: true, force: true })
    set({ state: 'downloading', version: update.version, notes: update.notes, received: 0, bytes: update.bytes })
    const response = await fetchImpl(update.url)
    if (!response.ok || !response.body) throw new Error(`HTTP ${response.status}`)
    const tmp = file + '.part'
    const out = fs.createWriteStream(tmp)
    const hash = crypto.createHash('sha256')
    let received = 0, lastReport = 0
    try {
      for await (const chunk of response.body) {
        hash.update(chunk)
        received += chunk.length
        if (!out.write(chunk)) await new Promise(resolve => out.once('drain', resolve))
        if (Date.now() - lastReport > 1000) { lastReport = Date.now(); set({ state: 'downloading', version: update.version, notes: update.notes, received, bytes: update.bytes }) }
      }
    } finally {
      await new Promise(resolve => out.end(resolve))
    }
    if (received !== update.bytes || hash.digest('hex') !== update.sha256) {
      fs.rmSync(tmp, { force: true })
      throw new Error(`downloaded file does not match update.json (${received} bytes)`)
    }
    fs.renameSync(tmp, file)
    return file
  }

  // check：读 update.json，只看有没有新版本，不下载
  async function check() {
    if (checking) return checking
    checking = (async () => {
      if (!app.isPackaged && !process.env.ANNULO_UPDATE_URL && !process.env.SHUTTLE_UPDATE_URL) return state
      if (downloading || state.state === 'ready' || state.state === 'installing') return state
      try {
        if (!allowedURL(feedURL)) throw new Error(`update feed must be https: ${feedURL}`)
        const response = await fetchImpl(feedURL, { headers: { 'Cache-Control': 'no-cache' } })
        if (!response.ok) throw new Error(`${feedURL}: HTTP ${response.status}`)
        update = pickUpdate(await response.json(), app.getVersion(), process.platform, process.arch)
        if (!update) { set({ state: 'idle' }); return state }
        log(`Update ${update.version} available: ${update.url}`)
        set({ state: 'available', version: update.version, notes: update.notes, bytes: update.bytes })
      } catch (error) {
        log(`Update check failed: ${error.stack || error}`)
        // 检查失败不打扰用户：之前发现的新版本照旧显示，没有就当作没有
        if (!update) set({ state: 'idle', error: String(error.message || error) })
      }
      return state
    })().finally(() => { checking = null })
    return checking
  }

  // fetchUpdate：用户点了「更新」才下载；下好返回 state（ready），失败抛错、state 回到 available 带上 error
  async function fetchUpdate() {
    if (state.state === 'ready') return state
    if (!update) throw new Error('no update available')
    if (downloading) return downloading
    downloading = (async () => {
      try {
        log(`Downloading update ${update.version}: ${update.url}`)
        const file = await download(update)
        log(`Update ${update.version} downloaded: ${file}`)
        set({ state: 'ready', version: update.version, notes: update.notes, bytes: update.bytes, file })
        return state
      } catch (error) {
        log(`Update download failed: ${error.stack || error}`)
        set({ state: 'available', version: update.version, notes: update.notes, bytes: update.bytes, error: String(error.message || error) })
        throw error
      }
    })().finally(() => { downloading = null })
    return downloading
  }

  // install：下载好（ready）之后、用户点了「安装并重启」才调；调用方已经停掉了本机服务。Mac 装好返回 true 由调用方重启；Windows 启动安装器后调用方直接退出
  async function install() {
    // 主进程开始装时已经把状态改成 installing（界面转圈），ready / installing 都算下好了
    if (!['ready', 'installing'].includes(state.state) || !state.file || !fs.existsSync(state.file)) throw new Error('no downloaded update')
    if (process.platform === 'darwin') return installMac(state.file)
    if (process.platform === 'win32') {
      // 安装器等 App 退出后覆盖安装；--updated 让它按上次的安装位置装，--force-run 装完启动 App
      spawn(state.file, ['/S', '--updated', '--force-run'], { detached: true, stdio: 'ignore', windowsHide: true }).unref()
      return true
    }
    throw new Error(`auto update is not supported on ${process.platform}`)
  }

  async function installMac(dmg) {
    const bundle = macAppBundle(process.execPath)
    if (!bundle) throw new Error(`not running from an .app bundle: ${process.execPath}`)
    return installAppFrom(dmg, bundle, dir, log)
  }

  // 上次更新留下的旧 App、安装器：启动时清掉
  function cleanup() {
    if (process.platform === 'darwin') {
      const bundle = macAppBundle(process.execPath)
      if (bundle) {
        const parent = path.dirname(bundle)
        for (const name of ['Annulo.app', 'Shuttle.app']) for (const p of ['.old', '.new']) fs.rmSync(path.join(parent, name + p), { recursive: true, force: true })
      }
    }
    try {
      for (const name of fs.readdirSync(dir)) {
        const v = /^(?:Annulo|Shuttle)-(.+)\.(dmg|exe)$/.exec(name)?.[1]
        if (!v || !newer(v, app.getVersion())) fs.rmSync(path.join(dir, name), { recursive: true, force: true })
      }
    } catch {}
  }

  // installing：主进程开始装 / 装失败回到 ready，告诉界面（按钮显示「正在安装…」）
  const installing = on => { if (state.file) set({ ...state, state: on ? 'installing' : 'ready' }) }

  return { check, fetchUpdate, install, installing, cleanup, get state() { return state }, feedURL }
}

// installAppFrom 把 DMG 里的 App 装到正在运行的 bundle 旁边（见 installMac），返回新 App 的可执行文件
async function installAppFrom(dmg, bundle, dir, log) {
  const parent = path.dirname(bundle)
  fs.accessSync(parent, fs.constants.W_OK) // 装在没有写权限的地方：报错，界面让用户手动装
  const mount = fs.mkdtempSync(path.join(dir, 'mount-'))
  await run('hdiutil', ['attach', '-nobrowse', '-readonly', '-noautoopen', '-mountpoint', mount, dmg], { timeout: 120000 })
  // 新的 App 放在当前这个旁边：DMG 里是 Annulo.app（老版本是 Shuttle.app）。从 Shuttle.app 升级上来的，新的装成同目录的 Annulo.app，
  // 老的挪成 Shuttle.app.old、下次启动清掉（cleanup）；重启时启动新的那个（返回它的可执行文件）
  let target, staged
  try {
    const name = ['Annulo.app', 'Shuttle.app'].find(n => fs.existsSync(path.join(mount, n)))
    if (!name) throw new Error('Annulo.app not found in the DMG')
    target = path.join(parent, name)
    staged = target + '.new'
    fs.rmSync(staged, { recursive: true, force: true })
    await run('ditto', [path.join(mount, name), staged], { timeout: 300000 })
  } finally {
    await run('hdiutil', ['detach', mount, '-force'], { timeout: 60000 }).catch(error => log(`hdiutil detach: ${error}`))
    fs.rmSync(mount, { recursive: true, force: true })
  }
  // 运行中的 App 可以改名：先把当前的（和目标位置上已有的）挪开，新的放进去；放不进去就挪回来
  const old = bundle + '.old', displaced = target !== bundle && fs.existsSync(target) ? target + '.old' : null
  fs.rmSync(old, { recursive: true, force: true })
  if (displaced) { fs.rmSync(displaced, { recursive: true, force: true }); fs.renameSync(target, displaced) }
  fs.renameSync(bundle, old)
  try { fs.renameSync(staged, target) } catch (error) {
    fs.renameSync(old, bundle)
    if (displaced) fs.renameSync(displaced, target)
    throw error
  }
  log(`Installed update into ${target}`)
  return { execPath: path.join(target, 'Contents', 'MacOS', path.basename(target, '.app')) }
}

async function sha256(file) {
  const hash = crypto.createHash('sha256')
  for await (const chunk of fs.createReadStream(file)) hash.update(chunk)
  return hash.digest('hex')
}

module.exports = { createUpdater, parseVersion, newer, assetKey, pickUpdate, allowedURL, macAppBundle, installAppFrom }
