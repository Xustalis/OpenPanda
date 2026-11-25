// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AgentInfo } from '../api/client.ts'
import { agentRoles, type AgentRole } from '../format/agent-role.ts'

/** An `@` token whose query contains path punctuation is a filesystem path, not
 *  an agent name, so agent completions stay quiet while typing `@src/main` or
 *  `@main.go`. */
export function isFilePathAtToken(token: string): boolean {
  return /[\\/.]/.test(token)
}

/** Whether `@` completion should offer roster agents at the current caret.
 *  Suppressed while typing a filesystem path or an argument to a non-prompt
 *  slash command (`/read`, `/new`, `/cost`, etc.). Prompt modes (`/plan`,
 *  `/goal`) still allow agent mentions. */
export function shouldOfferAgentCompletion(
  input: string,
  caret: number,
  atTok: { start: number; token: string } | null,
): boolean {
  if (!atTok) return false
  if (isFilePathAtToken(atTok.token)) return false
  const lineHead = input.slice(0, caret).trimStart()
  const cmdMatch = /^\/(\S+)\s+/.exec(lineHead)
  if (cmdMatch) {
    const cmd = cmdMatch[1]?.toLowerCase() ?? ''
    return cmd === 'plan' || cmd === 'goal'
  }
  return true
}

/** Filter and sort roster agents for `@` completion:
 *  1. Installed agents always sort ahead of uninstalled ones.
 *  2. Prefix matches on `name` or `display_name` sort ahead of mid-string matches.
 *  3. Agents already on the thread's roster sort ahead of unselected ones. */
export function filterAndSortAgents(
  agents: readonly AgentInfo[],
  query: string,
  team: readonly string[],
): AgentInfo[] {
  const q = query.trim().toLowerCase()
  const teamSet = new Set(team)
  const indexed = agents
    .map((agent, index) => ({ agent, index }))
    .filter(({ agent }) => {
      if (!q) return true
      const name = agent.name.toLowerCase()
      const display = (agent.display_name ?? '').toLowerCase()
      return name.includes(q) || display.includes(q)
    })

  indexed.sort((a, b) => {
    const instDiff = (a.agent.installed ? 0 : 1) - (b.agent.installed ? 0 : 1)
    if (instDiff !== 0) return instDiff

    if (q) {
      const aPrefix =
        a.agent.name.toLowerCase().startsWith(q) ||
        (a.agent.display_name ?? '').toLowerCase().startsWith(q)
      const bPrefix =
        b.agent.name.toLowerCase().startsWith(q) ||
        (b.agent.display_name ?? '').toLowerCase().startsWith(q)
      if (aPrefix !== bPrefix) return aPrefix ? -1 : 1
    }

    const teamDiff = (teamSet.has(a.agent.name) ? 0 : 1) - (teamSet.has(b.agent.name) ? 0 : 1)
    if (teamDiff !== 0) return teamDiff

    return a.index - b.index
  })

  return indexed.map(({ agent }) => agent)
}

export interface AgentHintLabels {
  onDuty: string
  notInstalled: string
  roleLabel(role: AgentRole): string
}

/** Format the right-hand hint for an agent completion row: display name (when
 *  different from ID), up to two role hints, and install status. */
export function formatAgentCompletionHint(agent: AgentInfo, labels: AgentHintLabels): string {
  const parts: string[] = []
  if (agent.display_name && agent.display_name !== agent.name) {
    parts.push(agent.display_name)
  }
  const roles = agentRoles(agent.best_at)
    .slice(0, 2)
    .map((r) => labels.roleLabel(r))
  if (roles.length > 0) {
    parts.push(roles.join(' · '))
  } else if (agent.installed && agent.binary && agent.binary !== agent.name) {
    parts.push(agent.binary)
  }
  parts.push(agent.installed ? labels.onDuty : labels.notInstalled)
  return parts.join(' · ')
}

export interface AgentMentionSpan {
  name: string
  start: number
  end: number
  terminated: boolean
}

const MENTION_BOUNDARY_AFTER = /^[\s.,;:!?)]/

/** Locate every `@<agent>` mention in `text` whose identifier matches
 *  `knownAgents` (excluding email-like `a@codex` and path-like `@codex/foo`). */
export function findAgentMentionSpans(
  text: string,
  knownAgents: readonly string[],
): AgentMentionSpan[] {
  if (knownAgents.length === 0 || !text.includes('@')) return []
  const knownMap = new Map<string, string>()
  for (const name of knownAgents) knownMap.set(name, name)

  const spans: AgentMentionSpan[] = []
  const re = /(^|\s)@([^\s@/\\.,;:!?)]+)/g
  let match: RegExpExecArray | null
  while ((match = re.exec(text)) !== null) {
    const prefix = match[1] ?? ''
    const raw = match[2] ?? ''
    const canonical = knownMap.get(raw)
    if (!canonical) continue
    const start = match.index + prefix.length
    const end = start + 1 + raw.length
    const after = text.slice(end)
    if (after !== '' && !MENTION_BOUNDARY_AFTER.test(after)) continue
    spans.push({ name: canonical, start, end, terminated: after !== '' })
  }
  return spans
}

