// SPDX-License-Identifier: AGPL-3.0-or-later

// Tiny dependency-free i18n. Adding a language = one messages file + one
// entry in the registry below; nothing else in the app changes.
//
// English is the only dictionary imported eagerly: it is the fallback `t()`
// needs before anything else loads. Every other locale is a dynamic import,
// so a Chinese or German reader downloads their language chunk (and the
// English fallback), not all five. The app awaits the detected locale in
// main.tsx before the first render, so no key flashes.

export type Locale = 'en' | 'zh-CN' | 'ja' | 'es' | 'de'

export interface Messages {
  [key: string]: string
}

import en from './en.ts'

export const localeNames: Record<Locale, string> = {
  en: 'English',
  'zh-CN': '简体中文',
  ja: '日本語',
  es: 'Español',
  de: 'Deutsch',
}

type LoadedLocale = Exclude<Locale, 'en'>

const loaders: Record<LoadedLocale, () => Promise<{ default: Messages }>> = {
  'zh-CN': () => import('./zh-CN.ts'),
  ja: () => import('./ja.ts'),
  es: () => import('./es.ts'),
  de: () => import('./de.ts'),
}

// English is always present; the detected locale is added by loadLocale().
const english: Messages = en
const dictionaries: Partial<Record<Locale, Messages>> = { en: english }

export const locales: Locale[] = ['en', 'zh-CN', 'ja', 'es', 'de']

function isLocale(value: string | null | undefined): value is Locale {
  return value !== null && value !== undefined && (locales as string[]).includes(value)
}

/** Load one locale's dictionary. Idempotent; English is already loaded. */
export async function loadLocale(l: Locale): Promise<void> {
  if (l === 'en' || dictionaries[l]) return
  const mod = await loaders[l]()
  dictionaries[l] = mod.default
}

const STORAGE_KEY = 'openpanda.locale'

const listeners = new Set<() => void>()
let current: Locale = detectLocale()

// The detected locale applies to the document from the start — not only
// after an explicit switch — so <html lang> is right on first paint.
if (typeof document !== 'undefined') {
  document.documentElement.lang = current
}

function detectLocale(): Locale {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (isLocale(saved)) return saved
  } catch {
    // private mode etc. — fall through to navigator
  }
  // Guarded so the module stays importable outside a browser (node tests).
  const tags = typeof navigator !== 'undefined' ? navigator.languages ?? [] : []
  for (const tag of tags) {
    if (isLocale(tag)) return tag
    const base = tag.split('-')[0]
    const hit = locales.find((l) => l.split('-')[0] === base)
    if (hit) return hit
  }
  return 'en'
}

/** Translate a key, interpolating `{param}` placeholders. Falls back to
 *  English, then to the key itself so missing strings stay greppable. The
 *  second argument may instead be a plain fallback string (used for
 *  wire-stable identifiers such as task states). */
export function t(key: string, params?: Record<string, string | number> | string): string {
  let text = dictionaries[current]?.[key] ?? english[key] ?? key
  if (typeof params === 'string') {
    if (text === key) return params
    return text
  }
  if (params) {
    for (const [name, value] of Object.entries(params)) {
      // split/join, not replaceAll: a string replacement interprets `$&`,
      // `$1` and friends, so a title or path containing `$&` used to be
      // mangled by its own interpolation.
      text = text.split(`{${name}}`).join(String(value))
    }
  }
  return text
}

export function locale(): Locale {
  return current
}

/** Switch locale. The dictionary is loaded first, so listeners — and the
 *  re-render they trigger — already see the new strings. */
export async function setLocale(l: Locale): Promise<void> {
  if (!isLocale(l) || l === current) return
  await loadLocale(l)
  current = l
  try {
    localStorage.setItem(STORAGE_KEY, l)
  } catch {
    // storage unavailable — locale just won't persist
  }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = l
  }
  listeners.forEach((fn) => fn())
}

/** Subscribe to locale changes. Returns an unsubscribe function. */
export function onLocaleChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
