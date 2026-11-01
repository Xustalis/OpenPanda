// SPDX-License-Identifier: AGPL-3.0-or-later

// Three of the task page's four cards — 执行链路, 任务简报, 任务信息.
//
// Each is a `TaskPanel`: a title, the count that says how much is behind the
// fold, a lead that is worth the space either way (the trace strip), and a body
// that opens in place. The 输出 card lives in `task-output.tsx`, which carries
// the result parsing with it.

import type { Task } from '../api/client'
import { t } from '../i18n'
import { StateBadge } from './state-badge'
import { TaskPanel } from './task-panel'
import { InfoSection, SpecCard, TimelineSection, briefShape, fmtFull } from './task-sections'
import { TraceDetail, TraceStrip, useTaskTrace } from './task-trace'

/** Expanded, the chain card scrolls past this: the trace cells, the hop list
 *  and the log are three different lengths, and the page should not grow to
 *  the longest of them. */
const CHAIN_OPEN_PX = 560
/** Two rows of the fact grid. */
const INFO_CLAMP_PX = 96
/** Three lines of brief prose. */
const BRIEF_CLAMP_PX = 78

/** 执行链路: how the run was classified, routed and executed — the strip on the
 *  card's face, the cells and the event log behind the fold. The delegation
 *  hops and the sub-agent list are part of the same answer (where this run
 *  travelled, what it fanned out to), so they live here rather than in the
 *  facts card. */
export function ChainPanel({
  task,
  onOpenTask,
}: {
  task: Task
  onOpenTask?(id: string): void
}) {
  const model = useTaskTrace(task)
  const events = task.events ?? []
  const hops = task.delegation_chain ?? []
  const children = task.children ?? []
  const foldable = model.hasDetail || events.length > 0 || hops.length > 0 || children.length > 0

  return (
    <TaskPanel
      title={t('detail.trace')}
      wide
      hint={
        events.length > 0
          ? t('detail.timeline.count', { n: String(events.length) })
          : undefined
      }
      lead={<TraceStrip model={model} />}
      foldable={foldable}
      openMax={CHAIN_OPEN_PX}
    >
      {model.hasDetail && <TraceDetail model={model} />}

      {hops.length > 0 && (
        <div class="dp-sub">
          <span class="dp-sub-label">{t('detail.chain')}</span>
          <div class="dt-chain">
            <span class="chain-node root">{hops[0]?.from_node}</span>
            {hops.map((hop, i) => (
              <span key={i} style="display:contents">
                <span class="chain-arrow" aria-hidden="true">
                  →
                </span>
                <span
                  class={`chain-node${hop.accepted ? '' : ' unacked'}`}
                  title={`hop ${hop.hop}${hop.via ? ` · ${hop.via}` : ''} · ${fmtFull(new Date(hop.ts * 1000).toISOString())}`}
                >
                  {hop.to_node}
                </span>
              </span>
            ))}
          </div>
        </div>
      )}

      {children.length > 0 && (
        <div class="dp-sub">
          <span class="dp-sub-label">{t('detail.children', { n: String(children.length) })}</span>
          <div class="dt-children">
            {children.map((c) => (
              <button
                key={c.id}
                type="button"
                class="dt-child"
                onClick={() => onOpenTask?.(c.id)}
                title={c.id}
              >
                <StateBadge state={c.state} />
                <span class="dt-child-title">{c.title || c.id}</span>
              </button>
            ))}
          </div>
        </div>
      )}

      {events.length > 0 && (
        <div class="dp-sub">
          <span class="dp-sub-label">{t('detail.timeline')}</span>
          <TimelineSection events={events} suppressResult bare />
        </div>
      )}
    </TaskPanel>
  )
}

/** 任务简报: what the run was asked to produce — target, guardrails, scope and
 *  the bar the supervisor judges against. The card face shows the target's
 *  opening lines; the constraints and the user's own words are behind the
 *  fold. */
export function BriefPanel({ task }: { task: Task }) {
  const shape = briefShape(task.spec, task.intent)
  if (!shape.has) return null
  return (
    <TaskPanel
      title={t('detail.spec.title')}
      hint={
        shape.constraints.length > 0
          ? t('detail.spec.constraints', { n: String(shape.constraints.length) })
          : undefined
      }
      clamp={BRIEF_CLAMP_PX}
      foldable
    >
      <SpecCard spec={task.spec} fallback={task.intent} bare />
    </TaskPanel>
  )
}

/** 任务信息: the ids and settings you check once. Two rows on the card face,
 *  the rest behind the fold — the header already carries the four that name
 *  the run, so this card is reference, not reading. */
export function InfoPanel({
  task,
  onOpenTask,
}: {
  task: Task
  onOpenTask?(id: string): void
}) {
  return (
    <TaskPanel title={t('detail.info')} clamp={INFO_CLAMP_PX} foldable>
      <InfoSection task={task} onOpenTask={onOpenTask} bare />
    </TaskPanel>
  )
}
