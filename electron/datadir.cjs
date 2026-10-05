// 数据目录（和 Go 的 internal/brand.MigrateDataDir 同一套规则，docs/annulo-plan.md 第 6 步）：
// 有 ~/.annulo 用它；只有真目录 ~/.shuttle（老版本 Shuttle 的）就改名成 ~/.annulo，原地留一个指过去的链接
// （Windows 是目录联接，不用管理员权限）；改名失败（老版本还开着）就接着用 ~/.shuttle。
const fs = require('node:fs')
const path = require('node:path')

function migrateDataDir(home) {
  const neu = path.join(home, '.annulo')
  const old = path.join(home, '.shuttle')
  if (fs.existsSync(neu)) return neu
  let st
  try {
    st = fs.lstatSync(old)
  } catch {
    return neu
  }
  if (!st.isDirectory() || st.isSymbolicLink()) return neu
  try {
    fs.renameSync(old, neu)
  } catch (e) {
    console.error(`数据目录没能从 ${old} 迁到 ${neu}，接着用老的：${e.message}`)
    return old
  }
  try {
    fs.symlinkSync(neu, old, process.platform === 'win32' ? 'junction' : 'dir')
  } catch (e) {
    console.error(`在 ${old} 留链接失败：${e.message}`)
  }
  return neu
}

module.exports = { migrateDataDir }
