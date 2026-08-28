// Typed client for the panel JSON API. The Bearer token lives in
// localStorage (entered once at the token gate); a 401 clears it and fires
// `unauthorized` so the app shell can drop back to the gate.

const TOKEN_KEY = 'openpanda.token'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

export function setToken(token: string): void {
  try {
    localStorage.setItem(TOKEN_KEY, token)
  } catch {
    // storage unavailable — token lasts only for this session
    sessionToken = token
  }
  sessionToken = token
}

export function clearToken(): void {
  sessionToken = ''
  try {
    localStorage.removeItem(TOKEN_KEY)
  } catch {
    // ignore
  }
}

// Session fallback when localStorage is unavailable (private mode).
let sessionToken = ''

/** The token for the current session — localStorage or the session fallback.
 *  Used by both header-based fetches and the ?token= EventSource URL. */
export function currentToken(): string {
  return getToken() || sessionToken
}

// Auto-login: `panda web` / `/web` open the browser at /?token=… so no
// manual paste is needed. Consume it once — store the token, then strip it
// from the address bar so it does not linger in history or get re-shared
// accidentally when copying the URL.
{
  const urlToken = new URLSearchParams(location.search).get('token')
  if (urlToken) {
    setToken(urlToken)
    history.replaceState(null, '', location.pathname + location.hash)
  }
}

const unauthorizedListeners = new Set<() => void>()

export function onUnauthorized(fn: () => void): () => void {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(60_000),
  })
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      unauthorizedListeners.forEach((fn) => fn())
    }
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, text || res.statusText)
  }
  return (await res.json()) as T
}

// ---- Wire types (mirror webui/panel JSON) ----

export interface TaskEvent {
  ts: number
  type: string
  data: string
}

// — Decision-orbit wire types. Mirrors the Go taskJSON additions in
// webui/panel/panel.go. Every field is optional so old task payloads paint
// without crashing; the orbit degrades gracefully to a hidden/collapsed
// placeholder when traces are empty.

export type TraceKind =
  | 'classify_result'
  | 'route_decision'
  | 'delegation_hop'
  | 'exec_agent_start'
  | 'supervision_round'
  | 'tier2_triggered'
  | 'plan_stage_changed'
  | 'artifact_transfer'

export interface ScoreBreakdown {
  resource_efficiency: number
  scheduler_tier: number
  wait_time: number
  // heartbeat_age is the wire-contract age in seconds (§3.1.1): 0 = brand-new
  // heartbeat, higher = stale. heartbeat_freshness is the internal (0,1]
  // weight used by routing; both are populated so the orbit can show either
  // a friendly age label ("12s ago") or a fitness bar.
  heartbeat_age?: number
  heartbeat_freshness?: number
  local_bonus?: number
  total: number
}

export interface ScoredCandidate {
  node_id: string
  node_name?: string
  // total_score is the flat numeric ranking (§3.1.1 short path); consumers
  // that want the "why" read breakdown.*. Both are populated on the wire.
  total_score: number
  breakdown: ScoreBreakdown
}

export interface TraceEvent {
  id: number
  ts: number
  type: TraceKind
  task_id?: string
  data?: any
  data_raw?: string
}

export interface PlanMeta {
  plan_id?: string
  stage_id?: string
  stage_count?: number
  stage_labels?: string[]
  needs?: string
  output_artifact?: string
}

// delegationHop is a single leg of a task's trip. hop=1 is the originator →
// first executor leg. accepted=true means the TO node wrote back an
// acknowledgement event; if only the outbound leg is present the hop was
// attempted but never confirmed. via = "route" | "direct" | "retarget".
export interface DelegationHop {
  hop: number
  from_node: string
  to_node: string
  via?: 'route' | 'direct' | 'retarget'
  accepted: boolean
  ts: number
}

// supervision rolls up the latest supervision_round event so UI badges can
// show "Round 1 / 5" without re-scanning traces. latest_verdict is absent
// until the first round completes (typically "done" | "continue").
export interface Supervision {
  round: number
  budget: number
  latest_verdict?: string
}

// tier2Op is one irreversible operation the defense layer flagged. Users
// review these in the approval dock before clicking Approve.
export interface Tier2Op {
  op: string
  target?: string
  risk: 'high' | 'medium' | 'low' | string
}

