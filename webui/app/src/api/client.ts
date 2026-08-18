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

const unauthorizedListeners = new Set<() => void>()

export function onUnauthorized(fn: () => void): () => void {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken() || sessionToken
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
  created_at: string
  updated_at: string
  events?: TaskEvent[]
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

export interface NodeInfo {
  id: string
  status: string
  chip: string
  last_seen: string
  abilities: string[]
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

  task(id: string): Promise<Task> {
    return request('GET', `/api/tasks/${id}`)
  },

  approve(id: string): Promise<void> {
    return request('POST', `/api/tasks/${id}/approve`)
  },

  reject(id: string, reason?: string): Promise<void> {
    const q = reason ? `?reason=${encodeURIComponent(reason)}` : ''
    return request('POST', `/api/tasks/${id}/reject${q}`)
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
}
