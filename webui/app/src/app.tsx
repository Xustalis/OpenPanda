import { useEffect, useState } from 'preact/hooks'
import { PandaAscii, PandaWordmark } from './brand/panda'
import { clearToken, getToken, onUnauthorized, setToken } from './api/client'
import { onLocaleChange, t } from './i18n'
import { defaultCollapsed, navGroups, navigate, parseHash, type Route } from './nav'
import { QueueView } from './views/queue'
import { DetailView } from './views/detail'
import { SessionsView } from './views/sessions'
import { ProjectsView } from './views/projects'
import { NodesView } from './views/nodes'
import { SettingsView } from './views/settings'
import { OnboardingBanner } from './views/onboarding'
import { SkillsView } from './views/skills'
import { SystemView } from './views/system'
import { RemindersView } from './views/reminders'
import { MemoryView } from './views/memory'
import { ToastHost } from './components/toast'
import { ConfirmHost } from './components/confirm'
import { PaletteHost, openPalette } from './components/palette'

/** Re-render the subtree on locale change (the whole shell is cheap). */
function useLocaleRerender(): void {
  const [, force] = useState(0)
  useEffect(() => onLocaleChange(() => force((v) => v + 1)), [])
}

/** Hash-based routing: back/forward work, deep links to a task work. */
function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseHash)
  useEffect(() => {
    const on = () => setRoute(parseHash())
    addEventListener('hashchange', on)
    return () => removeEventListener('hashchange', on)
  }, [])
  return route
}

/** True while the shell is in its narrow layout, where the sidebar is a
 *  horizontal strip rather than a column.
 *
 *  The strip shows every destination flattened into one scrolling row, because
 *  a collapsible group head is a disclosure control for a *column* and there
 *  is no column here to disclose. That in turn means the groups have to be
 *  open in the strip whatever the user folded on the desktop layout —
 *  otherwise hiding the heads would leave Nodes, Skills, Reminders, Memory and
 *  System with nothing anywhere that reaches them. Kept in JS rather than CSS
 *  for exactly that reason: the collapse is state, not presentation.
 *
 *  Matches the `max-width: 720px` breakpoint in styles.css. */
