// SPDX-License-Identifier: AGPL-3.0-or-later

// Wiring for the eight content pages (capabilities, docs, downloads, release
// notes, status, security, community and FAQ).
// They share the dictionary and the language toggle with index.html but have
// no starfield, palette or chapter index. Importing i18n.js translates the
// document before this module runs; everything below is optional chrome.

import { initLangToggle, ui } from './i18n.js'

initLangToggle()

/* ── copy buttons ───────────────────────────────────────────────────────── */

let toast
let toastTimer

function showToast(message) {
  if (!toast) {
    toast = document.createElement('div')
    toast.className = 'toast'
    toast.setAttribute('role', 'status')
    document.body.appendChild(toast)
  }
  toast.textContent = message
  toast.classList.add('show')
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => toast.classList.remove('show'), 1800)
}

async function writeClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // Clipboard API is unavailable on http:// and in some embedded frames.
    // Say so instead of pretending the copy worked.
    return false
  }
}

for (const btn of document.querySelectorAll('[data-copy-target]')) {
  btn.addEventListener('click', async () => {
    const target = document.getElementById(btn.dataset.copyTarget)
    if (!target) return
    const ok = await writeClipboard(target.textContent.trim())
    if (ok) {
      btn.classList.add('copied')
      setTimeout(() => btn.classList.remove('copied'), 1600)
    }
    showToast(ok ? ui().copied : ui().copyFail)
  })
}

/* ── table of contents ─────────────────────────────────────────────────── */

const tocLinks = [...document.querySelectorAll('.pg-toc nav a')]
if (tocLinks.length && 'IntersectionObserver' in window) {
  const byId = new Map(tocLinks.map((a) => [a.getAttribute('href').slice(1), a]))
  const spy = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      for (const link of tocLinks) link.classList.remove('on')
      byId.get(entry.target.id)?.classList.add('on')
    }
  }, { rootMargin: '-18% 0px -72% 0px', threshold: 0 })
  for (const id of byId.keys()) {
    const section = document.getElementById(id)
    if (section) spy.observe(section)
  }
}
