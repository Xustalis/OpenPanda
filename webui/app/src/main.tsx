import { render } from 'preact'
import { App } from './app'
import './styles.css'

render(<App />, document.getElementById('app')!)

// PWA: register the service worker for offline shell reloads and Web Push
// (reminder notifications). The SW never caches /api/*, so live data stays
// live. Loopback is a secure context too, so `panda web` on 127.0.0.1 gets
// push notifications without TLS.
const isSecureContext =
  location.protocol === 'https:' ||
  ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)
if ('serviceWorker' in navigator && isSecureContext) {
  navigator.serviceWorker.register('/sw.js').catch(() => {
    // offline support is best-effort
  })
}
