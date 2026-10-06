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
 * adapter's progress note uses. */
export function toolArgSummary(input: unknown): string {
  if (typeof input === 'string') return input
  if (!input || typeof input !== 'object') return ''
  const m = input as Record<string, unknown>
  for (const k of ['command', 'file_path', 'pattern', 'path', 'url', 'query', 'description']) {
    const v = m[k]
    if (typeof v === 'string' && v) return v
  }
  return ''
}

/** One transcript row's tree depth, computed in stream order: a tool_use
 * registers its id so later blocks carrying parent=<id> render nested
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
    case 'exec_agent_start': {
      const agent = String(data?.agent ?? '')
      const adapter = String(data?.adapter ?? '')
      if (agent) tags.push({ key: 'Agent', value: agent })
      if (adapter) tags.push({ key: t('events.tag.adapter'), value: adapter })
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
  }

  // Fallback for custom or unrecognized events: extract clean key-values without noise
  if (data) {
    for (const [k, v] of Object.entries(data)) {
      if (NOISE_KEYS.has(k)) continue
      if (v === null || v === undefined || v === '' || v === false || v === 0) continue
      if (typeof v === 'object') {
        const s = JSON.stringify(v)
        if (s !== '{}' && s !== '[]') {
          tags.push({ key: k, value: s.length > 50 ? s.slice(0, 50) + '…' : s })
        }
      } else {
        tags.push({ key: k, value: String(v) })
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
