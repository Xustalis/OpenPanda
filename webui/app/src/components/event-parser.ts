// Parses and formats task events for human-readable display.
// Replaces raw JSON walls with clear structured information and formats
// Chain-of-Thought (reasoning) into readable thinking blocks.

export interface FormattedEvent {
  type: string
  label: string
  badgeClass: 'accent' | 'info' | 'warn' | 'danger' | 'dim'
  summary?: string
  thought?: string
  tags: { key: string; value: string }[]
  rawJson?: string
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
      label: '模型思考过程',
      badgeClass: 'accent',
      thought: thought || rawData,
      tags: [],
      rawJson,
    }
  }

  // Lifecycle & trace events
  switch (type) {
    case 'classify_result': {
      const kind = String(data?.kind ?? '')
      const note = String(data?.note ?? '')
      return {
        type,
        label: '意图识别',
        badgeClass: 'info',
        summary: note || undefined,
        tags: kind ? [{ key: '类型', value: kind }] : [],
        rawJson,
      }
    }
    case 'route_decision': {
      const target = String(data?.target ?? '')
      const action = String(data?.action ?? '')
      const reason = String(data?.reason ?? '')
      if (target) tags.push({ key: '目标节点', value: target })
      if (action) tags.push({ key: '动作', value: action })
      return {
        type,
        label: '路由决策',
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
      if (adapter) tags.push({ key: '适配器', value: adapter })
      return {
        type,
        label: 'Agent 执行启动',
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'supervision_round': {
      const round = data?.round !== undefined ? String(data.round) : ''
      const verdict = String(data?.verdict ?? '')
      const summary = String(data?.judge_summary ?? data?.summary ?? '')
      if (round) tags.push({ key: '轮次', value: round })
      if (verdict) tags.push({ key: '裁决', value: verdict })
      return {
        type,
        label: '结果监督评估',
        badgeClass: verdict === 'pass' || verdict === 'ok' ? 'accent' : 'warn',
        summary: summary || undefined,
        tags,
        rawJson,
      }
    }
    case 'judge_start': {
      const round = data?.round !== undefined ? String(data.round) : ''
      if (round) tags.push({ key: '轮次', value: round })
      return {
        type,
        label: '监督评审开始',
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'tier2_triggered': {
      return {
        type,
        label: '需要审批',
        badgeClass: 'warn',
        summary: '任务涉及高风险操作，正在等待人工授权',
        tags,
        rawJson,
      }
    }
    case 'state_change': {
      const from = String(data?.from ?? '')
      const to = String(data?.to ?? '')
      if (from || to) tags.push({ key: '状态', value: `${from} ➔ ${to}` })
      return {
        type,
        label: '状态流转',
        badgeClass: to === 'completed' ? 'accent' : to === 'failed' ? 'danger' : 'info',
        tags,
        rawJson,
      }
    }
    case 'task_complete':
    case 'completed': {
      return {
        type,
        label: '任务完成',
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
        label: '任务失败',
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
        label: '任务取消',
        badgeClass: 'dim',
        tags,
        rawJson,
      }
    }
    case 'delegation_hop': {
      const from = String(data?.from ?? '')
      const to = String(data?.to ?? '')
      if (from || to) tags.push({ key: '跳点', value: `${from} ➔ ${to}` })
      return {
        type,
        label: '跨设备委派',
        badgeClass: 'info',
        tags,
        rawJson,
      }
    }
    case 'project_sync': {
      const path = String(data?.path ?? '')
      if (path) tags.push({ key: '路径', value: path })
      return {
        type,
        label: '项目文件同步',
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