export interface Task {
  id: string
  parent_id: string
  project: string
  title: string
  state: string
  owner: string
  attempt_id: string
  intent?: string
  spec?: string
  result?: string
  risk?: string
  // Queue redesign fields: the board sorts by seq (drag order) then
  // priority, and jumps into session_id when set.
  priority?: 'high' | 'normal' | 'low'
  seq?: number
  session_id?: string
  resource_keys?: string[]
  scheduled?: boolean
  created_at: string
  updated_at: string
  events?: TaskEvent[]

  // Decision-orbit visibility (P0 redesign, §5.2). All optional.
  traces?: TraceEvent[]
  // §3.3 extended visibility: structured delegation hops with per-hop
  // confirmation + round counters + tier-2 operation rollups.
  delegation_chain?: DelegationHop[]
  plan_meta?: PlanMeta
  supervision?: Supervision
  tier2_ops?: Tier2Op[]
}

/** One probed agent CLI (GET /api/agents): install state + version + install guidance. */
export interface AgentInfo {
  name: string
  display_name?: string
  binary: string
  installed: boolean
  path?: string
  version?: string
  install_hint?: string
  install_url?: string
}

export type AgentTestResult = {
  name: string
  ok: boolean
  path?: string
  version?: string
  error?: string
}

export type CreateTaskResult = {
  task_id: string
  session_id: string
  state: string
}

export interface AskResult {
  kind: 'answer' | 'task'
  answer?: string
  task_id?: string
  task_state?: string
  ok?: boolean
  stdout?: string
  stderr?: string
  exit_code?: number
}

/** GET /api/update — the self-update pipeline status snapshot. */
export interface UpdateStatus {
  stage: 'idle' | 'checking' | 'available' | 'downloading' | 'staged' | 'applying' | 'done' | 'error'
  current: string
  latest?: string
  /** Changelog digest of the latest release (present once a check found one). */
  notes?: string
  available: boolean
  idle: boolean
  error?: string
  /** True when the update loop has backed off to idle for this run due to
   *  a transient upstream error (GitHub 403/rate limit, network offline).
   *  The UI shows a soft "updates paused" banner in this case. */
  degraded?: boolean
}

/** One agent declared on a node's capability card (GET /api/nodes). */
export interface NodeAgentDetail {
  capabilities?: string[]
  best_at?: string[]
  not_for?: string[]
  cost_tier?: string
  tier?: number
}

/** One decoded ledger directory row — the full capability card. */
export interface NodeInfo {
  id: string
  name: string
  status: string
  node_kind: 'physical' | 'vm' | string
  node_identity?: string
  is_local?: boolean
  running: boolean
  chip?: string
  last_seen: string
  scheduler_tier: number
  abilities: string[]
  native_ids?: string[]
  agents?: Record<string, NodeAgentDetail>
  capacity: { cpu_cores: number; ram_gb: number; max_concurrent_tasks: number; current_tasks: number }
  resource_profile?: { cpu: number; ram_gb: number; gpu_vram_gb: number; duration_hint: string }
}

/** GET /api/self — this machine's device profile (+ its ledger card),
 *  plus the running version and a projection of the updater's live status.
 *  `update` is omitted entirely when the node runs without an updater
 *  (e.g. some dev or daemon-only binaries). */
export interface SelfInfo {
  hostname: string
  os: string
  arch: string
  chip?: string
  cpu_cores: number
  ram_gb?: number
  node_name?: string
  node_id?: string
  node_kind?: 'physical' | 'vm' | string
  node_identity?: string
  node_running: boolean
  node?: NodeInfo
  version: string
  update?: UpdateStatus
}

/** GET/PUT /api/settings/app — the four app policy groups (C1). */
export interface AppSettings {
  injection_model: 'auto' | 'always' | 'never'
  preferred_agents: string[]
  memory_limits: { user: number; memory: number; project: number }
  approval_mode: 'always' | 'on-request' | 'never'
  sandbox?: { work_path: string } // GET-only: read-only confinement info
}

/** One topics/*.md (or daily/*.md) file in GET /api/memory. */
export interface TopicFile {
  name: string
  content: string
}

