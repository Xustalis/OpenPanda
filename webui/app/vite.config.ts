import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

// The build lands in webui/panel/dist so `go:embed` can fold it into the
// panel binary (single-binary distribution). Dev proxies /api to a local
// panel sidecar; the Bearer token is handled by the app itself.
export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: '../panel/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5273,
    proxy: {
      '/api': 'http://127.0.0.1:7840',
    },
  },
})
