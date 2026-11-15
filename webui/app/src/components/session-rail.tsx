// SPDX-License-Identifier: AGPL-3.0-or-later

/** The thread's status line, always printed in the pane head: what is running
 *  (and on which machine), how full the last request's context window was,
 *  and what the worktree holds.
 *
 *  One line, one click target. The segments used to be their own controls —
 *  the task words jumped to the queue, the change words opened the diff pane —
 *  so a sentence that reads as one fact had three destinations, and the only
 *  one anybody found was the first: clicking the line went to the task page
 *  and nothing else, however much the line was carrying. The line now opens
 *  the thread's overview, where each of the three answers has room to be read
 *  and still keeps a link to its own page.
 *
 *  The overview is a popover under the head, not a card on the transcript: it
 *  opens deliberately, covers only the top-right corner while it is up, and
 *  folds away on the next click outside.
 *
 *  The change description is the only part of the line that can be arbitrarily
 *  long (a path list, or the sentence explaining why there is no worktree). It
 *  scrolls inside a fixed viewport instead of being cut with an ellipsis or
 *  pushing the rest of the head off the row.
 *
 *  Deliberately NOT here: a per-category context breakdown. The line reports
 *  a turn's billed tokens, not what composed the prompt, so a breakdown would
 *  be invented numbers. The share is omitted when the window size is unknown. */

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { NodeInfo, SessionDiff, Task } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'
import { compactNumber } from '../format/number'
import { formatDuration } from '../format/time'
import { prettyAgent } from './chat-speaker'
import { layerAboveOwnsEscape } from './card-dismiss'
import { isActiveState } from './task-activity'
import { contextShare, patchFileStats } from './session-rail-stats'

/** Gap between the two copies of a ticker's text. The keyframe translates by
 *  half the track plus half this gap, so the second copy lands exactly where
 *  the first one started and the loop has no visible seam. */
const TICKER_GAP = 28
const SEP = ' · '

/** How many changed files the overview lists before it defers to the diff
 *  pane. Five rows is what fits under the context block without the popover
 *  growing past the corner it hangs in. */
const FILE_ROWS = 5

/** `git status --porcelain` prints two columns (index, worktree); the overview
 *  trims them into one token, so "MM" and "??" both arrive whole. */
function statusTone(status: string): string {
  if (status.includes('?')) return 'is-new'
  if (status.includes('D')) return 'is-gone'
  if (status.includes('A')) return 'is-new'
  if (status.includes('R')) return 'is-moved'
  return 'is-edit'
}

/** One run of text in the ticker. The +/− counts keep the diff drawer's
 *  semantic colours, so even a scrolling line still reads as a diff. */
interface TickerPart {
  text: string
  tone?: 'add' | 'del' | 'muted'
}

function nodeName(nodes: NodeInfo[], id: string): string {
  const hit = nodes.find((n) => n.id === id)
  return hit?.name || hit?.id || id
}

/** Task titles are user text: a newline pasted into a title would otherwise
 *  break the one-line status bar into two. */
function oneLine(text: string): string {
  return text.replace(/\s+/g, ' ').trim()
}

/** A viewport that scrolls its text only when the text does not fit. Two
 *  copies make the loop seamless; a short string keeps one copy and sits
 *  still, because animating text that already fits is noise. Hovering pauses
 *  the scroll so a path can actually be read, and reduced-motion turns the
 *  viewport into a plain horizontally scrollable box. */
