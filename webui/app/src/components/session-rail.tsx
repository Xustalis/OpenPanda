// SPDX-License-Identifier: AGPL-3.0-or-later

/** The thread's overview card: the small panel pinned to the top-right corner
 *  of the reading area. It answers, without scrolling, the three questions a
 *  thread raises while it runs — what did it change, how full is the context,
 *  and what is still working (on which machine).
 *
 *  Modelled on the session-details panel both reference clients ship. Claude
 *  Code's docs name three cards — Changes (line counts such as `+12 -1`, click
 *  to open the diff), Environment (where the session runs) and Context (window
 *  usage as a percentage, expandable by category) — and the panel "appears
 *  after you start a session, while the chat is wide enough for it". Codex
 *  splits the same information between its review panel (per-file diff, git
 *  staging) and its environment picker (Local / Worktree / Cloud), because a
 *  run's machine is a first-class part of the session there too.
 *
 *  This rail keeps that shape and nothing more: three sections, each one
 *  opening into the surface that already owns the detail — the worktree diff
 *  drawer, the task detail, the queue board. It is a summary, not a second
 *  copy of the app: no staging, no revert, no editing. Those stay where they
 *  were, one click away.
 *
 *  Deliberately NOT here: a per-category context breakdown. The panel reports
 *  a turn's billed tokens, not what composed the prompt, so a breakdown would
 *  be invented numbers. The card shows what it can stand behind and says so
 *  when the window size is unknown. */

import type { ComponentChildren } from 'preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { api, type NodeInfo, type SessionDiff, type Task } from '../api/client'
import { useAsync } from '../hooks'
import { t } from '../i18n'
import { Icon } from './icons'
import { compactNumber } from '../format/number'
import { formatDuration } from '../format/time'
import { prettyAgent } from './chat-speaker'
import { isActiveState } from './task-activity'
import { contextShare, patchFileStats } from './session-rail-stats'

/** How many changed files the card lists before it defers to the drawer. */
const FILE_ROWS = 6

/** `git status --porcelain` prints two columns (index, worktree); the panel
 *  trims them into one token, so "MM" and "??" both arrive whole. */
function statusTone(status: string): string {
  if (status.includes('?')) return 'is-new'
  if (status.includes('D')) return 'is-gone'
  if (status.includes('A')) return 'is-new'
  if (status.includes('R')) return 'is-moved'
  return 'is-edit'
}

function nodeName(nodes: NodeInfo[], id: string): string {
  const hit = nodes.find((n) => n.id === id)
  return hit?.name || hit?.id || id
}

export interface SessionRailProps {
  /** The thread's worktree changes, or null before one exists. */
  diff: SessionDiff | null
  /** The thread's tasks, live rows included. */
  tasks: Task[]
  nodes: NodeInfo[]
  /** Billed tokens of the newest turn: the context footprint the last request
   *  actually carried. 0 when the thread has not asked anything yet. */
  contextTokens: number
  /** The model that wrote the newest turn, used only when the registry has no
   *  active row to name (a fresh install, a failed read). */
  model: string
  /** Where this thread is pinned to run, already resolved for display. */
  machine: string
  onOpenDiff(): void
  onOpenTask(id: string): void
  onOpenQueue(): void
}

