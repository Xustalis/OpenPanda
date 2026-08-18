import { useEffect, useState } from 'preact/hooks'
import { PandaWordmark } from './brand/panda'
import { clearToken, getToken, onUnauthorized, setToken } from './api/client'
import { locale, localeNames, locales, onLocaleChange, setLocale, t } from './i18n'
import { QueueView } from './views/queue'
import { DetailView } from './views/detail'
import { SessionsView } from './views/sessions'
import { ProjectsView } from './views/projects'
import { NodesView } from './views/nodes'
import { SettingsView } from './views/settings'
import { SkillsView } from './views/skills'
import { SystemView } from './views/system'
import { RemindersView } from './views/reminders'
import { MemoryView } from './views/memory'

type Route =
  | { view: 'sessions'; id: string | null }
  | { view: 'queue' }
  | { view: 'projects' }
  | { view: 'nodes' }
  | { view: 'skills' }
  | { view: 'reminders' }
  | { view: 'memory' }
  | { view: 'system' }
  | { view: 'settings' }
  | { view: 'detail'; id: string }

function parseHash(): Route {
  const hash = location.hash.replace(/^#\/?/, '')
  if (hash.startsWith('task/')) return { view: 'detail', id: decodeURIComponent(hash.slice(5)) }
  if (hash.startsWith('chat/')) return { view: 'sessions', id: decodeURIComponent(hash.slice(5)) }
  if (hash === 'queue') return { view: 'queue' }
  if (hash === 'projects') return { view: 'projects' }
  if (hash === 'nodes') return { view: 'nodes' }
  if (hash === 'skills') return { view: 'skills' }
  if (hash === 'reminders') return { view: 'reminders' }
  if (hash === 'memory') return { view: 'memory' }
  if (hash === 'system') return { view: 'system' }
  if (hash === 'settings') return { view: 'settings' }
  return { view: 'sessions', id: null }
}

function navigate(route: Route): void {
  if (route.view === 'detail') location.hash = `#/task/${encodeURIComponent(route.id)}`
  else if (route.view === 'sessions')
    location.hash = route.id ? `#/chat/${encodeURIComponent(route.id)}` : '#/chat'
  else location.hash = `#/${route.view}`
}

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

export function App() {
  useLocaleRerender()
  const route = useRoute()
  const [authed, setAuthed] = useState<boolean>(getToken() !== '')
  const [gateError, setGateError] = useState('')

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

  return (
    <div class="shell">
      <aside class="sidebar">
        <a href="#/chat" class="wordmark-link">
          <PandaWordmark />
        </a>
        {(
          [
            ['sessions', 'nav.sessions'],
            ['queue', 'nav.queue'],
            ['projects', 'nav.projects'],
            ['nodes', 'nav.nodes'],
            ['skills', 'nav.skills'],
            ['reminders', 'nav.reminders'],
            ['memory', 'nav.memory'],
            ['system', 'nav.system'],
          ] as const
        ).map(([v, key]) => (
          <a key={v} href={v === 'sessions' ? '#/chat' : `#/${v}`} class={`nav-item${active === v ? ' active' : ''}`}>
            {t(key)}
          </a>
        ))}
        <div class="sidebar-footer">
          <a href="#/settings" class={`nav-item${active === 'settings' ? ' active' : ''}`}>
            {t('nav.settings')}
          </a>
          <LocalePicker />
          <button
            class="nav-item"
            onClick={() => {
              clearToken()
              setAuthed(false)
            }}
          >
            {t('token.logout')}
          </button>
        </div>
      </aside>
      <main class="main">
        {route.view === 'sessions' && (
          <SessionsView
            activeId={route.id}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null })}
            onOpenTask={(id) => navigate({ view: 'detail', id })}
          />
        )}
        {route.view === 'queue' && <QueueView onOpen={(id) => navigate({ view: 'detail', id })} />}
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

function LocalePicker() {
  return (
    <select
      class="input"
      value={locale()}
      onChange={(e) => setLocale((e.target as HTMLSelectElement).value as never)}
      aria-label="Language"
    >
      {locales.map((l) => (
        <option key={l} value={l}>
          {localeNames[l]}
        </option>
      ))}
    </select>
  )
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
        <h1>
          <PandaWordmark />
        </h1>
        <p>{t('token.description')}</p>
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
