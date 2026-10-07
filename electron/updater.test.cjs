const test = require('node:test')
const assert = require('node:assert/strict')
const crypto = require('node:crypto')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { newer, pickUpdate, assetKey, allowedURL, macAppBundle, createUpdater } = require('./updater.cjs')

const sha = '0'.repeat(64)
const feed = version => ({ version, downloads: {
  macArm64: { url: 'https://fsu.creght.com/a.dmg', sha256: sha, bytes: 10 },
  windowsAmd64: { url: 'https://fsu.creght.com/b.exe', sha256: sha, bytes: 10 },
} })

test('date versions: -N is a later build of the same day, not a prerelease', () => {
  assert.equal(newer('2026.9.29-1', '2026.9.29'), true)
  assert.equal(newer('2026.9.30', '2026.9.29-3'), true)
  assert.equal(newer('2026.10.1', '2026.9.30'), true)
  assert.equal(newer('2026.9.29', '2026.9.29'), false)
  assert.equal(newer('2026.9.29', '2026.9.29-1'), false)
  assert.equal(newer('garbage', '2026.9.29'), false)
})

test('picks this machine\'s installer only when it is newer and well-formed', () => {
  assert.equal(assetKey('darwin', 'x64'), 'macIntel')
  assert.equal(assetKey('win32', 'arm64'), 'windowsArm64')
  assert.equal(pickUpdate(feed('2026.9.30'), '2026.9.29-1', 'darwin', 'arm64').url, 'https://fsu.creght.com/a.dmg')
  assert.equal(pickUpdate(feed('2026.9.29'), '2026.9.29-1', 'darwin', 'arm64'), null)
  assert.equal(pickUpdate(feed('2026.9.30'), '2026.9.29', 'darwin', 'x64'), null) // 没有 Intel 包
  const bad = feed('2026.9.30'); bad.downloads.macArm64.url = 'http://evil.example/a.dmg'
  assert.equal(pickUpdate(bad, '2026.9.29', 'darwin', 'arm64'), null)
})

test('feeds and downloads must be https, except localhost for testing', () => {
  assert.equal(allowedURL('https://annulo.creght.cn/update.json'), true)
  assert.equal(allowedURL('http://127.0.0.1:8000/update.json'), true)
  for (const u of ['http://example.com/a', 'file:///tmp/a.dmg', 'javascript:1']) assert.equal(allowedURL(u), false)
})

test('finds the running .app bundle', () => {
  assert.equal(macAppBundle('/Applications/Shuttle.app/Contents/MacOS/Shuttle'), '/Applications/Shuttle.app')
  assert.equal(macAppBundle('/usr/local/bin/electron'), null)
})

test('checking only finds the update; it downloads when asked and rejects a file that does not match', async () => {
  if (!assetKey(process.platform, process.arch)) return // 只在 Mac / Windows 上跑
  const body = Buffer.from('installer bytes')
  const digest = crypto.createHash('sha256').update(body).digest('hex')
  const key = assetKey(process.platform, process.arch)
  let served = body, downloads = 0
  const fetchImpl = async url => url.endsWith('update.json')
    ? { ok: true, json: async () => ({ version: '2099.1.1', downloads: { [key]: { url: 'https://x.test/Shuttle.dmg', sha256: digest, bytes: body.length } } }) }
    : (downloads++, { ok: true, body: (async function* () { yield served })() })
  const app = { getVersion: () => '2026.9.29', isPackaged: true }
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'shuttle-update-'))
  const updater = createUpdater({ app, dataDir, log: () => {}, fetchImpl })

  let s = await updater.check()
  assert.equal(s.state, 'available')
  assert.equal(s.version, '2099.1.1')
  assert.equal(downloads, 0) // 检查不下载

  s = await updater.fetchUpdate()
  assert.equal(s.state, 'ready')
  assert.equal(fs.readFileSync(s.file).toString(), 'installer bytes')

  served = Buffer.from('tampered bytes!')
  const second = createUpdater({ app, dataDir: fs.mkdtempSync(path.join(os.tmpdir(), 'shuttle-update-')), log: () => {}, fetchImpl })
  await second.check()
  await assert.rejects(second.fetchUpdate())
  assert.equal(second.state.state, 'available') // 可以再点一次
  assert.match(second.state.error, /does not match/)
  served = body
  assert.equal((await second.fetchUpdate()).state, 'ready')
})