export interface ProjectMemory {
  project: string
  content: string
  limit: number
}

export interface ProjectList {
  projects: string[]
}

// ---- Endpoints ----

export const api = {
  tasks(params?: { state?: string; project?: string }): Promise<Task[]> {
    const q = new URLSearchParams()
    if (params?.state) q.set('state', params.state)
    if (params?.project) q.set('project', params.project)
    const suffix = q.size > 0 ? `?${q}` : ''
    return request('GET', `/api/tasks${suffix}`)
  },

  // ---- Board task management (queue redesign) ----

  /** Submit a task from the board: enqueues it and links a fresh session
   *  (title = task title, prompt = first user turn). */
  createTask(body: {
    title: string
    prompt?: string
    priority?: 'high' | 'normal' | 'low'
    project?: string
    resource_keys?: string[]
  }): Promise<CreateTaskResult> {
    return request('POST', '/api/tasks', body)
  },

  /** Quick priority change from a board card. */
  patchTask(id: string, priority: 'high' | 'normal' | 'low'): Promise<{ id: string; priority: string }> {
    return request('PATCH', `/api/tasks/${encodeURIComponent(id)}`, { priority })
  },

  /** Persist a drag: ids arrive top-to-bottom and get seq 1..n. */
  reorderTasks(ids: string[]): Promise<{ updated: number }> {
    return request('POST', '/api/tasks/reorder', { ids })
  },

  // ---- Agent CLIs (settings visibility) ----

  agents(): Promise<AgentInfo[]> {
    return request('GET', '/api/agents')
  },

  testAgent(name: string): Promise<AgentTestResult> {
    return request('POST', `/api/agents/${encodeURIComponent(name)}/test`)
  },

  task(id: string): Promise<Task> {
    return request('GET', `/api/tasks/${id}`)
  },

  approve(id: string): Promise<void> {
    return request('POST', `/api/tasks/${id}/approve`)
  },

  reject(id: string, reason?: string): Promise<void> {
    return request('POST', `/api/tasks/${id}/reject`, reason ? { reason } : undefined)
  },

  cancel(id: string): Promise<{ id: string; cancelled: number }> {
    return request('POST', `/api/tasks/${id}/cancel`)
  },

  logs(id: string): Promise<{ id: string; events: TaskEvent[] }> {
    return request('GET', `/api/tasks/${id}/logs`)
  },

  ask(prompt: string, authorize: boolean): Promise<AskResult> {
    return request('POST', '/api/ask', { prompt, authorize })
  },

  projects(): Promise<ProjectList> {
    return request('GET', '/api/projects')
  },

  createProject(name: string): Promise<{ name: string; status: string }> {
    return request('POST', '/api/projects', { name })
  },

  nodes(): Promise<NodeInfo[]> {
    return request('GET', '/api/nodes')
  },

  /** Drop a stale node row (offline remote only — the server refuses the
   *  local node and online nodes, since both re-register themselves). */
  removeNode(id: string): Promise<{ id: string; removed: boolean }> {
    return request('DELETE', `/api/nodes/${encodeURIComponent(id)}`)
  },

  /** This machine's device profile and its capability card. */
  self(): Promise<SelfInfo> {
    return request('GET', '/api/self')
  },

  // ---- App policy settings (injection / routing / memory caps / approval) ----

  getAppSettings(): Promise<AppSettings> {
    return request('GET', '/api/settings/app')
  },

  putAppSettings(s: AppSettings): Promise<AppSettings> {
    return request('PUT', '/api/settings/app', s)
  },

  // ---- Model settings ----

  getModelSettings(): Promise<ModelSettings> {
    return request('GET', '/api/settings/model')
  },

  putModelSettings(s: ModelSettings): Promise<ModelSettings> {
    return request('PUT', '/api/settings/model', s)
  },

  testModelSettings(s: ModelSettings): Promise<{ ok: boolean; reply?: string; error?: string }> {
    return request('POST', '/api/settings/model/test', s)
  },

  // ---- Chat sessions (worktree-isolated threads) ----

  sessions(): Promise<Session[]> {
    return request('GET', '/api/sessions')
  },

  createSession(title?: string): Promise<Session> {
    return request('POST', '/api/sessions', { title })
  },

  session(id: string): Promise<Session> {
    return request('GET', `/api/sessions/${encodeURIComponent(id)}`)
  },

  deleteSession(id: string): Promise<void> {
    return request('DELETE', `/api/sessions/${encodeURIComponent(id)}`)
  },

  sessionDiff(id: string): Promise<SessionDiff> {
    return request('GET', `/api/sessions/${encodeURIComponent(id)}/diff`)
  },

  sessionMerge(id: string, message?: string): Promise<{ merged: boolean; subject: string }> {
    return request('POST', `/api/sessions/${encodeURIComponent(id)}/merge`, message ? { message } : {})
  },

  // ---- Reminders ----

  reminders(): Promise<Reminder[]> {
    return request('GET', '/api/reminders')
  },

  createReminder(body: { message: string; after_minutes?: number; due_at?: string }): Promise<Reminder> {
    return request('POST', '/api/reminders', body)
  },

  deleteReminder(id: number): Promise<{ deleted: number }> {
    return request('DELETE', `/api/reminders/${id}`)
  },

  // ---- Memory (USER.md / MEMORY.md / DREAMS.md) ----

  memory(): Promise<MemoryFiles> {
    return request('GET', '/api/memory')
  },

  /** Rewrite USER.md / MEMORY.md (§ entries) or DREAMS.md (free-form diary). */
  saveMemory(
    file: 'user' | 'memory' | 'dreams',
    content: string,
  ): Promise<{ file: string; chars: number; limit: number }> {
    return request('PUT', `/api/memory/${file}`, { content })
  },

  /** Create or rewrite one topics/<name>.md file (§ entries). */
  saveMemoryTopic(name: string, content: string): Promise<{ topic: string; chars: number; limit: number }> {
    return request('PUT', `/api/memory/topics/${encodeURIComponent(name)}`, { content })
  },

  deleteMemoryTopic(name: string): Promise<{ topic: string; status: string }> {
    return request('DELETE', `/api/memory/topics/${encodeURIComponent(name)}`)
  },

  /** One project's MEMORY.md (§ entries) with its configured cap. */
  projectMemory(name: string): Promise<ProjectMemory> {
    return request('GET', `/api/projects/${encodeURIComponent(name)}/memory`)
  },

  saveProjectMemory(name: string, content: string): Promise<{ project: string; chars: number; limit: number }> {
    return request('PUT', `/api/projects/${encodeURIComponent(name)}/memory`, { content })
  },

  // ---- MCP settings ----

  getMCPSettings(): Promise<MCPSettings> {
    return request('GET', '/api/settings/mcp')
  },

  putMCPSettings(s: MCPSettings): Promise<MCPSettings> {
    return request('PUT', '/api/settings/mcp', s)
  },

  // ---- Web Push (reminder notifications) ----

  pushKey(): Promise<{ key: string }> {
    return request('GET', '/api/push/key')
  },

  pushSubscribe(sub: PushSubscriptionJSON): Promise<{ status: string }> {
    return request('POST', '/api/push/subscribe', sub)
  },

  // ---- System (version / metrics / audit / skills) ----

  version(): Promise<{ version: string }> {
    return request('GET', '/api/version')
  },

  // ---- Self-update (check / download / apply / discard) ----

  updateStatus(): Promise<UpdateStatus> {
    return request('GET', '/api/update')
  },

  checkUpdate(): Promise<UpdateStatus> {
    return request('POST', '/api/update/check')
  },

  downloadUpdate(): Promise<UpdateStatus> {
    return request('POST', '/api/update/download')
  },

  applyUpdate(): Promise<UpdateStatus> {
    return request('POST', '/api/update/apply')
  },

  cancelUpdate(): Promise<UpdateStatus> {
    return request('POST', '/api/update/cancel')
  },

  metrics(): Promise<DelegationMetric[]> {
    return request('GET', '/api/metrics')
  },

  verifyAudit(taskId?: string): Promise<{ ok: boolean; scope: string; entries?: number; error?: string }> {
    const q = taskId ? `?task_id=${encodeURIComponent(taskId)}` : ''
    return request('GET', `/api/audit${q}`)
  },

  auditEntries(): Promise<AuditEntry[]> {
    return request('GET', '/api/audit/entries')
  },

  skills(): Promise<SkillEntry[]> {
    return request('GET', '/api/skills')
  },

  approveSkill(name: string): Promise<void> {
    return request('POST', '/api/skills/approve', { name })
  },

  rejectSkill(name: string): Promise<void> {
    return request('POST', '/api/skills/reject', { name })
  },
}

