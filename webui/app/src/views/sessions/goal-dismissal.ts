// SPDX-License-Identifier: AGPL-3.0-or-later
import type { ServerGoal } from '../../api/client.ts'

type Completion = Pick<ServerGoal, 'id' | 'generation' | 'status' | 'rounds'>

export function completionKey(goal?: Completion): string | undefined {
  return goal?.status === 'complete' ? [goal.id, goal.generation, goal.rounds].join(':') : undefined
}

// Capture at send time. Only acknowledge the completion the sender actually saw.
export function onCompletionAcknowledged(current: () => Completion | undefined, dismiss: (key: string) => void) {
  const key = completionKey(current())
  if (!key) return undefined
  return () => {
    if (completionKey(current()) === key) dismiss(key)
  }
}
