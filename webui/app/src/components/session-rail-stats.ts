// SPDX-License-Identifier: AGPL-3.0-or-later

/** The numbers the session rail prints, kept out of the view so they can be
 *  tested without a DOM.
 *
 *  Two of them are not in the payload the panel sends:
 *
 *  - Per-file `+n −m`. `/api/sessions/{id}/diff` carries a unified patch and
 *    an aggregate stat, not per-file counts (see webui/panel/sessions.go,
 *    `sessionDiff`). The rail wants the count beside the file that got the
 *    edit — that is the whole reason a change list is more useful than a
 *    number — so it is read back off the patch the diff drawer already
 *    holds. No extra request, and no second source of truth.
 *  - The context share. The panel reports tokens per turn; the window size
 *    lives on the model row. The percentage is their quotient, clamped.
 *
 *  Both parsers are deliberately conservative: a number the rail cannot
 *  stand behind is left out rather than guessed. */

import { compactNumber } from '../format/number.ts'

export interface PatchFileStat {
  insertions: number
  deletions: number
}

/** The marker `Worktrees.Diff` appends when it cuts the patch at 128 KiB. */
const TRUNCATED = '… (diff truncated)'

/** Git prints a path either bare (`+++ b/src/a.ts`) or C-quoted when it holds
 *  a space or a control character (`+++ "b/src/a b.ts"`). Undo the quoting. */
