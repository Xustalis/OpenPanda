// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { PandaAscii } from './brand/panda'
import { api, clearToken, displayVersion, getToken, onUnauthorized, setToken, type UpdateStatus } from './api/client'
import { onLocaleChange, t } from './i18n'
import { useAsync, useChangeSignal } from './hooks'
import { isSelfNodeUp } from './format/node-status'
import { navigate, parseHash, type Route } from './nav'
import { ControlView } from './views/control'
import { DetailView } from './views/detail'
import { SessionsView } from './views/sessions'
import { SidebarHistory } from './views/sidebar-history'
import { WorkspaceView } from './views/workspace'
import { TeamView } from './views/team'
import { MemoryView } from './views/memory'
import { ProfileView } from './views/profile'
import { SettingsView } from './views/settings'
import { OnboardingBanner } from './views/onboarding'
import { ToastHost } from './components/toast'
import { Icon } from './components/icons'
import { ConfirmHost } from './components/confirm'
import { PaletteHost, openPalette } from './components/palette'
import { ReminderBell, openReminders } from './components/reminders'

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
  const [menuOpen, setMenuOpen] = useState(false)
  const sidebar = useRef<HTMLElement>(null)

  // 设置 is a sheet, not a destination: opening it raises a top-layer dialog
  // over whatever you were reading and closing it drops you back there. The
  // hash still carries the route (deep links and the back button keep
  // working), so the view underneath has to be remembered rather than
  // re-derived — `#/settings` says nothing about what it was opened from.
  const lastView = useRef<Route>({ view: 'sessions', id: null })
  if (route.view !== 'settings') lastView.current = route
  const base: Route = route.view === 'settings' ? lastView.current : route

  useEffect(() => onUnauthorized(() => setAuthed(false)), [])
  // A route change means the reader picked a destination; the mobile panel
  // has done its job and must not cover the page they asked for.
  useEffect(() => setMenuOpen(false), [route])
  useEffect(() => {
    if (!menuOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenuOpen(false)
    }
    const onDown = (e: MouseEvent) => {
      if (!sidebar.current?.contains(e.target as Node)) setMenuOpen(false)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('mousedown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onDown)
    }
  }, [menuOpen])

  // #/reminders was a tab of 设置 before the bell took over the reminder
  // system. A bookmark saved back then still has to land somewhere useful, so
  // it drops the reader on the thread with the card raised; the URL is
  // rewritten because the card is state, not a destination. Keyed on the route
  // so a hash typed into the address bar of an already-open tab works too, and
  // gated on `authed` so a bookmark opened against the token gate keeps its
  // meaning — consuming it before the bell mounted would lose the card.
  useEffect(() => {
    if (!authed) return
    if (!location.hash.replace(/^#\/?/, '').startsWith('reminders')) return
    history.replaceState(null, '', '#/chat')
    requestAnimationFrame(openReminders)
  }, [route, authed])

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

  // 任务详情 is a drill-down of the board, so it keeps 队列·任务·项目 lit on the rail.
  const active: string = route.view === 'detail' ? 'control' : route.view
  const reviewCount = useReviewCount()
  const logout = () => {
    setMenuOpen(false)
    // Disconnecting from inside the settings sheet would otherwise leave the
    // hash on #/settings: the token gate takes over, and reconnecting would
    // drop the reader straight back into the sheet they just left. Close it
    // first, so the gate hands them back the page they were reading.
    if (route.view === 'settings') navigate(lastView.current)
    clearToken()
    setAuthed(false)
  }

  return (
    <div class="shell">
      <ToastHost placement={base.view === 'workspace' ? 'top-center' : 'bottom-right'} />
      <ConfirmHost />
      <PaletteHost onLogout={logout} />
      <aside
        ref={sidebar}
        class={`sidebar${menuOpen ? ' mobile-open' : ''}`}
      >
        {/* One line of chrome at the head of the rail: the name and the one
            ambient action that works from any view. The name is a label, not a
            control — it used to open 队列·任务·项目 → 项目, which meant a stray click on
            the wordmark threw the reader out of whatever thread they were in.
            The projects board is still one click away in the rail below. No
            chevron after the name either: the head is the quietest thing in
            the column, and the search field that used to live here is now a
            magnifier, because a bordered input plus a "Ctrl K" chip made the
            head the heaviest thing in a column whose whole job is to be quiet
            enough to scan. */}
        <div class="sidebar-head">
          <div class="sidebar-workspace">
            <span class="sidebar-workspace-name">
              Open<b>Panda</b>
            </span>
          </div>
          <div class="sidebar-head-actions">
            {/* 提醒 and 搜索: the two things you reach for from wherever you
                are. The bell replaced the 设置 → 提醒 tab, and it opens a card
                rather than a page (see components/reminders.tsx). */}
            <ReminderBell />
            <button
              type="button"
              class="icon-btn"
              title={`${t('palette.trigger')} ${modKeyLabel()}K`}
              aria-label={t('palette.trigger')}
              onClick={() => {
                setMenuOpen(false)
                openPalette()
              }}
            >
              <Icon name="search" />
            </button>
            <button
              type="button"
              class="icon-btn mobile-menu-btn"
              aria-label={t(menuOpen ? 'nav.menuClose' : 'nav.menuOpen')}
              aria-expanded={menuOpen}
              aria-controls="sidebar-panel"
              onClick={() => setMenuOpen((open) => !open)}
            >
              <Icon name={menuOpen ? 'x' : 'menu'} />
            </button>
          </div>
        </div>
        <div class="sidebar-panel" id="sidebar-panel">
        {/* The quick block: every destination the rail has, in one list.
            It used to be 新对话 and 工作空间 over a `<nav>` of sectioned rows;
            the sections are gone (each heading labelled the single row under
            it), so what is left is four rows at one altitude, and the history
            below them.
            新对话 is an action, not a destination — but it reads as a nav row
            (same 15px label, same hover wash) instead of a filled button, so
            the head above it stays the only chrome in the column. It navigates
            without minting a thread: the thread comes into being when the first
            message leaves the composer, titled after it, so clicking twice
            cannot litter the history with empty untitled sessions.
            It also carries no working directory. The composer's pick starts
            empty here — a blank sheet, not last project you visited — and the
            history's per-directory "+" is the shortcut that fills it in.
            The two after it are the machines and the work: 工作空间 is the
            fleet as a map, and 队列·任务·项目 is the boards that work lands on.
            That last one is the page that used to be 总控 — it is named after
            its three tabs now, because "control room" was a metaphor you had
            to learn and the tabs are what the row actually opens. */}
        <div class="nav-quick">
          <button
            type="button"
            class="nav-item nav-new-chat"
            onClick={() => navigate({ view: 'sessions', id: null })}
          >
            <Icon name="square-pen" />
            <span class="nav-label">{t('sessions.new')}</span>
          </button>
          <a
            href="#/workspace"
            class={`nav-item${active === 'workspace' ? ' active' : ''}`}
            aria-current={active === 'workspace' ? 'page' : undefined}
          >
            <Icon name="devices" />
            <span class="nav-label">{t('nav.workspace')}</span>
          </a>
          {/* The boards' row, and the one row in the rail that can carry a
              count: the review badge rides at the far end of it. Nested anchors
              are invalid HTML, so the badge is a span that hijacks the click —
              preventDefault kills the parent link's navigation, then we route
              to the review-focused board ourselves. */}
          <a
            href="#/control"
            class={`nav-item${active === 'control' ? ' active' : ''}`}
            aria-current={active === 'control' ? 'page' : undefined}
          >
            <Icon name="kanban" />
            <span class="nav-label">{t('nav.control')}</span>
            {reviewCount > 0 && (
              <span
                class="nav-badge"
                title={t('queue.col.review')}
                role="button"
                tabIndex={0}
                onClick={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  navigate({ view: 'control', tab: 'queue', review: true })
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    e.stopPropagation()
                    navigate({ view: 'control', tab: 'queue', review: true })
                  }
                }}
              >
                {reviewCount}
              </span>
            )}
          </a>
          {/* 团队 sits beside the boards, not inside them: 队列·任务·项目 is
              what the node is doing, this is who does it with you. The page is
              deliberately empty for now — the row is here so the slot exists
              before the feature does. It is the only way in. */}
          <a
            href="#/team"
            class={`nav-item${active === 'team' ? ' active' : ''}`}
            aria-current={active === 'team' ? 'page' : undefined}
          >
            <Icon name="users" />
            <span class="nav-label">{t('nav.team')}</span>
          </a>
          {/* 记忆 closes the block, right under 团队: what the node has learned
              is something you go and read, not a knob — so it is a destination
              here rather than a tab buried in 设置. 技能 stayed in the sheet
              because installing one *is* configuration. */}
          <a
            href="#/memory"
            class={`nav-item${active === 'memory' ? ' active' : ''}`}
            aria-current={active === 'memory' ? 'page' : undefined}
          >
            <Icon name="orbit" />
            <span class="nav-label">{t('nav.memory')}</span>
          </a>
        </div>

        <nav class="primary-nav" aria-label={t('nav.aria')}>
          {/* The rail's tail, not a row inside it. The history is what the rail
              is for, and 对话 above it only named the list again — so the row
              is gone and the list is always visible instead of folded. It
              scrolls with the nav, so a long history never pushes the
              destinations out of reach. */}
          <SidebarHistory
            activeId={route.view === 'sessions' ? route.id : null}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null })}
            onNewChat={(ws) => navigate({ view: 'sessions', id: null, ws })}
          />
        </nav>

        <div class="sidebar-footer">
          {/* The tail is one row: the node chip, then the gear that opens 设置.
              设置 lost its label on purpose — a word as long as
              "Einstellungen" was costing half a row in a 255px rail to say
              something a gear already says, and the icon matches the head's
              search button. 断开连接 is not here any more: it is a session
              verb, and the sheet that describes the session is where it now
              lives (设置 → 通用 → 连接). */}
          <div class="sidebar-foot">
            <SidebarNode />
            <a
              href="#/settings"
              class={`icon-btn sidebar-gear${active === 'settings' ? ' active' : ''}`}
              title={t('nav.settings')}
              aria-label={t('nav.settings')}
              aria-current={active === 'settings' ? 'page' : undefined}
            >
              <Icon name="gear" />
            </a>
          </div>
        </div>
        </div>
      </aside>
      <main class="main">
        <OnboardingBanner />
        <UpdateBanner />
        {base.view === 'sessions' && (
          <SessionsView
            activeId={base.id}
            project={base.project ?? null}
            ws={base.ws ?? null}
            onOpenSession={(id) => navigate({ view: 'sessions', id: id || null, project: base.project })}
            onExitProject={() => {
              api.exitProject().catch(() => {})
              navigate({ view: 'sessions', id: null, project: null })
            }}
            onLogout={logout}
          />
        )}
        {base.view === 'control' && (
          <ControlView
            tab={base.tab ?? 'queue'}
            project={base.project}
            focusReview={!!base.review}
            onSelectTab={(tab) => navigate({ view: 'control', tab })}
            onOpen={(id) => navigate({ view: 'detail', id })}
            onOpenSession={(id) => navigate({ view: 'sessions', id })}
            onNewChat={(project) => {
              if (project) api.enterProject(project).catch(() => {})
              navigate({ view: 'sessions', id: null, project })
            }}
            onOpenProject={(name) => {
              api.enterProject(name).catch(() => {})
              navigate({ view: 'sessions', id: null, project: name })
            }}
            onOpenQueue={(name) => navigate({ view: 'control', tab: 'queue', project: name })}
          />
        )}
        {base.view === 'detail' && (
          <DetailView
            id={base.id}
            onBack={() => navigate({ view: 'control', tab: 'queue' })}
            onOpenSession={(sid) => navigate({ view: 'sessions', id: sid })}
            onOpenTask={(tid) => navigate({ view: 'detail', id: tid })}
          />
        )}
        {base.view === 'team' && <TeamView />}
        {base.view === 'memory' && <MemoryView />}
        {base.view === 'workspace' && <WorkspaceView />}
        {base.view === 'profile' && <ProfileView />}
        {route.view === 'settings' && (
          <SettingsView
            initialSection={route.tab ?? undefined}
            onSelectTab={(tab) => navigate({ view: 'settings', tab })}
            onClose={() => navigate(lastView.current, true)}
            onLogout={logout}
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
 *  and what am I on?". It opens the profile page, which is where those three
 *  facts live in full, alongside the build's codename and its usage
 *  calendar. It used to point at Settings → System, which answered "what am
 *  I on" but buried the identity two tabs deep and had nothing to say about
 *  what this node had actually been doing. */
function SidebarNode() {
  useLocaleRerender()
  const { data: self } = useAsync(() => api.self(), [])
  if (!self) return null
  const name = self.node_name || self.hostname
  if (!name) return null
  // Green means connected, not "a daemon holds the identity lock": `panda
  // web` serves the node itself, so `node_running` alone left this dot grey
  // on a node that was online and heartbeating.
  const up = isSelfNodeUp(self)
  return (
    <a
      href="#/profile"
      class="sidebar-node"
      title={t(up ? 'sidebar.node.up' : 'sidebar.node.down')}
    >
      <span class={`dot${up ? '' : ' off'}`} aria-hidden />
      <span class="mono sidebar-node-name">{name}</span>
      <span class="ver mono" title={displayVersion(self.version, self.codename)}>
        {displayVersion(self.version, self.codename)}
      </span>
    </a>
  )
}

/** Pending-approval count for the Queue nav badge — the approval centre's
 *  ambient signal. Rides the shared event bus: this used to open its own
 *  /api/events stream, which is exactly the one-connection-per-subscriber
 *  design hooks.ts replaced (and it doubled the app's steady-state SSE
 *  connections, against a server that caps them). */
function useReviewCount(): number {
  const [n, setN] = useState(0)
  const tick = useChangeSignal()
  useEffect(() => {
    let alive = true
    api
      .tasks({ state: 'review' })
      .then((ts) => alive && setN(ts.length))
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [tick])
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
