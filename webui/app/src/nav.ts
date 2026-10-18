// SPDX-License-Identifier: AGPL-3.0-or-later

// The console's navigation vocabulary: the route union, the hash codec, and
// the sidebar's grouping. It lives in its own module because two surfaces need
// it — the sidebar (app.tsx) and the ⌘K palette (components/palette.tsx) — and
// a palette that lists a different set of destinations than the sidebar is a
// bug waiting to be filed.

export type Route =
  | { view: 'sessions'; id: string | null; project?: string | null }
  | { view: 'queue'; project?: string | null; review?: boolean }
  | { view: 'plans' }
  | { view: 'projects' }
  | { view: 'fleet' }
  | { view: 'memory'; tab?: string | null }
  | { view: 'skills' }
  | { view: 'settings'; tab?: string | null }
  | { view: 'detail'; id: string }

/** One section of the sidebar rail: an optional heading, then its rows. */
export type NavGroup = {
  /** Stable identity — the rail's React key. */
  id: string
  /** Heading rendered above the rows as a small uppercase label. Omitted for
   *  the work group, which sits at the top of the rail where a "Work" label
   *  would only label the first thing on screen. */
  head?: string
  items: Array<[view: string, key: string]>
}

/** The primary workspace views on the main sidebar rail, grouped the way the
 *  product reads: work first (tasks → pipelines → workspaces), then the
 *  network that runs it (fleet), then what the agent knows (memory → skills).
 *
 *  Two views are deliberately not rows here. 新对话 is an action, so it lives
 *  with the search box above the nav. 对话 is not a row either: the thread
 *  history is the tail of the rail (app.tsx), and a row whose only job was to
 *  unfold a list that is now always visible just named the list again. */
export const primaryNavGroups: NavGroup[] = [
  {
    id: 'work',
    items: [
      ['queue', 'nav.queue'],
      ['plans', 'nav.plans'],
      ['projects', 'nav.projects'],
    ],
  },
  {
    id: 'fleet',
    head: 'nav.group.fleet',
    items: [['fleet', 'nav.fleet']],
  },
  {
    id: 'knowledge',
    head: 'nav.group.knowledge',
    items: [
      ['memory', 'nav.memory'],
      ['skills', 'nav.skills'],
    ],
  },
]

/** Flat nav list — the mobile top bar and any code that iterates views. */
export const primaryNav: Array<[view: string, key: string]> = primaryNavGroups.flatMap(
  (g) => g.items,
)

/** Palette views: includes primary workspaces plus direct jumps to settings
 *  sections. 对话 is listed here even though the rail has no row for it — the
 *  palette is a jump list, and chat is still a destination (the rail reaches
 *  it through the history and 新对话). Dropping it here too would leave ⌘K
 *  with no way to reach the chat surface. */
export const navViews: Array<[view: string, key: string]> = [
  ['sessions', 'nav.sessions'],
  ...primaryNav,
  ['settings', 'nav.settings'],
]

export function parseHash(): Route {
  const raw = location.hash.replace(/^#\/?/, '')
  const [path = '', queryStr = ''] = raw.split('?', 2)
  const query = new URLSearchParams(queryStr)
  const projectParam = query.get('project') || null
  const tabParam = query.get('tab') || null

  if (path.startsWith('task/')) return { view: 'detail', id: decodeURIComponent(path.slice(5)) }
  if (path.startsWith('chat/')) return { view: 'sessions', id: decodeURIComponent(path.slice(5)), project: projectParam }
  if (path === 'chat') return { view: 'sessions', id: null, project: projectParam }
  if (path.startsWith('projects/') && path.length > 9) {
    return { view: 'sessions', id: null, project: decodeURIComponent(path.slice(9)) }
  }
  if (path === 'queue') {
    return { view: 'queue', project: projectParam, review: query.has('review') }
  }
  if (path === 'plans') return { view: 'plans' }
  if (path === 'projects') return { view: 'projects' }
  if (path === 'fleet' || path === 'nodes') return { view: 'fleet' }
  if (path.startsWith('memory/')) {
    return { view: 'memory', tab: decodeURIComponent(path.slice(7)) }
  }
  if (path === 'memory') return { view: 'memory', tab: tabParam }
  if (path === 'skills') return { view: 'skills' }
  if (path.startsWith('settings/') || path === 'settings') {
    const tab = path.startsWith('settings/') ? decodeURIComponent(path.slice(9)) : tabParam
    // Promoted views: old settings deep-links land on the standalone routes.
    if (tab === 'nodes' || tab === 'devices') return { view: 'fleet' }
    if (tab === 'memory') return { view: 'memory' }
    if (tab === 'skills') return { view: 'skills' }
    return { view: 'settings', tab }
  }

  // Graceful redirects for legacy URLs: promoted views land on their own
  // routes now; reminders/system stay inside Settings.
  if (path === 'reminders') return { view: 'settings', tab: 'reminders' }
  if (path === 'system') return { view: 'settings', tab: 'system' }

  return { view: 'sessions', id: null, project: projectParam }
}

export function navigate(route: Route): void {
  if (route.view === 'detail') {
    location.hash = `#/task/${encodeURIComponent(route.id)}`
  } else if (route.view === 'sessions') {
    let base = route.id ? `#/chat/${encodeURIComponent(route.id)}` : '#/chat'
    if (route.project) {
      base += `?project=${encodeURIComponent(route.project)}`
    }
    location.hash = base
  } else if (route.view === 'queue') {
    let base = '#/queue'
    const params: string[] = []
    if (route.project) params.push(`project=${encodeURIComponent(route.project)}`)
    if (route.review) params.push('review=1')
    if (params.length) base += `?${params.join('&')}`
    location.hash = base
  } else if (route.view === 'memory') {
    location.hash = route.tab ? `#/memory/${encodeURIComponent(route.tab)}` : '#/memory'
  } else if (route.view === 'settings') {
    location.hash = route.tab ? `#/settings?tab=${encodeURIComponent(route.tab)}` : '#/settings'
  } else {
    location.hash = `#/${route.view}`
  }
}

/** Navigate by bare view name — supports settings:tab and memory:tab syntax
 *  for palette jumps. */
export function navigateView(view: string): void {
  if (view.startsWith('settings:')) {
    navigate({ view: 'settings', tab: view.slice(9) })
  } else if (view.startsWith('memory:')) {
    navigate({ view: 'memory', tab: view.slice(7) })
  } else if (view === 'sessions') {
    navigate({ view: 'sessions', id: null })
  } else {
    navigate({ view } as Route)
  }
}
