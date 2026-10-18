// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { PandaAscii } from './brand/panda'
import { api, clearToken, displayVersion, getToken, onUnauthorized, setToken, subscribeEvents, type UpdateStatus } from './api/client'
import { onLocaleChange, t } from './i18n'
import { useAsync } from './hooks'
import { navigate, parseHash, primaryNavGroups, type Route } from './nav'
import { QueueView } from './views/queue'
import { DetailView } from './views/detail'
import { SessionsView } from './views/sessions'
import { SidebarHistory } from './views/sidebar-history'
import { ProjectsView } from './views/projects'
import { PlansView } from './views/plans'
import { NodesView } from './views/nodes'
import { MemoryView } from './views/memory'
import { SkillsView } from './views/skills'
import { SettingsView } from './views/settings'
import { OnboardingBanner } from './views/onboarding'
import { ToastHost } from './components/toast'
import { Icon } from './components/icons'
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
        onUnreachable={() => setGateError(t('token.unreachable'))}
      />
    )
  }

  const active: string = route.view === 'detail' ? 'queue' : route.view
  const reviewCount = useReviewCount()
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
        {/* One line of chrome at the head of the rail: the name (which is also
            the way into the workspace list) and the one ambient action that
            works from any view. No chevron after the name — the head is the
            quietest thing in the column and the glyph only promised a menu
            the name already opens; no bell either, since reminders live in
            Settings and in ⌘K. The search field that used to live here is now
            a magnifier: a bordered input plus a "Ctrl K" chip made the head
            the heaviest thing in a column whose whole job is to be quiet
            enough to scan. */}
        <div class="sidebar-head">
          <button
            type="button"
            class="sidebar-workspace"
            title={t('nav.projects')}
            onClick={() => navigate({ view: 'projects' })}
          >
            <span class="sidebar-workspace-name">
              Open<b>Panda</b>
            </span>
          </button>
          <div class="sidebar-head-actions">
            <button
              type="button"
              class="icon-btn"
              title={`${t('palette.trigger')} ${modKeyLabel()}K`}
              aria-label={t('palette.trigger')}
              onClick={openPalette}
            >
              <Icon name="search" />
            </button>
          </div>
        </div>
        {/* 新对话: an action, not a destination — but it reads as a nav row
            (same 15px label, same hover wash) instead of a filled button, so
            the head above it stays the only chrome in the column. Navigates
            without minting a thread: the thread comes into being when the first
            message leaves the composer, titled after it, so clicking twice
            cannot litter the history with empty untitled sessions. */}
        <button
          type="button"
          class="nav-item nav-new-chat"
          onClick={() => navigate({ view: 'sessions', id: null })}
        >
          <Icon name="square-pen" />
          {t('sessions.new')}
        </button>

        <nav class="primary-nav" aria-label={t('nav.aria')}>
          {primaryNavGroups.map((g) => (
            <div class="nav-group" key={g.id}>
              {g.head && <div class="nav-group-head">{t(g.head)}</div>}
              {g.items.map(([v, key]) => (
                <a key={v} href={`#/${v}`} class={`nav-item${active === v ? ' active' : ''}`}>
                  {t(key)}
                  {v === 'queue' && reviewCount > 0 && (
                    <span
                      class="nav-badge"
                      title={t('queue.col.review')}
                      role="button"
                      tabIndex={0}
                      onClick={(e) => {
                        // Nested anchors are invalid HTML, so the badge is a
                        // span that hijacks the click: preventDefault kills the
                        // parent link's default navigation, then we route to
                        // the review-focused board ourselves.
                        e.preventDefault()
                        e.stopPropagation()
                        navigate({ view: 'queue', review: true })
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          e.stopPropagation()
                          navigate({ view: 'queue', review: true })
                        }
                      }}
                    >
                      {reviewCount}
                    </span>
                  )}
                </a>
              ))}
            </div>
          ))}
          {/* The rail's tail, not a row inside it. The history is what the rail
              is for, and 对话 above it only named the list again — so the row
              is gone and the list is always visible instead of folded. It
              scrolls with the nav, so a long history never pushes the
              destinations out of reach. */}
          <SidebarHistory
            activeId={route.view === 'sessions' ? route.id : null}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null })}
          />
        </nav>

        <div class="sidebar-footer">
          <SidebarNode />
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
            onLogout={logout}
          />
        )}
        {route.view === 'queue' && (
          <QueueView
            project={route.project}
            focusReview={!!route.review}
            onOpen={(id) => navigate({ view: 'detail', id })}
            onOpenSession={(id) => navigate({ view: 'sessions', id })}
          />
        )}
        {route.view === 'detail' && (
          <DetailView
            id={route.id}
            onBack={() => navigate({ view: 'queue' })}
            onOpenSession={(sid) => navigate({ view: 'sessions', id: sid })}
          />
        )}
        {route.view === 'plans' && <PlansView />}
        {route.view === 'fleet' && <NodesView />}
        {route.view === 'memory' && <MemoryView initialTab={route.tab ?? undefined} />}
        {route.view === 'skills' && <SkillsView />}
        {route.view === 'projects' && (
          <ProjectsView
            onOpenProject={(name) => {
              api.enterProject(name).catch(() => {})
              navigate({ view: 'sessions', id: null, project: name })
            }}
            onOpenQueue={(name) => navigate({ view: 'queue', project: name })}
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
        <Icon name="bell-off" class="banner-ico" />
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
      <Icon name="sparkles" class="banner-ico" />
      <span class="banner-body">
        <strong>{t('ui.update.available.title', { version: displayVersion(up.latest, up.latest_codename) })}</strong>
        <span class="banner-sub">{up.notes ?? t('ui.update.available.sub')}</span>
      </span>
      <a class="banner-link" href="#/system">{t('ui.update.available.cta')}</a>
      <button class="banner-close" onClick={() => setDismissed(true)} aria-label={t('ui.update.degraded.close')}>×</button>
    </div>
  )
}

