// SPDX-License-Identifier: AGPL-3.0-or-later

// Typed client for the panel JSON API. The Bearer token lives in
// localStorage (entered once at the token gate); a 401 clears it and fires
// `unauthorized` so the app shell can drop back to the gate.

const TOKEN_KEY = 'openpanda.token'

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
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
// accidentally when copying the URL. The guard keeps this module importable
// outside a browser (the node-based unit tests).
if (typeof location !== 'undefined') {
  const urlToken = new URLSearchParams(location.search).get('token')
  if (urlToken) {
    setToken(urlToken)
    try {
      history.replaceState(null, '', location.pathname + location.hash)
    } catch {
      // history unavailable (some embedded contexts) — the token is stored,
      // the URL cleanup is best-effort.
    }
  }
}

const unauthorizedListeners = new Set<() => void>()

/** Multipart variant of request(): the browser sets the boundary, so no
 *  Content-Type is set by hand. No retry — a streamed body cannot be re-read. */
async function upload<T>(method: string, path: string, body: FormData): Promise<T> {
  const headers: Record<string, string> = {}
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`

  const res = await fetch(path, { method, headers, body })
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

export interface SessionUploadedFile {
  name: string
  /** Absolute — what /api/fs/read accepts. */
  path: string
  /** What the composer inserts: a work-path relative (or absolute) path the
   *  fs endpoints resolve, computed by the server. */
  mention: string
  size: number
}

export interface SessionUploadResult {
  files: SessionUploadedFile[]
}

export function onUnauthorized(fn: () => void): () => void {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts?: { timeoutMs?: number },
): Promise<T> {
  const headers: Record<string, string> = {}
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const isGet = method.toUpperCase() === 'GET'
  const maxAttempts = isGet ? 3 : 1
  // 0 (or a negative) means "no deadline": a modal OS window is open on the
  // other end and the answer arrives when the user makes up their mind.
  const timeoutMs = opts?.timeoutMs ?? 60_000
  let lastErr: unknown

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    try {
      const res = await fetch(path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: timeoutMs > 0 ? AbortSignal.timeout(timeoutMs) : undefined,
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
    } catch (err) {
      lastErr = err
      // Fail fast on client errors (4xx) or non-retryable requests
      if (err instanceof ApiError && err.status < 500) {
        throw err
      }
      if (attempt < maxAttempts) {
        await new Promise((resolve) => setTimeout(resolve, attempt * 200))
        continue
      }
    }
  }
  throw lastErr
}

// requestText is request's text-body twin — patch previews and other raw
// payloads that are not JSON.
async function requestText(method: string, path: string): Promise<string> {
  const headers: Record<string, string> = {}
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`
  const res = await fetch(path, { method, headers, signal: AbortSignal.timeout(60_000) })
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      unauthorizedListeners.forEach((fn) => fn())
    }
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, text || res.statusText)
  }
  return res.text()
}

// ---- Wire types (mirror webui/panel JSON) ----

/** Self-evolution patch set as GET /api/selfevo/{id} returns it. */
export interface SelfEvoSet {
  id: string
  state: string
  title: string
  base: string
  version: string
  producer: string
  files: string[]
  critical: boolean
  critical_paths?: string[]
  gate_ok: boolean
}

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
  // Live-narrative set streamed alongside the orbit tracks (panel events.go):
  // agent-level activity — the work happening *inside* a phase.
  | 'agent_event'
  | 'progress'
  | 'agent_usage'
  | 'agent_fallback'
  | 'judge_start'
  | 'supervise'
  | 'result'

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
  // that want the "why" read score_breakdown.*. Both are populated on the wire.
  total_score: number
  score_breakdown: ScoreBreakdown
}

export interface TraceEvent {
  id: number
  ts: number
  /** TraceKind for stream frames; last_event stamps may carry ANY event
   *  type (submit/queue/session_summary…) — the `(string & {})` union arm
   *  keeps the kind autocomplete while admitting arbitrary rows. */
  type: TraceKind | (string & {})
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

// Stall detection mirrors the CLI watcher's rules (cmd/panda/repl_watch.go):
// a task in a state that should still move on its own, whose row has not been
// touched past its own bound, is stalled — no scheduler is consuming it, its
// executor went silent, or no monitor is expiring it. The console must show
// that instead of letting the card sit forever.
export const STALL_QUEUED_MS = 10 * 60 * 1000
export const STALL_ACTIVE_MS = 25 * 60 * 1000

export function isTaskStalled(task: Task, now = Date.now()): boolean {
  const updated = task.updated_at ? Date.parse(task.updated_at) : 0
  if (!updated) return false
  const age = now - updated
  switch (task.state) {
    case 'submitted':
      // An unreleased plan stage waits on its dependency graph by design —
      // the plan sweep owns that wait and propagates failures.
      if (task.plan_meta?.plan_id) return false
      return age > STALL_QUEUED_MS
    case 'queued':
      return age > STALL_QUEUED_MS
    case 'dispatched':
    case 'waiting_context':
    case 'running':
      // Lease renewals bump updated_at every lease/3 (~7min at the default),
      // so a row frozen this long means the executor is gone and nothing
      // expired the task.
      return age > STALL_ACTIVE_MS
    default:
      return false
  }
}

/** One sub-agent reference inside a parent task's detail payload. */
export interface TaskChild {
  id: string
  state: string
  title?: string
  isolate?: boolean
  scope_paths?: string[]
}

export interface Task {
  id: string
  parent_id: string
  /** Sub-agent generation inside a delegation tree: 1 = direct child,
      2 = grandchild, ... Absent (0) for root/standalone tasks. */
  spawn_depth?: number
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
  /** Set only on state=review: what approving the task would do —
   *  accept_work (work already produced, approve accepts it as done),
   *  resume_execution (parked before running, approve re-runs it), or
   *  needs_changed_input (approve alone cannot help). */
  approval_disposition?: 'accept_work' | 'resume_execution' | 'needs_changed_input'
  /** Same-tree parallelism declaration: the project-relative lanes this
   *  task claims (disjoint siblings run simultaneously), or — when isolate
   *  is set — a private copy whose changes merge back on completion. */
  scope_paths?: string[]
  isolate?: boolean
  /** WorkDir is the submitter's pin (a session worktree); ExecWorkDir is where
   *  the run actually happened. The detail card shows the latter — a finished
   *  row should be locatable on the machine that ran it. */
  work_dir?: string
  exec_work_dir?: string
  /** Spawned sub-agents (getTask only): the tree a parent is orchestrating,
   *  with each child's live state and its own parallel mode. */
  children?: TaskChild[]
  created_at: string
  updated_at: string
  events?: TaskEvent[]

