// OpenPanda service worker: cache-first for static assets, network-only for
// /api/* (the API is live data behind auth — never cached). Deliberately
// minimal: install → precache the shell, fetch → pass through.

const CACHE = 'openpanda-v1'
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

  e.respondWith(
    caches.match(e.request).then(
      (hit) =>
        hit ??
        fetch(e.request).then((res) => {
          // Cache same-origin static responses for offline reloads.
          if (res.ok && url.origin === location.origin) {
            const copy = res.clone()
            caches.open(CACHE).then((c) => c.put(e.request, copy))
          }
          return res
        }),
    ),
  )
})
