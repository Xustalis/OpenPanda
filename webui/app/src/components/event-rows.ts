// SPDX-License-Identifier: AGPL-3.0-or-later

// Timeline row grouping: the flat task_events stream becomes render rows —
// consecutive dispatch-plumbing writes (submit → queue → delegate → accept)
// fold into one pipeline strip so the log reads as what the scheduler did,
// not four bookkeeping cards; a run of quiet rows (the adapter's "still
// running, no output" heartbeats) folds into one row with a ×N repeat count
// so a four-minute silence is one thin line, not five badge cards.

import type { TaskEvent } from '../api/client'
import { formatTaskEvent } from './event-parser.ts'

export type TimelineRow =
  | { kind: 'pipeline'; ts: number; steps: { step: string; node?: string; failed?: boolean }[] }
  | { kind: 'event'; event: TaskEvent; formatted: ReturnType<typeof formatTaskEvent>; count?: number }

/** Groups the flat event list into render rows: consecutive dispatch-plumbing
 *  events merge into one pipeline strip; consecutive quiet events of the same
 *  type fold into their newest row with a repeat count. */
export function buildRows(events: TaskEvent[]): TimelineRow[] {
  const rows: TimelineRow[] = []
  let pipe: Extract<TimelineRow, { kind: 'pipeline' }> | null = null
  // The pushed row a quiet-run folds into — kept by reference so absorbing
  // the next same-type quiet event just updates it in place.
  let quietRun: Extract<TimelineRow, { kind: 'event' }> | null = null
  const flush = () => {
    if (pipe && pipe.steps.length) rows.push(pipe)
    pipe = null
  }
  for (const ev of events) {
    const formatted = formatTaskEvent(ev.type, ev.data)
    if (formatted.lifecycle) {
      quietRun = null
      if (!pipe) pipe = { kind: 'pipeline', ts: ev.ts, steps: [] }
      pipe.steps.push(formatted.lifecycle)
      continue
    }
    if (formatted.quiet && quietRun && quietRun.event.type === ev.type) {
      quietRun.event = ev
      quietRun.formatted = formatted
      quietRun.count = (quietRun.count ?? 1) + 1
      continue
    }
    flush()
    const row: Extract<TimelineRow, { kind: 'event' }> = { kind: 'event', event: ev, formatted }
    rows.push(row)
    quietRun = formatted.quiet ? row : null
  }
  flush()
  return rows
}
