import { defineConfig } from 'vite'

// 开发模式：本机起 Vite dev server (5173)，
// /api 与 /schema 代理到 Go 服务（默认 127.0.0.1:8080，
// 可用 SQLITEX_ADMIN_API 环境变量覆盖，如本机 8080 被占用时），
// 前后端各自独立迭代，无需重新编译 Go 二进制。
const apiTarget = process.env.SQLITEX_ADMIN_API || 'http://127.0.0.1:8080'

export default defineConfig({
  server: {
    port: 5173,
    proxy: {
      '/api': apiTarget,
      '/schema': apiTarget,
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
