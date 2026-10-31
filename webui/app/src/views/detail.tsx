// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, isTaskStalled, type ApprovalScope, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { mergeEvents, newestCursor } from '../components/event-merge'
import { t } from '../i18n'
import { formatDuration } from '../format/time'
import { navigate } from '../nav'
import { ScopeSelect } from '../components/scope-select'
import { StateBadge } from '../components/state-badge'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { fmtShort, parseResult, plainLine, shortId, text } from '../components/task-sections'
import { DiffViewer } from '../components/diff-viewer'
import { Icon } from '../components/icons'
import { OutputPanel } from '../components/task-output'
import { BriefPanel, ChainPanel, InfoPanel } from '../components/task-panels'

/** The task row plus its timeline, followed incrementally.
 *
 *  The first read is the whole history — the page needs it for the timeline,
 *  the review reason and the rollups — and every poll after that carries only
 *  the events past the cursor, which the client merges back in. Without the
 *  cursor this page re-downloaded a long run's entire timeline once per
 *  change frame, a request per second that grew with the run. */
function useLiveTask(id: string, changeKey: number) {
  const [task, setTask] = useState<Task | null>(null)
  const [error, setError] = useState<string | null>(null)
  const cursor = useRef(0)

  useEffect(() => {
    setTask(null)
    setError(null)
    cursor.current = 0
  }, [id])

  useEffect(() => {
    let alive = true
    const after = cursor.current
    api
      .task(id, after || undefined)
      .then((t) => {
        if (!alive) return
        cursor.current = newestCursor(t.events, after)
        // An incremental read carries only new rows; a full one (cursor 0)
        // carries the history, and the merge dedups either way.
        setTask((prev) => ({
          ...t,
          events: mergeEvents(prev && prev.id === t.id ? prev.events : [], t.events),
        }))
        setError(null)
      })
      .catch((e: unknown) => {
        if (!alive) return
        setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      alive = false
    }
  }, [id, changeKey])

  return { task, error }
}

// The task detail page reads as a document, not a stack of cards: a header
// with the title and the facts that identify the run, one line of trace, then
// two columns — what happened (result / brief / timeline) beside what it is
// (ids, delegation, children). Everything that used to be a full-width card
// with its own padding is now a rule-separated section, which is what made
// the page three screens tall for one failed run.
export function DetailView({
  id,
  onBack,
  onOpenSession,
  onOpenTask,
}: {
  id: string
  onBack(): void
  onOpenSession?(id: string): void
  onOpenTask?(id: string): void
}) {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { task, error } = useLiveTask(id, change + tick)
  // The task's board rides the same refresh cadence: a queued note posted
  // below must render, not just be accepted, or the send reads as lost.
  const { data: board } = useAsync(() => api.taskBoardMessages(id), [id], change + tick)
  // A stalled task emits no SSE event, so the badge can only appear on a
  // local clock tick.
  useVisibleInterval(() => setTick((v) => v + 1), 30_000)
  const [busy, setBusy] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectReason, setRejectReason] = useState('')
  // Where the review answer is remembered — "once" by default; session and
  // project write a standing gate answer for what comes after.
  const [scope, setScope] = useState<ApprovalScope>('once')
  const [msgText, setMsgText] = useState('')

  async function act(fn: () => Promise<unknown>, okMsg?: string) {
    if (busy) return
    setBusy(true)
    try {
      await fn()
      if (okMsg) toast(okMsg, 'success')
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  async function cancelTask() {
    const ok = await confirmDialog({
      title: t('detail.cancelConfirmTitle'),
      message: t('detail.cancelConfirmMsg'),
      confirmLabel: t('detail.cancelTask'),
    })
    if (!ok) return
    await act(() => api.cancel(task!.id), t('detail.cancelledToast'))
  }

  async function revertTask() {
    const ok = await confirmDialog({
      title: t('detail.revertConfirmTitle'),
      message: t('detail.revertConfirmMsg'),
      confirmLabel: t('detail.revert'),
    })
    if (!ok) return
    await act(async () => {
      const r = await api.taskRevert(task!.id)
      toast(
        t('detail.revertedToast', {
          restored: String(r.restored?.length ?? 0),
          removed: String(r.removed?.length ?? 0),
        }),
        'success',
      )
    })
  }

  // The two send modes: queue leaves the current round alone and the note
  // lands as context at the next one; inject aborts the in-flight round when
  // this console runs the task and makes the text the next round's leading
  // directive. The toast reports which one actually happened.
  async function sendMessage(inject: boolean) {
    const text = msgText.trim()
    if (!text) return
    await act(async () => {
      const r = await api.sendTaskMessage(task!.id, text, inject)
      toast(
        r.warning ||
          (r.status === 'interrupted' ? t('detail.msgInjectedToast') : t('detail.msgQueuedToast')),
        r.warning ? 'info' : 'success',
      )
      setMsgText('')
      // Refetch now: a store-only delivery (no engine to fan out a trace
      // event) emits no SSE signal, so the note would sit invisible until
      // the next tick — which reads exactly like a lost message.
      setTick((v) => v + 1)
    })
  }

  if (error)
    return (
      <section>
        <button class="dt-back" onClick={onBack}>
          <Icon name="arrow-left" size={14} /> {t('detail.back')}
        </button>
        <div class="card error-state u-mt-16">
          <span class="error-state-title">{t('common.loadFail')}</span>
          <span class="dim error-state-detail">{error}</span>
          <div>
            <button class="btn" onClick={() => setTick((v) => v + 1)}>
              {t('common.retry')}
            </button>
          </div>
        </div>
      </section>
    )
  if (!task) return <p class="dim">{t('common.loading')}</p>

  const cancellable = !['done', 'failed', 'cancelled', 'expired'].includes(task.state)
  // The third answer to a supervised "more work remains" park: approve would
  // accept the half-finished work, reject would throw it away — continue
  // spends a fresh round budget on finishing it, in the same agent session.
  const needsFollowup =
    task.state === 'review' && parseResult(task.result ?? '')?.needs_followup === true
  // A needs_changed_input park carries its failure in the review event, not in
  // result (no work was produced, so there is nothing to show there). Lift it
  // out of the timeline: a disabled Approve with no visible cause reads as a
  // broken button — the user clicks it, nothing happens, and the reason sits
  // hundreds of pixels below the fold.
  const blocked = task.state === 'review' && task.approval_disposition === 'needs_changed_input'
  // Read for every review state, not only the blocked banner: the verdict
  // block falls back to it when the result carries no reason of its own.
  const parkReason = task.state === 'review' ? reviewReason(task.events ?? []) : ''
  // Computed once: the verdict sentence is also the lead line of the output
  // when a run reported its outcome as prose, and printing it twice — once as
  // the answer, once as the first line of the panel — is the page's oldest
  // habit. The output card drops it from the body when they match.
  const verdict = verdictOf(task, parkReason)

  return (
    <section class="dt">
      <button class="dt-back" onClick={onBack}>
        <Icon name="arrow-left" size={14} /> {t('detail.back')}
      </button>

      <header class="dt-head">
        <div class="dt-head-text">
          <h1 class="dt-title">{task.title || task.id}</h1>
          {/* Identity only: state, scope and the three facts that name the run.
              The timestamps, elapsed time, tokens and cost moved into the
              verdict block below — on one line with the rest they were seven
              grey tokens of equal weight, and the reader had to scan all of
              them to find the two that mattered. */}
          <div class="dt-meta">
            <StateBadge state={task.state} />
            {task.isolate && (
              <span class="badge blue" title={t('detail.isolateHint')}>{t('detail.isolated')}</span>
            )}
            {!task.isolate && task.scope_paths && task.scope_paths.length > 0 && (
              <span class="badge dim" title={task.scope_paths.join('\n')}>
                {t('detail.scoped')}: {task.scope_paths.join(', ')}
              </span>
            )}
            <span class="dt-meta-item mono" title={task.id}>
              #{shortId(task.id)}
            </span>
            {task.project && <span class="dt-meta-item">{task.project}</span>}
            {task.owner && <span class="dt-meta-item">{task.owner}</span>}
            {task.plan_meta?.plan_id && (
              <button
                type="button"
                class="dt-meta-link"
                onClick={() => navigate({ view: 'control', tab: 'plans' })}
              >
                {t('orbit.actions.openPlan')} →
              </button>
            )}
          </div>
        </div>

        <div class="dt-actions">
          {task.session_id && onOpenSession && (
            <button
              type="button"
              class="btn primary"
              onClick={() => onOpenSession(task.session_id!)}
            >
              <Icon name="message" size={13} /> {t('detail.openSession')}
            </button>
          )}
          {task.state === 'review' && task.approval_disposition && (
            <span class="dim review-kind">{t(`detail.reviewKind.${task.approval_disposition}`)}</span>
          )}
          {task.state === 'review' && !rejecting && (
            <>
              {needsFollowup && (
                <button
                  class="btn"
                  disabled={busy}
                  title={t('detail.continueHint')}
                  onClick={() =>
                    act(async () => {
                      const out = await api.continueTask(task.id)
                      toast(out.warning || t('detail.continuedToast'), out.warning ? 'info' : 'success')
                    })
                  }
                >
                  {t('detail.continue')}
                </button>
              )}
              <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
              <button
                class="btn primary"
                disabled={busy || task.approval_disposition === 'needs_changed_input'}
                title={blocked ? t('detail.needsChangedInputHint') : undefined}
                onClick={() => act(() => api.approve(task.id, scope), t('detail.approvedToast'))}
              >
                {t('detail.approve')}
              </button>
              <button class="btn danger" disabled={busy} onClick={() => setRejecting(true)}>
                {t('detail.reject')}
              </button>
            </>
          )}
          {task.state === 'review' && rejecting && (
            <div class="reject-form">
              <input
                class="input"
                type="text"
                placeholder={t('detail.rejectReason')}
                value={rejectReason}
                onInput={(e) => setRejectReason((e.target as HTMLInputElement).value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    act(() =>
                      api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'), scope),
                    ).then(() => setRejecting(false))
                  }
                  if (e.key === 'Escape') setRejecting(false)
                }}
              />
              <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
              <button
                class="btn danger"
                disabled={busy}
                onClick={() =>
                  act(() =>
                    api.reject(task.id, rejectReason.trim() || t('detail.rejectedViaWeb'), scope),
                  ).then(() => setRejecting(false))
                }
              >
                {t('detail.rejectConfirm')}
              </button>
              <button class="btn" disabled={busy} onClick={() => setRejecting(false)}>
                {t('common.cancel')}
              </button>
            </div>
          )}
          {cancellable && (
            <button class="btn" disabled={busy} onClick={cancelTask}>
              {t('detail.cancelTask')}
            </button>
          )}
          {!cancellable && task.state !== 'expired' && (
            <button class="btn danger" disabled={busy} onClick={revertTask}>
              {t('detail.revert')}
            </button>
          )}
        </div>
      </header>

      {blocked && (
        // Why Approve cannot be clicked, and what to do instead. The button
        // stays disabled on purpose (consent cannot fix a deterministic
        // failure) — so the page owes the reader the instruction, not just a
        // greyed-out control. The failure itself now lives in the verdict
        // block, so it is not printed a second time here.
        <div class="dt-blocked">
          <div class="dt-blocked-head">
            <span class="badge yellow">{t('detail.reviewKind.needs_changed_input')}</span>
            <span class="dim">{t('detail.needsChangedInputHint')}</span>
          </div>
        </div>
      )}

      {/* What happened, first. Everything below this block is the evidence
          for its one sentence. */}
      <VerdictBlock verdict={verdict} />

      {cancellable && (
        // Send into the task while it runs: queue leaves the round alone
        // (the note is read as context at the next one), inject steers it
        // (the in-flight round aborts and the text leads the next one).
        <div class="dt-msg">
          <input
            class="input"
            type="text"
            placeholder={t('detail.msgPlaceholder')}
            value={msgText}
            onInput={(e) => setMsgText((e.target as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void sendMessage(false)
            }}
          />
          <button class="btn" disabled={busy || !msgText.trim()} onClick={() => sendMessage(false)}>
            {t('detail.msgQueue')}
          </button>
          <button
            class="btn primary"
            disabled={busy || !msgText.trim()}
            onClick={() => sendMessage(true)}
          >
            {t('detail.msgInject')}
          </button>
          <span class="dim dt-msg-hint">{t('detail.msgHint')}</span>
        </div>
      )}

      {board && board.messages.length > 0 && (
        // The board scope is where queued user notes, steers and sibling posts
        // land — reading it back is what makes "queued" a state the user can
        // see rather than a toast that forgot itself. It sits under the
        // composer because that is where the notes were written.
        <section class="dt-sec">
          <h2 class="dt-sec-title">{t('detail.board.title')}</h2>
          <ul class="dt-board">
            {board.messages.map((m, i) => (
              <li key={`${m.sender}-${m.sender_seq}-${i}`} class="dt-board-row">
                <span class="dt-board-sender">
                  {m.sender === 'user' || m.sender.startsWith('user:')
                    ? t('detail.board.you')
                    : m.sender_label || m.sender}
                </span>
                {m.grade !== 'status' && (
                  <span class={`badge ${m.grade === 'steer' ? 'blue' : 'dim'}`}>{m.grade}</span>
                )}
                {/* Delivery state, on the user's own notes only: a note lands
                    on the board the moment it is posted, so without this the
                    row reads "queued" forever and "did the agent see it?"
                    has no answer on screen. consumed_at is stamped by the
                    round that folded the note into its prompt. */}
                {(m.sender === 'user' || m.sender.startsWith('user:')) &&
                  (m.consumed_at ? (
                    <span class="badge dim">{t('detail.board.read')}</span>
                  ) : (
                    <span class="badge yellow">{t('detail.board.pending')}</span>
                  ))}
                <span class="dt-board-body">{m.body}</span>
                <span class="dim dt-board-time">
                  {m.sent_at ? fmtShort(new Date(m.sent_at * 1000).toISOString()) : ''}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div class="dt-body">
        {isTaskStalled(task) && (
          // The row has not moved past its own bound — nothing is consuming,
          // expiring, or answering for it. Surface that instead of letting the
          // open state sit silently.
          <div class="dt-note">
            <span class="badge yellow">{t('task.stalled')}</span>
            <span class="dim">{t('task.stalledHint')}</span>
          </div>
        )}

        {/* A self-merge proposal's "spec" IS the patch set — the review the
            approve button answers is this diff, so it renders ahead of the
            four cards. */}
        <SelfMergeCard spec={task.spec} />

        {/* The four answers this page owes: what came out, how it ran, what it
            was asked to do, and what it is. All four are on screen at once —
            each one's body is a preview or a fold, so a 2,000-character output
            no longer pushes the other three off the page. */}
        <div class="dt-panels">
          <OutputPanel task={task} lead={verdict.text} />
          <ChainPanel task={task} onOpenTask={onOpenTask} />
          <BriefPanel task={task} />
          <InfoPanel task={task} onOpenTask={onOpenTask} />
        </div>
      </div>
    </section>
  )
}

// —---------------------------------------------------------------------------
// Verdict — the one block the page owes the reader first.
//
// The old page opened on a facts table: five columns of grey UUIDs, then a
// trace strip, then a result section whose useful line was a 300-character
// error string. A reader arriving from the queue — usually because something
// failed — had to scan all of it to answer "did it work, and why not".
//
// This block answers that in one sentence, then lists what the answer cost
// (elapsed, rounds, tokens, money). It deliberately does NOT repeat the state
// word: the header badge already says 失败, and printing it twice was one of
// the ways the page said the same thing in three places.
// —---------------------------------------------------------------------------

type VerdictTone = 'ok' | 'bad' | 'warn' | 'info'

interface Verdict {
  tone: VerdictTone
  /** The sentence that answers "what happened". */
  text: string
  /** Cost of finding out: elapsed, rounds, tokens, money. */
  metrics: Array<[string, string]>
}

const OPEN_STATES = ['submitted', 'queued', 'dispatched', 'running', 'waiting_context']

function verdictOf(task: Task, fallbackReason: string): Verdict {
  const r = parseResult(task.result ?? '') ?? {}
  const failure = text(r.failed) || text(r.error)
  const settledBad = task.state === 'failed' || task.state === 'cancelled' || task.state === 'expired'
  const open = OPEN_STATES.includes(task.state)

  // The reason, in priority order: what the run itself reported, then the
  // review event that parked it. A cancelled run never produced one.
  let reason = failure
  if (!reason && task.state === 'review') reason = fallbackReason
  if (!reason) reason = text(r.verdict_reason)
  if (!reason && task.state === 'done') reason = firstLine(text(r.stdout)) || text(r.verdict)
  if (!reason) reason = open ? t('detail.verdict.empty') : t('detail.verdict.noDetail')

  const tone: VerdictTone = settledBad ? 'bad' : task.state === 'review' ? 'warn' : open ? 'info' : 'ok'

  const metrics: Array<[string, string]> = []
  const elapsed = elapsedLabel(task.created_at, task.updated_at, open)
  if (elapsed) metrics.push([t('detail.verdict.elapsed'), elapsed])
  if (task.supervision?.round !== undefined) {
    metrics.push([
      t('detail.verdict.rounds'),
      `${task.supervision.round}/${task.supervision.budget ?? '?'}`,
    ])
  }
  if (typeof r.tokens === 'number' && r.tokens > 0) {
    metrics.push([t('detail.verdict.tokens'), String(r.tokens)])
  }
  if (typeof r.cost === 'number' && r.cost > 0) {
    metrics.push([t('detail.verdict.cost'), '$' + r.cost.toFixed(4)])
  }

  return { tone, text: reason || t('detail.verdict.empty'), metrics }
}

/** Long reasons — an endpoint timeout plus a fallback's 429 is 300 characters
 *  — get three lines and a way to read the rest. Clamping the text is the
 *  point of the block: a wall of error is what it replaced. */
const VERDICT_CLAMP = 200

function VerdictBlock({ verdict }: { verdict: Verdict }) {
  const [open, setOpen] = useState(false)
  const v = verdict
  const long = v.text.length > VERDICT_CLAMP
  const shown = open || !long ? v.text : v.text.slice(0, VERDICT_CLAMP) + '…'
  return (
    <section class={`dt-verdict is-${v.tone}`}>
      <div class="dt-verdict-main">
        <span class="dt-verdict-dot" aria-hidden="true" />
        <p class="dt-verdict-text">{shown}</p>
      </div>
      {long && (
        <button type="button" class="dt-verdict-more" onClick={() => setOpen((o) => !o)}>
          {open ? t('events.collapse') : t('detail.verdict.more')}
        </button>
      )}
      {v.metrics.length > 0 && (
        <dl class="dt-verdict-metrics">
          {v.metrics.map(([label, value]) => (
            <div key={label} class="dt-verdict-metric">
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  )
}

/** First line of a multi-line output, shortened to something a sentence can
 *  carry. Used when a successful run has no verdict of its own. Markdown
 *  decoration is dropped: a report opening on `## 测试结果` was quoted as
 *  "## 测试结果：通过", hashes and all. */
function firstLine(s: string): string {
  const line = plainLine(s.trim().split('\n')[0]?.trim() ?? '')
  return line.length > 160 ? line.slice(0, 160) + '…' : line
}

/** SelfMergeCard renders a self-evolution merge proposal: the signed patch
 *  set's manifest (producer, base, target version, the RECOMPUTED critical
 *  flag) plus each patch file's diff behind an expander — the artifact the
 *  reviewer's approve/reject actually decides on. Only renders for tasks
 *  whose spec carries the self_merge envelope. */
function SelfMergeCard({ spec }: { spec?: string }) {
  const env = spec ? parseResult(spec) : null
  const setID = env && env.kind === 'self_merge' ? String(env.patchset || '') : ''
  const [open, setOpen] = useState(false)
  const [patchOpen, setPatchOpen] = useState<Record<string, boolean>>({})
  const { data: set } = useAsync(() => (setID ? api.selfEvoSet(setID) : Promise.resolve(null)), [setID])
  if (!setID || !set) return null
  return (
    <section class="dt-sec">
      <div class="dt-sec-head">
        <h2 class="dt-sec-title">
          {t('detail.selfmerge.title')}
          {set.critical && <span class="badge red">{t('detail.selfmerge.critical')}</span>}
        </h2>
        <span class="dt-sec-hint">
          {set.files.length} {t('detail.selfmerge.patches')}
          {set.gate_ok ? ` · ${t('detail.selfmerge.gateOk')}` : ` · ${t('detail.selfmerge.gateNone')}`}
        </span>
        <button type="button" class="dt-sec-action" onClick={() => setOpen((v) => !v)}>
          {open ? t('events.collapse') : t('detail.spec.expand')}
        </button>
      </div>

      <dl class="dt-spec-foot">
        <dt>{t('detail.selfmerge.producer')}</dt>
        <dd class="mono">{set.producer}</dd>
        <dt>{t('detail.selfmerge.base')}</dt>
        <dd class="mono">{set.base}</dd>
        <dt>{t('detail.selfmerge.version')}</dt>
        <dd class="mono">{set.version}</dd>
      </dl>
      {set.critical && set.critical_paths && set.critical_paths.length > 0 && (
        <p class="gate-error">
          {t('detail.selfmerge.critical')}: <span class="mono">{set.critical_paths.join(', ')}</span>
        </p>
      )}

      {open && (
        <ul class="diff-files">
          {set.files.map((f) => (
            <li key={f}>
              <button
                type="button"
                class="dt-sec-action"
                onClick={() => setPatchOpen((m) => ({ ...m, [f]: !m[f] }))}
              >
                {patchOpen[f] ? '▾' : '▸'}
              </button>
              <span class="mono">{f}</span>
              {patchOpen[f] && <SelfMergePatch id={set.id} name={f} />}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/** SelfMergePatch lazy-loads one patch file's unified diff. */
function SelfMergePatch({ id, name }: { id: string; name: string }) {
  const { data, error } = useAsync(() => api.selfEvoFile(id, name), [id, name])
  if (error) return <p class="gate-error">{String(error)}</p>
  if (!data) return <p class="dim">…</p>
  return <DiffViewer patch={data} />
}

/** The failure that parked a task in review: the newest review event carrying
 *  a `reason`. Rejections log `rejected` instead and are skipped, so a task
 *  that was rejected once and parked again still shows the live reason. */
function reviewReason(events: { type: string; data?: string }[]): string {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (!ev || ev.type !== 'review') continue
    const r = text(parseResult(ev.data ?? '')?.reason)
    if (r) return r
  }
  return ''
}

/** Wall-clock the run held the queue. Only for settled tasks: an open one
 *  would read as a duration and then keep growing while the page sits. */
function elapsedLabel(created: string, updated: string, open: boolean): string {
  if (open || !created || !updated) return ''
  const ms = new Date(updated).getTime() - new Date(created).getTime()
  if (!Number.isFinite(ms) || ms <= 0) return ''
  return formatDuration(ms)
}
