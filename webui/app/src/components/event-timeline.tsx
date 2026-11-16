// SPDX-License-Identifier: AGPL-3.0-or-later
import type { TaskEvent } from '../api/client'
import { ExecutionSteps } from './execution-steps-view'
import { taskSteps } from './execution-steps'

export interface EventTimelineProps {
 scope?: string
 events: TaskEvent[]
 className?: string
 defaultOpenThought?: boolean
 suppressResultBody?: boolean
}

export function EventTimeline({ events, className = '', scope, suppressResultBody = false }: EventTimelineProps) {
 return <ExecutionSteps scope={scope} steps={taskSteps(events ?? [], false, suppressResultBody)} className={className} />
}