/** Unique canonical agent names mentioned in `text`, in order of appearance. */
export function extractAgentMentions(text: string, knownAgents: readonly string[]): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const span of findAgentMentionSpans(text, knownAgents)) {
    if (seen.has(span.name)) continue
    seen.add(span.name)
    out.push(span.name)
  }
  return out
}

/** Reconcile the thread's team roster when the composer draft changes.
 *  - Removing an `@agent` mention that was present in `prevText` drops it from
 *    the roster (without disturbing agents inherited from earlier turns that
 *    were never in `prevText`).
 *  - Completing an `@agent` mention with trailing whitespace/punctuation adds it
 *    when that agent is installed. */
export function syncTeamFromInput(
  prevText: string,
  nextText: string,
  currentTeam: readonly string[],
  knownAgents: readonly string[],
  installedAgents: readonly string[],
): string[] {
  const prevMentions = new Set(extractAgentMentions(prevText, knownAgents))
  const nextSpans = findAgentMentionSpans(nextText, knownAgents)
  const nextMentions = new Set(nextSpans.map((s) => s.name))
  const installedSet = new Set(installedAgents)

  const nextTeam: string[] = []
  for (const member of currentTeam) {
    if (prevMentions.has(member) && !nextMentions.has(member)) continue
    if (!nextTeam.includes(member)) nextTeam.push(member)
  }
  for (const span of nextSpans) {
    if (!span.terminated || !installedSet.has(span.name)) continue
    if (!nextTeam.includes(span.name)) nextTeam.push(span.name)
  }
  return nextTeam
}

/** Ensure any installed agent `@mention` still in the prompt at send time
 *  (even without a trailing space) is included in the dispatched roster. */
export function mergeMentionsIntoTeam(
  currentTeam: readonly string[],
  promptText: string,
  installedAgents: readonly string[],
): string[] {
  const installedSet = new Set(installedAgents)
  const out = [...currentTeam]
  for (const name of extractAgentMentions(promptText, installedAgents)) {
    if (installedSet.has(name) && !out.includes(name)) out.push(name)
  }
  return out
}

/** Compute editor token deletions (ordered end-to-start so offsets remain valid)
 *  and appended mention tokens when the user updates the team via the card. */
export function planTeamMentionEdits(
  text: string,
  prevTeam: readonly string[],
  nextTeam: readonly string[],
  knownAgents: readonly string[],
): {
  removals: Array<{ start: number; end: number }>
  additions: string[]
} {
  const prevSet = new Set(prevTeam)
  const nextSet = new Set(nextTeam)
  const removedNames = new Set(prevTeam.filter((n) => !nextSet.has(n)))
  const addedNames = nextTeam.filter((n) => !prevSet.has(n))

  const spans = findAgentMentionSpans(text, knownAgents)
  const removals: Array<{ start: number; end: number }> = []
  for (let i = spans.length - 1; i >= 0; i--) {
    const span = spans[i]!
    if (!removedNames.has(span.name)) continue
    let start = span.start
    let end = span.end
    if (text[end] === ' ') end += 1
    else if (start > 0 && text[start - 1] === ' ') start -= 1
    removals.push({ start, end })
  }

  const existing = new Set(spans.map((s) => s.name))
  const additions = addedNames.filter((n) => !existing.has(n)).map((n) => `@${n}`)
  return { removals, additions }
}

/** Pure string counterpart of `planTeamMentionEdits` for tests and non-DOM
 *  callers. */
export function syncInputWithTeamChange(
  text: string,
  prevTeam: readonly string[],
  nextTeam: readonly string[],
  knownAgents: readonly string[],
): string {
  const { removals, additions } = planTeamMentionEdits(text, prevTeam, nextTeam, knownAgents)
  let next = text
  for (const r of removals) {
    next = next.slice(0, r.start) + next.slice(r.end)
  }
  if (removals.length > 0 && !next.trim()) next = ''
  if (additions.length > 0) {
    const chunk = additions.map((t) => `${t} `).join('')
    if (!next) next = chunk
    else if (/\s$/.test(next)) next = next + chunk
    else next = `${next} ${chunk}`
  }
  return next
}

/** Step to the next non-disabled completion row when navigating with
 *  ArrowUp/ArrowDown; returns `current` if every row is disabled. */
export function stepEnabledIndex(
  items: readonly { disabled?: boolean }[],
  current: number,
  delta: 1 | -1,
): number {
  const len = items.length
  if (len === 0) return 0
  for (let step = 1; step <= len; step++) {
    const idx = (current + delta * step + len * 2) % len
    if (!items[idx]?.disabled) return idx
  }
  return current
}

/** Pick the active index when the menu list changes: keep `cursor` if it
 *  points to an enabled item, otherwise fall back to the first enabled item. */
export function clampEnabledIndex(
  items: readonly { disabled?: boolean }[],
  cursor: number,
): number {
  if (items.length === 0) return 0
  const clamped = Math.min(Math.max(cursor, 0), items.length - 1)
  if (!items[clamped]?.disabled) return clamped
  const firstEnabled = items.findIndex((item) => !item.disabled)
  return firstEnabled >= 0 ? firstEnabled : clamped
}