  /** The task's newest event row, clipped — stamped by listTasks/getTask so
   *  the live line says what a mid-flight task is doing on first paint,
   *  before the SSE trace stream's first frame lands. */
  last_event?: TraceEvent
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
  /** What this agent is good at, from the registry's own defaults. The team
   *  picker reads these as the person's role. */
  best_at?: string[]
  /** Routing cost tier ("low" … "high") — how expensive this hire is. */
  cost_tier?: string
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
  /** Advisory when the task queued into a store no live consumer drains —
   *  parked work, not dispatch. Server-localized; empty when fine. */
  warning?: string
}

export type TaskMessageResult = {
  task_id: string
  /** "interrupted" — a live round was aborted and the text leads the next
   *  one; "queued" — the note waits for the next round (or the first run). */
  status: 'queued' | 'interrupted' | string
  /** No consumer drains this store — the note is durable but nobody reads
   *  it until a daemon or console picks the queue up. Server-localized. */
  warning?: string
}

/** One board entry under the task's own scope: a queued user note, a steer,
 *  or a sibling-task message the agent could read at a round boundary. */
export interface TaskBoardMessage {
  scope: string
  sender: string
  rcpt: string
  sender_seq: number
  sender_label: string
  /** status = advisory note, report = structured summary, steer = user inject,
   *  artifact_ref = produced-file pointer. */
  grade: 'status' | 'report' | 'steer' | 'artifact_ref' | string
  body: string
  sent_at: number
  recv_at: number
  /** When the task's round loop folded this note into its prompt; 0 = still
   *  waiting. recv_at cannot answer that (it is stamped on arrival), so this
   *  is the only field that separates "queued" from "read". */
  consumed_at?: number
}

/** One row of the composer's pending strip — a note typed mid-turn that no
 *  round has folded yet. `key` is what edit/drop/promote address. */
export interface SessionNote {
  key: string
  task_id?: string
  text: string
  attachments?: string[]
  /** queue = waits for the next round; inject = leads it. */
  mode: 'queue' | 'inject' | string
  at: number
  /** True for a board row (durable across reloads and restarts). */
  durable?: boolean
}

/** Outcome of an edit/drop/promote on a pending note. `warning` carries the
 *  server's caveat when an inject could not abort (another process runs the
 *  round), so the surface never claims a cut it did not make. */
export interface NoteResult {
  key: string
  task_id?: string
  status: 'edited' | 'dropped' | 'queued' | 'interrupted' | string
  warning?: string
}

export interface ApprovalOperation {
  operation_id: string
  task_id: string
  status: 'running' | 'done' | 'review' | 'failed' | 'cancelled' | 'expired' | string
  error?: string
}

export type ApprovalResult =
  | { id: string; status: 'done' }
  | ApprovalOperation

export interface AskResult {
  kind: 'answer' | 'task' | 'plan'
  answer?: string
  task_id?: string
  /** A plan classification: the plan row the turn started, its goal and the
   *  stages it released. The transcript renders the plan card from the ref. */
  plan_id?: string
  plan_goal?: string
  plan_stages?: string[]
  task_state?: string
  ok?: boolean
  stdout?: string
  stderr?: string
  exit_code?: number
  /** Execution attribution: the agent harness that ran the task, the node it
   *  ran on, the model it used, and whether panda injected that model. */
  agent?: string
  model?: string
  injected?: boolean
  executor?: string
  /** The entry model that served the ask's own classify/answer calls. */
  entry_model?: string
  /** LLM-generated summary of the task outcome. Filled by SummarizeResult
   *  after every inline task so the UI shows a human-readable summary
   *  instead of raw stdout/stderr. */
  report?: string
  /** Chain of thought / reasoning accumulated during the turn. */
  thought?: string
  /** Approval flow: a parked tier-2 task asks for a foreground decision;
   *  denied marks a standing remembered "no" that answered first; a set
   *  consent_scope (session|project) names the remembered scope that supplied
   *  consent without prompting. */
  needs_approval?: boolean
  approval?: AskApproval
  denied?: boolean
  consent_scope?: 'session' | 'project' | string
  /** Non-fatal advisory from the engine — e.g. the work was queued but no
   *  live consumer drains this store, so the receipt is parked work, not
   *  dispatch. Rendered verbatim (server-localized). */
  warning?: string
}

/** The parked tier-2 request inside an ask result — what it is, and the
 *  remember scope the approval card should preselect. */
export interface AskApproval {
  task_id: string
  title: string
  reason?: string
  project?: string
  scope: ApprovalScope
}

/** Where a "remember this answer" lands: once = this action only, session =
 *  this chat, project = every session of the project (persisted). */
export type ApprovalScope = 'once' | 'session' | 'project'

/** GET /api/sessions/{id}/approval — the policy the session's next tier-2
 *  gate will apply: resolved mode, the preselected remember scope, and any
 *  standing remembered decision (and which bucket it lives in). */
export interface SessionApprovalState {
  mode: 'never' | 'on-request' | 'always' | string
  scope: ApprovalScope
  decision?: 'approve' | 'deny' | ''
  decision_scope?: '' | 'session' | 'project' | string
  project?: string
}

export interface ChooseDirectoryResult {
  path?: string
  canceled?: boolean
  /** Older panels emit the British spelling; accept either. */
  cancelled?: boolean
  /** This request cannot be served by a native chooser — the panel is being
   *  viewed from another device, or the host has no desktop shell. The
   *  console opens its own in-page browser instead of waiting. */
  unsupported?: boolean
  error?: string
}

export interface DirectoryListing {
  current: string
  parent: string
  separator: string
  directories: { name: string; path: string }[]
}

/** GET /api/update — the self-update pipeline status snapshot. */
export interface UpdateStatus {
  stage: 'idle' | 'checking' | 'available' | 'downloading' | 'staged' | 'applying' | 'done' | 'error'
  current: string
  /** The running build's release codename (e.g. "Periapsis"), when known. */
  current_codename?: string
  latest?: string
  /** The pending release's codename, parsed from the GitHub release title. */
  latest_codename?: string
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

/** Renders a release for humans: "v0.0.9 Periapsis" when the codename is
 *  known, bare "v0.0.9" when it is not. Empty version renders as-is so a
 *  not-yet-checked status never shows a dangling "v". */
export function displayVersion(version?: string, codename?: string): string {
  if (!version) return ''
  return 'v' + version + (codename ? ' ' + codename : '')
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
  capacity: { cpu_cores: number; ram_gb: number; max_concurrent_tasks: number; current_tasks: number; queued_tasks?: number }
  resource_profile?: { cpu: number; ram_gb: number; gpu_vram_gb: number; duration_hint: string }
  /** Fleet observability: advertised software version (empty until a
   *  new-protocol peer speaks), and this node's measured edge toward the
   *  peer — transport kind ("ws"/"udp") and ping RTT in ms. Absent for
   *  the local row and peers only reachable via gossip. */
  ver?: string
  rtt_ms?: number
  transport?: string
  /** Trust-zone tier pinned on the row (experimental): absent/"mesh" is the
   *  default fleet-wide trust; guarded/open mark a zoned peer. */
  trust?: string
  /** Machine facts advertised by the node itself (v50): the system it runs
   *  ("darwin"/"windows"/"linux") and its form factor ("laptop"/"desktop"/
   *  "mini"/"server"/"vm"). Empty on a peer that predates the fields. */
  os?: string
  form_factor?: string
  /** Link-state advertisement: the peer ids this row holds a live connection
   *  to, plus the measured RTT / transport for those edges. The workspace
   *  topology draws one edge per entry. */
  neighbors?: string[]
  link_metrics?: Record<string, number>
  link_kinds?: Record<string, string>
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
  /** This machine's form factor as the node probed it ("laptop"/"desktop"/
   *  "mini"/"server"/"vm"); empty when no probe answered. */
  form_factor?: string
  node?: NodeInfo
  version: string
  codename?: string
  update?: UpdateStatus
  /** Release line's ship date ("2026-10-15"), empty on ad-hoc builds. */
  release_date?: string
}

/** One day of activity from GET /api/stats. Days with no activity are not
 *  sent at all; `models` splits that day's tokens by model and is absent on
 *  days whose calls reported no usage. */
export interface StatsDay {
  date: string
  sessions: number
  messages: number
  tasks: number
  tokens: number
  models?: Record<string, number>
}

/** GET /api/stats — the profile page's usage rollup. `daily` is chronological
 *  and sparse (active days only). */
export interface StatsInfo {
  sessions: number
  messages: number
  tasks: number
  tokens: number
  active_days: number
  total_days: number
  /** Busiest hour of the day, 0–23; null on a node that has never worked. */
  peak_hour: number | null
  /** All-time top model by tokens, falling back to the configured model. */
  favorite_model?: string
  first_day?: string
  last_day?: string
  daily: StatsDay[]
}

/** — Capability card (stage 6): GET /api/card's parsed form mirrors
 * ledger.Card on the wire (snake_case). native/manual are lists, agents a
 * name-keyed record; tier is 1 (reversible) or 2 (irreversible/needs auth). */
export interface CardNative {
  id: string
  command: string
  args?: string[]
  tier: number
  description?: string
}

export interface CardAgent {
  adapter: string
  install_check?: string
  /** argv template generic.py expands — {prompt} placeholder, plus {stdin},
   *  {cwd}, {resume} and {max_turns}. Set only for agents wired through the
   *  generic adapter. */
  command?: string
  capabilities?: string[]
  best_at?: string[]
  not_for?: string[]
  cost_tier?: string
  tier: number
}

export interface CardManual {
  id: string
  notify: string
}

export interface CapabilityCard {
  device: string
  resource_class: string
  node_kind?: string
  node_identity?: string
  chip?: string
  native?: CardNative[]
  agents?: Record<string, CardAgent>
  manual?: CardManual[]
}

/** GET /api/card — parsed card + raw YAML + path (the raw editor's text). */
export interface CardFile {
  path: string
  raw: string
  card: CapabilityCard
}

/** POST /api/nodes/add — what `panda nodes add` prints, structured: the
 * join guide for the other machine (steps + install command + where the
 * shared secret lives) plus whether the peer was dialed live. The secret
 * itself is never on the wire — only the file it lives in. */
export interface NodesAddResult {
  addr: string
  added: boolean
  secret_generated: boolean
  dialed: boolean
  dial_error?: string
  cleartext_hint?: boolean
  config_path: string
  listen_addr: string
  invite_steps: string[]
  install_command: string
}

/** GET /api/pair — the Bluetooth-style pairing surface: inbound requests
 *  awaiting this operator's answer, devices the LAN heard recently, and
 *  the console's own outgoing sessions (the pair dialog polls these). */
export interface PairView {
  requests: PairRequest[]
  discovered: DiscoveredDevice[]
  outgoing: OutgoingPair[]
}

/** One inbound pairing request (a row in pair_sessions on the daemon). */
export interface PairRequest {
  id: string
  peer_name: string
  peer_addr: string
  code: string
  state: string
  expires_at: string
}

/** One LAN-discovered device the pair button can target. */
export interface DiscoveredDevice {
  id: string
  addr: string
  verified: boolean
}

/** One console-initiated pairing session, polled until done/rejected. */
export interface OutgoingPair {
  session: string
  target: string
  addr: string
  code: string
  state: string // waiting | done | rejected | expired | error
  err?: string
}

/** POST /api/pair/initiate — the session + code to show while waiting. */
export interface PairInitiated {
  session: string
  code: string
  target: string
  state: string
}

/** GET/POST /api/onboarding — the first-run wizard's persisted state. */
export interface OnboardingState {
  locale: string
  terms_accepted: boolean
  /** Revision of the accepted terms — MIT-era installs have
   *  terms_accepted without a version, so a license change re-shows the
   *  terms step. Compare against terms_version_current. */
  terms_version: number
  /** The revision this binary enforces. Comes from the server so the
   *  comparison can never drift from config.TermsVersionCurrent. */
  terms_version_current: number
  onboarded: boolean
  approval_mode: 'always' | 'on-request' | 'never'
  model_configured: boolean
}

export interface OnboardingPatch {
  locale?: string
  terms_accepted?: boolean
  onboarded?: boolean
  approval_mode?: 'always' | 'on-request' | 'never'
}

/** GET/PUT /api/settings/app — the four app policy groups (C1). */
export interface AppSettings {
  injection_model: 'auto' | 'always' | 'never'
  preferred_agents: string[]
  memory_limits: { user: number; memory: number; project: number }
  approval_mode: 'always' | 'on-request' | 'never'
  /** Agent tool face: minimal keeps each adapter's whitelist, extended reaches
   *  the agent's own skills, sub-agents and MCP servers. */
  tools_policy: 'minimal' | 'extended'
  /** GET-only: read-only confinement info. mode is off|standard|strict from
   *  config.yaml; backend is the OS mechanism in effect (seatbelt|bwrap|"");
   *  active is false when mode is configured but no backend exists. */
  sandbox?: { work_path: string; mode?: string; backend?: string; active?: boolean }
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

// ProjectDetail is a project as the console now sees it: metadata, how big its
// memory is, and whether it is the one currently entered. `projects` keeps the
// name-only array so callers that just want names do not have to change.
export interface ProjectDetail {
  name: string
  work_dir?: string
  description?: string
  created_at: string
  updated_at: string
  active: boolean
  memory_entries: number
  memory_chars: number
  sessions?: number
  /** Work filed under the project: state → count (queued/running/review/done/…).
   *  Absent on nodes whose panel predates the summary. */
  tasks?: Record<string, number>
  /** Per-project approval policy: '' mode inherits the global approval.mode;
   *  scope is where the approval card preselects its remember; decision is a
   *  remembered project-scope answer ('' | approve | deny). */
  approval_mode?: '' | 'never' | 'on-request' | 'always'
  approval_scope?: ApprovalScope
  approval_decision?: '' | 'approve' | 'deny'
}

export interface ProjectList {
  projects: string[]
  detail: ProjectDetail[]
  active: string
}

export interface HealthStatus {
  status: 'ok' | 'degraded'
  database: string
  uptime: string
  version: string
  timestamp: string
}

// ---- Endpoints ----

export const api = {
  health(): Promise<HealthStatus> {
    return request('GET', '/api/healthz')
  },

  tasks(params?: { state?: string; project?: string; session?: string }): Promise<Task[]> {
    const q = new URLSearchParams()
    if (params?.state) q.set('state', params.state)
    if (params?.project) q.set('project', params.project)
    // One conversation's tasks — the chat header's orchestration card. Server
    // side this is a different read (by session_id, uncapped) than the board's
    // state listing, so it must not be emulated by filtering a board response.
    if (params?.session) q.set('session', params.session)
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
    node?: string
    scope_paths?: string[]
    isolate?: boolean
    workdir?: string
  }): Promise<CreateTaskResult> {
    return request('POST', '/api/tasks', body)
  },