export interface SessionDiff {
  id: string
  branch: string
  changes: { status: string; path: string }[]
  patch: string
}

export interface DelegationMetric {
  id: number
  task_id: string
  delegator: string
  executor: string
  abilities: string
  success: boolean
  latency_ms: number
  tokens: number | null
  created_at: string
}

export interface AuditEntry {
  ts: string
  who: string
  what: string
  target: string
  result: string
  detail: string
}

export interface SkillEntry {
  name: string
  description: string
  scope: string
  key?: string
  status: string
  use_count: number
}

export interface ModelSettings {
  api_type: 'anthropic' | 'openai'
  base_url: string
  model: string
  max_tokens: number
  api_key?: string // write-only: empty = keep the stored key
  api_key_set?: boolean
  api_key_hint?: string
}

export interface MCPSettings {
  command: string // space-separated stdio MCP server argv; "" = disabled
  from_config?: boolean
}

export interface Reminder {
  id: number
  message: string
  due_at: number // Unix seconds
  created_at: number
  fired_at: number // 0 = pending
  source: string // "tool" | "cli" | "web"
}

export interface MemoryFiles {
  user: string
  memory: string
  dreams: string
  time: string // node's current time, RFC3339
  user_limit: number // char caps for the live edit counter (config values)
  mem_limit: number
  project_limit: number
  topics: TopicFile[] // topics/*.md — selective-load memory files
  daily: TopicFile[] // warm-layer diary (read-only, newest first)
}

