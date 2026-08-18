import { render } from 'preact'
import { App } from './app'
import './styles.css'

render(<App />, document.getElementById('app')!)

// PWA: register the service worker for offline shell reloads. The SW never
// caches /api/*, so live data stays live.
if ('serviceWorker' in navigator && location.protocol === 'https:') {
  navigator.serviceWorker.register('/sw.js').catch(() => {
    // offline support is best-effort
  })
}
