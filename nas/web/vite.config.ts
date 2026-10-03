import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vite'

// 打包結果由 Go 用 go:embed 內嵌（nas/internal/api/dist）。
// 開發時 `npm run dev`，API 轉給本機的 kara-nas（預設 :8765）。
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/api/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8765',
      '/media': 'http://127.0.0.1:8765',
      '/fonts': 'http://127.0.0.1:8765',
      '/healthz': 'http://127.0.0.1:8765',
    },
  },
})
