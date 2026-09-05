import { useEffect, useState } from 'preact/hooks'
import { PandaAscii, PandaWordmark } from './brand/panda'
import { api, clearToken, getToken, onUnauthorized, setToken, type UpdateStatus } from './api/client'
import { onLocaleChange, t } from './i18n'
import { useAsync } from './hooks'
import { navigate, parseHash, primaryNav, type Route } from './nav'
import { QueueView } from './views/queue'
import { DetailView } from './views/detail'
import { SessionsView } from './views/sessions'
import { ProjectsView } from './views/projects'
import { SettingsView } from './views/settings'
import { OnboardingBanner } from './views/onboarding'
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
        <button class="palette-trigger" onClick={openPalette}>
          <span>{t('palette.trigger')}</span>
          <kbd>{modKeyLabel()}K</kbd>
        </button>

        <nav class="primary-nav" aria-label="Primary Navigation">
          {primaryNav.map(([v, key]) => (
            <a
              key={v}
              href={`#/${v === 'sessions' ? 'chat' : v}`}
              class={`nav-item${active === v ? ' active' : ''}`}
            >
              {t(key)}
            </a>
          ))}
        </nav>

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
        <UpdateBanner />
        {route.view === 'sessions' && (
          <SessionsView
            activeId={route.id}
            project={route.project ?? null}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null, project: route.project })}
            onExitProject={() => {
              api.exitProject().catch(() => {})
              navigate({ view: 'sessions', id: null, project: null })
            }}
            onOpenTask={(id) => navigate({ view: 'detail', id })}
            onOpenNodes={() => navigate({ view: 'settings', tab: 'nodes' })}
            onLogout={logout}
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
        {route.view === 'projects' && (
          <ProjectsView
            onOpenProject={(name) => {
              api.enterProject(name).catch(() => {})
              navigate({ view: 'sessions', id: null, project: name })
            }}
          />
        )}
        {route.view === 'settings' && (
          <SettingsView
            initialSection={route.tab ?? undefined}
            onSelectTab={(tab) => navigate({ view: 'settings', tab })}
          />
        )}
      </main>
    </div>
  )
}

/** Paints a soft "updates paused" banner at the top of every view when the
 *  updater has backed off to idle (403 / rate limit / network offline).
 *  Also paints a "new version available" pill in the rare case a check
 *  succeeded and found one. Both are non-blocking — the banner is
 *  dismissible and collapses automatically after 15 seconds. */
function UpdateBanner() {
  const { data: self } = useAsync(() => api.self(), [])
  const [dismissed, setDismissed] = useState(false)
  const up: UpdateStatus | undefined = self?.update
  useEffect(() => {
    if (!up) return
    const id = setTimeout(() => setDismissed(true), 15000)
    return () => clearTimeout(id)
  }, [up?.stage, up?.error, up?.latest, up?.degraded])
  if (!up || dismissed) return null
  const paused = up.degraded || (up.stage === 'idle' && !!up.error)
  if (!paused && !up.available) return null
  if (paused) {
    return (
      <div class="banner banner-warn update-banner" role="status">
        <span class="banner-ico" aria-hidden>🔕</span>
        <span class="banner-body">
          <strong>{t('ui.update.degraded.title')}</strong>
          <span class="banner-sub">
            {up.error || t('ui.update.degraded.sub')}
          </span>
        </span>
        <a class="banner-link" href="#/system">{t('ui.update.degraded.cta')}</a>
        <button
          class="banner-close"
          onClick={() => setDismissed(true)}
          aria-label={t('ui.update.degraded.close')}
        >×</button>
      </div>
    )
  }
  // New version available is a rarer, softer path.
  return (
    <div class="banner banner-info update-banner" role="status">
      <span class="banner-ico" aria-hidden>✨</span>
      <span class="banner-body">
        <strong>{t('ui.update.available.title', { version: up.latest ?? '' })}</strong>
        <span class="banner-sub">{up.notes ?? t('ui.update.available.sub')}</span>
      </span>
      <a class="banner-link" href="#/system">{t('ui.update.available.cta')}</a>
      <button class="banner-close" onClick={() => setDismissed(true)} aria-label={t('ui.update.degraded.close')}>×</button>
    </div>
  )
}

/** The modifier the palette actually listens for, spelled the way the user's
 *  keyboard spells it: ⌘ on Apple hardware, Ctrl everywhere else. */
function modKeyLabel(): string {
  // Prefer the modern UA-CH hint; `navigator.platform` is deprecated but
  // remains the fallback on browsers without userAgentData.
  const ua = navigator as Navigator & { userAgentData?: { platform?: string } }
  const p = ua.userAgentData?.platform ?? navigator.platform ?? ''
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
