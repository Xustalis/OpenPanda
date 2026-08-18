import { useEffect, useState } from 'preact/hooks'
import { PandaWordmark } from './brand/panda'
import { clearToken, getToken, onUnauthorized, setToken } from './api/client'
import { locale, localeNames, locales, onLocaleChange, setLocale, t } from './i18n'
import { QueueView } from './views/queue'
import { DetailView } from './views/detail'
import { AskView } from './views/ask'
import { ProjectsView } from './views/projects'
import { NodesView } from './views/nodes'

type Route =
  | { view: 'queue' }
  | { view: 'ask' }
  | { view: 'projects' }
  | { view: 'nodes' }
  | { view: 'detail'; id: string }

function parseHash(): Route {
  const hash = location.hash.replace(/^#\/?/, '')
  if (hash.startsWith('task/')) return { view: 'detail', id: decodeURIComponent(hash.slice(5)) }
  if (hash === 'ask') return { view: 'ask' }
  if (hash === 'projects') return { view: 'projects' }
  if (hash === 'nodes') return { view: 'nodes' }
  return { view: 'queue' }
}

function navigate(route: Route): void {
  location.hash =
    route.view === 'detail' ? `#/task/${encodeURIComponent(route.id)}` : `#/${route.view}`
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
        <a href="#/queue" class="wordmark-link">
          <PandaWordmark />
        </a>
        {(
          [
            ['queue', 'nav.queue'],
            ['ask', 'nav.ask'],
            ['projects', 'nav.projects'],
            ['nodes', 'nav.nodes'],
          ] as const
        ).map(([v, key]) => (
          <a key={v} href={`#/${v}`} class={`nav-item${active === v ? ' active' : ''}`}>
            {t(key)}
          </a>
        ))}
        <div class="sidebar-footer">
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
        {route.view === 'queue' && <QueueView onOpen={(id) => navigate({ view: 'detail', id })} />}
        {route.view === 'detail' && (
          <DetailView id={route.id} onBack={() => navigate({ view: 'queue' })} />
        )}
        {route.view === 'ask' && (
          <AskView onTaskCreated={(id) => navigate({ view: 'detail', id })} />
        )}
        {route.view === 'projects' && <ProjectsView onOpenProject={() => navigate({ view: 'queue' })} />}
        {route.view === 'nodes' && <NodesView />}
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
