// Appearance theme: light / dark / follow-system (C1). The choice rides on
// <html data-theme> so the CSS-variable layers in styles.css can react
// without any re-render: `data-theme="light"` forces light tokens,
// `data-theme="dark"` forces dark tokens, and with "auto" (or no attribute)
// the prefers-color-scheme media query decides. Persisted in localStorage.

export type Theme = 'light' | 'dark' | 'auto'

const STORAGE_KEY = 'openpanda.theme'

const listeners = new Set<() => void>()
let current: Theme = detectTheme()

function detectTheme(): Theme {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved === 'light' || saved === 'dark' || saved === 'auto') return saved
  } catch {
    // storage unavailable — theme just won't persist
  }
  return 'auto'
}

/** Apply the choice to <html data-theme>. "auto" removes the attribute so
 *  the OS preference media query is in charge. */
function apply(theme: Theme): void {
  const el = document.documentElement
  if (theme === 'auto') el.removeAttribute('data-theme')
  else el.setAttribute('data-theme', theme)
  try {
    // Keep color-scheme hints (scrollbars, form controls) in sync too.
    el.style.colorScheme = theme === 'auto' ? 'dark light' : theme
  } catch {
    // ignore
  }
}

// Apply on import so the first paint already has the right tokens.
apply(current)

export function theme(): Theme {
  return current
}

export function setTheme(next: Theme): void {
  if (next === current) return
  current = next
  try {
    localStorage.setItem(STORAGE_KEY, next)
  } catch {
    // ignore
  }
  apply(next)
  listeners.forEach((fn) => fn())
}

/** Subscribe to theme changes (for picker re-render). Returns unsubscribe. */
export function onThemeChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
