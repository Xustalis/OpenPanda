// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type PlanDetail, type Task } from '../../api/client'
import { ArtifactSection } from '../../components/artifact'
import { ChatAvatar, PlanCast, TaskCast, TaskDelivery, usePlanBrief, useTaskBrief } from '../../components/chat-cast'
import { prettyAgent, roundTally, sharedTitlePrefix, speakerOf, stateTone, stripRefId, type RunSummary } from '../../components/chat-speaker'
import { MessageCopyButton } from '../../components/copy'
import { EventTimeline } from '../../components/event-timeline'
import { ExecutionSteps } from '../../components/execution-steps-view'
import { operationSteps } from '../../components/execution-steps'
import { Icon } from '../../components/icons'
import { UserFiles } from '../../components/msg-files'
import { splitReportHead } from '../../components/report-head'
import { clockStamp, clockTime } from '../../format/time'
import { useAsync, useChangeSignal, useTaskEvents } from '../../hooks'
import { t, locale } from '../../i18n'
import { Markdown } from '../../md/render'
import { TaskApproval, TaskQuickSend } from './task-actions'
import type { ChatMsg } from './transcript-state'

// ErrHints is the troubleshooting fold under a classified model error: the
// kind names its bullet list in i18n; the provider's own detail heads it when
// one arrived. A browser-native <details> needs no state and reopens across
// rerenders for free.
function ErrHints({ kind, detail }: { kind: string; detail?: string }) {
  const raw = t(`err.hints.${kind}`)
  const known = raw !== `err.hints.${kind}`
  const lines = (known ? raw : t('err.hints.generic')).split('\n').filter(Boolean)
  return (
    <details class="err-hints">
      <summary class="dim">{t('err.hints.title')}</summary>
      {detail && <p class="dim err-hint-detail">{t('err.hints.detail', { detail })}</p>}
      <ul class="err-hint-list">
        {lines.map((line, i) => (
          <li key={i}>{line.replace(/^-\s*/, '')}</li>
        ))}
      </ul>
    </details>
  )
}

/** Wall-clock for the foot of a finished reply. Sub-minute waits keep one
 *  decimal — 2.4s and 2.9s are different answers to "how long did that take" —
 *  and past that it walks the same ladder the rest of the console uses. */
/** A turn's wall clock, in the two-unit form the reply header shows it:
 *  "41.2秒", "2分钟 41秒", "1小时 4分钟". Seconds stay next to minutes because
 *  a turn is a short thing — "2分钟" alone hides the half of it you actually
 *  sat through. Rounds once, up front, so 119.7s cannot print "1分钟 60秒". */
function fmtDuration(ms: number): string {
  const total = Math.max(0, ms) / 1000
  if (total < 60) return `${total.toFixed(1)}${t('time.unit.s')}`
  const whole = Math.round(total)
  if (whole < 3600) {
    return `${Math.floor(whole / 60)}${t('time.unit.m')} ${whole % 60}${t('time.unit.s')}`
  }
  return `${Math.floor(whole / 3600)}${t('time.unit.h')} ${Math.floor((whole % 3600) / 60)}${t('time.unit.m')}`
}

/** Token counts in the reply header: exact below a thousand, one decimal in
 *  the thousands, one decimal in the millions. A live count that printed all
 *  seven digits would shove the rest of the header row around every update. */
function fmtTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1_000_000).toFixed(1)}m`
}

/** The reply's timer while the turn is still running: one tick a second, in the
 *  row the finished duration will occupy.
 *
 *  Its own component rather than a number computed by the parent, so the
 *  re-render each second touches this bubble only — a long thread holds
 *  hundreds of messages, and re-rendering all of them to advance one number is
 *  felt while scrolling.
 *
 *  It formats through `fmtDuration`, the same function the finished line uses,
 *  so the number does not change shape the moment the turn lands.
 *
 *  `last` is the newest delta/reasoning/usage/status timestamp. Once the turn
 *  has gone quiet past a few seconds, the same one-second tick adds how long,
 *  so a slow model reads differently from a dead connection. Before any event
 *  it says the turn is waiting on the model, which is the prefill window. */
function LiveElapsed({ from, last }: { from: number; last?: number }) {
  const [ms, setMs] = useState(() => Date.now() - from)
  const [idle, setIdle] = useState(0)
  // last changes on every delta; a ref keeps it out of the effect's dependency
  // list so the one-second timer is not torn down and rebuilt constantly.
  const lastRef = useRef(last)
  lastRef.current = last
  useEffect(() => {
    const tick = () => {
      const now = Date.now()
      setMs(now - from)
      setIdle(lastRef.current ? now - lastRef.current : now - from)
    }
    tick()
    const id = setInterval(tick, 1000)
    return () => clearInterval(id)
  }, [from])
  return (
    <>
      <span class="msg-dur">{t('sessions.elapsed', { d: fmtDuration(ms) })}</span>
      {idle >= 5000 ? (
        <span class="msg-dur" data-testid="msg-quiet">
          {last
            ? t('sessions.quiet', { d: fmtDuration(idle) })
            : t('sessions.awaiting', { d: fmtDuration(idle) })}
        </span>
      ) : null}
    </>
  )
}

/** Transcript placeholder while a thread loads: rows in the shape of the
 *  messages about to replace them. An empty pane reads as a broken request,
 *  and the hero would claim a thread with history is brand new. */
export function ChatSkeleton() {
  return (
    <div class="chat-skeleton" aria-hidden="true">
      {[68, 90, 52].map((w, i) => (
        <div class="skel-msg" key={i}>
          <div class="skel-avatar" />
          <div class="skel-lines">
            <div class="skel-line" style={{ width: `${w}%` }} />
            <div class="skel-line" style={{ width: `${Math.max(w - 24, 28)}%` }} />
          </div>
        </div>
      ))}
    </div>
  )
}

/** Empty state: the greeting, one line of orientation, and three prompts that
 *  fill the composer. The brand mark and fleet strip stay out — the pet is
 *  already standing on the composer — but a blank page gives a first-time
 *  reader no verb, and a chip is cheaper to click than a sentence to invent. */
export function ChatEmpty({
  onPick,
  hasDraft,
}: {
  onPick(text: string): void
  hasDraft: boolean
}) {
  return (
    <div class="chat-empty">
      <div class="chat-empty-cta">
        <h1>{t('sessions.hello')}</h1>
        <p class="chat-empty-hint">{t('sessions.hint')}</p>
        {/* Once there is a draft or an attachment, a suggestion would replace
            work the reader already did; the chips step aside instead. */}
        {!hasDraft && (
          <div class="chat-starters">
            {(['sessions.s1', 'sessions.s2', 'sessions.s3'] as const).map((key) => (
              <button
                type="button"
                class="chat-starter"
                key={key}
                onClick={() => onPick(t(key))}
              >
                {t(key)}
                <Icon name="chevron-right" size={14} />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** A transcript's 查看任务 link opens the orchestration card where the reader
 *  already is. The href stays real so ⌘/ctrl/shift-click and "open in new tab"
 *  still reach the full task page — a plain click is the only one that stays
 *  in the conversation. */
function inPlaceTaskClick(e: MouseEvent, id: string, open: (id: string) => void) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  e.preventDefault()
  open(id)
}

/** The receipt a turn came back with, when it is not the task's own card.
 *
 *  A parked tier-2 task must still render its decision in the same breath it
 *  parked in, and a remembered answer must say so where the user can see it.
 *  The turn that carries this is not always the head of a fanned-out round —
 *  the live result lands on whichever bubble was streaming — so the bubble
 *  asks every member of its group instead of only itself.
 *
 *  `needs_approval` is skipped for a turn with a task ref: that task's card
 *  already draws the approval gate (see `DispatchBlock`), and drawing it twice
 *  would ask the same question twice. */
function MetaRow({ msg }: { msg: ChatMsg }) {
  const res = msg.result
  if (!res?.needs_approval && !res?.denied && !res?.consent_scope && !res?.warning) return null
  return (
    <div class="bubble-slot-row slot-meta">
      {res.needs_approval && res.approval && !msg.ref && (
        <TaskApproval taskId={res.approval.task_id} preselect={res.approval.scope} />
      )}
      {res.denied && <span class="badge red">{t('approval.deniedRemembered')}</span>}
      {res.consent_scope && (
        <span class="dim approval-consent">{t(`approval.consent.${res.consent_scope}`)}</span>
      )}
      {/* Engine advisory — e.g. the dispatch went to a queue nothing drains.
          The string arrives server-localized. */}
      {res.warning && <span class="badge yellow">{res.warning}</span>}
    </div>
  )
}

/** Old finalizer turns predate the ref field. A round with exactly one task
 *  dispatch is unambiguous: the report below it is that task's report, so the
 *  process fold can still open. A fan-out has several candidates and gets no
 *  guess — the wrong process under a summary is worse than none. */
export function reportFallbackRefs(msgs: ChatMsg[]): Map<string, string> {
  const out = new Map<string, string>()
  let refs: string[] = []
  for (const m of msgs) {
    if (m.role === 'user') {
      refs = []
      continue
    }
    if (m.kind === 'task' && m.ref) {
      if (!refs.includes(m.ref)) refs.push(m.ref)
      continue
    }
    if (m.kind === 'answer' && !m.ref && refs.length === 1 && splitReportHead(m.text ?? '')) {
      out.set(m.k, refs[0]!)
    }
  }
  return out
}

/** One row of the transcript, drawn the way a group chat draws it.
 *
 *  A thread has more than one voice in it: you ask, the panda works out how to
 *  split the job and who gets it, an employee does it and reports back, and the
 *  panda closes the round. Drawing all of that as one anonymous column — a
 *  duration caption, a task card, and a `[done] @agent` line with nobody
 *  attached — is what made a finished task read like a log file. So a row now
 *  carries what a chat message carries: a face, a name, what that speaker is
 *  doing in the conversation, and a bubble of their own.
 *
 *  The speaker is derived, never stored (see components/chat-speaker.ts): a
 *  dispatch turn is the panda orchestrating, a report head that names an
 *  employee is that employee reporting, and everything else is the panda. */
function ChatBubble(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  /** Open a finished task's page in the dock's browser panel. */
  onPreviewTask?: (task: Task) => void
  /** How long the turn behind this reply took, once it is done. The local
   *  measurement comes from `turnMs` (it is the only one that exists between
   *  the reply landing and the transcript being re-fetched); `duration_ms`
   *  from the stored turn is what survives a reload. */
  elapsedMs?: number
  /** Wall-clock start of the turn still streaming, or 0. When set and this
   *  bubble is the streaming one, the timer runs here instead of printing a
   *  fixed number. */
  liveFrom?: number
  /** The plan a plan turn started. */
  plan?: PlanDetail | null
  /** The rest of a fanned-out round: the sibling dispatch turns this reply
   *  absorbed (see `groupDispatches`). Drawn under this one's card, in the same
   *  bubble, so the round reads as one statement. */
  batch?: ChatMsg[]
  /** The thread's task rows, keyed by id — each `DispatchBlock` reads its own
   *  from here instead of fetching a copy on every change frame. */
  tasksById?: Map<string, Task>
  /** The task a legacy report turn belongs to. Reports written before the
   *  finalizer stamped `ref` have no id of their own; the closest single
   *  dispatch in the round is the best evidence for the process fold. */
  fallbackRef?: string
}) {
  const { msg, onOpenTask, onPreviewTask, elapsedMs, liveFrom = 0, plan = null, batch = [], tasksById, fallbackRef } = props

  const report = msg.kind === 'error' ? null : splitReportHead(msg.text ?? '')
  const speaker = speakerOf(msg, report)
  const isTaskKind = msg.kind === 'task' && Boolean(msg.ref)
  const isPlanKind = msg.kind === 'plan' && Boolean(msg.ref)
  // The dispatch template appends the task id so the model's history keeps it
  // (sessions.go). In a chat row the card is the link and a uuid is not a
  // thing a person reads, so it comes back off.
  const text = isTaskKind || isPlanKind ? stripRefId(msg.text ?? '', msg.ref) : msg.text ?? ''
  const hasTaskStatus = Boolean(msg.streaming && msg.status)
  const hasThinking = Boolean(msg.streaming && !msg.text && !msg.status)
  // Which model wrote this reply. The live stream carries it on the result;
  // the stored turn carries it after a reload (see `Turn.Model`).
  const replyModel = msg.result?.entry_model || msg.model || ''
  // The round's own display-only state lands on whichever bubble was streaming
  // when it finished — for a fan-out that is the last absorbed sibling, not
  // the head (see `isBareDispatch`). Read it from the whole group so the fold
  // and the meta row do not vanish with the bubble that used to own them.
  const group = [msg, ...batch]
  const thought = group.find((m) => m.thought)?.thought ?? ''

  const name =
    speaker.who === 'agent'
      ? prettyAgent(speaker.agent)
      : speaker.who === 'user'
        ? t('sessions.you')
        : t('sessions.speakerPanda')
  // An employee's chip is the state of their job — that is what the row is
  // about. Everyone else's chip names the part they are playing.
  const chip =
    report && speaker.role === 'report'
      ? t(`state.${report.state}`)
      : t(`sessions.role.${speaker.role}`)
  const chipState = report && speaker.role === 'report' ? report.state : speaker.role

  return (
    <div class={`msg ${speaker.who} is-${speaker.role}`}>
      {speaker.who !== 'user' && <ChatAvatar speaker={speaker} />}
      <div class="msg-body">
        {/* — the head: who is talking, what they are doing here, and when.
              The duration used to sit above the reply as a caption with a
              hairline under it; in a chat it belongs beside the clock.
              Your own row keeps only the clock: the right side of the column
              already says who it is, and a face of your own beside it would be
              saying it a second time. */}
        <div class="msg-head">
          {speaker.who !== 'user' && (
            <>
              <span class="msg-name">{name}</span>
              <span class={`msg-role is-${chipState}`}>{chip}</span>
            </>
          )}
          <span class="msg-stamp">
            {msg.ts ? (
              <time
                class="msg-time"
                dateTime={new Date(msg.ts * 1000).toISOString()}
                title={clockStamp(msg.ts, locale())}
              >
                {clockTime(msg.ts, locale())}
              </time>
            ) : null}
            {msg.streaming && liveFrom > 0 ? (
              <LiveElapsed from={liveFrom} last={msg.lastActivity} />
            ) : elapsedMs !== undefined ? (
              <span class="msg-dur">{t('sessions.elapsed', { d: fmtDuration(elapsedMs) })}</span>
            ) : null}
            {msg.tokens ? (
              <span class="msg-dur" data-testid="msg-tokens">
                {t('sessions.tokens', {
                  n: (msg.tokensEstimated ? '~' : '') + fmtTokens(msg.tokens),
                })}
              </span>
            ) : null}
          </span>
        </div>

        <div
          class={`msg-bubble${speaker.who === 'user' ? ' is-you' : ''}${
            isTaskKind || isPlanKind || report ? ' is-wide' : ''
          }`}
        >
          {/* — what the panda is thinking on its way to a plan. It streamed
                into state and never reached the page before this; a reader who
                wants to know why the job was cut up the way it was gets to
                look. Open while it is still arriving, folded once the reply
                lands, because by then the answer is what matters. */}
          {thought && (
            <ExecutionSteps scope={'thought:' + (msg.operation_id || msg.k)} steps={[{ id: 'reasoning', title: t('sessions.thought'), status: 'info', ts: 0, thought: true, output: thought }]} />
          )}

          {!!msg.activities?.length && <ExecutionSteps scope={'operation:' + (msg.operation_id || msg.k)} steps={operationSteps(msg.activities)} />}
          {msg.role === 'user' ? (
            // The attachments ride in the sentence, the way the composer
            // showed them while typing — a pill before the words it belongs
            // to, never a block of its own.
            (msg.text || msg.files) && (
              <p class="msg-text">
                {msg.files && <UserFiles files={msg.files} />}
                {msg.text}
              </p>
            )
          ) : msg.kind === 'error' ? (
            <p class="msg-text">{msg.text}</p>
          ) : report ? (
            // The employee's report: the job as its subject, their own closing
            // paragraph under it. The state is on the head's chip.
            <>
              {report.title && <p class="msg-subject">{report.title}</p>}
              {(msg.ref || fallbackRef) ? <TaskDelivery
                taskId={(msg.ref || fallbackRef)!} delivery={msg.delivery}
                truncated={msg.delivery_truncated} summary={report.body} state={report.state}
                verdict={msg.verdict}
                attemptId={msg.delivery_attempt_id} reportedAt={msg.ts}
              /> : report.body && <Markdown text={report.body} class="msg-text" />}
            </>
          ) : isTaskKind ? (
            // The orchestration: who got the job, which stage, how it stands —
            // and, for a fanned-out round, the same for every sibling in the
            // bubble below it.
            <>
              <DispatchBlock
                msg={msg}
                onOpenTask={onOpenTask}
                onPreviewTask={onPreviewTask}
                tasksById={tasksById}
              />
              {batch.map((b) => (
                <DispatchBlock
                  key={b.k}
                  msg={b}
                  onOpenTask={onOpenTask}
                  onPreviewTask={onPreviewTask}
                  tasksById={tasksById}
                />
              ))}
            </>
          ) : (
            <>
              {text && <Markdown text={text} class="msg-text" streaming={msg.streaming} />}
              {isPlanKind && plan && <PlanCast plan={plan} />}
            </>
          )}

          {/* The task card lives inside `DispatchBlock` — it is part of the
                per-task block, so a round that handed out five jobs draws five
                of them in this one bubble. */}

          {/* The live ask path carries its approval state on the result, not
              through a persisted turn ref — a parked tier-2 task must still
              render its decision card in the same breath it parked in, and a
              remembered answer must say so where the user can see it. */}
          {/* One row per group member that has something to say: the round's
              receipt can sit on any of the absorbed turns (see
              `isBareDispatch`), and dropping the row because the head is empty
              would swallow an approval or an advisory. `MetaRow` renders
              nothing for a member with no meta, so the common case is one
              empty fragment. */}
          {group.map((m) => (
            <MetaRow key={`meta-${m.k}`} msg={m} />
          ))}

          {/* — slot-hints: troubleshooting fold under a classified model error.
              err_kind is also persisted on the turn, so a reloaded thread
              shows the same fold the live stream drew. */}
          {msg.kind === 'error' && msg.err_kind && (
            <ErrHints kind={msg.err_kind} detail={msg.err_detail} />
          )}
          {msg.kind === 'error' && msg.code === 'model_config' && (
            <a class="btn small" href="#/settings?tab=models">
              {t('sessions.fixModel')}
            </a>
          )}

          {/* — the live state, at the foot of the bubble: thinking, or what the
                turn is doing right now. */}
          {(hasThinking || hasTaskStatus) && (
            <div class="bubble-slot-row slot-status" role="status" aria-live="polite" aria-atomic="true">
              {hasThinking && (
                <span class="status-line u-color-tert">
                  <span class="spinner" aria-hidden="true" />
                  {t('sessions.thinking')}
                </span>
              )}
              {hasTaskStatus && (
                <span class="status-line u-color-tert">
                  <span class="spinner" aria-hidden="true" />
                  {msg.status}
                </span>
              )}
            </div>
          )}
        </div>

        {/* The reply is copied as the Markdown it was written in, the way
            `/copy` does in the TUI — the rendered text would lose its
            structure the moment it is pasted anywhere. It stays away while
            the turn is still streaming: half an answer is not the message. */}
        {msg.text && !msg.streaming && (
          <div class="msg-foot">
            <MessageCopyButton text={msg.delivery ? `${msg.text.split('\n', 1)[0]}\n\n${msg.delivery}` : msg.text} markdown={msg.kind !== 'error'} />
            {/* A task's own card already carries the employee in its cast
                line; this is the plain-answer case. */}
            {!isTaskKind && speaker.who === 'panda' && replyModel && (
              <span class="slot-model">{replyModel}</span>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/** One dispatched task inside a reply: its dispatch line, the cast row naming
 *  the employee and the stage, and the card that opens it.
 *
 *  It is a block rather than part of `ChatBubble` because a single reply can
 *  carry several: a round that fanned out is filed as one turn per task and
 *  drawn as one bubble, so the bubble holds one of these per job. The task row
 *  is read here, per block — the only place that knows who is actually running
 *  it. */
function DispatchBlock(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  /** Open a finished task's page in the dock's browser panel. */
  onPreviewTask?: (task: Task) => void
  tasksById?: Map<string, Task>
}) {
  const { msg, onOpenTask, onPreviewTask, tasksById } = props
  const [chainOpen, setChainOpen] = useState(true)
  // The thread's list is polled once per frame already; reading this block's
  // row from it keeps a five-card round from issuing five extra requests per
  // frame. The own read is only for a task outside the list.
  const listed = msg.ref ? tasksById?.get(msg.ref) : undefined
  const fetched = useTaskBrief(listed ? undefined : msg.kind === 'task' ? msg.ref : undefined)
  const task = listed ?? fetched
  const text = stripRefId(msg.text ?? '', msg.ref)
  return (
    <div class="msg-batch-item" data-task-id={msg.ref}>
      {text && <Markdown text={text} class="msg-text" />}
      {task && <TaskCast task={task} />}
      {msg.ref && (
        <div class="task-card u-flex-1 u-min-w-0">
          {/* A task that needs consent must be decidable where the user is
              looking. Without this the conversation only said "review" and the
              decision lived on another page — so it waited, or worse, was never
              seen. Nothing else may answer it on the user's behalf: this is the
              web twin of the TUI's approval card. */}
          <TaskApproval taskId={msg.ref} hint={task} />
          {msg.result?.report && <Markdown text={msg.result.report} class="task-report" />}
          {msg.result?.stdout && <TaskOutput text={msg.result.stdout} />}
          {/* One quiet row of controls: open the chain, talk to the employee,
              jump into the task. As three stacked children of the card's column
              they read as a form — a chip-shaped toggle above a full-width
              centred button — instead of what they are, three links about the
              same job. */}
          <div class="task-card-actions">
            <button class="chain-toggle" onClick={() => setChainOpen((v) => !v)}>
              {chainOpen ? t('sessions.chainHide') : t('sessions.chainShow')}
            </button>
            <TaskQuickSend taskId={msg.ref} hint={task} />
            <a
              class="task-card-open"
              href={`#/task/${encodeURIComponent(msg.ref)}`}
              onClick={(e) => inPlaceTaskClick(e, msg.ref!, onOpenTask)}
            >
              {t('sessions.viewTask')} →
            </a>
          </div>
          {chainOpen && <TaskChain taskId={msg.ref} />}
          {/* The task's output, where the result is read: the file list, the
              downloads, and the page view on the preview origin. It hangs off
              the card rather than the task page because the moment a user
              wants to look at what a task produced is the moment it reports. */}
          {task?.plan_meta?.output_artifact && (
            <ArtifactSection task={task} onPreview={onPreviewTask} />
          )}
        </div>
      )}
    </div>
  )
}

