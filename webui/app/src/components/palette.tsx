import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { locale, localeNames, locales, setLocale, t, type Locale } from '../i18n'
import { navigateView, navViews } from '../nav'
import { setTheme, theme, type Theme } from '../theme'
import { rank } from './fuzzy'

// ⌘K command palette. Every destination and appearance switch in the console
// reachable from one keystroke, so learning the sidebar's tree is optional
// rather than required — the fastest path to "Reminders" should be typing
// "rem", not remembering which of four collapsed groups it lives under.
//
// The command list is derived from nav.ts, not written out again here: a view
// added to the sidebar is in the palette the same day.

interface Command {
  id: string
  /** Localized section heading, shown above the row in the unfiltered list. */
  group: string
  /** Localized label — the primary thing the query is matched against. */
  label: string
  /** Extra English search terms, so `queue` finds 队列 in any locale. */
  alias?: string
  /** Right-aligned note: the current value of a setting, mostly. */
  hint?: string
  run(): void
}

let openFn: (() => void) | null = null

/** Open the palette from anywhere (the sidebar's trigger button). No-op until
 *  the host is mounted, which matches ConfirmHost's contract. */
export function openPalette(): void {
  openFn?.()
}

/** Mount once in the app shell, below the sidebar so it can cover it. */
export function PaletteHost({ onLogout }: { onLogout(): void }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLDivElement>(null)

  useEffect(() => {
    openFn = () => setOpen(true)
    return () => {
      openFn = null
    }
  }, [])

  // ⌘K / Ctrl+K anywhere, including from inside the chat composer — the whole
  // point is that it works without first reaching for the mouse.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setOpen((v) => !v)
      }
    }
    addEventListener('keydown', on)
    return () => removeEventListener('keydown', on)
  }, [])

  // A fresh open is a fresh search, and the caret belongs in the box.
  useEffect(() => {
    if (!open) return
    setQuery('')
    setCursor(0)
    input.current?.focus()
  }, [open])

  const commands = useMemo(() => (open ? buildCommands(onLogout) : []), [open, onLogout])
  const shown = useMemo(
    () => rank(commands, query.trim(), (c) => [c.label, c.alias ?? '']),
    [commands, query],
  )
  // Ranking reorders on every keystroke; the highlight follows the top hit
  // rather than whatever row happens to sit at the old index.
  useEffect(() => setCursor(0), [query])

  if (!open) return null

  function runAt(i: number) {
    const cmd = shown[i]
    if (!cmd) return
    setOpen(false)
    cmd.run()
  }

  function onKeyDown(e: KeyboardEvent) {
    const step = (d: number) => {
      e.preventDefault()
      if (shown.length === 0) return
      const next = (cursor + d + shown.length) % shown.length
      setCursor(next)
      // Keep the highlight on screen — the list scrolls, the selection must
      // not slide out from under the arrow keys.
      list.current?.children[next]?.scrollIntoView({ block: 'nearest' })
    }
    if (e.key === 'ArrowDown' || (e.ctrlKey && e.key === 'n')) return step(1)
    if (e.key === 'ArrowUp' || (e.ctrlKey && e.key === 'p')) return step(-1)
    if (e.key === 'Enter') {
      e.preventDefault()
      runAt(cursor)
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      setOpen(false)
    }
  }

  const grouped = query.trim() === ''

  return (
    <div
      class="palette-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) setOpen(false)
      }}
    >
      <div class="palette" role="dialog" aria-modal="true" aria-label={t('palette.title')}>
        <input
          ref={input}
          class="palette-input"
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-list"
          aria-autocomplete="list"
          placeholder={t('palette.placeholder')}
          value={query}
          onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
          onKeyDown={onKeyDown}
        />
        <div class="palette-list" id="palette-list" role="listbox" ref={list}>
          {shown.map((cmd, i) => (
            <div key={cmd.id} class="palette-row">
              {grouped && shown[i - 1]?.group !== cmd.group && (
                <div class="palette-group">{cmd.group}</div>
              )}
              <button
                class={`palette-item${i === cursor ? ' active' : ''}`}
                role="option"
                aria-selected={i === cursor}
                onMouseMove={() => setCursor(i)}
                onClick={() => runAt(i)}
              >
                <span class="palette-label">{cmd.label}</span>
                {cmd.hint && <span class="palette-hint">{cmd.hint}</span>}
              </button>
            </div>
          ))}
          {shown.length === 0 && <p class="palette-empty">{t('palette.noMatch')}</p>}
        </div>
        <div class="palette-foot">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t('palette.footMove')}
          </span>
          <span>
            <kbd>↵</kbd> {t('palette.footRun')}
          </span>
          <span>
            <kbd>esc</kbd> {t('palette.footClose')}
          </span>
        </div>
      </div>
    </div>
  )
}

/** The command list, rebuilt on each open so labels follow the locale and the
 *  appearance/language hints show what is currently selected. */
function buildCommands(onLogout: () => void): Command[] {
  const go = t('palette.group.go')
  const cmds: Command[] = navViews.map(([view, key]) => ({
    id: `go:${view}`,
    group: go,
    label: t(key),
    alias: view === 'sessions' ? 'chat sessions' : view,
    run: () => navigateView(view),
  }))

  const themes: Array<[Theme, string]> = [
    ['light', 'settings.theme.light'],
    ['dark', 'settings.theme.dark'],
    ['auto', 'settings.theme.auto'],
  ]
  for (const [value, key] of themes) {
    cmds.push({
      id: `theme:${value}`,
      // Reuses the settings page's own wording — two names for one switch is
      // exactly the kind of thing that makes a console feel bigger than it is.
      group: t('settings.theme'),
      label: t(key),
      alias: `theme appearance ${value}`,
      hint: theme() === value ? '✓' : undefined,
      run: () => setTheme(value),
    })
  }

  for (const l of locales) {
    cmds.push({
      id: `lang:${l}`,
      group: t('palette.group.language'),
      label: localeNames[l],
      alias: `language locale ${l}`,
      hint: locale() === l ? '✓' : undefined,
      run: () => setLocale(l as Locale),
    })
  }

  cmds.push({
    id: 'session:logout',
    group: t('palette.group.account'),
    label: t('token.logout'),
    alias: 'logout sign out token',
    run: onLogout,
  })
  return cmds
}
