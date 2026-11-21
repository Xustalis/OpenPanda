// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { Task } from '../../api/client'
import { ArtifactSection } from '../../components/artifact'
import { ChatAvatar, TaskDelivery, TaskProcessBody } from '../../components/chat-cast'
import { dispatchAgent, prettyAgent, stripRefId } from '../../components/chat-speaker'
import { operationSteps } from '../../components/execution-steps'
import { ExecutionSteps } from '../../components/execution-steps-view'
import { useFileOpener } from '../../components/file-link'
import { Icon } from '../../components/icons'
import { splitReportHead } from '../../components/report-head'
import { receiptCopy } from '../../components/task-delivery'
import { isStalledReview } from '../../components/task-failure'
import { mergeLive, recentAttemptSteps } from '../../components/task-live'
import { clockTime, formatDuration } from '../../format/time'
import { useChangeSignal, useTaskEvents, useTraceForTask } from '../../hooks'
import { locale, t } from '../../i18n'
import { Markdown } from '../../md/render'
import { TaskApproval, TaskCancel, TaskQuickSend } from './task-actions'
import { currentRoundReport, roundReportText, type RoundJob, type TaskRound } from './task-round-state'

type OpenTask = (id: string) => void
type PreviewTask = (task: Task) => void
export type OpenRoundReport = (key: string, origin: HTMLButtonElement, taskId?: string) => void
export interface RoundReportSelection { key: string; taskId?: string }
export const ROUND_REPORT_ID = 'session-round-report-pane'

const terminal = new Set(['done', 'failed', 'cancelled', 'expired'])
const emptyTraces: NonNullable<Task['traces']> = []

