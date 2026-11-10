// SPDX-License-Identifier: AGPL-3.0-or-later

import { render } from 'preact'
import { App } from './app'
import { ErrorBoundary } from './components/error-boundary'
import { loadLocale, locale } from './i18n'
import './theme' // applies the persisted light/dark/auto choice before first paint
import './styles.css'

const root = document.getElementById('app')!

// Every non-English message table is a dynamic chunk. Load the reader's
// locale (English is already in the main bundle) before the first render so
// no key flashes; a failed chunk falls back to English rather than a blank
// page.
loadLocale(locale())
  .catch(() => undefined)
  .finally(() => {
    render(
      <ErrorBoundary>
        <App />
      </ErrorBoundary>,
      root,
    )
  })

// PWA: register the service worker for offline shell reloads and Web Push
// (approval notifications). The SW never caches /api/*, so live data stays
// live; navigations are network-first, so a fresh deploy always wins.
// `updateViaCache: 'none'` forces the browser to revalidate sw.js itself on
// every update check instead of trusting its HTTP cache. Loopback is a
// secure context too, so `panda web` on 127.0.0.1 gets push notifications
// without TLS.
const isSecureContext =
  location.protocol === 'https:' ||
  ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)
if ('serviceWorker' in navigator && isSecureContext) {
  navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).catch(() => {
    // offline support is best-effort
  })
}
