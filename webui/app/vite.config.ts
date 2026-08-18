import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

// The build lands in webui/panel/dist/app so `go:embed` can fold it into the
// panel binary (single-binary distribution). The placeholder index.html in
// dist/ is committed and NEVER overwritten by the build — vite empties only
// dist/app, so `git add -A` can never leak a hashed build artifact into the
// repo (a leaked index.html pointing at missing /assets/* = white screen for
// anyone cloning). Dev proxies /api to a local panel sidecar; the Bearer
// token is handled by the app itself.
export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: '../panel/dist/app',
    emptyOutDir: true,
  },
  server: {
    port: 5273,
    proxy: {
      '/api': 'http://127.0.0.1:7840',
    },
  },
})