export interface SessionTurn {
  role: 'user' | 'assistant'
  text: string
  kind?: 'answer' | 'task' | 'error'
  ref?: string
}

export interface Session {
  id: string
  title: string
  created_at: string
  updated_at: string
  branch?: string
  worktree?: string
  turns: SessionTurn[]
}

// ---- Streaming session ask (SSE over fetch POST) ----

export interface AskStreamHandlers {
  onDelta(text: string): void
  onStatus(text: string): void
  onResult(r: AskResult): void
  onError(message: string): void
}

/** POST /api/sessions/{id}/ask and consume its SSE stream. Resolves when the
 * stream ends; errors surface through onError (and reject on transport
 * failure before the stream starts).
 *
 * `signal` aborts the request mid-stream — that is what the composer's stop
 * button is wired to. An abort rejects with an `AbortError`, which the caller
 * is expected to recognize rather than report as a failure: the partial reply
 * already on screen is the useful outcome. */
export async function askSessionStream(
  id: string,
  prompt: string,
  authorize: boolean,
  h: AskStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  const token = currentToken()
  const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/ask`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({ prompt, authorize }),
    signal,
  })
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      unauthorizedListeners.forEach((fn) => fn())
    }
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, text || res.statusText)
  }
  if (!res.body) throw new ApiError(0, 'no response body')

  const reader = res.body.getReader()
  // fetch's own abort does not always propagate to a reader that is parked in
  // read(); cancelling it explicitly ends the loop immediately.
  const onAbort = () => void reader.cancel().catch(() => {})
  signal?.addEventListener('abort', onAbort, { once: true })
  const decoder = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      let sep: number
      while ((sep = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, sep)
        buf = buf.slice(sep + 2)
        let event = 'message'
        const dataLines: string[] = []
        for (const line of frame.split('\n')) {
          if (line.startsWith('event: ')) event = line.slice(7).trim()
          else if (line.startsWith('data: ')) dataLines.push(line.slice(6))
        }
        if (dataLines.length === 0) continue
        let payload: any
        try {
          payload = JSON.parse(dataLines.join('\n'))
        } catch {
          continue
        }
        if (event === 'delta') h.onDelta(payload.text ?? '')
        else if (event === 'status') h.onStatus(payload.text ?? '')
        else if (event === 'result') h.onResult(payload as AskResult)
        else if (event === 'error') h.onError(payload.message ?? 'unknown error')
      }
    }
  } finally {
    signal?.removeEventListener('abort', onAbort)
  }
  // A cancelled reader ends the loop cleanly, so surface the abort the way
  // fetch would have if it had won the race.
  if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
}

/** Whether a caught error is an abort (the user pressing stop), not a fault. */
export function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

// ---- SSE live subscriptions -----------------------------------------------

/** Change payload delivered on `event: change` — the "what changed" portion of
 *  the SSE line: `tasks/nodes/reminders [fp/tasks /fp/nodes /fp/reminders]`.
 *  Old servers that only send `init` or a single task fingerprint still
 *  deserialize safely (unknown keys are empty strings). */
export interface ChangeEvent {
  kinds: string[]
  taskFP?: string
  nodeFP?: string
  reminderFP?: string
  raw: string
}

/** SSE subscription options. Pass `trace: true` to opt into the orbit-level
 *  `event: trace` stream; by default you only get `event: change` and the
 *  15-second heartbeat comment — byte-for-byte identical to the old feed. */
export interface SubscribeEventsOptions {
  trace?: boolean
  onChange?: (ev: ChangeEvent) => void
  onTrace?: (ev: TraceEvent) => void
  signal?: AbortSignal
}

/**
 * Opens GET /api/events as a long-lived SSE stream. Returns a Promise that
 * resolves when the connection ends naturally (server close, network drop);
 * consumers unsubscribe by passing an AbortSignal and aborting it.
 *
 * Design rationale (§5.1): this is one shared EventSource per call so the
 * orbit/hooks layer can multiplex `onTrace` events to the right task's
 * reducer. Authentication uses the ?token= query param because browsers'
 * EventSource API cannot send Authorization headers.
 */
export function subscribeEvents(opts: SubscribeEventsOptions): Promise<void> {
  const params = new URLSearchParams()
  const token = currentToken()
  if (token) params.set('token', token)
  if (opts.trace) params.set('trace', '1')
  const url = '/api/events' + (params.size ? '?' + params.toString() : '')

  const es = new EventSource(url, { withCredentials: false })

  const cleanup = () => {
    try { es.close() } catch { /* closed twice during abort; ignore */ }
  }
  if (opts.signal) {
    if (opts.signal.aborted) { cleanup(); return Promise.resolve() }
    opts.signal.addEventListener('abort', cleanup, { once: true })
  }

  return new Promise<void>((resolve) => {
    es.addEventListener('change', (e: any) => {
      if (!opts.onChange) return
      const raw: string = e.data ?? ''
      const parts = raw.split(' ')
      const kinds = (parts[0] ?? '').split(',').filter(Boolean)
      const fps = (parts[1] ?? '').split('/')
      opts.onChange({
        kinds,
        taskFP: fps[0],
        nodeFP: fps[1],
        reminderFP: fps[2],
        raw,
      })
    })
    es.addEventListener('trace', (e: any) => {
      if (!opts.onTrace) return
      try {
        const payload = JSON.parse(e.data ?? '{}') as any
        opts.onTrace({
          id: payload.id ?? 0,
          ts: payload.ts ?? 0,
          task_id: payload.task_id,
          type: payload.type,
          data: payload.data,
          data_raw: typeof payload.data === 'string' ? payload.data : undefined,
        })
      } catch {
        // Malformed trace frames are dropped: the orbit fall-back is the
        // hydrate-from-getTask() call, not the live stream.
      }
    })
    es.addEventListener('error', () => {
      // EventSource auto-reconnects with exponential backoff on its own. We
      // surface the drop via a synthetic change-event kind "reconnect" so the
      // app can re-fetch once if it wants, but never block on a glitch.
      try {
        opts.onChange?.({ kinds: ['reconnect'], raw: 'reconnect' })
      } catch { /* ignore */ }
    })
    es.addEventListener('close', () => { cleanup(); resolve() })
    // The underlying EventSource has no standardized "close" lifecycle on its
    // own (it retries forever). We honor abort via the signal path above; if
    // nothing else tears the stream down this promise simply never resolves,
    // which matches the "infinite subscription" semantics the caller wants.
  })
}
