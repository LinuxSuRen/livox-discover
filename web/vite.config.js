import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// dev 模式把 /api 代理到本地 Go 服务（livox-discover web -listen 127.0.0.1:8080）
export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
