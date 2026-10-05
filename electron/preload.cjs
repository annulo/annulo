const { contextBridge, ipcRenderer } = require('electron')

// 只提供错误页的三个动作和 App 更新；网页不能调用任意命令、读文件或拿到 Node.js。主进程还会核对发送方（main.cjs）。
contextBridge.exposeInMainWorld('shuttleDesktop', {
  retry: () => ipcRenderer.send('shuttle:retry'),
  copyDiagnostics: () => ipcRenderer.send('shuttle:copy-diagnostics'),
  openLogs: () => ipcRenderer.send('shuttle:open-logs'),
  update: {
    state: () => ipcRenderer.invoke('shuttle:update-state'),
    check: () => ipcRenderer.invoke('shuttle:update-check'),
    download: () => ipcRenderer.send('shuttle:update-download'),
    install: () => ipcRenderer.send('shuttle:update-install'),
    onChange: callback => {
      const listener = (_event, state) => callback(state)
      ipcRenderer.on('shuttle:update', listener)
      return () => ipcRenderer.removeListener('shuttle:update', listener)
    },
  },
})
