const test = require('node:test')
const assert = require('node:assert')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { migrateDataDir } = require('./datadir.cjs')

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'annulo-home-'))

test('全新安装用 ~/.annulo', () => {
  const home = tmp()
  assert.strictEqual(migrateDataDir(home), path.join(home, '.annulo'))
})

test('老用户的 ~/.shuttle 迁过去、留链接', () => {
  const home = tmp()
  fs.mkdirSync(path.join(home, '.shuttle', 'backends'), { recursive: true })
  fs.writeFileSync(path.join(home, '.shuttle', 'config.json'), '{}')
  assert.strictEqual(migrateDataDir(home), path.join(home, '.annulo'))
  assert.ok(fs.existsSync(path.join(home, '.annulo', 'config.json')))
  assert.ok(fs.existsSync(path.join(home, '.shuttle', 'backends')), '老路径还能用')
  assert.ok(fs.lstatSync(path.join(home, '.shuttle')).isSymbolicLink())
  assert.strictEqual(migrateDataDir(home), path.join(home, '.annulo'), '再启动不动')
})

test('两个都有就用新的、不碰老的', () => {
  const home = tmp()
  fs.mkdirSync(path.join(home, '.shuttle'))
  fs.mkdirSync(path.join(home, '.annulo'))
  assert.strictEqual(migrateDataDir(home), path.join(home, '.annulo'))
  assert.ok(!fs.lstatSync(path.join(home, '.shuttle')).isSymbolicLink())
})
