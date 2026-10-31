// SPDX-License-Identifier: AGPL-3.0-or-later

// The console's navigation vocabulary: the route union, the hash codec, and
// the sidebar's grouping. It lives in its own module because two surfaces need
// it — the sidebar (app.tsx) and the ⌘K palette (components/palette.tsx) — and
// a palette that lists a different set of destinations than the sidebar is a
// bug waiting to be filed.

/** 队列·任务·项目's three boards. They used to be three rail rows (任务队列, 计划, 项目)
 *  over three routes; they are now one page with one tab strip, because all
 *  three answer the same question — "what is this node working on, and where
 *  does that work live" — and a rail row per board made the work group read
 *  as three unrelated destinations. */
export type ControlTab = 'queue' | 'plans' | 'projects'

export type Route =
  | { view: 'sessions'; id: string | null; project?: string | null; ws?: string | null }
  | { view: 'control'; tab?: ControlTab; project?: string | null; review?: boolean }
  | { view: 'team' }
  | { view: 'memory' }
  | { view: 'workspace' }
  | { view: 'profile' }
  | { view: 'settings'; tab?: string | null }
  | { view: 'detail'; id: string }

/** The rail's destinations, in the order they are painted.
 *
 *  The rail no longer has sections: every row that is a *place to work* now
 *  sits in one block at the top (app.tsx's quick block), because the headings
 *  had stopped earning their 24px. 新对话 leads
 *  (it is an action, but it reads as a row); then the machines-and-work
 *  destinations: 工作空间 (the fleet as a map) and 队列·任务·项目 (the node's
 *  boards). 团队 closes the block — it is a slot reserved for team work, and
 *  its page is empty until that lands. The 节点网络 page (fleet) was removed
 *  from the console; its old routes land on the thread list instead.
 *
 *  What is deliberately NOT a row: 对话 — the thread history is the tail of the
 *  rail (app.tsx), and a row whose only job was to unfold a list that is now
 *  always visible just named the list again. 计划 and 项目 — both are tabs of
 *  队列·任务·项目, and ⌘K still jumps straight to each. 记忆 and 技能 — both are
 *  tabs of 设置: the rail keeps only the destinations you work in, and
 *  everything about the agent's mind or its configuration lives in the one
 *  management centre. The palette below is therefore a superset of the rail,
 *  not a mirror of it. */
export const navViews: Array<[view: string, key: string]> = [
  ['sessions', 'nav.sessions'],
  ['workspace', 'nav.workspace'],
  ['control', 'nav.control'],
  ['team', 'nav.team'],
  // 记忆 sits right under 团队 in the rail: what the node has learned is a
  // place you go and read, not a knob you turn, so it left 设置 when the sheet
  // was rebuilt. 技能 stayed behind — it is configuration.
  ['memory', 'nav.memory'],
  // The two boards that are tabs rather than rows.
  ['control:plans', 'nav.plans'],
  ['control:projects', 'nav.projects'],
  ['profile', 'nav.profile'],
  ['settings', 'nav.settings'],
]

/** `?tab=` → a valid board tab. Anything unknown (including a missing param)
 *  falls back to the kanban, which is the page's default board. */
function parseControlTab(raw: string | null): ControlTab {
  return raw === 'plans' || raw === 'projects' ? raw : 'queue'
}