function ScrollingText({ text, parts }: { text: string; parts: TickerPart[] }) {
  const viewport = useRef<HTMLSpanElement>(null)
  const copy = useRef<HTMLSpanElement>(null)
  const [scroll, setScroll] = useState(false)
  const [span, setSpan] = useState(0)

  useEffect(() => {
    const vp = viewport.current
    const first = copy.current
    if (!vp || !first) return
    const measure = () => {
      const width = first.getBoundingClientRect().width
      setScroll(width > vp.clientWidth + 1)
      setSpan(width + TICKER_GAP)
    }
    measure()
    // Both the pane (a dock opening, a window resize) and the text itself can
    // change the fit; one observer answers both without a resize listener.
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(vp)
    ro.observe(first)
    return () => ro.disconnect()
  }, [text])

  // Both copies are built from the same parts: the second one is a clone for
  // the loop, not a place where the colours may drift out of step.
  const renderCopy = (hidden: boolean) => (
    <span
      class="rail-ticker-copy"
      ref={hidden ? undefined : copy}
      aria-hidden={hidden ? 'true' : undefined}
    >
      {parts.map((part, i) => (
        <span key={i} class={part.tone ? `rail-tone-${part.tone}` : undefined}>
          {part.text}
        </span>
      ))}
    </span>
  )

  // ~20px/s, and the keyframes hold the first and last frames: long path
  // lists are not rushed, and the beginning of a sentence stays readable for
  // a moment instead of being scrolled away the instant it is printed.
  const duration = Math.min(36, Math.max(12, span / 20))
  return (
    <span class={`rail-ticker${scroll ? ' is-scrolling' : ''}`} ref={viewport} title={text}>
      <span class="rail-ticker-track" style={scroll ? `animation-duration:${duration}s` : undefined}>
        {renderCopy(false)}
        {scroll && renderCopy(true)}
      </span>
    </span>
  )
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
  /** The active model's context window, or 0 when the registry has no row to
   *  read one from. Read by the page, not here: the dock's start pane prints
   *  the same number, and two readers of one registry is one read too many. */
  contextWindow: number
  /** The model that wrote the newest turn, already resolved for display. */
  modelName: string
  /** Where this thread is pinned to run, already resolved for display. */
  machine: string
  /** Entry-model work exists before a dispatched task row does. */
  working?: boolean
  onOpenDiff(): void
  onOpenTask(id: string): void
  onOpenQueue(): void
}

