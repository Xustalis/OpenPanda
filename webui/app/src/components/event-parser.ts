// SPDX-License-Identifier: AGPL-3.0-or-later

// Parses and formats task events for human-readable display.
// Replaces raw JSON walls with clear structured information and formats
// Chain-of-Thought (reasoning) into readable thinking blocks.

import { t } from '../i18n/index.ts'

export interface FormattedEvent {
  type: string
  label: string
  badgeClass: 'accent' | 'info' | 'warn' | 'danger' | 'dim'
  summary?: string
  thought?: string
  tags: { key: string; value: string }[]
  rawJson?: string
  /** Structured agent-activity block (type === 'agent_event'). */
  agent?: AgentBlock
  /** Lifecycle plumbing (submit → queue → delegate → accept): the timeline
   *  collapses a consecutive run of these into one dispatch-pipeline row
   *  instead of one card per internal bookkeeping write. */
  lifecycle?: { step: string; node?: string; failed?: boolean }
  /** Parsed result event (type === 'result'): rendered as a compact result
   *  card — status + readable stdout — never as raw key/value tags. */
  result?: { ok: boolean; exitCode?: number; stdout?: string; stderr?: string }
  /** Bookkeeping events with no user value (session_summary): the timeline
   *  renders them as one thin dim line, not a full card. */
  quiet?: boolean
}

/** AgentBlock is one typed activity block the adapter streamed back
 * (event protocol v2): assistant text, thinking, a tool_use call, or a
 * tool_result. `parent` carries the enclosing tool_use id when the block
 * ran inside a harness sub-agent — the timeline nests it into the
 * delegation tree. */
export interface AgentBlock {
  ev: string
  id?: string
  parent?: string
  name?: string
  toolUseId?: string
  text?: string
  thinking?: string
  content?: string
  input?: unknown
  isError?: boolean
}

function str(v: unknown): string | undefined {
  const s = typeof v === 'string' ? v : ''
  return s === '' ? undefined : s
}

/** The one argument a tool_use row summarizes — same pick order the
 * adapter's progress note uses. Inputs that carry none of the named keys
 * (an opaque {argc:3} from an internal shell tool) fall back to a compact
 * shape hint instead of rendering empty or as raw JSON soup. */
export function toolArgSummary(input: unknown): string {
  if (typeof input === 'string') return input
  if (!input || typeof input !== 'object') return ''
  const m = input as Record<string, unknown>
  for (const k of ['command', 'cmd', 'file_path', 'pattern', 'path', 'url', 'query', 'description']) {
    const v = m[k]
    if (typeof v === 'string' && v) return v
  }
  const argc = m.argc
  if (typeof argc === 'number' && argc > 0) {
    return t('events.toolArgs', { n: String(argc) })
  }
  try {
    const compact = JSON.stringify(input)
    if (compact && compact !== '{}' && compact.length <= 60) return compact
  } catch {
    // unserializable — nothing to show
  }
  return ''
}

/** An image a transcript row can actually show. `path` is a filesystem path
 *  the console fetches through /api/fs/image; `dataUrl` is bytes the adapter
 *  already inlined into the event. */
export interface ImageRef {
  path?: string
  dataUrl?: string
}

const IMAGE_EXT_RE = /\.(png|jpe?g|gif|webp|bmp|avif)$/i
const DATA_URL_RE = /^data:image\/[a-z0-9.+-]+;base64,[a-z0-9+/=\s]+$/i
/** Path-shaped enough to fetch: no whitespace (a sentence is not a path), and
 *  no shell metacharacters — `rm -f t*.png` is a command, not a picture. */
