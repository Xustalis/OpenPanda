// SPDX-License-Identifier: AGPL-3.0-or-later
import type { ComponentChildren } from 'preact'
import { useId, useLayoutEffect, useMemo, useState } from 'preact/hooks'
import type { TaskEvent, TraceEvent } from '../api/client'
import { useChangeSignal, useTaskEvents } from '../hooks'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { toolArgSummary } from './event-parser'
import { expansion, rememberExpansion } from './execution-expansion'
import type { ExecutionStep } from './execution-steps'
import { StepContent } from './execution-steps-view'
import { Icon } from './icons'
import { mergeLive } from './task-live'
import { activityToolKind, turnActivitySteps } from './turn-activity'
import './task-turn-activity.css'

const NO_TRACES: TraceEvent[] = []

export function TaskActivityFold({ scope, label, active, children }: {
  scope: string; label: string; active?: boolean; children: ComponentChildren
}) {
  const key = 'turn-activity:' + scope
  const [open, setOpen] = useState(() => expansion(key).open)
  const id = useId()
  return <div class="task-turn-activity">
    <button type="button" class="task-turn-activity-toggle" aria-expanded={open} aria-controls={id}
      onClick={() => setOpen(value => rememberExpansion(key, { open: !value, rows: {} }).open)}>
      <span class={active ? 'task-turn-working' : undefined}>{label}</span>
      <Icon name={open ? 'chevron-down' : 'chevron-right'} size={14} />
    </button>
    <div id={id} hidden={!open}>{open && children}</div>
  </div>
}

export function TaskActivityLog({ taskId, active = false, attemptId, finalText, traces = NO_TRACES, details }: {
  taskId: string; active?: boolean; attemptId?: string; finalText?: string; traces?: TraceEvent[]
  details?(events: TaskEvent[]): ComponentChildren
}) {
  const change = useChangeSignal()
  const { events, error } = useTaskEvents(taskId, change)
  // Batch the live presentation, not the transport. A terminal render takes
  // the authoritative snapshot immediately instead of waiting for the frame.
  const [frame, setFrame] = useState(() => ({ events, traces }))
  useLayoutEffect(() => {
    if (!active) return
    const id = requestAnimationFrame(() => setFrame({ events, traces }))
    return () => cancelAnimationFrame(id)
  }, [active, events, traces])
  const history = active ? frame.events : events
  const stream = active ? frame.traces : traces
  const timeline = useMemo(() => mergeLive(history, stream).map(event => ({
    id: event.id, ts: event.ts, type: event.type,
    data: event.raw || JSON.stringify(event.data ?? {}),
  })), [history, stream])
  const steps = useMemo(() => turnActivitySteps(timeline, active, attemptId, finalText),
    [timeline, active, attemptId, finalText])
  return <div class="task-turn-log">
    {steps.length > 0 && <ol class="task-turn-timeline" aria-label={t('sessions.processTitle')}>
      {steps.map((step, i) => <li key={step.id} class={active ? 'is-live' : undefined}>
        {step.kind === 'text'
          ? <Markdown text={step.output ?? ''} class="msg-text task-turn-commentary"
              streaming={active && i === steps.length - 1} reveal />
          : step.kind === 'gap'
            ? <p class="task-run-empty">{t(step.unavailable ? 'work.unavailable' : 'events.transcriptTruncatedNote')}</p>
            : <ActivityTool step={step} />}
      </li>)}
    </ol>}
    {error && <p class="task-run-empty" role="status">{t('sessions.deliveryStepsUnavailable')}</p>}
    {!error && steps.length === 0 && <p class="task-run-empty">
      {t(active ? 'sessions.liveWaiting' : 'sessions.processEmpty')}
    </p>}
    {details?.(history)}
  </div>
}

function ActivityTool({ step }: { step: ExecutionStep }) {
  const [open, setOpen] = useState(false)
  const kind = activityToolKind(step)
  const state = step.status === 'running' ? 'running' : step.status === 'success' ? 'done'
    : step.status === 'failed' ? 'failed' : 'unknown'
  const arg = kind === 'image' ? '' : toolArgSummary(step.input).replace(/\s+/g, ' ').trim()
  return <details class="task-turn-tool" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary class={'task-turn-tool-line is-' + state}>
      <Icon name={state === 'failed' ? 'alert' : kind === 'image' ? 'image' : kind === 'command' ? 'terminal' : 'code'} size={16} />
      <span>{kind === 'tool' ? step.title
        : t(`sessions.activity.${kind}.${state}`)}</span>
      {arg && <span class="task-turn-tool-arg">{arg}</span>}
    </summary>
    {open && <div class="task-turn-tool-detail"><StepContent step={step} /></div>}
  </details>
}
