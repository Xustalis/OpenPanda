// Tiny dependency-free i18n. Adding a language = one messages file + one
// line in the registry below; nothing else in the app changes.

export type Locale = 'en' | 'zh-CN' | 'ja' | 'es' | 'de'

export interface Messages {
  [key: string]: string
}

import de from './de'
import en from './en'
import es from './es'
import ja from './ja'
import zhCN from './zh-CN'

export const localeNames: Record<Locale, string> = {
  en: 'English',
  'zh-CN': '简体中文',
  ja: '日本語',
  es: 'Español',
  de: 'Deutsch',
}

const dictionaries: Record<Locale, Messages> = {
  en,
  'zh-CN': zhCN,
  ja,
  es,
  de,
}

export const locales = Object.keys(dictionaries) as Locale[]

const STORAGE_KEY = 'openpanda.locale'

const listeners = new Set<() => void>()
let current: Locale = detectLocale()

function detectLocale(): Locale {
  try {
    const saved = localStorage.getItem(STORAGE_KEY) as Locale | null
    if (saved && saved in dictionaries) return saved
  } catch {
    // private mode etc. — fall through to navigator
  }
  for (const tag of navigator.languages ?? []) {
    if (tag in dictionaries) return tag as Locale
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
  let text = dictionaries[current][key] ?? dictionaries.en[key] ?? key
  if (typeof params === 'string') {
    if (text === key) return params
    return text
  }
  if (params) {
    for (const [name, value] of Object.entries(params)) {
      text = text.replaceAll(`{${name}}`, String(value))
    }
  }
  return text
}

export function locale(): Locale {
  return current
}

export function setLocale(l: Locale): void {
  if (!(l in dictionaries) || l === current) return
  current = l
  try {
    localStorage.setItem(STORAGE_KEY, l)
  } catch {
    // storage unavailable — locale just won't persist
  }
  document.documentElement.lang = l
  listeners.forEach((fn) => fn())
}

/** Subscribe to locale changes. Returns an unsubscribe function. */
export function onLocaleChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