export function SessionRail(props: SessionRailProps) {
  // Three rows is the resting state: this is a summary card, and every row
  // already carries the number that made you look. Changes opens by default —
  // "which files" is what the card gets opened for — and Tasks opens itself
  // only while something is actually running.
  const [folded, setFolded] = useState<Record<string, boolean>>(() => ({
    context: true,
    tasks: !props.tasks.some((task) => !task.spawn_depth && isActiveState(task.state)),
  }))
  // The window size lives on the registry's active row, not on the turn: the
  // panel reports what a request cost, never what the model could hold. Read
  // here rather than lifted into the thread view — the rail is the only
  // consumer, and it unmounts when folded, so a stale window cannot outlive
  // the card that shows it.
  const models = useAsync(() => api.models().catch(() => null), [])
  const contextWindow = models.data?.active?.context_window ?? 0
  const modelName = models.data?.active?.alias || props.model
  // Root rows only: a sub-agent is part of its parent's row, and the count of
  // them is the "N 子任务" tail — the rail is a summary, so a delegation tree
  // must not read as N unrelated jobs.
  const live = props.tasks.filter((task) => !task.spawn_depth && isActiveState(task.state))
  const [now, setNow] = useState(() => Date.now())
  // One tick a second, and only while something is actually running: a
  // finished thread should not keep a timer alive to redraw a still card.
  useEffect(() => {
    if (live.length === 0) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [live.length])

  const open = (key: string) => !folded[key]
  const toggle = (key: string) => setFolded((f) => ({ ...f, [key]: !f[key] }))

  // What the thread's tasks reported touching. `files_changed` in a run's
  // result is a snapshot diff of its working directory, so unlike the worktree
  // diff it works in a plain folder — which is the only signal a thread whose
  // project is not a git repository ever gets.
  const reported = useMemo(() => {
    const out: string[] = []
    const seen = new Set<string>()
    for (const task of props.tasks) {
      for (const file of task.files_changed ?? []) {
        if (!file || seen.has(file)) continue
        seen.add(file)
        out.push(file)
      }
    }
    return out
  }, [props.tasks])

  const changes = props.diff?.changes ?? []
  // A file the worktree already lists is not repeated from a task report: the
  // worktree row carries a line count, the report row does not, so the same
  // path would appear twice with different amounts of truth.
  const changePaths = new Set(changes.map((c) => c.path))
  const extra = reported.filter((path) => !changePaths.has(path))
  // What the run could establish about each reported path. A created file
  // carries its line count, so its row can print the same `+N -0` a worktree
  // row does; an edit or a deletion has no "before" content left to count and
  // says so by carrying the kind alone.
  const reportedStats = useMemo(() => {
    const map = new Map<string, { kind: string; lines: number }>()
    for (const task of props.tasks) {
      for (const stat of task.files_stat ?? []) {
        if (!stat?.path) continue
        map.set(stat.path, { kind: stat.kind, lines: stat.lines ?? 0 })
      }
    }
    return map
  }, [props.tasks])
  const reportedAdded = extra.reduce(
    (sum, path) => sum + (reportedStats.get(path)?.kind === 'added' ? reportedStats.get(path)!.lines : 0),
    0,
  )
  // The patch is up to 128 KiB and the rail re-renders once a second while a
  // task runs; parse it only when the diff actually changed.
  const patch = props.diff?.patch ?? ''
  const perFile = useMemo(() => patchFileStats(patch), [patch])
  const stat = props.diff?.stat
  const pct = contextShare(props.contextTokens, contextWindow)
  // A thread that has spent 144 tokens of a million rounds to 0%, which reads
  // as "nothing was used" — the opposite of what the number says. Below one
  // percent the card says so instead of rounding down to nothing.
  const shareLabel =
    pct === null
      ? ''
      : pct === 0 && props.contextTokens > 0
        ? t('sessionRail.contextUnder1')
        : `${pct}%`

  const head = (key: string, label: string, summary: ComponentChildren) => (
    <button
      type="button"
      class="rail-head"
      aria-expanded={open(key)}
      onClick={() => toggle(key)}
    >
      <span class="rail-title">{label}</span>
      <span class="rail-sum">{summary}</span>
      <Icon name={open(key) ? 'chevron-down' : 'chevron-right'} size={13} class="rail-chev" />
    </button>
  )

  return (
    <aside class="session-rail" aria-label={t('sessionRail.label')}>
      <header class="rail-top">
        <span class="rail-top-title">{t('sessionRail.label')}</span>
      </header>

      <div class="rail-body">
        {/* ---- Tasks -------------------------------------------------- */}
        <section class="rail-sec">
          {head(
            'tasks',
            t('sessionRail.tasks'),
            live.length > 0 ? (
              <span class="rail-live">{t('sessionRail.running', { n: String(live.length) })}</span>
            ) : (
              <span class="rail-none">{t('sessionRail.idle')}</span>
            ),
          )}
          {open('tasks') && (
            <div class="rail-sec-body">
              {live.length === 0 ? (
                <p class="rail-note">{t('sessionRail.idleWhere', { machine: props.machine })}</p>
              ) : (
                <ul class="rail-tasks">
                  {live.map((task) => {
                    const started = Date.parse(task.created_at)
                    const elapsed = Number.isFinite(started) ? Math.max(0, now - started) : 0
                    const kids = props.tasks.filter((c) => c.parent_id === task.id).length
                    const where = task.origin_local
                      ? t('sessionRail.thisMachine')
                      : task.origin_node
                        ? nodeName(props.nodes, task.origin_node)
                        : props.machine
                    // The dot and the header already say "running", so the
                    // state word is only worth its room when it says something
                    // else (queued, dispatched, waiting) — otherwise the line
                    // spends its width on the machine, which is the answer
                    // this card exists to give.
                    const meta: string[] = []
                    if (task.agent) meta.push(prettyAgent(task.agent))
                    if (!task.agent || task.state !== 'running') {
                      meta.push(t(`state.${task.state}`, task.state))
                    }
                    meta.push(where)
                    if (elapsed > 0) meta.push(formatDuration(elapsed))
                    if (kids > 0) meta.push(t('sessionRail.kids', { n: String(kids) }))
                    return (
                      <li key={task.id}>
                        <button
                          type="button"
                          class="rail-task"
                          onClick={() => props.onOpenTask(task.id)}
                          title={task.title}
                        >
                          <span class="rail-dot" aria-hidden="true" />
                          <span class="rail-task-body">
                            <span class="rail-task-title">{task.title}</span>
                            <span class="rail-task-meta">{meta.join(' · ')}</span>
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              )}
              {props.tasks.length > 0 && (
                <button type="button" class="rail-more" onClick={props.onOpenQueue}>
                  {t('sessionRail.openQueue')}
                </button>
              )}
            </div>
          )}
        </section>


        {/* ---- Context ------------------------------------------------ */}
        <section class="rail-sec">
          {head(
            'context',
            t('sessionRail.context'),
            pct === null ? (
              <span class="rail-none">—</span>
            ) : (
              <span class={`rail-pct mono${pct >= 80 ? ' is-high' : ''}`}>{shareLabel}</span>
            ),
          )}
          {open('context') && (
            <div class="rail-sec-body">
              <div
                class={`rail-meter${pct !== null && pct >= 80 ? ' is-high' : ''}`}
                role="img"
                aria-label={pct === null ? t('sessionRail.contextUnknown') : shareLabel}
              >
                <span class="rail-meter-fill" style={pct === null ? undefined : `width:${pct}%`} />
              </div>
              <div class="rail-ctx-nums">
                <span class="mono">
                  {props.contextTokens > 0 ? compactNumber(props.contextTokens) : '—'}
                </span>
                <span class="rail-dim mono">
                  {contextWindow > 0 ? `/ ${compactNumber(contextWindow)}` : ''}
                </span>
              </div>
              <p class="rail-note">
                {contextWindow > 0
                  ? t('sessionRail.contextNote', { model: modelName })
                  : t('sessionRail.contextNoWindow')}
              </p>
            </div>
          )}
        </section>

        {/* ---- Changes ------------------------------------------------ */}
        <section class="rail-sec">
          {head(
            'changes',
            t('sessionRail.changes'),
            changes.length > 0 ? (
              stat ? (
                <>
                  <span class="rail-add mono">+{stat.insertions}</span>
                  <span class="rail-del mono">−{stat.deletions}</span>
                </>
              ) : (
                <span class="rail-none">
                  {t('sessionRail.files', { n: String(changes.length) })}
                </span>
              )
            ) : reportedAdded > 0 ? (
              <>
                <span class="rail-add mono">+{reportedAdded}</span>
                <span class="rail-del mono">−0</span>
              </>
            ) : extra.length > 0 ? (
              <span class="rail-none">{t('sessionRail.files', { n: String(extra.length) })}</span>
            ) : (
              <span class="rail-none">—</span>
            ),
          )}
          {open('changes') && (
            <div class="rail-sec-body">
              {changes.length === 0 && extra.length === 0 && (
                // Two different emptinesses, and the difference matters: a
                // thread whose project is not a git repository has no worktree
                // to compare at all, so "no changes" would be a claim the card
                // cannot make. A task that wrote outside the directory being
                // watched lands in the same blank card — the note names the
                // scope so the reader stops looking for something that was
                // never tracked.
                <p class="rail-note">
                  {props.diff ? t('sessionRail.noChanges') : t('sessionRail.noWorktree')}
                </p>
              )}
              {changes.length === 0 && extra.length === 0 && props.tasks.length > 0 && (
                // The run finished and reported nothing. Saying so is the
                // difference between "the card is broken" and "the file went
                // somewhere the snapshot does not cover".
                <p class="rail-note">{t('sessionRail.noReported')}</p>
              )}
              {changes.length > 0 && (
                <>
                  <ul class="rail-files">
                    {changes.slice(0, FILE_ROWS).map((c) => {
                      const s = perFile.get(c.path)
                      return (
                        <li key={c.path} class="rail-file">
                          <span class={`rail-file-state mono ${statusTone(c.status)}`}>{c.status}</span>
                          <span class="rail-file-path" title={c.path}>
                            {c.path}
                          </span>
                          {s && (
                            <span class="rail-file-stat mono">
                              <span class="rail-add">+{s.insertions}</span>
                              <span class="rail-del">−{s.deletions}</span>
                            </span>
                          )}
                        </li>
                      )
                    })}
                  </ul>
                  <button type="button" class="rail-more" onClick={props.onOpenDiff}>
                    {changes.length > FILE_ROWS
                      ? t('sessionRail.moreFiles', { n: String(changes.length - FILE_ROWS) })
                      : t('sessionRail.openDiff')}
                  </button>
                </>
              )}
              {extra.length > 0 && (
                <ul class="rail-files">
                  {extra.slice(0, FILE_ROWS).map((path) => {
                    const stat = reportedStats.get(path)
                    // No stat means the run predates the annotation: the path
                    // is known but its fate is not, and a guessed letter would
                    // be worse than a neutral dot.
                    const status = !stat ? null : stat.kind === 'added' ? 'A' : stat.kind === 'removed' ? 'D' : 'M'
                    return (
                      <li key={path} class="rail-file">
                        <span class={`rail-file-state mono ${status ? statusTone(status) : 'rail-none'}`}>
                          {status ?? '·'}
                        </span>
                        <span class="rail-file-path" title={path}>
                          {path}
                        </span>
                        {stat?.kind === 'added' && (stat.lines ?? 0) > 0 && (
                          <span class="rail-file-stat mono">
                            <span class="rail-add">+{stat!.lines}</span>
                            <span class="rail-del">−0</span>
                          </span>
                        )}
                      </li>
                    )
                  })}
                  {extra.length > FILE_ROWS && (
                    <li class="rail-file rail-none">
                      {t('sessionRail.moreFiles', { n: String(extra.length - FILE_ROWS) })}
                    </li>
                  )}
                </ul>
              )}
            </div>
          )}
        </section>

      </div>
    </aside>
  )
}