/** A chat row. The employee's own progress row does *not* hang off it any
 *  more: a still-running job belongs at the foot of the thread, not under the
 *  bubble that dispatched it (see `liveTaskIds` / `TaskProgress`). */
export function ChatTurn(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  /** Open a finished task's page in the dock's browser panel. */
  onPreviewTask?: (task: Task) => void
  elapsedMs?: number
  liveFrom?: number
  batch?: ChatMsg[]
  /** The session's own rows for this thread's tasks, keyed by id. It is
   *  fresher than a fetch and costs no request; a block whose task is not in
   *  the list (one delegated elsewhere, or the first paint before the list
   *  lands) falls back to its own read. */
  tasksById?: Map<string, Task>
  fallbackRef?: string
}) {
  const { msg } = props
  const plan = usePlanBrief(msg.kind === 'plan' ? msg.ref : undefined)
  return <ChatBubble {...props} plan={plan} />
}

/** Whether the thread's round still has work in flight.
 *
 *  The roll-up under the transcript says "本轮工作已全部完成", so it may only
 *  appear once every task this thread dispatched has settled. The turns alone
 *  cannot answer that — the finalizer files one report per task as each one
 *  lands, so the newest turn is a finished employee while its colleagues are
 *  still running (see `roundStillOpen`). Nor is every `review` row settled: a
 *  row parked before execution — 待审批, "尚未执行——批准后将继续执行" — has not
 *  done its work yet, so it holds the round open the same way a running row
 *  does. The task board knows both, so it is read here and re-read on every
 *  change frame. Only a thread that dispatched something pays for the read.
 *
 *  The same list answers the other half of the question — which employees are
 *  working right now, see `liveTaskIds`. It is one read, not two. */