  /** Quick priority change from a board card. */
  patchTask(id: string, priority: 'high' | 'normal' | 'low'): Promise<{ id: string; priority: string }> {
    return request('PATCH', `/api/tasks/${encodeURIComponent(id)}`, { priority })
  },

  /** Deliver a user message into a running or queued task — the two send
   *  modes: queue (board context the agent reads at its next round) or
   *  inject (a steer that leads the next round, aborting the in-flight one
   *  when this console runs the task). */
  sendTaskMessage(id: string, text: string, inject: boolean): Promise<TaskMessageResult> {
    return request('POST', `/api/tasks/${encodeURIComponent(id)}/messages`, { text, inject })
  },

  /** The task's own board scope — queued user notes, steers and sibling
   *  messages the detail view renders so a posted note is visible, not just
   *  accepted. */
  taskBoardMessages(id: string): Promise<{ task_id: string; messages: TaskBoardMessage[] }> {
    return request('GET', `/api/tasks/${encodeURIComponent(id)}/messages`)
  },

  /** Type into a thread while its turn streams — the composer's queue/inject
   *  pair. A thread whose task is still live takes the durable board path
   *  (the response then carries its task_id); a plain answer turn rides the
   *  streaming ask's steer channel, which only exists while the turn runs.
   *  attachments are upload mentions (see uploadSessionFiles); the server
   *  appends them to the note as `@path` refs, so a screenshot can ride a
   *  queue/inject instead of being refused with "no files mid-turn". */
  sendSessionMessage(
    id: string,
    text: string,
    inject: boolean,
    attachments: string[] = [],
  ): Promise<TaskMessageResult> {
    return request('POST', `/api/sessions/${encodeURIComponent(id)}/messages`, {
      text,
      inject,
      attachments,
    })
  },

  /** The composer's pending strip: notes this user queued or injected that no
   *  round has folded yet. Board rows survive a reload; `mem:` keys are the
   *  process-local steer queue of a plain answer turn. */
  sessionNotes(id: string): Promise<{ session_id: string; notes: SessionNote[] }> {
    return request('GET', `/api/sessions/${encodeURIComponent(id)}/notes`)
  },

  /** Rewrite a pending note before a round reads it. Refused with 409 once the
   *  round has consumed it — the board must not disagree with the prompt. */
  editSessionNote(id: string, key: string, text: string, attachments: string[] = []): Promise<NoteResult> {
    return request('PATCH', `/api/sessions/${encodeURIComponent(id)}/notes/${encodeURIComponent(key)}`, {
      text,
      attachments,
    })
  },

  /** Take a pending note back out of the queue. */
  deleteSessionNote(id: string, key: string): Promise<NoteResult> {
    return request('DELETE', `/api/sessions/${encodeURIComponent(id)}/notes/${encodeURIComponent(key)}`)
  },

