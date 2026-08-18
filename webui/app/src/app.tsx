import { useEffect, useState } from 'preact/hooks'
import { PandaWordmark } from './brand/panda'
import { clearToken, getToken, onUnauthorized, setToken } from './api/client'
import { locale, localeNames, locales, onLocaleChange, setLocale, t } from './i18n'

type View = 'queue' | 'ask' | 'projects' | 'nodes'

const VIEW_KEYS: Record<View, string> = {
  queue: 'nav.queue',
  ask: 'nav.ask',
  projects: 'nav.projects',
  nodes: 'nav.nodes',
}

/** Re-render the subtree on locale change (the whole shell is cheap). */
function useLocaleRerender(): void {
  const [, force] = useState(0)
  useEffect(() => onLocaleChange(() => force((v) => v + 1)), [])
}

export function App() {
  useLocaleRerender()

  const [view, setView] = useState<View>('queue')
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

  return (
    <div class="shell">
      <aside class="sidebar">
        <PandaWordmark />
        {(Object.keys(VIEW_KEYS) as View[]).map((v) => (
          <button
            key={v}
            class={`nav-item${view === v ? ' active' : ''}`}
            onClick={() => setView(v)}
          >
            {t(VIEW_KEYS[v])}
          </button>
        ))}
        <div class="sidebar-footer">
          <LocalePicker />
          <button class="nav-item" onClick={() => { clearToken(); setAuthed(false) }}>
            {t('token.logout')}
          </button>
        </div>
      </aside>
      <main class="main">
        {/* Views land in Phase C2; the shell proves nav + i18n + auth wiring. */}
        <h1 class="page-title">{t(VIEW_KEYS[view])}</h1>
        <p class="page-sub">{t('app.tagline')}</p>
        <div class="card">{t('common.empty')}</div>
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
      // Cheap auth probe: 401 fires onUnauthorized and throws ApiError(401).
      const res = await fetch('/api/tasks', { headers: { Authorization: `Bearer ${token.trim()}` } })
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