const PATH_SHAPE_RE = /^[^\s"'<>|*?]+$/
/** Anchored = the token names a location, not just a filename: it starts at
 *  the root (`/`, `~`) or carries a separator (`C:\a\b.png`, `./a.png`). A
 *  bare `shot.png` inside prose is a mention, not a path to fetch. */
const ANCHORED_RE = /^(?:\/|~|[^\s]*[\\/])/
const PATH_KEYS = ['file_path', 'path', 'filePath', 'notebook_path', 'image_path']

/** The picture a tool row is about, if it is about one.
 *
 *  A `Read` of a screenshot carries the path in its input and nothing else —
 *  the adapter drops the image bytes — so the path is the only handle the
 *  transcript has on the picture. Results are read too, because a capture tool
 *  answers "saved to /tmp/shot.png" with no path in the call, and because some
 *  adapters inline the bytes as a data URL (which must never be printed as
 *  base64). */
export function imageRefOf(block: AgentBlock | undefined): ImageRef | null {
  if (!block) return null
  if (block.ev === 'tool_use') {
    const input = block.input
    if (typeof input === 'string') return pathRef(input)
    if (!input || typeof input !== 'object') return null
    const m = input as Record<string, unknown>
    for (const k of PATH_KEYS) {
      const v = m[k]
      if (typeof v === 'string') {
        const ref = pathRef(v)
        if (ref) return ref
      }
    }
    return null
  }
  if (block.ev !== 'tool_result') return null
  const content = (block.content ?? '').trim()
  if (!content) return null
  if (DATA_URL_RE.test(content)) return { dataUrl: content.replace(/\s+/g, '') }
  // One line only: a result that merely *mentions* an image among a page of
  // output ("wrote shot.png, then 40 more files") should not turn the whole
  // row into a picture. The first image-shaped line wins.
  for (const line of content.split('\n').slice(0, 8)) {
    const ref = pathRef(line)
    if (ref) return ref
  }
  return null
}

/** `pathRef` accepts an image path — absolute, relative, or `~`. A whole line
 *  that is only a path is taken as-is; inside prose, only an *anchored* token
 *  counts, so "the shot.png file" never turns into a broken preview. Prose
 *  around a real path ("saved to /tmp/shot.png") does count. */
function pathRef(raw: string): ImageRef | null {
  const s = raw.trim().replace(/^["'`]+|["'`]+$/g, '')
  if (pathShaped(s)) return { path: s }
  for (const tok of s.split(/\s+/)) {
    const t = tok.replace(/^[("'`[]+/, '').replace(/[)"'`\],.;:]+$/, '')
    if (pathShaped(t) && ANCHORED_RE.test(t)) return { path: t }
  }
  return null
}

/** No whitespace (a sentence is not a path), no glob or shell metacharacters
 *  (`rm -f t*.png` is a command, not a picture), and an image extension. */
function pathShaped(s: string): boolean {
  return s.length > 0 && s.length <= 400 && IMAGE_EXT_RE.test(s) && PATH_SHAPE_RE.test(s)
}

/** The adapter's silence watchdog emits `no output for 45s — agent still
 * running` heartbeats. They are still-alive proof rather than work, and they
 * are English on the wire in every locale — so they are re-worded here instead
 * of being printed raw into a Chinese transcript. Returns null for any note
 * that is not a heartbeat. */
export function silenceNote(note: string | undefined): string | null {
  const m = /^no output for\s+(.+?)\s*(?:—|–|-)\s*agent still running\s*$/i.exec((note ?? '').trim())
  const raw = m?.[1] ?? ''
  if (!raw) return null
  const secs = parseShortDuration(raw)
  return t('queue.live.silent', { d: secs === null ? raw : fmtShortDuration(secs) })
}

/** `45s` / `1m41s` / `2h3m` — the Go short-duration shape the watchdog writes
 *  — into seconds. Null when the string holds no recognizable unit. */
function parseShortDuration(s: string): number | null {
  const re = /(\d+(?:\.\d+)?)(h|m|s|ms)/g
  let total = 0
  let hit = false
  for (let m = re.exec(s); m; m = re.exec(s)) {
    hit = true
    const n = Number(m[1])
    total += m[2] === 'h' ? n * 3600 : m[2] === 'm' ? n * 60 : m[2] === 'ms' ? n / 1000 : n
  }
  return hit ? Math.round(total) : null
}

/** Seconds into the units a heartbeat reads well in: 45s, 2m26s, 1h4m. The
 *  seconds stay: the watchdog kills a run at `timeouts.silence_s`, so "how
 *  long has it been quiet" is the number that matters, not a rounded one. */
function fmtShortDuration(sec: number): string {
  const su = t('time.unit.s')
  const mu = t('time.unit.m')
  const hu = t('time.unit.h')
  if (sec < 60) return `${sec}${su}`
  if (sec < 3600) {
    const m = Math.floor(sec / 60)
    const r = sec % 60
    return r ? `${m}${mu}${r}${su}` : `${m}${mu}`
  }
  const h = Math.floor(sec / 3600)
  const r = Math.floor((sec % 3600) / 60)
  return r ? `${h}${hu}${r}${mu}` : `${h}${hu}`
}

/** One transcript row's tree depth, computed in stream order: a tool_use * registers its id so later blocks carrying parent=<id> render nested
 * under it — the harness sub-agent tree (Claude Task) shows as a tree.
 * A parent outside the visible window (pagination, truncation) still
 * nests one level rather than pretending to be top-level work. */
export function blockDepth(block: AgentBlock, depths: Map<string, number>): number {
  let depth = 0
  if (block.parent) {
    depth = (depths.get(block.parent) ?? 0) + 1
  }
  if (block.ev === 'tool_use' && block.id) {
    depths.set(block.id, depth)
  }
  return Math.min(depth, 6)
}

const NOISE_KEYS = new Set(['candidates', 'score_breakdown'])

/** Parse thought string from reasoning payload */
export function extractThought(data: unknown): string {
  if (!data) return ''
  if (typeof data === 'string') {
    try {
      const parsed = JSON.parse(data)
      return extractThought(parsed)
    } catch {
      return data
    }
  }
  if (typeof data === 'object' && data !== null) {
    const obj = data as Record<string, unknown>
    if (typeof obj.thought === 'string') return obj.thought
    if (typeof obj.text === 'string') return obj.text
    if (typeof obj.reasoning === 'string') return obj.reasoning
  }
  return ''
}

export function parseEventData(raw?: string): Record<string, unknown> | null {
  if (!raw || raw === '{}' || raw === 'null') return null
  try {
    const parsed = JSON.parse(raw)
    if (typeof parsed === 'object' && parsed !== null) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // not valid JSON
  }
  return null
}

export function formatTaskEvent(type: string, rawData?: string): FormattedEvent {
  const data = parseEventData(rawData)
  const rawJson = rawData && rawData !== '{}' && rawData !== 'null' ? rawData : undefined
  const tags: { key: string; value: string }[] = []

  // Extract thought if this is a reasoning event
  if (type === 'reasoning') {
    const thought = extractThought(data || rawData)
    return {
      type,
      label: t('events.reasoning'),
      badgeClass: 'accent',
      thought: thought || rawData,
      tags: [],
      rawJson,
    }
  }

  // Structured agent-activity events (adapter protocol v2): the timeline
  // renders the block itself, so here we only unpack it and pick the badge.
  if (type === 'agent_event' && data) {
    const block: AgentBlock = {
      ev: String(data.ev ?? ''),
      id: str(data.id),
      parent: str(data.parent),
      name: str(data.name),
      toolUseId: str(data.tool_use_id),
      text: str(data.text),
      thinking: str(data.thinking),
      content: str(data.content),
      input: data.input,
      isError: Boolean(data.is_error),
    }
    let label = t('events.agent_event')
    let badgeClass: FormattedEvent['badgeClass'] = 'dim'
    switch (block.ev) {
      case 'text':
        label = t('events.agent_text')
        badgeClass = 'accent'
        break
      case 'thinking':
        label = t('events.agent_thinking')
        badgeClass = 'dim'
        break
      case 'tool_use':
        label = block.name ? `${t('events.agent_tool_use')}: ${block.name}` : t('events.agent_tool_use')
        badgeClass = 'info'
        break
      case 'tool_result':
        label = t('events.agent_tool_result')
        badgeClass = block.isError ? 'danger' : 'dim'
        break
      case 'transcript_truncated':
        label = t('events.transcript_truncated')
        badgeClass = 'warn'
        break
    }
    return { type, label, badgeClass, agent: block, tags, rawJson }
  }

  // Terminal payload: the task's result blob. The timeline renders it as a
  // compact result card, not a k/v dump — the full JSON stays one toggle away.
  if (type === 'result' && data) {
    const inner = data.result && typeof data.result === 'object'
      ? (data.result as Record<string, unknown>)
      : data
    const dropped = str(inner.dropped) || str(data.dropped)
    const ok = inner.ok !== false && !dropped
    const result = {
      ok,
      exitCode: typeof inner.exit_code === 'number' ? inner.exit_code : undefined,
      stdout: str(inner.stdout),
      stderr: str(inner.stderr),
    }
    // A failed run often reports `failed`/`error` and nothing else — no
    // stdout, no stderr. Without lifting that string out here the row's card
    // rendered empty: a badge, a timestamp and a lone raw-JSON toggle.
    const reason = str(inner.failed) || str(inner.error)
    const summary =
      str(inner.stderr) ||
      reason ||
      (dropped ? t('events.resultDropped', { reason: dropped }) : undefined) ||
      (result.stdout ? firstLine(result.stdout) : undefined)
    return {
      type,
      label: t('events.result'),
      badgeClass: ok ? 'accent' : 'danger',
      summary,
      tags: [],
      rawJson,
      result,
    }
  }

  // Session bookkeeping is machine glue, not activity — thin dim line.
  if (type === 'session_summary') {
    return { type, label: t('events.session_summary'), badgeClass: 'dim', tags: [], quiet: true }
  }

  // Dispatch plumbing (submit → queue → delegate → accept/decline): the
  // timeline folds a consecutive run into one pipeline row, so these carry a
  // short step label + the node they touched instead of a whole card.
  const lifecycleStep = LIFECYCLE_STEPS[type]
  if (lifecycleStep) {
    const node =
      str(data?.target) || str(data?.owner) || str(data?.node) || str(data?.to) || undefined
    return {
      type,
      label: t(lifecycleStep.labelKey),
      badgeClass: type === 'decline' ? 'danger' : 'dim',
      tags: [],
      rawJson,
      lifecycle: {
        step: t(lifecycleStep.stepKey),
        node,
        failed: type === 'decline',
      },
    }
  }

  // Lifecycle & trace events
  switch (type) {
    case 'classify_result': {
      const kind = String(data?.kind ?? '')
      const note = String(data?.note ?? '')
      return {
        type,
        label: t('events.classify_result'),
        badgeClass: 'info',
        summary: note || undefined,
        tags: kind ? [{ key: t('events.tag.type'), value: kind }] : [],
        rawJson,
      }
    }
    case 'route_decision': {
      const target = String(data?.target ?? '')
      const action = String(data?.action ?? '')
      const reason = String(data?.reason ?? '')
      if (target) tags.push({ key: t('events.tag.target'), value: target })
      if (action) tags.push({ key: t('events.tag.action'), value: action })
      return {
        type,
        label: t('events.route_decision'),
        badgeClass: 'accent',
        summary: reason || undefined,
        tags,
        rawJson,
      }
    }
    case 'skill_load': {
      if (data?.name) tags.push({ key: t('events.tag.skill'), value: String(data.name) })
      if (data?.path) tags.push({ key: t('events.tag.resource'), value: String(data.path) })
      if (data?.bytes !== undefined) tags.push({ key: t('events.tag.promptBytes'), value: String(data.bytes) + ' B' })
      return { type, label: t('events.skill_load'), badgeClass: 'info', tags, rawJson }
    }
    case 'prompt_context': {
      const mode = String(data?.mode ?? 'full')
      tags.push({ key: t('events.tag.contextMode'), value: t('events.context.' + mode, mode) })
      if (data?.bytes !== undefined) tags.push({ key: t('events.tag.promptBytes'), value: String(data.bytes) + ' / ' + String(data?.full_bytes ?? data.bytes) + ' B' })
      if (Array.isArray(data?.loaded_skills)) {
        const names = data.loaded_skills.map((s: { name?: string }) => s.name ?? '').filter(Boolean)
        if (names.length) tags.push({ key: t('events.tag.loadedSkills'), value: names.join(', ') })
      }
      return { type, label: t('events.prompt_context'), badgeClass: 'info', tags, rawJson }
    }
    case 'exec_agent_start': {
      const agent = String(data?.agent ?? '')
      const adapter = String(data?.adapter ?? '')
      if (agent) tags.push({ key: 'Agent', value: agent })
      if (adapter) tags.push({ key: t('events.tag.adapter'), value: adapter })
      if (data?.prompt_bytes !== undefined) tags.push({ key: t('events.tag.promptBytes'), value: String(data.prompt_bytes) + ' B' })
      if (Array.isArray(data?.loaded_skills)) {
        const names = data.loaded_skills.map((s: { name?: string }) => s.name ?? '').filter(Boolean)
        if (names.length) tags.push({ key: t('events.tag.loadedSkills'), value: names.join(', ') })
      }

      return {
        type,
        label: t('events.exec_agent_start'),
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'supervision_round': {
      const round = data?.round !== undefined ? String(data.round) : ''
      const verdict = String(data?.verdict ?? '')
      const summary = String(data?.judge_summary ?? data?.summary ?? '')
      if (round) tags.push({ key: t('events.tag.round'), value: round })
      if (verdict) tags.push({ key: t('events.tag.verdict'), value: verdict })
      return {
        type,
        label: t('events.supervision_round'),
        badgeClass: verdict === 'pass' || verdict === 'ok' ? 'accent' : 'warn',
        summary: summary || undefined,
        tags,
        rawJson,
      }
    }
    case 'judge_start': {
      const round = data?.round !== undefined ? String(data.round) : ''
      if (round) tags.push({ key: t('events.tag.round'), value: round })
      return {
        type,
        label: t('events.judge_start'),
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'tier2_triggered': {
      return {
        type,
        label: t('events.tier2_triggered'),
        badgeClass: 'warn',
        summary: t('events.tier2_summary'),
        tags,
        rawJson,
      }
    }
    case 'state_change': {
      const from = String(data?.from ?? '')
      const to = String(data?.to ?? '')
      if (from || to) tags.push({ key: t('events.tag.state'), value: `${from} ➔ ${to}` })
      return {
        type,
        label: t('events.state_change'),
        badgeClass: to === 'completed' ? 'accent' : to === 'failed' ? 'danger' : 'info',
        tags,
        rawJson,
      }
    }
    case 'task_complete':
    case 'completed': {
      return {
        type,
        label: t('events.task_complete'),
        badgeClass: 'accent',
        tags,
        rawJson,
      }
    }
    case 'task_failed':
    case 'failed': {
      const err = String(data?.error ?? '')
      return {
        type,
        label: t('events.task_failed'),
        badgeClass: 'danger',
        summary: err || undefined,
        tags,
        rawJson,
      }
    }
    case 'task_cancel':
    case 'cancelled': {
      return {
        type,
        label: t('events.task_cancel'),
        badgeClass: 'dim',
        tags,
        rawJson,
      }
    }
    case 'delegation_hop': {
      const from = String(data?.from ?? '')
      const to = String(data?.to ?? '')
      if (from || to) tags.push({ key: t('events.tag.hop'), value: `${from} ➔ ${to}` })
      return {
        type,
        label: t('events.delegation_hop'),
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'delegate_request': {
      // The fan-out milestone: one row per sub-agent the parent dispatched,
      // with its parallel mode — the "parent orchestrating N children" the
      // board otherwise has to reconstruct from separate cards.
      const title = String(data?.title ?? '') || String(data?.child ?? '')
      if (title) tags.push({ key: t('events.tag.child'), value: title })
      if (data?.isolate === true) {
        tags.push({ key: t('events.tag.mode'), value: t('events.mode.isolated') })
      } else {
        const paths = Array.isArray(data?.paths) ? (data.paths as unknown[]).map(String) : []
        if (paths.length > 0) tags.push({ key: t('events.tag.mode'), value: paths.join(', ') })
      }
      return {
        type,
        label: t('events.delegate_request'),
        badgeClass: 'accent',
        tags,
        rawJson,
      }
    }
    case 'isolated_workspace': {
      // The isolated child's private copy was created — the merge that
      // follows lands here when it finishes.
      const workspace = String(data?.workspace ?? '')
      if (workspace) tags.push({ key: t('events.tag.path'), value: workspace })
      return {
        type,
        label: t('events.isolated_workspace'),
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'isolated_merge': {
      // The three-way merge outcome: applied count, and the paths that both
      // sides changed (kept with the project's bytes, workspace retained).
      const merged = typeof data?.merged === 'number' ? String(data.merged) : ''
      const conflicts = Array.isArray(data?.conflicts) ? (data.conflicts as unknown[]).map(String) : []
      if (merged) tags.push({ key: t('events.tag.merged'), value: merged })
      if (conflicts.length > 0) {
        tags.push({ key: t('events.tag.conflicts'), value: conflicts.slice(0, 3).join(', ') })
      }
      return {
        type,
        label: t('events.isolated_merge'),
        badgeClass: conflicts.length > 0 ? 'danger' : 'accent',
        tags,
        rawJson,
      }
    }
    case 'project_sync': {
      const path = String(data?.path ?? '')
      if (path) tags.push({ key: t('events.tag.path'), value: path })
      return {
        type,
        label: t('events.project_sync'),
        badgeClass: 'accent',
        tags,
        rawJson,
      }
    }
    case 'supervise': {
      const round = data?.round !== undefined ? String(data.round) : ''
      const status = String(data?.status ?? '')
      const reason = String(data?.reason ?? '')
      const followup = String(data?.followup ?? '')
      const recovery = data?.phase === 'failure_recovery'
      if (round) tags.push({ key: t('events.tag.round'), value: round })
      if (status) tags.push({ key: t('events.tag.status'), value: status })
      return {
        type,
        label: t(recovery ? 'events.failureRecovery' : 'events.supervise'),
        badgeClass: status === 'done' || status === 'pass' || status === 'ok' ? 'accent' : 'warn',
        summary: recovery ? [data?.error, reason, followup].filter(Boolean).join('\n\n') || undefined
          : reason || followup || undefined,
        tags,
        rawJson,
      }
    }
    case 'progress': {
      const note = String(data?.note ?? data?.msg ?? data?.message ?? '')
      const pct = data?.pct ?? data?.percent
      if (pct !== undefined && pct !== null) {
        tags.push({ key: t('events.tag.progress'), value: `${pct}%` })
      }
      // The adapter's silence watchdog emits "no output for Ns" heartbeats —
      // still-alive proof, not work. They render as thin dim lines (and the
      // row builder folds a run of them into one) instead of five badge
      // cards shouting that nothing happened.
      const silence = silenceNote(note)
      return {
        type,
        label: silence ?? t('events.progress'),
        badgeClass: 'info',
        summary: silence ? undefined : note || undefined,
        tags,
        rawJson,
        quiet: Boolean(silence),
      }
    }
    case 'review': {
      const approved = data?.approved === true
      const rejected = String(data?.rejected ?? '')
      const resumed = data?.resumed === true
      if (approved) tags.push({ key: t('events.tag.approved'), value: t('events.tag.yes') })
      if (resumed) tags.push({ key: t('events.tag.resumed'), value: t('events.tag.yes') })
      return {
        type,
        label: approved ? t('events.reviewApproved') : rejected ? t('events.reviewRejected') : t('events.review'),
        badgeClass: approved ? 'accent' : rejected ? 'danger' : 'warn',
        summary: rejected || undefined,
        tags,
        rawJson,
      }
    }
    case 'retry': {
      return {
        type,
        label: t('events.retry'),
        badgeClass: 'warn',
        summary: str(data?.reason) || str(data?.error) || undefined,
        tags,
        rawJson,
      }
    }
    case 'expire':
    case 'expired': {
      return {
        type,
        label: t('events.expired'),
        badgeClass: 'dim',
        summary: str(data?.reason) || undefined,
        tags,
        rawJson,
      }
    }
    case 'cancel': {
      return {
        type,
        label: t('events.task_cancel'),
        badgeClass: 'dim',
        summary: str(data?.reason) || undefined,
        tags,
        rawJson,
      }
    }
    case 'recover': {
      return {
        type,
        label: t('events.recovered'),
        badgeClass: 'info',
        summary: str(data?.reason) || undefined,
        tags,
        rawJson,
      }
    }
    case 'clarification': {
      return {
        type,
        label: t('events.clarification'),
        badgeClass: 'warn',
        summary: str(data?.question) || str(data?.prompt) || str(data?.note) || undefined,
        tags,
        rawJson,
      }
    }
    case 'context_degraded': {
      const level = str(data?.level) || str(data?.stage)
      if (level) tags.push({ key: t('events.tag.level'), value: level })
      return {
        type,
        label: t('events.context_degraded'),
        badgeClass: 'warn',
        summary: str(data?.note) || str(data?.reason) || undefined,
        tags,
        rawJson,
      }
    }
    case 'context_overflow': {
      // action=compact_retry is recovery in progress (info); action=parked
      // or a bare event is the terminal state a human must resolve (warn).
      const action = str(data?.action)
      return {
        type,
        label: t('events.context_overflow'),
        badgeClass: action === 'compact_retry' ? 'info' : 'warn',
        summary: str(data?.note) || str(data?.reason) || (action === 'compact_retry' ? 'compact retry' : undefined),
        tags,
        rawJson,
      }
    }
    case 'agent_fallback': {
      const from = str(data?.from)
      const to = str(data?.to)
      if (from || to) tags.push({ key: t('events.tag.agent'), value: `${from ?? '?'} ➔ ${to ?? '?'}` })
      return {
        type,
        label: t('events.agent_fallback'),
        badgeClass: 'warn',
        summary: str(data?.reason) || str(data?.error) || undefined,
        tags,
        rawJson,
      }
    }
    case 'route_fallback': {
      return {
        type,
        label: t('events.route_fallback'),
        badgeClass: 'warn',
        summary: str(data?.reason) || str(data?.note) || undefined,
        tags,
        rawJson,
      }
    }
    case 'agent_usage': {
      const agent = str(data?.agent)
      // The recorder emits input/output/cache token counts, never a total —
      // sum what the wire actually carries.
      const num = (k: string) => (typeof data?.[k] === 'number' ? (data[k] as number) : 0)
      const tokens = num('input_tokens') + num('output_tokens') +
        num('cache_read_tokens') + num('cache_write_tokens') + num('tokens')
      const cost = num('cost') || num('cost_usd')
      if (agent) tags.push({ key: t('events.tag.agent'), value: agent })
      if (tokens > 0) {
        tags.push({ key: t('events.tag.tokens'), value: String(tokens) })
      }
      if (cost > 0) {
        tags.push({ key: t('events.tag.cost'), value: `$${cost.toFixed(4)}` })
      }
      return { type, label: t('events.agent_usage'), badgeClass: 'dim', tags, rawJson }
    }
    case 'memory_promotion': {
      return {
        type,
        label: t('events.memory_promotion'),
        badgeClass: 'dim',
        summary: str(data?.note) || str(data?.summary) || undefined,
        tags,
        rawJson,
      }
    }
    case 'model_injection': {
      // The event carries what the notice prints, in structured form: which
      // agent got which model, from which endpoint, and why. That is what
      // lets the one-line notice stay one line — the detail lives here.
      const agent = str(data?.agent)
      const model = str(data?.model)
      const endpoint = str(data?.base_url)
      const kind = str(data?.kind)
      if (agent) tags.push({ key: t('events.tag.agent'), value: agent })
      if (model) tags.push({ key: t('events.tag.model'), value: model })
      if (kind) tags.push({ key: t('events.tag.mode'), value: kind })
      return {
        type,
        label: t('events.model_injection'),
        badgeClass: 'info',
        summary: endpoint ? endpoint.replace(/^https?:\/\//, '').replace(/\/$/, '') : str(data?.reason) || undefined,
        tags,
        rawJson,
      }
    }
    case 'user_msg': {
      // A note the user sent into a running or queued task. It used to fall
      // through to the unknown-event branch and render as the bare string
      // "user_msg" — visible only if you knew what to look for, which is the
      // same as invisible.
      const mode = str(data?.mode)
      if (mode) tags.push({ key: t('events.tag.mode'), value: mode })
      return {
        type,
        label: t('events.user_msg'),
        badgeClass: mode === 'inject' ? 'info' : 'dim',
        summary: str(data?.text) || undefined,
        tags,
        rawJson,
      }
    }
    case 'user_msg_consumed': {
      return {
        type,
        label: t('events.user_msg_consumed'),
        badgeClass: 'accent',
        summary: str(data?.text) || undefined,
        tags,
        rawJson,
      }
    }
    case 'plan_stage_changed': {
      const stage = str(data?.stage_title) || str(data?.stage_label) || str(data?.stage_id)
      const transition = str(data?.transition)
      if (stage) tags.push({ key: t('events.tag.stage'), value: stage })
      if (transition) tags.push({ key: t('events.tag.status'), value: transition })
      return {
        type,
        label: t('events.plan_stage'),
        badgeClass: transition === 'completed' ? 'accent' : 'info',
        tags,
        rawJson,
      }
    }
    case 'subagent_event': {
      const name = str(data?.name) || str(data?.agent)
      if (name) tags.push({ key: t('events.tag.agent'), value: name })
      return {
        type,
        label: t('events.subagent_event'),
        badgeClass: 'dim',
        summary: str(data?.note) || str(data?.summary) || undefined,
        tags,
        rawJson,
      }
    }
    case 'artifact_transfer':
    case 'transfer': {
      const artifact = str(data?.artifact) || str(data?.name) || str(data?.path)
      const dir = str(data?.direction) || (data?.to ? `➔ ${String(data.to)}` : '')
      if (artifact) tags.push({ key: t('events.tag.artifact'), value: artifact })
      if (dir) tags.push({ key: t('events.tag.direction'), value: dir })
      return {
        type,
        label: t('events.transfer'),
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'accept_failed': {
      return {
        type,
        label: t('events.accept_failed'),
        badgeClass: 'danger',
        summary: str(data?.reason) || str(data?.error) || undefined,
        tags,
        rawJson,
      }
    }
    case 'approve': {
      return { type, label: t('events.reviewApproved'), badgeClass: 'accent', tags, rawJson }
    }
  }

  // Fallback for custom or unrecognized events: a few short scalar hints,
  // never the whole payload — the raw JSON toggle stays one click away.
  if (data) {
    for (const [k, v] of Object.entries(data)) {
      if (tags.length >= 3) break
      if (NOISE_KEYS.has(k)) continue
      if (v === null || v === undefined || v === '' || v === false || v === 0) continue
      if (typeof v === 'object') {
        const s = JSON.stringify(v)
        if (s !== '{}' && s !== '[]') {
          tags.push({ key: k, value: s.length > 50 ? s.slice(0, 50) + '…' : s })
        }
      } else {
        const s = String(v)
        tags.push({ key: k, value: s.length > 80 ? s.slice(0, 80) + '…' : s })
      }
    }
  }

  return {
    type,
    label: type,
    badgeClass: 'dim',
    tags,
    rawJson,
  }
}

/** Internal plumbing events the timeline folds into one pipeline row rather
 *  than a card each. stepKey is the short chip label; labelKey is the a11y
 *  name used if the row is ever read out on its own. */
const LIFECYCLE_STEPS: Record<string, { stepKey: string; labelKey: string }> = {
  submit: { stepKey: 'events.lifecycle.submit', labelKey: 'events.submit' },
  queue: { stepKey: 'events.lifecycle.queued', labelKey: 'events.queued' },
  dispatch: { stepKey: 'events.lifecycle.dispatched', labelKey: 'events.dispatched' },
  delegate: { stepKey: 'events.lifecycle.delegated', labelKey: 'events.delegated' },
  accept: { stepKey: 'events.lifecycle.accepted', labelKey: 'events.accepted' },
  decline: { stepKey: 'events.lifecycle.declined', labelKey: 'events.declined' },
}

/** First non-empty line of a longer text — used as a summary under cards. */
function firstLine(s: string): string {
  const line = s.split('\n').find((l) => l.trim() !== '') ?? ''
  return line.length > 140 ? line.slice(0, 140) + '…' : line
}