export function useSessionTasks(sessionId: string | null, enabled: boolean): Task[] | null {
  const change = useChangeSignal()
  const { data } = useAsync<Task[] | null>(
    () =>
      enabled && sessionId
        ? api.tasks({ session: sessionId, brief: true }).catch(() => null)
        : Promise.resolve(null),
    [sessionId, enabled],
    change,
  )
  return data
}

/** The panda's closing line for a round that just finished.
 *
 *  The employees have each said what they did; what a round still owes the
 *  reader is the roll-up — it is over, how the batch split, this many people
 *  worked on it, here is where the work lives. It is derived from the turns
 *  already in the thread (see `trailingRunSummary`), not a stored message:
 *  nothing new is written to the transcript, so the model's own history is
 *  untouched. */
export function RunSummaryRow({ run, onOpenTask }: { run: RunSummary; onOpenTask(id: string): void }) {
  // A round is a batch. Reading its verdict off the last report alone said
  // "已失败" over two employees who finished, so the split is counted here and
  // the headline names it: all home is "done", some home is "finished — 2 done,
  // 1 failed", nothing home keeps the single-state line.
  const total = run.outcomes.length
  const ok = run.outcomes.filter((s) => s === 'done').length
  const mixed = ok > 0 && ok < total
  const line = mixed
    ? t('sessions.runSummary.partial', {
        parts: roundTally(run.outcomes)
          .map(({ state, n }) => t(`sessions.runSummary.tally.${state}`, { n: String(n) }))
          .join(t('sessions.runSummary.sep')),
      })
    : ok === total && total > 0
      ? t('sessions.runSummary.done')
      : t('sessions.runSummary.ended', { state: t(`state.${run.state}`) })
  // The dot carries the batch's tone, not the last employee's: one failure
  // among successes is amber, and only a round where nobody finished is red.
  const tone = mixed ? 'warn' : ok === total && total > 0 ? 'ok' : stateTone(run.state)
  // Three jobs off one dispatch template share a head ("连通性测试：调用
  // agent:") that the reader does not need three times.
  const prefix = sharedTitlePrefix(run.titles)
  const titles = prefix ? run.titles.map((title) => title.slice(prefix.length).trim()) : run.titles
  const ref = run.ref
  const isTaskRef = run.refKind === 'task'
  const isPlanRef = run.refKind === 'plan'
  return (
    <div class="msg panda is-summary">
      <ChatAvatar speaker={{ who: 'panda', agent: '', role: 'summary' }} />
      <div class="msg-body">
        <div class="msg-head">
          <span class="msg-name">{t('sessions.speakerPanda')}</span>
          <span class="msg-role is-summary">{t('sessions.role.summary')}</span>
        </div>
        <div class="msg-bubble is-summary">
          <p class="msg-summary-line">
            <span class={`msg-cast-state is-${tone}`}>
              <span class="msg-report-dot" aria-hidden="true" />
              {line}
            </span>
            {/* The mixed headline already counts everyone; repeating the
                headcount under it would be the same number twice. */}
            {!mixed && run.agents.length > 0 && (
              <span class="msg-summary-people">
                {t('sessions.runSummary.people', { n: String(run.agents.length) })}
              </span>
            )}
          </p>
          {/* A single job is already named by the row above; the recap only
              earns its line when the round covered more than one. */}
          {titles.length > 1 && <p class="msg-summary-what">{titles.join(' · ')}</p>}
          {ref && isTaskRef && (
            <a
              class="msg-summary-link"
              href={`#/task/${encodeURIComponent(ref)}`}
              onClick={(e) => inPlaceTaskClick(e, ref, onOpenTask)}
            >
              {t('sessions.viewTask')} →
            </a>
          )}
          {ref && isPlanRef && (
            <a class="msg-summary-link" href="#/control?tab=plans">
              {t('sessions.castPlan')} →
            </a>
          )}
        </div>
      </div>
    </div>
  )
}

/** Agent result text under a task bubble — markdown like the reply lane,
 *  collapsed past a few lines so a long dump never pushes the chat down. */
function TaskOutput({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const long = text.length > 600
  const shown = open || !long ? text : text.slice(0, 600) + '…'
  return (
    <div class="task-out-wrap">
      <Markdown text={shown} class="task-out" />
      {long && (
        <button type="button" class="btn small" onClick={() => setOpen((v) => !v)}>
          {open ? t('sessions.outputCollapse') : t('sessions.outputExpand')}
        </button>
      )}
    </div>
  )
}

/** The task's thinking chain: the task_events log replayed live — history
 *  comes from GET /api/tasks/{id}/logs, and the panel's SSE change signal
 *  triggers a refetch while the chain is open (design §5). */
function TaskChain({ taskId }: { taskId: string }) {
  const change = useChangeSignal()
  const { events, error } = useTaskEvents(taskId, change)
  if (error) return <p class="dim chain-empty">{t('common.error')} ({error})</p>
  if (events.length === 0) return <p class="dim chain-empty">{t('sessions.chainEmpty')}</p>
  return <EventTimeline scope={'task:' + taskId} events={events} className="task-chain-timeline" />
}
