// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'
import { fileURLToPath } from 'node:url'

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
    rollupOptions: {
      output: {
        // One 1 MiB entry chunk re-downloads the whole console on every
        // change. The framework and the locale tables are the two parts that
        // move independently of the views, so they get their own cacheable
        // chunks instead of inflating the app bundle.
        manualChunks(id) {
          if (id.includes('node_modules/preact')) return 'preact'
          if (id.includes('/src/i18n/')) return 'i18n'
          if (id.includes('/src/md/')) return 'markdown'
        },
      },
    },
  },
  server: {
    port: 5273,
    // The terms card imports ../../TERMS*.md as raw text so the consent copy has
    // one home. Vite's dev server only serves files under the project root, so
    // the repo root has to be allowed explicitly (the build needs nothing).
    fs: {
      allow: [
        fileURLToPath(new URL('.', import.meta.url)),
        fileURLToPath(new URL('../..', import.meta.url)),
      ],
    },
    proxy: {
      '/api': process.env.PANDA_PANEL || 'http://127.0.0.1:7840',
    },
  },
})