export function parseHash(): Route {
  const raw = location.hash.replace(/^#\/?/, '')
  const [path = '', queryStr = ''] = raw.split('?', 2)
  const query = new URLSearchParams(queryStr)
  const projectParam = query.get('project') || null
  // The workspace a *new* chat starts in — the rail's per-directory "+". It is
  // not `project`: entering a project scopes the whole pane (its rail, its
  // files, its boundary guard), while this only pre-answers "where does this
  // thread run", leaving the reader in the unfiltered history.
  const wsParam = query.get('ws') || null
  const tabParam = query.get('tab') || null

  if (path.startsWith('task/')) return { view: 'detail', id: decodeURIComponent(path.slice(5)) }
  if (path.startsWith('chat/')) return { view: 'sessions', id: decodeURIComponent(path.slice(5)), project: projectParam }
  if (path === 'chat') return { view: 'sessions', id: null, project: projectParam, ws: wsParam }
  if (path.startsWith('projects/') && path.length > 9) {
    return { view: 'sessions', id: null, project: decodeURIComponent(path.slice(9)) }
  }
  if (path === 'control') {
    return {
      view: 'control',
      tab: parseControlTab(query.get('tab')),
      project: projectParam,
      review: query.has('review'),
    }
  }
  // Legacy URLs: 任务队列, 计划 and 项目 were their own routes before they
  // became the three tabs of 队列·任务·项目. Old deep links (and anything still linking
  // `#/queue`, `#/plans`, `#/projects`) land on the same board, just under the
  // merged route.
  if (path === 'queue') {
    return { view: 'control', tab: 'queue', project: projectParam, review: query.has('review') }
  }
  if (path === 'plans') return { view: 'control', tab: 'plans' }
  if (path === 'projects') return { view: 'control', tab: 'projects' }
  // 团队: its own destination, beside the boards. The page is empty for now —
  // it holds the slot for team work so the rail's shape does not have to change
  // again when that lands. `#/todos` was this row before the rename, so old
  // links land here instead of falling through to the thread list.
  if (path === 'team' || path === 'todos' || path === 'todo') return { view: 'team' }
  // 节点网络 (fleet) is gone from the console; its legacy URLs (`#/fleet`,
  // `#/nodes`) fall through to the thread list below.
  if (path === 'workspace') return { view: 'workspace' }
  // 记忆 is its own destination (under 团队 in the rail) and 技能 is a tab of
  // 设置. Old deep links (`#/memory`, `#/memory/topics`) still land on the
  // memory page; `#/skills` still opens the settings sheet on its tab, and a
  // `#/settings?tab=memory` bookmark is answered by the redirect below.
  if (path === 'memory' || path.startsWith('memory/')) return { view: 'memory' }
  if (path === 'skills') return { view: 'settings', tab: 'skills' }
  // The identity chip's destination. It is not a rail row: the chip in the
  // footer is the only way in, which is the point — "who am I / what am I
  // running / what have I done" belongs to the identity, not to Settings.
  if (path === 'profile') return { view: 'profile' }
  if (path.startsWith('settings/') || path === 'settings') {
    const tab = path.startsWith('settings/') ? decodeURIComponent(path.slice(9)) : tabParam
    // 记忆 used to be a tab of the sheet; a bookmark to it is a bookmark to
    // the page, so answer with the page instead of a sheet on a dead tab.
    if (tab === 'memory') return { view: 'memory' }
    return { view: 'settings', tab }
  }

  // Graceful redirects for legacy URLs: promoted views land on their own
  // routes now; system stays inside Settings. 提醒 left Settings for a bell in
  // the rail's head, so `#/reminders` lands on the thread and the app raises
  // the reminder card over it (app.tsx) — the card is state, not a route.
  if (path === 'reminders') return { view: 'sessions', id: null }
  if (path === 'system') return { view: 'settings', tab: 'system' }

  return { view: 'sessions', id: null, project: projectParam }
}

export function navigate(route: Route, replace = false): void {
  // Closing a modal must not leave a history entry behind: Back would reopen
  // the dialog the user just dismissed. replaceState does not fire
  // hashchange, so the route listener is nudged by hand.
  const commit = (hash: string) => {
    if (!replace) {
      location.hash = hash
      return
    }
    if (location.hash === hash) return
    history.replaceState(history.state, '', hash)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
  }
  if (route.view === 'detail') {
    commit(`#/task/${encodeURIComponent(route.id)}`)
  } else if (route.view === 'sessions') {
    let base = route.id ? `#/chat/${encodeURIComponent(route.id)}` : '#/chat'
    const params: string[] = []
    if (route.project) params.push(`project=${encodeURIComponent(route.project)}`)
    // Only a thread that does not exist yet can be pointed at a directory; an
    // open thread answers for itself, from its own project row.
    if (!route.id && route.ws) params.push(`ws=${encodeURIComponent(route.ws)}`)
    if (params.length) base += `?${params.join('&')}`
    commit(base)
  } else if (route.view === 'control') {
    // The queue tab is the page's default, so it stays out of the URL — a
    // plain `#/control` is the board, and only 计划 / 项目 need saying.
    const params: string[] = []
    if (route.tab && route.tab !== 'queue') params.push(`tab=${route.tab}`)
    if (route.project) params.push(`project=${encodeURIComponent(route.project)}`)
    if (route.review) params.push('review=1')
    commit(params.length ? `#/control?${params.join('&')}` : '#/control')
  } else if (route.view === 'settings') {
    commit(route.tab ? `#/settings?tab=${encodeURIComponent(route.tab)}` : '#/settings')
  } else {
    commit(`#/${route.view}`)
  }
}

/** Navigate by bare view name — supports settings:tab and control:tab syntax
 *  for palette jumps. */
export function navigateView(view: string): void {
  if (view.startsWith('settings:')) {
    navigate({ view: 'settings', tab: view.slice(9) })
  } else if (view.startsWith('control:')) {
    navigate({ view: 'control', tab: view.slice(8) as ControlTab })
  } else if (view === 'sessions') {
    navigate({ view: 'sessions', id: null })
  } else {
    navigate({ view } as Route)
  }
}
