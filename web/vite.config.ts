import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwind from '@tailwindcss/vite'

// 开发时前端跑在 vite 上，/_shuttle/api 转给本地跑着的 shuttle（make dev-server）。
const backend = process.env.SHUTTLE_BACKEND || 'http://127.0.0.1:7799'

export default defineConfig({
  base: '/_shuttle/',
  plugins: [react(), tailwind()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  server: {
    proxy: {
      '/_shuttle/api': { target: backend, changeOrigin: true },
    },
  },
  // 原生 macOS 外壳使用系统 WebKit；不能按构建机的新 Safari 生成语法。
  build: { outDir: 'dist', emptyOutDir: true, target: ['es2020', 'safari15'], cssTarget: 'safari15' },
})
