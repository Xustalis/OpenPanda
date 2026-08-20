// OpenPanda service worker: network-first for navigations (index.html must
// always track the latest build), cache-first for static assets (build
// output is content-hashed, so new deploys carry new URLs and stale entries
// are never served), network-only for /api/* (live data behind auth).
// Bumping CACHE retires old caches on activate via the whitelist sweep.

const CACHE = 'openpanda-v2'
const SHELL = ['/', '/favicon.svg', '/manifest.webmanifest']

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url)
  if (url.pathname.startsWith('/api/')) return // live data: always network
  if (e.request.method !== 'GET') return
  if (url.origin !== location.origin) return

  // Navigations (index.html): network-first so a fresh deploy always wins;
  // fall back to the cached shell only when offline. Hash routing (#/...) 
  // keeps every navigation pointed at '/', which is what we precache.
  if (e.request.mode === 'navigate') {
    e.respondWith(
      fetch(e.request)
        .then((res) => {
          if (res.ok) {
            const copy = res.clone()
            caches.open(CACHE).then((c) => c.put('/', copy))
          }
          return res
        })
        .catch(() => caches.match(e.request).then((hit) => hit ?? caches.match('/'))),
    )
    return
  }

  e.respondWith(
    caches.match(e.request).then(
      (hit) =>
        hit ??
        fetch(e.request).then((res) => {
          // Cache same-origin static responses for offline reloads.
          if (res.ok) {
            const copy = res.clone()
            caches.open(CACHE).then((c) => c.put(e.request, copy))
          }
          return res
        }),
    ),
  )
})

// Web Push: show reminder notifications even when the tab is in the
// background. Payload: {title, body, id, icon, badge}.
self.addEventListener('push', (e) => {
  let data = {}
  try {
    data = e.data ? e.data.json() : {}
  } catch {
    data = { body: e.data ? e.data.text() : '' }
  }
  e.waitUntil(
    self.registration.showNotification(data.title || 'OpenPanda', {
      body: data.body || '',
      tag: data.id || undefined,
      icon: data.icon || '/icons/icon-192.png',
      badge: data.badge || '/icons/badge-72.png',
    }),
  )
})

self.addEventListener('notificationclick', (e) => {
  e.notification.close()
  e.waitUntil(self.clients.openWindow('/#/reminders'))
})