function TaskLink({ id, onOpenTask }: { id: string; onOpenTask: OpenTask }) {
  return <a href={`#/task/${encodeURIComponent(id)}`} onClick={ev => {
    if (ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return
    ev.preventDefault()
    onOpenTask(id)
  }}>{t('sessions.viewTask')} →</a>
}

function Outputs({ job, onPreview, compact }: { job: RoundJob; onPreview: PreviewTask; compact?: boolean }) {
  const opener = useFileOpener()
  const { task } = job
  if (!task) return null
  if (task.plan_meta?.output_artifact && !compact) return <ArtifactSection key={task.plan_meta.output_artifact} task={task} onPreview={onPreview} />
  const root = task.work_dir
  const deleted = new Set(task.files_stat?.filter(file => file.kind === 'deleted').map(file => file.path))
  const paths = task.files_changed?.filter(path => !deleted.has(path)) ?? []
  if (!opener || !root || !paths.length) return null
  return <ul class="session-round-files">{(compact ? paths.slice(0, 2) : paths).map(path => <li key={path}>
    <button type="button" class="chain-toggle" onClick={() => opener.open({ path: `${root}/${path}`, line: 0 })}><Icon name="file-text" size={14} />{path}</button>
  </li>)}</ul>
}

function jobTitle(job: RoundJob) {
  return job.task?.title || stripRefId(job.receipt?.text ?? '', job.id) || job.id
}

function jobAgent(job: RoundJob) {
  return job.task?.call_name || splitReportHead(currentRoundReport(job)?.text ?? '')?.agent ||
    (job.task && dispatchAgent(job.task)) || ''
}

function JobSpeaker({ job, finished }: { job: RoundJob; finished?: boolean }) {
  const agent = jobAgent(job)
  const state = job.task?.state
  const stateLabel = job.task && isStalledReview(job.task)
    ? t('detail.stalledReview') : t(state ? `state.${state}` : 'sessions.round.unknown')
  const report = finished ? currentRoundReport(job) : undefined
  const timestamp = report?.ts ?? (job.task ? Date.parse(job.task.updated_at) / 1000 : 0)
  const stamp = finished && Number.isFinite(timestamp) && clockTime(timestamp, locale())
  return <span class="session-round-speaker">
    {agent ? <ChatAvatar speaker={{ who: 'agent', agent, role: 'report' }} /> :
      <span class="session-round-avatar" aria-hidden="true"><Icon name="bot" size={16} /></span>}
    {agent && <span class="msg-name">{agent.includes(':') ? agent : prettyAgent(agent)}</span>}
    <span class={`msg-cast-state${state ? ` is-${state}` : ''}`}>
      <span class="msg-report-dot" aria-hidden="true" />{stateLabel}
    </span>
    {stamp && <span class="session-round-meta">{stamp}</span>}
    {report?.duration_ms ? <span class="session-round-meta">{t('sessions.elapsed', { d: formatDuration(report.duration_ms) })}</span> : null}
  </span>
}

function PandaSpeaker({ settled, ts }: { settled?: boolean; ts?: number }) {
  const stamp = clockTime(ts, locale())
  return <span class="session-round-speaker">
    <ChatAvatar speaker={{ who: 'panda', agent: '', role: 'summary' }} />
    <span class="msg-name">{t('sessions.speakerPanda')}</span>
    <span class={`msg-role is-${settled ? 'summary' : 'plan'}`}>{t(settled ? 'sessions.role.summary' : 'sessions.role.plan')}</span>
    {stamp && <span class="session-round-meta">{stamp}</span>}
  </span>
}

function RoundRow({ children }: { children: ComponentChildren }) {
  return <span class="session-round-row">
    <span class="session-round-row-content">{children}</span>
    <Icon name="chevron-right" size={15} class="session-round-chevron" />
  </span>
}

function RoundFold({ speaker, header, children }: { speaker?: ComponentChildren; header: ComponentChildren; children: ComponentChildren }) {
  const [open, setOpen] = useState(false)
  return <details class={`session-round-fold${speaker ? ' is-message' : ''}`} onToggle={e => setOpen(e.currentTarget.open)}>
    <summary class="session-round-toggle">
      {speaker && <span class="session-round-identity">{speaker}</span>}
      <RoundRow>{header}</RoundRow>
    </summary>
    {open && <div class="session-round-body">{children}</div>}
  </details>
}

function RoundContext({ round }: { round: TaskRound }) {
  const records = [...round.receipts, ...round.notes]
  const activities = new Map(records.flatMap(note => (note.activities ?? []).map(step => [step.id, step] as const)))
  const steps = operationSteps([...activities.values()])
  const thought = [...records].reverse().find(note => note.thought)?.thought
  if (thought) steps.unshift({ id: 'reasoning', title: t('sessions.thought'), status: 'info', ts: 0, thought: true, output: thought })
  return <>
    {round.notes.map(note => note.text && <Markdown key={note.k} class="msg-text" text={note.text} streaming={note.streaming} />)}
    {steps.length > 0 && <ExecutionSteps scope={'operation:' + round.key} steps={steps} />}
  </>
}

function JobReport({ job, onOpenTask, onPreview, showProcess, hideTitle }: { job: RoundJob; onOpenTask: OpenTask; onPreview: PreviewTask; showProcess?: boolean; hideTitle?: boolean }) {
  const task = job.task
  const report = currentRoundReport(job)
  const reports = job.reports.filter(item => item !== report)
  const verification = report?.delivery_status ?? (task?.state === 'done' && task.verification === 'verified' ? 'verified' : 'unverified')
  return <section class="session-round-report" data-task-id={job.id} tabIndex={-1}>
    <div class="session-round-report-head">
      <JobSpeaker job={job} />
      <span class="session-round-meta">{t(`sessions.round.verification.${verification}`)}</span>
    </div>
    {!hideTitle && <h3>{jobTitle(job)}</h3>}
    <div class="session-round-report-links">
      {report?.duration_ms ? <span>{t('sessions.elapsed', { d: formatDuration(report.duration_ms) })}</span> : null}
      {report?.approval_wait_ms ? <span>{t('sessions.round.approvalWait', { d: formatDuration(report.approval_wait_ms) })}</span> : null}
      <TaskLink id={job.id} onOpenTask={onOpenTask} />
    </div>
    <TaskDelivery key={`${job.id}:${task?.attempt_id}:${Boolean(showProcess)}`} taskId={job.id} state={task?.state ?? ''} document startProcess={showProcess}
      summary="" attemptId={task?.attempt_id} reportedAt={report?.ts}
      delivery={report?.delivery || task?.delivery_excerpt}
      truncated={report?.delivery ? report.delivery_truncated : Boolean(task?.delivery_excerpt)}>
      <Outputs job={job} onPreview={onPreview} />
    </TaskDelivery>
    {reports.length > 0 && <RoundFold header={<span class="session-round-meta">{t('sessions.round.previous')}</span>}>
      {reports.map(item => <Markdown key={item.k} class="msg-text" text={item.delivery || item.text} />)}
    </RoundFold>}
  </section>
}

function roundTally(round: TaskRound) {
  return (['done', 'failed', 'cancelled', 'expired'] as const).flatMap(state => {
    const n = round.jobs.filter(job => job.task?.state === state).length
    return n ? [t(`sessions.runSummary.tally.${state}`, { n })] : []
  }).join(t('sessions.runSummary.sep'))
}

function roundTitle(round: TaskRound) {
  const first = round.jobs[0]
  return first && round.jobs.length === 1 ? jobTitle(first) : t('sessions.round.report')
}

function RoundJobProgress({ job, onOpenTask, onPreview }: { job: RoundJob; onOpenTask: OpenTask; onPreview: PreviewTask }) {
  const { task } = job
  return <article class="session-round-job" data-task-id={job.id} data-executing={task?.state === 'running' && task.executing === true}>
    <RoundFold speaker={<JobSpeaker job={job} />} header={<>
      <strong class="session-round-subject">{jobTitle(job)}</strong>
      {task?.state === 'running' && <LiveRoundSteps key={task.attempt_id} task={task} />}
    </>}>
      {task && terminal.has(task.state) ? <JobReport job={job} onOpenTask={onOpenTask} onPreview={onPreview} /> : <>
        <div class="session-round-report-links">
          <TaskLink id={job.id} onOpenTask={onOpenTask} />
          {task && <><TaskQuickSend taskId={job.id} hint={task} /><TaskCancel task={task} /></>}
        </div>
        {task && <TaskProcessBody key={task.attempt_id} taskId={job.id} task={task} />}
      </>}
    </RoundFold>
    {task && <TaskApproval key={task.attempt_id} taskId={job.id} hint={task} />}
  </article>
}

function LiveRoundSteps({ task }: { task: Task }) {
  const change = useChangeSignal()
  const { events, error } = useTaskEvents(task.id, change, 48)
  const traces = useTraceForTask(task.id, emptyTraces)
  const step = recentAttemptSteps(mergeLive(events, traces), task.attempt_id).at(-1)
  return <span class="session-round-live">
    {step ? <span class="session-round-live-step">
      <span class="session-round-meta">{step.label}</span>
      <span>{step.summary}</span>
    </span> : <span class="session-round-meta">{t(error ? 'sessions.deliveryStepsUnavailable' : 'sessions.liveWaiting')}</span>}
  </span>
}

function FinishedReceipt({ job, roundKey, selected, onReport, onPreview }: {
  job: RoundJob; roundKey: string; selected: boolean; onReport: OpenRoundReport; onPreview: PreviewTask
}) {
  const text = roundReportText(job)
  const copy = useMemo(() => receiptCopy(text), [text])
  const id = `receipt-title-${roundKey}-${job.id}`
  return <article class="session-round-receipt" data-task-id={job.id}>
    <div class="session-round-identity"><JobSpeaker job={job} finished /></div>
    <div class="session-round-receipt-body">
      <h3 id={id}>{jobTitle(job)}</h3>
      <p class="session-round-lead">{copy.lead || t('sessions.round.noReportExcerpt')}</p>
      {copy.evidence && <p class="session-round-evidence"><span>{t('sessions.deliveryVerification')}</span>{copy.evidence}</p>}
      {copy.detail && <p class="session-round-detail">{copy.detail}</p>}
      <div class="session-round-receipt-actions">
        <Outputs job={job} onPreview={onPreview} compact />
        <button type="button" class="chain-toggle session-round-receipt-link" aria-controls={ROUND_REPORT_ID}
          aria-expanded={selected} aria-describedby={id} onClick={e => onReport(roundKey, e.currentTarget, job.id)}>
          {t('sessions.liveOpen')} <Icon name="chevron-right" size={13} />
        </button>
      </div>
    </div>
  </article>
}

export function SessionTaskRound({ round, reportSelection, onReport, onOpenTask, onPreview }: {
  round: TaskRound; reportSelection?: RoundReportSelection; onReport: OpenRoundReport; onOpenTask: OpenTask; onPreview: PreviewTask
}) {
  const stamp = [...round.notes].reverse().find(note => note.ts)?.ts ?? round.receipts[0]?.ts
  const onlyTask = round.jobs.length === 1 ? round.jobs[0]?.task : undefined
  return <section class="session-task-round" data-round={round.operation ?? round.key} data-settled={round.settled}>
    {round.settled ? <>
      <div class="session-round-receipts">{round.jobs.map(job => <FinishedReceipt key={job.id} job={job} roundKey={round.key}
        selected={reportSelection?.key === round.key && reportSelection.taskId === job.id} onReport={onReport} onPreview={onPreview} />)}</div>
      <div class="session-round-summary">
        <div class="session-round-identity"><PandaSpeaker settled /></div>
        <button type="button" class="session-round-document" aria-controls={ROUND_REPORT_ID}
          aria-expanded={reportSelection?.key === round.key && !reportSelection.taskId}
          onClick={e => onReport(round.key, e.currentTarget)}>
          <span class="session-round-document-icon"><Icon name="file-text" size={18} /></span>
          <span class="session-round-document-text">
            <strong class="session-round-document-title">{t('sessions.round.report')}</strong>
            <span class="session-round-meta">{[roundTally(round), onlyTask?.files_changed?.length &&
              t('detail.result.files', { n: onlyTask.files_changed.length })].filter(Boolean).join(' · ')}</span>
          </span>
          <Icon name="panel-right" size={17} class="session-round-chevron" />
        </button>
      </div>
    </> : <>
      <RoundFold speaker={<PandaSpeaker ts={stamp} />} header={<>
        <strong class="session-round-subject">{t('sessions.round.dispatched', { n: round.jobs.length })}</strong>
        {round.running >= 2 && <span class="session-round-parallel" data-testid="parallel-execution">{t('sessions.round.parallel')}</span>}
        {round.running > 0 && <span class="session-round-meta">{t('sessions.round.running', { n: round.running })}</span>}
        {round.waiting > 0 && <span class="session-round-meta">{t('sessions.round.queued', { n: round.waiting })}</span>}
      </>}><RoundContext round={round} /></RoundFold>
      {round.receipts.map(receipt => (receipt.warning || receipt.result?.warning) &&
        <p key={receipt.k} class="session-round-warning">{receipt.warning || receipt.result?.warning}</p>)}
      {round.jobs.map(job => <RoundJobProgress key={job.id} job={job} onOpenTask={onOpenTask} onPreview={onPreview} />)}
    </>}
  </section>
}

export function RoundReportPane({ round, selection, onOpenTask, onPreview, onClose }: {
  round: TaskRound; selection: RoundReportSelection; onOpenTask: OpenTask; onPreview: PreviewTask; onClose: () => void
}) {
  const pane = useRef<HTMLElement>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  useLayoutEffect(() => {
    const target = selection.taskId && Array.from(pane.current?.querySelectorAll<HTMLElement>('[data-task-id]') ?? [])
      .find(el => el.dataset.taskId === selection.taskId)
    const focus = target || heading.current
    focus?.focus({ preventScroll: true })
    if (pane.current && focus) pane.current.scrollTop = target ?
      focus.getBoundingClientRect().top - pane.current.getBoundingClientRect().top + pane.current.scrollTop : 0
  }, [selection])
  const hasContext = [...round.notes, ...round.receipts].some(note => note.thought || note.activities?.length) || round.notes.some(note => note.text)
  return <section ref={pane} id={ROUND_REPORT_ID} class="session-round-pane" aria-labelledby={`${ROUND_REPORT_ID}-title`} onKeyDown={e => {
    if (e.key !== 'Escape' || e.defaultPrevented || (e.target as Element | null)?.closest('dialog[open]')) return
    e.preventDefault()
    onClose()
  }}>
    <header class="session-round-pane-head">
      {round.jobs.length === 1 && <span class="session-round-meta"><Icon name="file-text" size={15} />{t('sessions.round.report')}</span>}
      <h2 ref={heading} tabIndex={-1} id={`${ROUND_REPORT_ID}-title`}>{roundTitle(round)}</h2>
      <p class="session-round-meta">{roundTally(round)}</p>
    </header>
    {round.jobs.map(job => <JobReport key={job.id} job={job} showProcess={selection.taskId === job.id}
      onOpenTask={onOpenTask} onPreview={onPreview} hideTitle={round.jobs.length === 1} />)}
    {hasContext && <RoundFold header={<><PandaSpeaker /><span class="session-round-meta">{t('sessions.processTitle')}</span></>}>
      <RoundContext round={round} />
    </RoundFold>}
  </section>
}