test('installing keeps the downloaded file and still counts as downloaded', async () => {
  if (!assetKey(process.platform, process.arch)) return
  const body = Buffer.from('installer bytes')
  const digest = crypto.createHash('sha256').update(body).digest('hex')
  const key = assetKey(process.platform, process.arch)
  const fetchImpl = async url => url.endsWith('update.json')
    ? { ok: true, json: async () => ({ version: '2099.1.1', downloads: { [key]: { url: 'https://x.test/Shuttle.dmg', sha256: digest, bytes: body.length } } }) }
    : { ok: true, body: (async function* () { yield body })() }
  const app = { getVersion: () => '2026.9.29', isPackaged: true }
  const updater = createUpdater({ app, dataDir: fs.mkdtempSync(path.join(os.tmpdir(), 'shuttle-update-')), log: () => {}, fetchImpl })
  await updater.check()
  const file = (await updater.fetchUpdate()).file
  updater.installing(true)
  assert.equal(updater.state.state, 'installing')
  assert.equal(updater.state.file, file)
  // 装的时候不能因为状态是 installing 就说「没下载」（以前的 bug）：这里只验证它过了那道检查、走到了平台相关的安装
  await assert.rejects(updater.install(), error => !/no downloaded update/.test(error.message))
  updater.installing(false)
  assert.equal(updater.state.state, 'ready')
})

// 用真的 hdiutil 做一个 DMG（只在 Mac 上跑）
const { installAppFrom } = require('./updater.cjs')
const { execFileSync } = require('node:child_process')
function fakeApp(dir, name, marker) {
  const exe = path.join(dir, name + '.app', 'Contents', 'MacOS')
  fs.mkdirSync(exe, { recursive: true })
  fs.writeFileSync(path.join(exe, name), marker)
}
function makeDMG(tmp, name, marker) {
  const src = path.join(tmp, 'src-' + name)
  fakeApp(src, name, marker)
  const dmg = path.join(tmp, name + '.dmg')
  execFileSync('hdiutil', ['create', '-quiet', '-fs', 'HFS+', '-srcfolder', src, '-volname', 'x', dmg])
  return dmg
}

test('updating Shuttle.app installs Annulo.app next to it and moves the old one aside', { skip: process.platform !== 'darwin' }, async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'annulo-upd-'))
  const apps = path.join(tmp, 'Applications'); fs.mkdirSync(apps)
  fakeApp(apps, 'Shuttle', 'old')
  const dmg = makeDMG(tmp, 'Annulo', 'new')
  const res = await installAppFrom(dmg, path.join(apps, 'Shuttle.app'), tmp, () => {})
  assert.equal(res.execPath, path.join(apps, 'Annulo.app', 'Contents', 'MacOS', 'Annulo'))
  assert.equal(fs.readFileSync(res.execPath, 'utf8'), 'new')
  assert.ok(fs.existsSync(path.join(apps, 'Shuttle.app.old')), 'old app moved aside for cleanup')
  assert.ok(!fs.existsSync(path.join(apps, 'Shuttle.app')))
})

test('updating Annulo.app replaces it in place', { skip: process.platform !== 'darwin' }, async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'annulo-upd-'))
  const apps = path.join(tmp, 'Applications'); fs.mkdirSync(apps)
  fakeApp(apps, 'Annulo', 'v1')
  const dmg = makeDMG(tmp, 'Annulo', 'v2')
  const res = await installAppFrom(dmg, path.join(apps, 'Annulo.app'), tmp, () => {})
  assert.equal(fs.readFileSync(res.execPath, 'utf8'), 'v2')
  assert.equal(fs.readFileSync(path.join(apps, 'Annulo.app.old', 'Contents', 'MacOS', 'Annulo'), 'utf8'), 'v1')
})
