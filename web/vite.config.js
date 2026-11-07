import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发时 Vite 跑 5173，API 请求代理到 Go 的 8090——
// 这样改前端只刷新页面，不用重编 Go 二进制。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8090',
      '/healthz': 'http://127.0.0.1:8090',
      '/readyz': 'http://127.0.0.1:8090',
    },
  },
})