export function SessionRail(props: SessionRailProps) {
  const contextWindow = props.contextWindow
  const modelName = props.modelName
  const [open, setOpen] = useState(false)
  const dock = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)

  // The overview closes on the two things that make a popover stale: a press
  // outside it, and Escape. It is anchored to the head, which never scrolls,
  // so a scrolling transcript is not a reason to fold it away — and a menu or
  // a modal that is already up keeps Escape for itself.
  useEffect(() => {
    if (!open) return
    const onDown = (event: PointerEvent) => {
      if (dock.current?.contains(event.target as Node)) return
      setOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || layerAboveOwnsEscape()) return
      event.stopPropagation()
      setOpen(false)
      trigger.current?.focus({ preventScroll: true })
    }
    // Capture, because a surface under the popover may swallow the press.
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  // The overview is a dialog, so the keys have to enter it: without this the
  // next Tab walked back into the head instead of through the panel's links.
  const pop = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (open) pop.current?.focus({ preventScroll: true })
  }, [open])

  // Root rows only: a sub-agent is part of its parent's row, and the count of
  // them is the "N 子任务" tail — the rail is a summary, so a delegation tree
  // must not read as N unrelated jobs.
  const live = props.tasks.filter((task) => !task.spawn_depth && isActiveState(task.state))
  const running = live[0]

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
  const stat = props.diff?.stat
  // The patch is up to 128 KiB and the head re-renders on every streamed
  // event; read the per-file counts only when the patch itself changed.
  const patch = props.diff?.patch ?? ''
  const perFile = useMemo(() => patchFileStats(patch), [patch])

  const pct = contextShare(props.contextTokens, contextWindow)
  // A thread that has spent 144 tokens of a million rounds to 0%, which reads
  // as "nothing was used" — the opposite of what the number says. Below one
  // percent the line says so instead of rounding down to nothing.
  const shareLabel =
    pct === null
      ? ''
      : pct === 0 && props.contextTokens > 0
        ? t('sessionRail.contextUnder1')
        : `${pct}%`

  // The running task's title and the machine it is on. When the thread is
  // idle the line answers the same question with the pinned machine instead.
  const where = running
    ? running.origin_local
      ? t('sessionRail.thisMachine')
      : running.origin_node
        ? nodeName(props.nodes, running.origin_node)
        : props.machine
    : props.machine
  const taskTitle = running
    ? oneLine(running.title || '') || t('sessionRail.running', { n: String(live.length) })
    : ''
  const taskLine = running
    ? [taskTitle, where, live.length > 1 ? `+${live.length - 1}` : ''].filter(Boolean).join(SEP)
    : props.working
      ? t('sessionRail.modelRunning')
      : t('sessionRail.idleWhere', { machine: props.machine })

  // Percentage first, then the raw numbers. A model with no catalogue window
  // still gets its token count: "unknown" is a reason to omit the share, not
  // a reason to hide the one number the turn actually reported.
  const used = props.contextTokens > 0 ? compactNumber(props.contextTokens) : '—'
  const total = contextWindow > 0 ? compactNumber(contextWindow) : ''
  const usage = total ? `${used} / ${total}` : props.contextTokens > 0 ? used : ''
  const contextLine = [shareLabel, usage].filter(Boolean).join(SEP) || '—'

  // The ticker is a file list, not prose. "Not a git repository" and "the task
  // reported no changes" used to scroll past here and told the reader nothing
  // they could act on; the reason now lives in the tooltip. With no paths the
  // line is a single dash — an empty list is an answer, not a missing panel.
  const paths = [...changes.map((c) => c.path), ...extra]
  const changeParts: TickerPart[] = []
  const add = (text: string, tone?: TickerPart['tone']) => changeParts.push({ text, tone })
  if (stat && changes.length > 0) {
    add(`+${stat.insertions}`, 'add')
    add(` −${stat.deletions}`, 'del')
    add(SEP)
  } else if (reportedAdded > 0) {
    add(`+${reportedAdded}`, 'add')
    add(' −0', 'del')
    add(SEP)
  }
  add(paths.length > 0 ? paths.slice(0, 20).join(' · ') : '—', 'muted')
  const changeText = changeParts.map((p) => p.text).join('')
  // Hover keeps what the line no longer spends width on: the totals, every
  // path (the ticker shows the first twenty), or why there is no list at all.
  const changeTitle = paths.length
    ? [
        stat ? `+${stat.insertions} −${stat.deletions}` : reportedAdded > 0 ? `+${reportedAdded} −0` : '',
        t('sessionRail.files', { n: String(paths.length) }),
        paths.join(' · '),
      ]
        .filter(Boolean)
        .join(SEP)
    : [
        props.diff ? t('sessionRail.noChanges') : t('sessionRail.noWorktree'),
        props.tasks.length > 0 ? t('sessionRail.noReported') : '',
      ]
        .filter(Boolean)
        .join(SEP)

  // What the overview lists under 改动: the worktree's own rows first, then
  // the files a run reported outside version control, capped at FILE_ROWS.
  // Anything that did not fit is named by the "还有 N 个" line, so the cap
  // never drops a path without saying so.
  const diffStat =
    stat && changes.length > 0
      ? { add: stat.insertions, del: stat.deletions }
      : reportedAdded > 0
        ? { add: reportedAdded, del: 0 }
        : null
  const fileRows: { path: string; status: string; add?: number; del?: number }[] = []
  for (const c of changes.slice(0, FILE_ROWS)) {
    const s = perFile.get(c.path)
    fileRows.push({ path: c.path, status: c.status, add: s?.insertions, del: s?.deletions })
  }
  for (const path of extra) {
    if (fileRows.length >= FILE_ROWS) break
    const s = reportedStats.get(path)
    // No stat means the run predates the annotation: the path is known but
    // its fate is not, and a guessed letter would be worse than a neutral dot.
    const status = !s ? '' : s.kind === 'added' ? 'A' : s.kind === 'removed' ? 'D' : 'M'
    const lines = s?.kind === 'added' ? (s.lines ?? 0) : 0
    fileRows.push({
      path,
      status,
      add: lines > 0 ? lines : undefined,
      del: lines > 0 ? 0 : undefined,
    })
  }
  const hiddenFiles = Math.max(0, paths.length - fileRows.length)

  // The overview prints state and machine on separate rows; the line joins
  // them into one sentence, so it cannot hand its words over whole.
  const stateWord =
    live.length > 0
      ? t('state.running')
      : props.working
        ? t('sessionRail.modelRunning')
        : t('sessionRail.idle')
  const whereLine = t('sessionRail.runsOn', { machine: where })

  // One running task's second line: who is on it, where, how long, and how
  // many sub-agents it grew. Elapsed is a snapshot taken when the panel
  // renders — a clock ticking in a folded panel is a timer with no reader.
  const taskMeta = (task: Task): string => {
    const parts: string[] = []
    if (task.agent) parts.push(prettyAgent(task.agent))
    parts.push(
      task.origin_local
        ? t('sessionRail.thisMachine')
        : task.origin_node
          ? nodeName(props.nodes, task.origin_node)
          : props.machine,
    )
    const kids = props.tasks.filter((c) => c.parent_id === task.id).length
    if (kids > 0) parts.push(t('sessionRail.kids', { n: String(kids) }))
    const started = Date.parse(task.created_at)
    if (Number.isFinite(started)) parts.push(formatDuration(Date.now() - started))
    return parts.join(SEP)
  }

  return (
    <div class="rail-dock" ref={dock}>
      {/* `has-changes` is what tells the stylesheet there is a list to make
          room for: without it the bar hugs the sentence, so an idle thread's
          line ends at the dash instead of holding a blank lane open beside
          it. The ticker's own measurement still decides scrolling. */}
      <button
        ref={trigger}
        type="button"
        class={`rail-bar${paths.length > 0 ? ' has-changes' : ''}${open ? ' is-open' : ''}`}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t('sessionRail.label')}
        title={t('sessionRail.label')}
        onClick={() => setOpen(!open)}
      >
        <Icon name="activity" size={16} class={`rail-bar-icon${running || props.working ? ' is-live' : ''}`} />
        <span class="rail-bar-task" title={taskLine}>
          {running ? (
            <>
              <span class="rail-bar-task-title">{taskTitle}</span>
              <span class="rail-bar-task-sep" aria-hidden="true">
                ·
              </span>
              <span class="rail-bar-task-where">
                {where}
                {live.length > 1 ? ` +${live.length - 1}` : ''}
              </span>
            </>
          ) : (
            <span class="rail-bar-task-title">{taskLine}</span>
          )}
        </span>
        <span class="rail-bar-sep" aria-hidden="true">
          ·
        </span>
        <span
          class={`rail-bar-ctx${pct !== null && pct >= 80 ? ' is-high' : ''}`}
          title={
            contextWindow > 0
              ? t('sessionRail.contextNote', { model: modelName })
              : t('sessionRail.contextNoWindow')
          }
        >
          {contextLine}
        </span>
        <span class="rail-bar-sep" aria-hidden="true">
          ·
        </span>
        <span class="rail-bar-changes" title={changeTitle}>
          <ScrollingText text={changeText} parts={changeParts} />
        </span>
        <Icon name="chevron-down" size={12} class="rail-bar-caret" />
      </button>

      {open && (
        <div
          ref={pop}
          class="rail-pop"
          role="dialog"
          aria-label={t('sessionRail.label')}
          tabIndex={-1}
        >
          {/* ---- Running --------------------------------------------- */}
          <section class="rail-pop-sec">
            <div class="rail-pop-status">
              <span
                class={`rail-pop-dot${running || props.working ? ' is-live' : ''}`}
                aria-hidden="true"
              />
              <span class="rail-pop-state">{stateWord}</span>
              <span class="rail-pop-where" title={where}>
                {whereLine}
              </span>
            </div>
            {live.length > 0 && (
              <ul class="rail-pop-tasks">
                {live.slice(0, 4).map((task) => (
                  <li key={task.id}>
                    <button
                      type="button"
                      class="rail-pop-task"
                      title={oneLine(task.title || '')}
                      onClick={() => {
                        setOpen(false)
                        props.onOpenTask(task.id)
                      }}
                    >
                      <span class="rail-pop-task-title">
                        {oneLine(task.title || '') ||
                          t('sessionRail.running', { n: String(live.length) })}
                      </span>
                      <span class="rail-pop-task-meta">{taskMeta(task)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {live.length > 4 && (
              <p class="rail-pop-note">
                {t('sessionRail.moreTasks', { n: String(live.length - 4) })}
              </p>
            )}
            {props.tasks.length > 0 && (
              <button
                type="button"
                class="rail-pop-link"
                onClick={() => {
                  setOpen(false)
                  props.onOpenQueue()
                }}
              >
                {t('sessionRail.openQueue')}
                <Icon name="arrow-right" size={13} />
              </button>
            )}
          </section>

          {/* ---- Context --------------------------------------------- */}
          <section class="rail-pop-sec">
            <div class="rail-pop-row">
              <span class="rail-pop-label">{t('sessionRail.context')}</span>
              <span class={`rail-pop-pct${pct !== null && pct >= 80 ? ' is-high' : ''}`}>
                {shareLabel || '—'}
              </span>
            </div>
            <span
              class={`rail-pop-meter${pct !== null && pct >= 80 ? ' is-high' : ''}${
                pct === null ? ' is-unknown' : ''
              }`}
              role="img"
              aria-label={
                pct === null ? t('sessionRail.contextUnknown') : shareLabel || t('sessionRail.contextUnknown')
              }
            >
              {pct !== null && <i style={`width:${pct}%`} />}
            </span>
            <div class="rail-pop-foot">
              <span class="rail-pop-nums">{usage || '—'}</span>
              <span class="rail-pop-model" title={contextWindow > 0 ? modelName : undefined}>
                {contextWindow > 0
                  ? t('sessionRail.contextNote', { model: modelName })
                  : t('sessionRail.contextNoWindow')}
              </span>
            </div>
          </section>

          {/* ---- Changes --------------------------------------------- */}
          <section class="rail-pop-sec">
            <div class="rail-pop-row">
              <span class="rail-pop-label">{t('sessionRail.changes')}</span>
              {diffStat ? (
                <span class="rail-pop-diffstat">
                  <span class="rail-tone-add">+{diffStat.add}</span>
                  <span class="rail-tone-del">−{diffStat.del}</span>
                  {paths.length > 0 && (
                    <span class="rail-pop-count">
                      {t('sessionRail.files', { n: String(paths.length) })}
                    </span>
                  )}
                </span>
              ) : (
                <span class="rail-pop-none">—</span>
              )}
            </div>
            {fileRows.length > 0 ? (
              <ul class="rail-pop-files">
                {fileRows.map((row) => (
                  <li key={row.path} class="rail-pop-file">
                    <span
                      class={`rail-pop-file-state mono ${
                        row.status ? statusTone(row.status) : 'is-none'
                      }`}
                    >
                      {row.status || <i class="rail-pop-file-dot" />}
                    </span>
                    <span class="rail-pop-file-path" title={row.path}>
                      {row.path}
                    </span>
                    {row.add !== undefined && row.del !== undefined && (
                      <span class="rail-pop-file-stat">
                        <span class="rail-tone-add">+{row.add}</span>
                        <span class="rail-tone-del">−{row.del}</span>
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            ) : (
              <>
                <p class="rail-pop-note">
                  {props.diff ? t('sessionRail.noChanges') : t('sessionRail.noWorktree')}
                </p>
                {props.tasks.length > 0 && (
                  <p class="rail-pop-note">{t('sessionRail.noReported')}</p>
                )}
              </>
            )}
            {paths.length > 0 && (
              <button
                type="button"
                class="rail-pop-link"
                onClick={() => {
                  setOpen(false)
                  props.onOpenDiff()
                }}
              >
                {hiddenFiles > 0
                  ? t('sessionRail.moreFiles', { n: String(hiddenFiles) })
                  : t('sessionRail.openDiff')}
                <Icon name="arrow-right" size={13} />
              </button>
            )}
          </section>
        </div>
      )}
    </div>
  )
}
