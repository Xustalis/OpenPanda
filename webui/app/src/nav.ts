// The console's navigation vocabulary: the route union, the hash codec, and
// the sidebar's grouping. It lives in its own module because two surfaces need
// it — the sidebar (app.tsx) and the ⌘K palette (components/palette.tsx) — and
// a palette that lists a different set of destinations than the sidebar is a
// bug waiting to be filed.

export type Route =
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

/** Sidebar groups: primary Chat stays standalone up top; the rest are folded
 *  into labeled sections so first-time users see a short menu, not eight
 *  competing concepts. Each group collapses to further reduce noise. */
export interface NavGroup {
  id: string
  label: string
  items: Array<[view: string, key: string]>
}

export const navGroups: NavGroup[] = [
  { id: 'tasks', label: 'nav.group.tasks', items: [['queue', 'nav.queue'], ['projects', 'nav.projects']] },
  { id: 'orchestrate', label: 'nav.group.orchestrate', items: [['nodes', 'nav.nodes'], ['skills', 'nav.skills']] },
  { id: 'personal', label: 'nav.group.personal', items: [['reminders', 'nav.reminders'], ['memory', 'nav.memory']] },
  { id: 'system', label: 'nav.group.system', items: [['system', 'nav.system']] },
]

// Advanced groups start folded; the two everyday sections stay open.
/** Which groups start folded. Nothing does: the whole console is nine
 *  destinations, and folding six of them left the sidebar showing three
 *  headings with no items under them — which reads as a broken list, not as a
 *  tidy one, and hides most of the product from a first-time user. The groups
 *  stay collapsible for people who want a shorter rail; they just do not
 *  start that way. */
export const defaultCollapsed: Record<string, boolean> = {
  tasks: false,
  orchestrate: false,
  personal: false,
  system: false,
}

/** Every reachable view paired with its label key, in sidebar order: Chat,
 *  then each group's items, then Settings. The palette lists exactly this, so
 *  adding a view to navGroups adds it to the palette too. */
export const navViews: Array<[view: string, key: string]> = [
  ['sessions', 'nav.sessions'],
  ...navGroups.flatMap((g) => g.items),
  ['settings', 'nav.settings'],
]

export function parseHash(): Route {
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

export function navigate(route: Route): void {
  if (route.view === 'detail') location.hash = `#/task/${encodeURIComponent(route.id)}`
  else if (route.view === 'sessions')
    location.hash = route.id ? `#/chat/${encodeURIComponent(route.id)}` : '#/chat'
  else location.hash = `#/${route.view}`
}

/** Navigate by bare view name — the palette's vocabulary, where a session or
 *  task id is never part of the choice. */
export function navigateView(view: string): void {
  navigate(view === 'sessions' ? { view: 'sessions', id: null } : ({ view } as Route))
}