  /** Escalate a queued note to an inject in place — no second row, so the
   *  round reads the same words once. Reports "interrupted" only when this
   *  console's engine actually aborted the running round. */
  promoteSessionNote(id: string, key: string): Promise<NoteResult> {
    return request('POST', `/api/sessions/${encodeURIComponent(id)}/notes/${encodeURIComponent(key)}/promote`)
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

  /** Self-evolution patch sets: the merge-review surface. A proposal is a
   *  review task whose spec.kind === 'self_merge'; these endpoints show the
   *  diff the approve button would merge. */
  selfEvoSet(id: string): Promise<SelfEvoSet> {
    return request('GET', `/api/selfevo/${encodeURIComponent(id)}`)
  },

  selfEvoFile(id: string, name: string): Promise<string> {
    return requestText('GET', `/api/selfevo/${encodeURIComponent(id)}/file?n=${encodeURIComponent(name)}`)
  },

  /** Artifact data plane: the file listing inside the task's output
   *  archive (a screenshot a computer-use run took, a plan stage's tree).
   *  The archive and per-file bytes stream through fetchBlob — browsers
   *  cannot send the Bearer header on <a href>/<img src>. */
  taskArtifactFiles(id: string): Promise<ArtifactFileList> {
    return request('GET', `/api/tasks/${encodeURIComponent(id)}/artifact/files`)
  },

  approve(id: string, scope?: ApprovalScope): Promise<ApprovalResult> {
    return request('POST', `/api/tasks/${id}/approve`, scope && scope !== 'once' ? { scope } : undefined)
  },

  approvalOperation(id: string, operationID: string): Promise<ApprovalOperation> {
    return request('GET', `/api/tasks/${encodeURIComponent(id)}/operations/${encodeURIComponent(operationID)}`)
  },

  /** Requeue a follow-up-parked task: the supervisor said more work remains
   *  and the operator's answer is "keep going" — the queue scheduler runs
   *  the continuation under a fresh round budget, keeping the agent session
   *  and work dir. An optional note leads the continued round's intent. */
  continueTask(id: string, note?: string): Promise<{ id: string; state: string; warning: string }> {
    return request(
      'POST',
      `/api/tasks/${encodeURIComponent(id)}/continue`,
      note ? { note } : undefined,
    )
  },

  reject(id: string, reason?: string, scope?: ApprovalScope): Promise<void> {
    const body: Record<string, string> = {}
    if (reason) body.reason = reason
    if (scope && scope !== 'once') body.scope = scope
    return request('POST', `/api/tasks/${id}/reject`, Object.keys(body).length ? body : undefined)
  },

  cancel(id: string): Promise<{ id: string; cancelled: number }> {
    return request('POST', `/api/tasks/${id}/cancel`)
  },

  /** Delete one task and its subtree (queued or finished only; the server
   *  answers 409 for a task still moving — cancel first). */
  deleteTask(id: string): Promise<{ id: string; deleted: number }> {
    return request('DELETE', `/api/tasks/${encodeURIComponent(id)}`)
  },

  /** One-click board wipe: cancel everything still moving, delete every
   *  task record. */
  clearTasks(): Promise<{ cancelled: number; deleted: number }> {
    return request('DELETE', '/api/tasks')
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

  createProject(body: {
    name: string
    work_dir?: string
    description?: string
    enter?: boolean
  }): Promise<ProjectDetail> {
    return request('POST', '/api/projects', body)
  },

  project(name: string): Promise<ProjectDetail> {
    return request('GET', `/api/projects/${encodeURIComponent(name)}`)
  },

  patchProject(
    name: string,
    body: {
      name?: string
      work_dir?: string
      description?: string
      approval_mode?: 'inherit' | 'never' | 'on-request' | 'always' | ''
      approval_scope?: ApprovalScope
    },
  ): Promise<ProjectDetail> {
    return request('PATCH', `/api/projects/${encodeURIComponent(name)}`, body)
  },

  deleteProject(
    name: string,
    keepMemory: boolean,
    sessions?: 'keep' | 'delete',
  ): Promise<{ removed: string; memory_kept: boolean; sessions_action?: string; sessions_affected?: number }> {
    const params = new URLSearchParams()
    if (keepMemory) params.set('keep_memory', '1')
    if (sessions) params.set('sessions', sessions)
    const q = params.toString() ? `?${params.toString()}` : ''
    return request('DELETE', `/api/projects/${encodeURIComponent(name)}${q}`)
  },

  enterProject(name: string): Promise<ProjectDetail> {
    return request('POST', `/api/projects/${encodeURIComponent(name)}/enter`)
  },

  /** Forget the project's stored approval decision — the row a project-scope
   *  "remember this" answer wrote. Returns the refreshed project view. */
  clearProjectApproval(name: string): Promise<ProjectDetail> {
    return request('DELETE', `/api/projects/${encodeURIComponent(name)}/approval`)
  },

  exitProject(): Promise<{ left: string }> {
    return request('POST', '/api/projects/exit')
  },


  nodes(): Promise<NodeInfo[]> {
    return request('GET', '/api/nodes')
  },

  /** Drop a stale node row (offline remote only — the server refuses the
   *  local node and online nodes, since both re-register themselves). */
  removeNode(id: string): Promise<{ id: string; removed: boolean }> {
    return request('DELETE', `/api/nodes/${encodeURIComponent(id)}`)
  },

  /** Join a device: append the peer to config.yaml (+ shared secret when
   *  missing) and dial it live when an engine is running. Returns the join
   *  guide for the other machine. trust pins the peer's trust-zone tier
   *  (experimental): absent = mesh, the pre-feature default. */
  addNode(addr: string, trust?: string): Promise<NodesAddResult> {
    return request('POST', '/api/nodes/add', trust ? { addr, trust } : { addr })
  },

  /** POST /api/nodes/{id}/trust — the console twin of `panda nodes trust`:
   *  pin a trust-zone tier on a known node row (or every sibling row of a
   *  "k:<pubkey>" stable identity). Live on the running daemon. */
  setNodeTrust(id: string, trust: string): Promise<{ id: string; trust: string }> {
    return request('POST', `/api/nodes/${encodeURIComponent(id)}/trust`, { trust })
  },

  // ---- Bluetooth-style pairing (`panda pair`'s web twin) ----

  pair(): Promise<PairView> {
    return request('GET', '/api/pair')
  },

  /** Start pairing with a discovered device id/name or a literal
   *  host:port — returns the code to compare on the other screen. */
  pairInitiate(target: string): Promise<PairInitiated> {
    return request('POST', '/api/pair/initiate', { target })
  },

  /** Answer an inbound request: confirm admits the device and the daemon
   *  hands over the mesh secret; reject ends the session. */
  pairAnswer(id: string, confirm: boolean): Promise<{ state: string }> {
    return request('POST', '/api/pair/answer', { id, confirm })
  },

  // ---- Capability card (structured editor + raw YAML editor) ----

  card(): Promise<CardFile> {
    return request('GET', '/api/card')
  },

  /** Whole-file replacement (the raw editor's save). Server-side validation
   *  via ledger.LoadCard; the previous card is kept as .bak. */
  putCardRaw(yaml: string): Promise<{ status: string; live: boolean }> {
    return request('PUT', '/api/card', { yaml })
  },

  addNativeAbility(body: {
    id: string
    command: string
    args?: string[]
    tier?: number
    description?: string
  }): Promise<{ status: string; live: boolean }> {
    return request('POST', '/api/card/native', body)
  },

  removeNativeAbility(id: string): Promise<{ status: string; live: boolean }> {
    return request('DELETE', `/api/card/native/${encodeURIComponent(id)}`)
  },

  addCardAgent(
    name: string,
    body: {
      adapter: string
      install_check?: string
      command?: string
      capabilities?: string[]
      best_at?: string[]
      not_for?: string[]
      cost_tier?: string
      tier?: number
    },
  ): Promise<{ status: string; live: boolean }> {
    return request('POST', `/api/card/agents/${encodeURIComponent(name)}`, body)
  },

  /** Partial update — only the fields present in the body are rewritten
   *  (undefined stays "leave the card alone"). */
  patchCardAgent(
    name: string,
    body: Partial<{
      adapter: string
      install_check: string
      command: string
      capabilities: string[]
      best_at: string[]
      not_for: string[]
      cost_tier: string
      tier: number
    }>,
  ): Promise<{ status: string; live: boolean }> {
    return request('PATCH', `/api/card/agents/${encodeURIComponent(name)}`, body)
  },

  removeCardAgent(name: string): Promise<{ status: string; live: boolean }> {
    return request('DELETE', `/api/card/agents/${encodeURIComponent(name)}`)
  },

  addManualAbility(body: { id: string; notify: string }): Promise<{ status: string; live: boolean }> {
    return request('POST', '/api/card/manual', body)
  },

  removeManualAbility(id: string): Promise<{ status: string; live: boolean }> {
    return request('DELETE', `/api/card/manual/${encodeURIComponent(id)}`)
  },

  /** This machine's device profile and its capability card. */
  self(): Promise<SelfInfo> {
    return request('GET', '/api/self')
  },

  /** Usage rollup behind the profile page's calendar: per-day activity plus
   *  the six headline numbers. Only days with activity are returned — the
   *  console owns the 26-week grid and paints the quiet days itself. */
  stats(): Promise<StatsInfo> {
    return request('GET', '/api/stats')
  },

  // ---- First-run onboarding wizard ----

  getOnboarding(): Promise<OnboardingState> {
    return request('GET', '/api/onboarding')
  },

  postOnboarding(patch: OnboardingPatch): Promise<OnboardingState> {
    return request('POST', '/api/onboarding', patch)
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
 
  sessions(project?: string): Promise<Session[]> {
    const q = project !== undefined ? `?project=${encodeURIComponent(project)}` : ''
    return request('GET', `/api/sessions${q}`)
  },

  createSession(title?: string, project?: string, team?: string[]): Promise<Session> {
    return request('POST', '/api/sessions', {
      title,
      project,
      team: team && team.length > 0 ? team : undefined,
    })
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

  /** Undo a recorded session merge — the console's `panda session revert`. */
  sessionRevert(id: string): Promise<{ reverted: boolean; subject: string }> {
    return request('POST', `/api/sessions/${encodeURIComponent(id)}/revert`, {})
  },

  /** Roll a task's git checkout back to its pre-run baseline. */
  taskRevert(id: string): Promise<{ reverted: boolean; restored: string[]; removed: string[]; dir: string }> {
    return request('POST', `/api/tasks/${encodeURIComponent(id)}/revert`, {})
  },

  patchSession(
    id: string,
    body: { title?: string; project?: string; pinned?: boolean; team?: string[] },
  ): Promise<Session> {
    return request('PATCH', `/api/sessions/${encodeURIComponent(id)}`, body)
  },

  /** Batch delete — one round trip for the rail's select-N flow. The reply
   *  splits ids into deleted vs failed so the caller can keep the failed
   *  rows selected rather than pretending they are gone. */
  deleteSessions(ids: string[]): Promise<SessionBulkDeleteResult> {
    return request('POST', '/api/sessions/bulk-delete', { ids })
  },

  cancelSession(id: string, operationID: string): Promise<{ id: string; operation_id: string; cancelled: boolean }> {
    return request('POST', `/api/sessions/${encodeURIComponent(id)}/cancel`, { operation_id: operationID })
  },

  /** The approval policy the session's next tier-2 gate will apply. */
  sessionApproval(id: string): Promise<SessionApprovalState> {
    return request('GET', `/api/sessions/${encodeURIComponent(id)}/approval`)
  },

  /** Forget the session's remembered approval answers (session bucket plus
   *  the project row's stored decision). Returns the now-clean state. */
  clearSessionApproval(id: string): Promise<SessionApprovalState> {
    return request('DELETE', `/api/sessions/${encodeURIComponent(id)}/approval`)
  },

  /** Raise the host's own folder chooser and answer with the folder picked.
   *  `defaultPath` is where it opens; `title` is the dialog's caption, which
   *  the caller localizes because the panel does not know the UI language.
   *  No timeout: the OS window stays up until the user decides, and cutting
   *  the request at a minute would discard a choice about to be made. */
  chooseDirectory(defaultPath?: string, title?: string): Promise<ChooseDirectoryResult> {
    const body: Record<string, string> = {}
    if (defaultPath) body.default_path = defaultPath
    if (title) body.title = title
    return request('POST', '/api/dialog/choose-directory', body, { timeoutMs: 0 })
  },

  listDirectories(path?: string): Promise<DirectoryListing> {
    const q = path ? `?path=${encodeURIComponent(path)}` : ''
    return request('GET', `/api/fs/directories${q}`)
  },

  // ---- Reminders ----

  reminders(): Promise<Reminder[]> {
    return request('GET', '/api/reminders')
  },

  createReminder(body: { message: string; after_minutes?: number; due_at?: string; repeat_seconds?: number }): Promise<Reminder> {
    return request('POST', '/api/reminders', body)
  },

  deleteReminder(id: number): Promise<{ deleted: number }> {
    return request('DELETE', `/api/reminders/${id}`)
  },

  // ---- 待办 (the user's own board) ----

  /** The whole board in one read; the page paints every lane from it. */
  todos(params?: { status?: string; project?: string; q?: string }): Promise<Todo[]> {
    const q = new URLSearchParams()
    if (params?.status) q.set('status', params.status)
    if (params?.project) q.set('project', params.project)
    if (params?.q) q.set('q', params.q)
    const suffix = q.size > 0 ? `?${q}` : ''
    return request('GET', `/api/todos${suffix}`)
  },

  createTodo(body: TodoInput): Promise<Todo> {
    return request('POST', '/api/todos', body)
  },

  /** Partial update: only the fields present are touched. `due_at` takes
   *  "" (clear), "YYYY-MM-DD", or an RFC3339 stamp. */
  patchTodo(id: number, body: TodoPatch): Promise<Todo> {
    return request('PATCH', `/api/todos/${id}`, body)
  },

  deleteTodo(id: number): Promise<{ deleted: number }> {
    return request('DELETE', `/api/todos/${id}`)
  },

  reorderTodos(ids: number[]): Promise<{ reordered: number }> {
    return request('POST', '/api/todos/reorder', { ids })
  },

  /** 交给熊猫: enqueue a task from this todo and link the two rows. */
  startTodo(id: number): Promise<TodoStartResult> {
    return request('POST', `/api/todos/${id}/start`, {})
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

  /** Remove one daily/<name>.md warm-layer diary file. */
  deleteMemoryDaily(name: string): Promise<{ daily: string; status: string }> {
    return request('DELETE', `/api/memory/daily/${encodeURIComponent(name)}`)
  },

  /** Apply the diary retention windows (30d archive / 90d delete) right now. */
  pruneMemoryDaily(): Promise<{ status: string }> {
    return request('POST', '/api/memory/daily/prune')
  },

  /** One project's MEMORY.md (§ entries) with its configured cap. */
  projectMemory(name: string): Promise<ProjectMemory> {
    return request('GET', `/api/projects/${encodeURIComponent(name)}/memory`)
  },

  saveProjectMemory(name: string, content: string): Promise<{ project: string; chars: number; limit: number }> {
    return request('PUT', `/api/projects/${encodeURIComponent(name)}/memory`, { content })
  },

  /** The knowledge graph over every memory surface plus the skill index:
   *  edges are provenance ([from:日志] promotions) or literal name mentions. */
  memoryGraph(): Promise<MemoryGraph> {
    return request('GET', '/api/memory/graph')
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

  version(): Promise<{ version: string; codename?: string }> {
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

  resetSkill(name: string): Promise<{ ok: boolean; name: string }> {
    return request('POST', '/api/skills/reset', { name })
  },

  hubSkills(query?: string): Promise<HubSkillEntry[]> {
    const p = query ? `?q=${encodeURIComponent(query)}` : ''
    return request('GET', `/api/skills/hub${p}`)
  },

  installHubSkill(name: string, scope?: string, force?: boolean): Promise<{ name: string; status: string }> {
    return request('POST', '/api/skills/hub/install', { name, scope, force })
  },

  importSkill(req: ImportSkillRequest): Promise<{ names?: string[]; name?: string; count: number; status: string }> {
    return request('POST', '/api/skills/import', req)
  },

  installRecommendedSkills(): Promise<{ count: number; names: string[]; status: string }> {
    return request('POST', '/api/skills/hub/install-recommended')
  },

  discoverSkill(query: string): Promise<{ name: string; description: string; status: string; is_new: boolean }> {
    return request('POST', '/api/skills/discover', { query })
  },

  // ---- Multi-model registry (TUI /model parity) ----

  models(): Promise<ModelsResponse> {
    return request('GET', '/api/models')
  },

  addModel(req: AddModelRequest): Promise<ModelEntry> {
    return request('POST', '/api/models', req)
  },

  useModel(alias: string, ref?: ModelRef): Promise<ModelEntry> {
    return request('POST', `/api/models/${encodeURIComponent(alias)}/use${refQuery(ref)}`)
  },

  /** Rewrite one registry row in place — the console's "edit". `ref` narrows
   *  which row a legacy config's duplicate alias means. */
  updateModel(alias: string, req: AddModelRequest, ref?: ModelRef): Promise<ModelEntry> {
    return request('PUT', `/api/models/${encodeURIComponent(alias)}${refQuery(ref)}`, req)
  },

  removeModel(alias: string, ref?: ModelRef): Promise<{ removed: string; switched_to?: string }> {
    return request('DELETE', `/api/models/${encodeURIComponent(alias)}${refQuery(ref)}`)
  },

  fetchModels(req: ModelRefRequest): Promise<{ ok: boolean; alias?: string; models?: string[]; error?: string }> {
    return request('POST', '/api/models/fetch', req)
  },

  testModelRef(req: ModelRefRequest): Promise<{ ok: boolean; alias?: string; reply?: string; error?: string }> {
    return request('POST', '/api/models/test', req)
  },

  // ---- Plans (TUI /plans parity) ----

  plans(): Promise<PlanSummary[]> {
    return request('GET', '/api/plans')
  },

  plan(id: string): Promise<PlanDetail> {
    return request('GET', `/api/plans/${encodeURIComponent(id)}`)
  },

  // ---- Diagnostics & parity extras ----

  doctor(): Promise<DoctorReport> {
    return request('GET', '/api/doctor')
  },

  contextInfo(): Promise<ContextInfo> {
    return request('GET', '/api/context')
  },

  cost(): Promise<CostReport> {
    return request('GET', '/api/cost')
  },

  fsRead(path: string): Promise<FsReadResult> {
    return request('GET', `/api/fs/read?path=${encodeURIComponent(path)}`)
  },

  fsFiles(path?: string): Promise<FsFilesResult> {
    const q = path ? `?path=${encodeURIComponent(path)}` : ''
    return request('GET', `/api/fs/files${q}`)
  },

  /** Ask the node to open `path` in the desktop's file manager. The panel
   *  only accepts directories inside its owned roots — the same boundary
   *  /api/fs/read enforces — so this cannot be used to browse the machine. */
  revealPath(path: string): Promise<{ opened: string }> {
    return request('POST', '/api/fs/reveal', { path })
  },

  /** Multipart upload into a session's uploads dir. Returns mention-ready
   *  paths, so the composer inserts whatever the server computed. */
  uploadSessionFiles(id: string, files: File[]): Promise<SessionUploadResult> {
    const body = new FormData()
    for (const f of files) body.append('file', f, f.name)
    return upload('POST', `/api/sessions/${encodeURIComponent(id)}/upload`, body)
  },

  /** Dictation: one recorded clip to the node's speech-to-text endpoint. The
   *  clip carries no filename — MediaRecorder blobs never have one — so the
   *  part's Content-Type is what names the container. A failure here is the
   *  vendor's own sentence (bad model, unsupported format) and is shown as-is. */
  transcribeAudio(clip: Blob): Promise<{ text: string }> {
    const body = new FormData()
    const ext = clip.type.includes('ogg') ? 'ogg'
      : clip.type.includes('mp4') ? 'mp4'
        : clip.type.includes('wav') ? 'wav'
          : 'webm'
    body.append('audio', clip, `dictation.${ext}`)
    return upload('POST', '/api/voice/transcribe', body)
  },
}

// ---- Multi-model registry wire types ----

/** The pair that tells two rows with one alias apart — a legacy config can
 *  hold both, and the address has to say which one is meant. */
export interface ModelRef {
  model?: string
  base_url?: string
}

function refQuery(ref?: ModelRef): string {
  if (!ref) return ''
  const q = new URLSearchParams()
  if (ref.model) q.set('model', ref.model)
  if (ref.base_url) q.set('base_url', ref.base_url)
  const s = q.toString()
  return s ? `?${s}` : ''
}

export interface ModelEntry {
  alias: string
  provider?: string
  provider_label?: string
  api_type: string
  base_url: string
  model: string
  max_tokens?: number
  context_window?: number
  thinking?: string // "on" | "off"; unset = provider default
  thinking_budget?: number
  max_retries?: number
  no_auth?: boolean
  active: boolean
  key_set: boolean
  key_hint?: string
  /** True when the stored limit is the catalogue's own answer, i.e. the user
   *  expressed no opinion — the form shows the number as a placeholder. */
  context_auto?: boolean
  max_tokens_auto?: boolean
  auto_context_window?: number
  auto_max_tokens?: number
  /** "endpoint-owner": the endpoint belongs to the row named by warning_arg.
   *  The console turns it into a sentence (and a fix), the wire stays stable. */
  warning_code?: string
  warning_arg?: string
}

export interface ProviderInfo {
  id: string
  label: string
  api_type: string
  base_url: string
  default_model: string
  context_window?: number
  no_auth: boolean
  key_saved: boolean
  region: string
}

export interface ModelsResponse {
  active: ModelEntry
  models: ModelEntry[]
  providers: ProviderInfo[]
}

export interface AddModelRequest {
  provider: string
  model?: string
  api_key?: string
  alias?: string
  /** Alias of the entry whose key this one borrows — says *which* account of
   *  a vendor is being extended when one vendor is registered twice. */
  reuse_from?: string
  api_type?: string
  base_url?: string
  max_tokens?: number
  /** Put the limit back on the catalogue's answer. Distinct from omitting the
   *  number, which means "leave what is stored". */
  max_tokens_reset?: boolean
  context_window?: number
  context_window_reset?: boolean
  thinking?: string // "on" | "off" | "auto"
  thinking_budget?: number
  thinking_budget_reset?: boolean
  max_retries?: number
  max_retries_reset?: boolean
  no_auth?: boolean
  params?: Record<string, unknown>
  headers?: Record<string, string>
}

export interface ModelRefRequest {
  alias?: string
  provider?: string
  api_key?: string
  model?: string
  api_type?: string
  base_url?: string
}

// ---- Plan wire types ----

export interface PlanSummary {
  plan_id: string
  stages: number
  done: number
  failed: number
  running: number
  review: number
  goal?: string
  stage_ids?: string[]
  created_at: number
  updated_at: number
}

export interface PlanArtifact {
  stage?: string
  hash: string
  source?: string
}

export interface ArtifactEntry {
  path: string
  mode: number
  size: number
}

export interface ArtifactFileList {
  id: string
  hash: string
  entries: ArtifactEntry[]
}

/** fetchBlob is the binary sibling of request(): same Bearer header and
 *  401 eviction, but hands back the raw bytes. Downloads and image previews
 *  need it — an <a href> or <img src> cannot carry Authorization. */
export async function fetchBlob(path: string): Promise<Blob> {
  const headers: Record<string, string> = {}
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`
  const res = await fetch(path, { headers, signal: AbortSignal.timeout(120_000) })
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      unauthorizedListeners.forEach((fn) => fn())
    }
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, text || res.statusText)
  }
  return res.blob()
}

export interface PlanStage {
  stage: string
  task_id: string
  title?: string
  state: string
  owner?: string
  needs?: string[]
  inputs?: PlanArtifact[]
  output_artifact?: string
  created_at: number
  updated_at: number
  priority?: string
}

export interface PlanDetail {
  plan_id: string
  stages: PlanStage[]
}

// ---- Diagnostics wire types ----

export interface DoctorCheck {
  key: string
  ok: boolean
  pairs?: string[]
}

export interface DoctorReport {
  checks: DoctorCheck[]
  problems: number
}

export interface ContextInfo {
  work_dir?: string
  node_name?: string
  model?: ModelEntry
  memory_files?: number
  project?: string
  project_work_dir?: string
  has_card?: boolean
}

export interface CostExecutor {
  executor: string
  calls: number
  tokens: number
  cost_usd: number
  successes: number
}

export interface CostReport {
  total_tokens: number
  total_cost_usd: number
  calls: number
  success_rate: number
  by_executor: CostExecutor[]
  since: number
  unavailable?: boolean
}

export interface FsFileEntry {
  name: string
  path: string
  dir: boolean
  size?: number
  mod_ts?: number
}

export interface FsFilesResult {
  current: string
  entries: FsFileEntry[]
}

export interface FsReadResult {
  path: string
  content: string
  size: number
  truncated: boolean
}

export interface SessionDiffStat {
  files_changed: number
  insertions: number
  deletions: number
  untracked: number
  binary?: string[]
  sensitive?: string[]
}

export interface SessionDiff {
  id: string
  branch: string
  changes: { status: string; path: string }[]
  patch: string
  /** Merge-preview roll-up; absent when the stat could not be computed. */
  stat?: SessionDiffStat
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
  builtin?: boolean
}

export interface HubSkillEntry {
  name: string
  description: string
  scope: string
  author?: string
  version?: string
  tags?: string[]
  url?: string
  doc_url?: string
  recommended?: boolean
  alias?: string
  installed: boolean
}

export interface ImportSkillRequest {
  source?: string
  content?: string
  name?: string
  scope?: string
  project?: string
  device?: string
  pending?: boolean
  force?: boolean
}

export interface ModelSettings {
  provider?: string
  name?: string
  api_type: 'anthropic' | 'openai' | 'responses' | 'google'
  base_url: string
  model: string
  max_tokens: number // 0 = provider/entry default; omitted = keep current
  context_window?: number // 0 = unknown/auto; omitted = keep current
  thinking?: string // "on" | "off" | "auto" | ""; "auto" resets to provider default
  thinking_budget?: number // 0 = provider default; omitted = keep current
  max_retries?: number // 0 = default (5), negative disables; omitted = keep current
  no_auth?: boolean // omitted = keep current
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
  due_at: number // Unix seconds — for recurring rows this is the NEXT occurrence
  created_at: number
  fired_at: number // 0 = never fired; for recurring rows it is the LAST fire time
  source: string // "tool" | "cli" | "web"
  repeat_seconds: number // >0 = recurring; the row stays pending between fires
}

/** 待办 lanes. `paused` is a todo the owner parked; `cancelled` is one they
 *  gave up on — both stay on the board rather than being deleted, so the
 *  history of "I decided not to" survives. */
export type TodoStatus = 'pending' | 'running' | 'paused' | 'done' | 'cancelled'

/** One row of the to-do board. Times are Unix seconds, 0 = unset. */
export interface Todo {
  id: number
  title: string
  description: string
  status: TodoStatus
  priority: 'high' | 'normal' | 'low'
  due_at: number
  started_at: number
  done_at: number
  tags: string[] | null
  project: string
  task_id: string // the task started from this todo; "" until 开始
  seq: number // manual lane order; 0 = never dragged
  created_at: number
  updated_at: number
}

export interface TodoInput {
  title: string
  description?: string
  status?: TodoStatus
  priority?: 'high' | 'normal' | 'low'
  due_at?: string
  tags?: string[]
  project?: string
}

export interface TodoPatch {
  title?: string
  description?: string
  status?: TodoStatus
  priority?: 'high' | 'normal' | 'low'
  due_at?: string
  tags?: string[]
  project?: string
  seq?: number
}

export interface TodoStartResult {
  todo?: Todo
  task_id: string
  session_id: string
  state: string
  warning?: string
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

/** GET /api/memory/graph — two layers over the same evidence. Provenance
 *  ("file") nodes are the memory surfaces and skills; knowledge ("entity")
 *  nodes are terms literally recurring inside them. Edges are provenance
 *  ([from:…] promotions out of the daily diary), literal name mentions between
 *  surfaces, entity mentions (surface → entity), or entity co-occurrence. */
export interface MemoryGraphNode {
  id: string
  kind: 'core' | 'topic' | 'project' | 'daily' | 'skill' | 'entity' | string
  /** Provenance surfaces vs extracted knowledge terms. */
  layer?: 'file' | 'entity' | string
  label: string
  chars?: number
  entries?: number // §-entry count (entry-shaped surfaces)
  files?: number // diary node: daily-log count
  status?: string
  /** Evidence mass — entries/files/chars for surfaces, occurrences for
   *  entities — scaling the rendered node. */
  weight?: number
  /** Dated evidence (YYYY-MM-DD) where the data honestly carries it; empty
   *  means "always visible" on the timeline. */
  first_seen?: string
  last_seen?: string
  /** Literal source lines the term appears in (entity nodes only) — the
   *  detail card shows them so a node explains itself. */
  evidence?: string[]
}

export interface MemoryGraphEdge {
  from: string
  to: string
  kind: 'promotion' | 'reference' | 'mention' | 'co' | string
  weight?: number // evidence count: promotions / mentions / co-occurrences
}

export interface MemoryGraph {
  nodes: MemoryGraphNode[]
  edges: MemoryGraphEdge[]
}

export interface SessionTurn {
  role: 'user' | 'assistant'
  text: string
  /** `plan` is the multi-stage receipt the panel files when the ask started a
   *  plan instead of a single task (sessions.go, `case "plan"`); its `ref` is
   *  the plan id, so the thread can draw the stages the panda cut the job
   *  into. */
  kind?: 'answer' | 'task' | 'plan' | 'error'
  ref?: string
  /** Machine reason on an error-kind turn (e.g. "model_config") — the panel
   *  stamps it so a replayed transcript keeps the matching fix-it CTA. */
  code?: string
  /** Classified model-error kind an error turn came from (entry.ErrKind*) —
   *  picks which troubleshooting <details> renders under the bubble.
   *  err_detail is the provider's own bounded reply, shown inside it. */
  err_kind?: string
  err_detail?: string
  /** How long the turn took, stamped by the panel when it files the reply.
   *  Absent on user turns and on transcripts written before the field
   *  existed; the console then has no duration to show. */
  duration_ms?: number
  /** The entry model that wrote the reply, filed with the turn so the console
   *  can name it beside the copy button — including after a reload, where the
   *  live `result` object no longer exists. Absent on user turns, on task
   *  turns (their card names the executor) and on older transcripts. */
  model?: string
  /** When the turn was written, in Unix seconds (the store stamps every turn).
   *  The console prints it beside the copy affordance under a question, so a
   *  long thread can be read against the clock. Absent on transcripts written
   *  before the field existed. */
  ts?: number
}

export interface SessionOperation {
  id: string
  status: 'running' | 'done' | 'review' | 'failed' | 'cancelled' | 'expired' | string
  task_id?: string
  error?: string
  updated_at: string
}

export interface Session {
  id: string
  title: string
  created_at: string
  updated_at: string
  branch?: string
  worktree?: string
  project?: string
  turns: SessionTurn[]
  operation?: SessionOperation
  pinned?: boolean
  /** The thread's agent roster — who may take this conversation's work.
   *  Empty/absent means no restriction. */
  team?: string[]
}

export interface SessionBulkDeleteResult {
  deleted: string[]
  failed: { id: string; error: string }[]
}

// ---- SSE transport ---------------------------------------------------------
//
// Both the streaming ask (POST) and the live event feed (GET) speak the same
// `event: …\ndata: …\n\n` framing over fetch + ReadableStream. One shared
// opener + one shared frame parser keep the two call sites honest: the token
// rides in an Authorization header (never in the URL, so it cannot leak via
// access logs, referrer headers, or shared links), and framing bugs get fixed
// exactly once.

/** Incremental SSE frame parser. Feed it decoded chunks; complete frames are
 *  delivered as (event, data) pairs — multi-line `data:` rejoined with \n,
 *  frames without data dropped, unknown lines ignored. Exported for tests. */
export function makeSSEFrameParser(onFrame: (event: string, data: string) => void): {
  push(chunk: string): void
} {
  let buf = ''
  return {
    push(chunk: string): void {
      buf += chunk
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
        onFrame(event, dataLines.join('\n'))
      }
    },
  }
}

export interface OpenSSEOptions {
  /** Aborting ends the stream promptly. An abort RESOLVES the promise —
   *  callers that need AbortError semantics (the composer's stop button)
   *  re-derive them from their own signal. */
  signal?: AbortSignal
  method?: 'GET' | 'POST'
  /** JSON request body for POST streams. */
  body?: unknown
  /** Fired once the response headers arrive with 2xx — the moment the
   *  (re)connection is proven live. */
  onOpen?(): void
  /** Every complete SSE frame. `data` is the raw payload string. */
  onEvent(event: string, data: string): void
}

/**
 * Opens one SSE stream over fetch and pumps frames into `onEvent` until the
 * stream ends. Promise semantics:
 *  - natural end of the stream → resolve
 *  - `signal` aborted → resolve (promptly: the parked reader is cancelled)
 *  - connection error (network failure, non-2xx) → reject (ApiError carries
 *    the status; a 401 also clears the token and fires `unauthorized`)
 */
export async function openSSE(url: string, opts: OpenSSEOptions): Promise<void> {
  const headers: Record<string, string> = { Accept: 'text/event-stream' }
  const token = currentToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'

  let res: Response
  try {
    res = await fetch(url, {
      method: opts.method ?? 'GET',
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal,
    })
  } catch (err) {
    // Aborting while fetch is in flight rejects; that is a clean end, not a
    // fault (the abort-resolves contract above).
    if (isAbort(err) || opts.signal?.aborted) return
    throw err
  }
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      unauthorizedListeners.forEach((fn) => fn())
    }
    const text = await res.text().catch(() => '')
    throw new ApiError(res.status, text || res.statusText)
  }
  if (!res.body) throw new ApiError(0, 'no response body')
  opts.onOpen?.()

  const reader = res.body.getReader()
  // fetch's own abort does not always propagate to a reader that is parked in
  // read(); cancelling it explicitly ends the loop immediately.
  const onAbort = () => void reader.cancel().catch(() => {})
  opts.signal?.addEventListener('abort', onAbort, { once: true })
  const decoder = new TextDecoder()
  const parser = makeSSEFrameParser(opts.onEvent)
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      parser.push(decoder.decode(value, { stream: true }))
    }
  } finally {
    opts.signal?.removeEventListener('abort', onAbort)
  }
  // A cancelled reader ends the loop cleanly — still an abort-driven end.
}

// ---- Streaming session ask (SSE over fetch POST) ----

/** One `status` SSE event. `text` is the server's English fallback; `kind`
 *  names the milestone ("task", "plan", "tool", "route", "exec", "spawn",
 *  "judge", "wait", "retry", "offline") so the console can re-phrase it in the
 *  user's locale. "spawn" marks a delegate fan-out — one per sub-agent the
 *  parent dispatched. The retry fields are populated when kind === 'retry';
 *  "offline" carries `detail` ("waiting" | "resumed") plus `waited_ms`. */
export interface AskStatusEvent {
  text: string
  kind?: string
  attempt?: number
  max?: number
  delay_ms?: number
  http_status?: number
  reason?: string
  /** kind === 'spawn': the child's label and its parallel-mode descriptor
   *  ("isolated", or the claimed lane paths) so the console phrases the
   *  fan-out milestone in the browser's own language. */
  name?: string
  detail?: string
  /** kind === 'offline': how long the outage lasted, on the "resumed" event. */
  waited_ms?: number
}

/** Structured fields on an `error` SSE event beyond the message. `kind` is
 *  the classified model-error kind (entry.ErrKind*) the console maps to a
 *  troubleshooting hint block; `http_status`/`detail` come from the provider. */
export interface AskErrorInfo {
  code?: string
  kind?: string
  http_status?: number
  detail?: string
}

export interface AskStreamHandlers {
  /** Chain-of-thought, on its own stream. Display-only (D14): never merge it
   *  into the answer or the stored turn. */
  onReasoning(text: string): void
  onDelta(text: string): void
  onStatus(text: string, ev?: AskStatusEvent): void
  onOperation(operationID: string): void
  onResult(r: AskResult): void
  /** A note the round loop actually folded into its prompt, with the round that
   *  carries it. The note was accepted earlier (POST /api/sessions/{id}/messages);
   *  this event is the proof it reached the model, so the console can stop
   *  calling it "queued". Fires zero or more times per turn. */
  onSteer?(text: string, round: number): void
  /** `code` carries the panel's machine reason when one exists —
   *  "model_config" marks the fix-in-settings class of failure. */
  onError(message: string, code?: string, info?: AskErrorInfo): void
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
  node?: string,
  /** The thread's roster as the composer shows it. Undefined keeps whatever
   *  the session already carries; an empty array clears the restriction. */
  agents?: string[],
): Promise<void> {
  await openSSE(`/api/sessions/${encodeURIComponent(id)}/ask`, {
    method: 'POST',
    body: { prompt, authorize, node: node || undefined, agents },
    signal,
    onEvent: (event, data) => {
      let payload: any
      try {
        payload = JSON.parse(data)
      } catch {
        return
      }
      if (event === 'reasoning') h.onReasoning(payload.text ?? '')
      else if (event === 'delta') h.onDelta(payload.text ?? '')
      else if (event === 'status') h.onStatus(payload.text ?? '', payload as AskStatusEvent)
      else if (event === 'operation') h.onOperation(payload.operation_id ?? '')
      else if (event === 'steer') h.onSteer?.(payload.text ?? '', payload.round ?? 0)
      else if (event === 'result') h.onResult(payload as AskResult)
      else if (event === 'error')
        h.onError(payload.message ?? 'unknown error', payload.code || undefined, {
          code: payload.code || undefined,
          kind: payload.kind || undefined,
          http_status: payload.http_status,
          detail: payload.detail || undefined,
        })
    },
  })
  // openSSE resolves on abort; the composer's stop button contract is an
  // AbortError, so surface the abort the way fetch would have.
  if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
}

/** Whether a caught error is an abort (the user pressing stop), not a fault. */
export function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

// ---- SSE live subscriptions -----------------------------------------------

/** Change payload delivered on `event: change` — the "what changed" portion of
 *  the SSE line: `kinds fp/tasks/fp/nodes/fp/reminders/fp/sessions`. Old
 *  servers that only send `init` or a single task fingerprint still
 *  deserialize safely (missing slots read as undefined). */
export interface ChangeEvent {
  kinds: string[]
  taskFP?: string
  nodeFP?: string
  reminderFP?: string
  sessionFP?: string
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
 * Opens GET /api/events as a long-lived SSE stream and keeps it alive:
 * transient connection errors and server-side closes trigger an automatic
 * reconnect with exponential backoff (1s → 15s cap). The returned Promise
 * resolves when the subscription is deliberately ended — the AbortSignal
 * fires, or the panel answers 401 (which also clears the token and fires
 * `unauthorized`; the gate screen handles the rest, so reconnecting into a
 * revoked token would be pointless).
 *
 * Each successful reconnection delivers a synthetic `onChange` event with
 * kinds ['reconnect'] so subscribers can re-fetch once and reconcile whatever
 * happened while the link was down — the same "refetch after reconnect"
 * semantics the old EventSource path had.
 *
 * Authentication rides in an Authorization header via openSSE; the URL stays
 * token-free (the server still accepts ?token= for legacy clients, but this
 * client never puts the secret in a URL).
 */
export function subscribeEvents(opts: SubscribeEventsOptions): Promise<void> {
  const params = new URLSearchParams()
  if (opts.trace) params.set('trace', '1')
  const url = '/api/events' + (params.size ? '?' + params.toString() : '')

  return new Promise<void>((resolve) => {
    let stopped = false
    let delay = RECONNECT_MIN_MS
    let attempt = 0
    let timer: ReturnType<typeof setTimeout> | null = null

    const finish = () => {
      if (stopped) return
      stopped = true
      if (timer !== null) {
        clearTimeout(timer)
        timer = null
      }
      resolve()
    }
    const onAbort = () => finish()
    if (opts.signal) {
      if (opts.signal.aborted) {
        resolve()
        return
      }
      opts.signal.addEventListener('abort', onAbort, { once: true })
    }

    const dispatch = (event: string, data: string) => {
      if (event === 'change') {
        if (!opts.onChange) return
        const parts = data.split(' ')
        const kinds = (parts[0] ?? '').split(',').filter(Boolean)
        const fps = (parts[1] ?? '').split('/')
        opts.onChange({
          kinds,
          taskFP: fps[0],
          nodeFP: fps[1],
          reminderFP: fps[2],
          sessionFP: fps[3],
          raw: data,
        })
      } else if (event === 'trace') {
        if (!opts.onTrace) return
        try {
          const payload = JSON.parse(data || '{}') as any
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
      }
    }

    const scheduleReconnect = () => {
      if (stopped) return
      const wait = delay
      delay = Math.min(delay * 2, RECONNECT_MAX_MS)
      timer = setTimeout(() => {
        timer = null
        connect()
      }, wait)
    }

    const connect = () => {
      if (stopped) return
      attempt++
      const nth = attempt
      openSSE(url, {
        signal: opts.signal,
        onOpen: () => {
          if (stopped) return
          // Link proven live again: reset the backoff and, when this was a
          // recovery rather than the first connection, tell subscribers to
          // re-fetch once (the synthetic "reconnect" change event).
          delay = RECONNECT_MIN_MS
          if (nth > 1) {
            try {
              opts.onChange?.({ kinds: ['reconnect'], raw: 'reconnect' })
            } catch {
              /* a throwing subscriber must not kill the bus */
            }
          }
        },
        onEvent: dispatch,
      })
        .then(() => {
          // Abort-driven or server-side end of the stream. An aborted signal
          // means the subscriber walked away; anything else we treat like a
          // dropped link and re-dial — the old EventSource reconnected on
          // every close too.
          if (stopped || opts.signal?.aborted) return finish()
          scheduleReconnect()
        })
        .catch((err: unknown) => {
          if (stopped || isAbort(err)) return finish()
          if (err instanceof ApiError && err.status === 401) {
            // Token revoked: openSSE already cleared it and broadcast
            // `unauthorized`. Reconnecting would just hammer a 401.
            return finish()
          }
          scheduleReconnect()
        })
    }

    connect()
  })
}

const RECONNECT_MIN_MS = 1000
const RECONNECT_MAX_MS = 15000
