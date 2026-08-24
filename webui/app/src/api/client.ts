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
  available: boolean
  idle: boolean
  error?: string
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

/** GET /api/self — this machine's device profile (+ its ledger card). */
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
 * failure before the stream starts). */
export async function askSessionStream(
  id: string,
  prompt: string,
  authorize: boolean,
  h: AskStreamHandlers,
): Promise<void> {
  const token = currentToken()
  const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/ask`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({ prompt, authorize }),
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
  const decoder = new TextDecoder()
  let buf = ''
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
}