function useNarrow(): boolean {
  const query = '(max-width: 720px)'
  const [narrow, setNarrow] = useState(() => matchMedia(query).matches)
  useEffect(() => {
    const mq = matchMedia(query)
    const on = () => setNarrow(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return narrow
}

export function App() {
  useLocaleRerender()
  const route = useRoute()
  const narrow = useNarrow()
  const [authed, setAuthed] = useState<boolean>(getToken() !== '')
  const [gateError, setGateError] = useState('')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(defaultCollapsed)

  const toggleGroup = (id: string) => setCollapsed((c) => ({ ...c, [id]: !c[id] }))

  useEffect(() => onUnauthorized(() => setAuthed(false)), [])

  if (!authed) {
    return (
      <TokenGate
        error={gateError}
        onConnected={() => {
          setGateError('')
          setAuthed(true)
        }}
        onRejected={() => setGateError(t('token.invalid'))}
      />
    )
  }

  const active: string = route.view === 'detail' ? 'queue' : route.view
  const logout = () => {
    clearToken()
    setAuthed(false)
  }

  return (
    <div class="shell">
      <ToastHost />
      <ConfirmHost />
      <PaletteHost onLogout={logout} />
      <aside class="sidebar">
        <a href="#/chat" class="wordmark-link">
          <PandaWordmark />
        </a>
        {/* The palette's trigger is a visible button, not just a shortcut: a
            keystroke nobody is told about is a feature nobody has. */}
        <button class="palette-trigger" onClick={openPalette}>
          <span>{t('palette.trigger')}</span>
          <kbd>{modKeyLabel()}K</kbd>
        </button>
        <a href="#/chat" class={`nav-item${active === 'sessions' ? ' active' : ''}`}>
          {t('nav.sessions')}
        </a>
        {navGroups.map((group) => {
          const containsActive = group.items.some(([v]) => v === active)
          // A group holding the current view always stays open, even if the
          // user folded it, so the active item is never hidden. In the narrow
          // strip every group is open — see useNarrow.
          const open = narrow || containsActive || !collapsed[group.id]
          return (
            <div class="nav-group" key={group.id}>
              <button
                class="nav-group-head"
                onClick={() => toggleGroup(group.id)}
                aria-expanded={open}
              >
                <span>{t(group.label)}</span>
                <span class={`nav-chevron${open ? ' open' : ''}`} aria-hidden="true">
                  ▾
                </span>
              </button>
              {open &&
                group.items.map(([v, key]) => (
                  <a key={v} href={`#/${v}`} class={`nav-item${active === v ? ' active' : ''}`}>
                    {t(key)}
                  </a>
                ))}
            </div>
          )
        })}
        {/* The footer holds only what is not a page of its own. Language and
            appearance used to sit here as well, which meant the console
            shipped two controls for one setting and neither told you the
            other existed — they live on the settings page now, and ⌘K
            switches them without leaving the current view. */}
        <div class="sidebar-footer">
          <a href="#/settings" class={`nav-item${active === 'settings' ? ' active' : ''}`}>
            {t('nav.settings')}
          </a>
          <button class="nav-item" onClick={logout}>
            {t('token.logout')}
          </button>
        </div>
      </aside>
      <main class="main">
        <OnboardingBanner />
        {route.view === 'sessions' && (
          <SessionsView
            activeId={route.id}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null })}
            onOpenTask={(id) => navigate({ view: 'detail', id })}
          />
        )}
        {route.view === 'queue' && (
          <QueueView
            onOpen={(id) => navigate({ view: 'detail', id })}
            onOpenSession={(id) => navigate({ view: 'sessions', id })}
          />
        )}
        {route.view === 'detail' && (
          <DetailView id={route.id} onBack={() => navigate({ view: 'queue' })} />
        )}
        {route.view === 'projects' && <ProjectsView onOpenProject={() => navigate({ view: 'queue' })} />}
        {route.view === 'nodes' && <NodesView />}
        {route.view === 'skills' && <SkillsView />}
        {route.view === 'reminders' && <RemindersView />}
        {route.view === 'memory' && <MemoryView />}
        {route.view === 'system' && <SystemView />}
        {route.view === 'settings' && <SettingsView />}
      </main>
    </div>
  )
}

/** The modifier the palette actually listens for, spelled the way the user's
 *  keyboard spells it: ⌘ on Apple hardware, Ctrl everywhere else. */
function modKeyLabel(): string {
  const p = navigator.platform || ''
  return /Mac|iPhone|iPad/.test(p) ? '⌘' : 'Ctrl '
}

/** First-run screen: ask for the panel token, validate it against /api/tasks. */
function TokenGate(props: { error: string; onConnected(): void; onRejected(): void }) {
  const [token, setTokenInput] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: Event) {
    e.preventDefault()
    if (!token.trim() || busy) return
    setBusy(true)
    try {
      setToken(token.trim())
      const res = await fetch('/api/tasks', {
        headers: { Authorization: `Bearer ${token.trim()}` },
      })
      if (res.ok) props.onConnected()
      else props.onRejected()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="gate">
      <form class="gate-card" onSubmit={submit}>
        <PandaAscii scale={9} />
        <p class="gate-lede">{t('token.description')}</p>
        <label for="token">{t('token.label')}</label>
        <input
          id="token"
          class="input"
          type="password"
          value={token}
          onInput={(e) => setTokenInput((e.target as HTMLInputElement).value)}
          autocomplete="off"
        />
        {props.error && <p class="gate-error">{props.error}</p>}
        <button class="btn primary" type="submit" disabled={busy || !token.trim()}>
          {t('token.submit')}
        </button>
      </form>
    </div>
  )
}