function unquote(raw: string): string {
  let p = raw.trim()
  if (p.length > 1 && p.startsWith('"') && p.endsWith('"')) {
    p = p
      .slice(1, -1)
      .replace(/\\(["\\])/g, '$1')
      .replace(/\\t/g, '\t')
      .replace(/\\n/g, '\n')
  }
  return p
}

/** A patch header's path, without the synthetic `a/` or `b/` side. */
function cleanPath(raw: string): string {
  const p = unquote(raw)
  return p.startsWith('a/') || p.startsWith('b/') ? p.slice(2) : p
}

/** A `git status --porcelain` path split into its two sides. A rename or copy
 *  prints `old -> new`; everything else has the same path on both sides.
 *  Deliberately does NOT strip a leading `a/` or `b/` — unlike a patch header,
 *  a status path is the real repo-relative one, and a file may legitimately
 *  live under `a/`. */
export interface PorcelainPath {
  from: string
  to: string
}

export function porcelainParts(raw: string): PorcelainPath {
  const p = raw.trim()
  const arrow = p.lastIndexOf(' -> ')
  if (arrow < 0) {
    const one = unquote(p)
    return { from: one, to: one }
  }
  return { from: unquote(p.slice(0, arrow)), to: unquote(p.slice(arrow + 4)) }
}

/** The destination side — the path the file has now, and the one a patch
 *  header names. */
export function porcelainPath(raw: string): string {
  return porcelainParts(raw).to
}

/** One file's block in a unified diff, header lines included. */
interface PatchSection {
  path: string
  lines: string[]
}

/** Split a unified diff into one block per file, named the way `git status`
 *  names it (the `+++` side, falling back to `---` for a deletion).
 *
 *  Only the `+++`/`---` pair *before* the first `@@` names a file: inside a
 *  hunk an added line can itself start with `++`, and reading that as a
 *  header would silently split the file in two. */
function patchSections(patch: string): PatchSection[] {
  const out: PatchSection[] = []
  let cur: PatchSection | null = null
  let inHunk = false
  let named = false
  for (const line of patch.split('\n')) {
    if (line.startsWith('diff --git ')) {
      if (cur && cur.path) out.push(cur)
      cur = { path: '', lines: [line] }
      inHunk = false
      named = false
      continue
    }
    if (!cur) continue
    cur.lines.push(line)
    if (inHunk) continue
    if (line.startsWith('+++ ')) {
      const p = cleanPath(line.slice(4))
      // A deletion's target side is /dev/null; the `---` line named it.
      if (p !== '/dev/null') {
        cur.path = p
        named = true
      }
    } else if (line.startsWith('--- ') && !named) {
      const p = cleanPath(line.slice(4))
      if (p !== '/dev/null') cur.path = p
    } else if (line.startsWith('@@')) {
      inHunk = true
    }
  }
  if (cur && cur.path) out.push(cur)
  return out
}

/** Path → line counts, for every file the patch has a hunk for.
 *
 *  The block the cap cut through is dropped. Its count is a partial one, and
 *  a partial `+12` under a diff that stopped at 128 KiB reads as the file's
 *  real size — worse than showing no number at all. */
export function patchFileStats(patch: string): Map<string, PatchFileStat> {
  const out = new Map<string, PatchFileStat>()
  if (!patch) return out
  const sections = patchSections(patch)
  const cut = patch.includes(TRUNCATED)
  sections.forEach((section, index) => {
    if (cut && index === sections.length - 1) return
    let hasHunk = false
    const stat: PatchFileStat = { insertions: 0, deletions: 0 }
    for (const line of section.lines) {
      if (line.startsWith('@@')) {
        hasHunk = true
        continue
      }
      if (!hasHunk) continue
      if (line.startsWith('+')) stat.insertions++
      else if (line.startsWith('-')) stat.deletions++
    }
    if (hasHunk) out.set(section.path, stat)
  })
  return out
}

export interface FilePatch {
  /** The file's own block, header lines included. Empty when the cap cut
   *  through it — a half hunk reads as a complete one. */
  patch: string
  /** This was the block the 128 KiB cap cut through. */
  truncated: boolean
}

/** One file's diff out of the session's whole patch, or null when the patch
 *  has no block for that path (binary, mode-only change, or a patch that
 *  stopped before reaching it). */
export function patchForFile(patch: string, path: string): FilePatch | null {
  const sections = patchSections(patch)
  const at = sections.findIndex((s) => s.path === path)
  if (at < 0) return null
  const cut = patch.includes(TRUNCATED) && at === sections.length - 1
  return { patch: cut ? '' : sections[at]!.lines.join('\n'), truncated: cut }
}

/** The first new-side line a patch touches, so "open the file" lands on the
 *  change instead of the top of a 600-line file. 0 when the block has no hunk
 *  (binary, rename, mode change). */
export function firstChangedLine(patch: string): number {
  const m = /^@@ -\d+(?:,\d+)? \+(\d+)/m.exec(patch)
  return m ? Number(m[1]) : 0
}

/** How full the context window is, in whole percent, or null when the window
 *  is unknown (a model row with no catalogue entry and no explicit limit).
 *  Clamped: a provider that bills past its advertised window must not render
 *  a bar wider than its track. */
export function contextShare(used: number, window: number): number | null {
  if (!Number.isFinite(used) || !Number.isFinite(window)) return null
  if (window <= 0 || used <= 0) return null
  return Math.min(100, Math.round((used / window) * 100))
}

/** The `used / window` caption the context rows print. A window the catalogue
 *  does not know drops the denominator, not the count. */
export function contextUsage(tokens: number, window: number): string {
  const used = tokens > 0 ? compactNumber(tokens) : '—'
  const total = window > 0 ? compactNumber(window) : ''
  return total ? `${used} / ${total}` : tokens > 0 ? used : ''
}

/** The subset of a task row the context selector reads. Declared here so the
 *  rule stays testable without dragging the API client into a DOM test. */
export interface AgentContextTask {
  agent?: string
  state?: string
  context_tokens?: number
  context_model?: string
  context_window?: number
}

/** One agent's context reading, ready for the rail and the dock. */
export interface AgentContextSample {
  /** The adapter key the sample belongs to ("claude_code"). */
  agent: string
  /** Prompt tokens of that agent's newest model request. */
  tokens: number
  /** The window the catalogue resolved for the model, 0 = unknown. */
  window: number
  /** The model id the adapter reported, "" when it names none. */
  model: string
  /** The task is still running, as opposed to the last one that ran. */
  live: boolean
}

/** One agent's reading, as the rail and the dock print it. */
export interface ContextAgentView {
  /** The agent's display name ("Claude Code"), never the adapter key. */
  name: string
  /** Prompt tokens of that agent's newest model request. */
  tokens: number
  /** The window the catalogue resolved for the model, 0 = unknown. */
  window: number
}

/** States in which a task still owns the thread's work. Mirrors
 *  `isActiveState` in task-activity; kept as a local set so this module stays
 *  free of the i18n/event-parser chain. */
const ACTIVE_STATES = new Set(['submitted', 'queued', 'dispatched', 'waiting_context', 'running'])

/** Whose context the thread should print.
 *
 *  The entry model's own turn used to be the only answer, but the reader is
 *  watching an agent work — the agent is the one with a window to fill, and
 *  its number is the one that predicts a compaction. A thread can have
 *  several agents running at once (a team, a fan-out), and each has its own
 *  window, so every live task that reported a sample is returned, newest
 *  first. With nothing running, the most recent sample still answers "how
 *  full did the last run get", and a thread that never dispatched anything
 *  returns [] so the caller keeps the entry model's turn.
 *
 *  `tasks` arrives newest-first (the panel sorts by updated_at), so the first
 *  live row is the current job and the first row overall is the last one.
 */
export function agentContextSamples(
  tasks: readonly AgentContextTask[] | null | undefined,
): AgentContextSample[] {
  if (!tasks || tasks.length === 0) return []
  const live: AgentContextSample[] = []
  let idle: AgentContextSample | null = null
  for (const task of tasks) {
    const tokens = task.context_tokens ?? 0
    if (tokens <= 0 || !task.agent) continue
    const sample: AgentContextSample = {
      agent: task.agent,
      tokens,
      window: task.context_window ?? 0,
      model: task.context_model ?? '',
      live: ACTIVE_STATES.has(task.state ?? ''),
    }
    if (sample.live) live.push(sample)
    else if (!idle) idle = sample
  }
  if (live.length > 0) return live
  return idle ? [idle] : []
}
