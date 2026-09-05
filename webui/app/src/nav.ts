// The console's navigation vocabulary: the route union, the hash codec, and
// the sidebar's grouping. It lives in its own module because two surfaces need
// it — the sidebar (app.tsx) and the ⌘K palette (components/palette.tsx) — and
// a palette that lists a different set of destinations than the sidebar is a
// bug waiting to be filed.

export type Route =
  | { view: 'sessions'; id: string | null; project?: string | null }
  | { view: 'queue'; project?: string | null }
  | { view: 'projects' }
  | { view: 'settings'; tab?: string | null }
  | { view: 'detail'; id: string }

/** The three primary workspace views on the main sidebar rail. */
export const primaryNav: Array<[view: string, key: string]> = [
  ['sessions', 'nav.sessions'],
  ['queue', 'nav.queue'],
  ['projects', 'nav.projects'],
]

/** Palette views: includes primary workspaces plus direct jumps to settings sections. */
export const navViews: Array<[view: string, key: string]> = [
  ['sessions', 'nav.sessions'],
  ['queue', 'nav.queue'],
  ['projects', 'nav.projects'],
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
  if (path === 'queue') return { view: 'queue', project: projectParam }
  if (path === 'projects') return { view: 'projects' }
  if (path.startsWith('settings/')) {
    return { view: 'settings', tab: decodeURIComponent(path.slice(9)) }
  }
  if (path === 'settings') return { view: 'settings', tab: tabParam }

  // Graceful redirects for legacy URLs: map old external views straight into Settings sub-tabs
  if (path === 'nodes') return { view: 'settings', tab: 'nodes' }
  if (path === 'skills') return { view: 'settings', tab: 'skills' }
  if (path === 'reminders') return { view: 'settings', tab: 'reminders' }
  if (path === 'memory') return { view: 'settings', tab: 'memory' }
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
    if (route.project) {
      base += `?project=${encodeURIComponent(route.project)}`
    }
    location.hash = base
  } else if (route.view === 'settings') {
    location.hash = route.tab ? `#/settings?tab=${encodeURIComponent(route.tab)}` : '#/settings'
  } else {
    location.hash = `#/${route.view}`
  }
}

/** Navigate by bare view name — supports settings:tab syntax for palette jumps. */
export function navigateView(view: string): void {
  if (view.startsWith('settings:')) {
    navigate({ view: 'settings', tab: view.slice(9) })
  } else if (view === 'sessions') {
    navigate({ view: 'sessions', id: null })
  } else {
    navigate({ view } as Route)
  }
}

