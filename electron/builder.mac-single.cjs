const base = require('./package.json').build

// Chromium 和 Go 后台程序都按架构拆，不能只拆 Electron、却仍带两份后台程序。
module.exports = {
  ...base,
  mac: {
    ...base.mac,
    target: [{ target: 'dmg', arch: ['arm64', 'x64'] }],
    extraResources: [{ from: '../dist/electron/mac-tools/${arch}/bin', to: 'bin', filter: ['annulo', 'shuttle'] }],
  },
}