/** Sidebar footer's node chip: this machine's name, a running/off status
 *  dot, and the running version — the at-a-glance answer to "is my node up,
 *  and what am I on?". Links to System, where the same data lives in full. */
function SidebarNode() {
  useLocaleRerender()
  const { data: self } = useAsync(() => api.self(), [])
  if (!self) return null
  const name = self.node_name || self.hostname
  if (!name) return null
  return (
    <a
      href="#/system"
      class="sidebar-node"
      title={t(self.node_running ? 'sidebar.node.up' : 'sidebar.node.down')}
    >
      <span class={`dot${self.node_running ? '' : ' off'}`} aria-hidden />
      <span class="mono sidebar-node-name">{name}</span>
      <span class="ver mono">{displayVersion(self.version, self.codename)}</span>
    </a>
  )
}

/** Pending-approval count for the Queue nav badge — the approval centre's
 *  ambient signal. Refreshes on the SSE change feed so it tracks the board
 *  without polling. */
function useReviewCount(): number {
  const [n, setN] = useState(0)
  useEffect(() => {
    const ctl = new AbortController()
    const load = () =>
      api
        .tasks({ state: 'review' })
        .then((ts) => setN(ts.length))
        .catch(() => {})
    load()
    subscribeEvents({
      signal: ctl.signal,
      onChange: (ev) => {
        if (ev.kinds.includes('tasks') || ev.kinds.includes('reconnect')) load()
      },
    }).catch(() => {})
    return () => ctl.abort()
  }, [])
  return n
}

/** The modifier the palette actually listens for, spelled the way the user's
 *  keyboard spells it: ⌘ on Apple hardware, Ctrl everywhere else. */
function modKeyLabel(): string {
  // Prefer the modern UA-CH hint; `navigator.platform` is deprecated but
  // remains the fallback on browsers without userAgentData.
  const ua = navigator as Navigator & { userAgentData?: { platform?: string } }
  const p = (ua.userAgentData?.platform ?? navigator.platform ?? '').toLowerCase()
  // UA-CH reports "macOS"/"iOS", navigator.platform the legacy "MacIntel"/
  // "iPhone" spellings — normalize case and match both generations.
  return /mac|iphone|ipad|ipod|ios/.test(p) ? '⌘' : 'Ctrl '
}

/** First-run screen: ask for the panel token, validate it against /api/tasks. */
function TokenGate(props: {
  error: string
  onConnected(): void
  onRejected(): void
  onUnreachable(): void
}) {
  const [token, setTokenInput] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: Event) {
    e.preventDefault()
    const candidate = token.trim()
    if (!candidate || busy) return
    setBusy(true)
    try {
      // The candidate is validated against a cheap authenticated endpoint
      // BEFORE setToken persists it — a rejected or unreachable token must
      // never land in storage, or the next reload briefly boots "authed"
      // into a storm of 401s.
      const res = await fetch('/api/tasks', {
        headers: { Authorization: `Bearer ${candidate}` },
      })
      if (res.ok) {
        setToken(candidate)
        props.onConnected()
      } else if (res.status === 401 || res.status === 403) {
        props.onRejected()
      } else {
        // 5xx or an unexpected status: the panel answered but the token's
        // validity is undecided — call it unreachable, not invalid.
        props.onUnreachable()
      }
    } catch {
      // Network failure: same undecided state, and the fetch rejection is
      // handled here rather than escaping the submit handler.
      props.onUnreachable()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="gate">
      <form class="gate-card" onSubmit={submit}>
        <PandaAscii scale={9} />
        <h1 class="gate-title">{t('token.title')}</h1>
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
        <p class="gate-hint dim">{t('token.hint')}</p>
      </form>
    </div>
  )
}
