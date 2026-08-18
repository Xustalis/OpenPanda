import { t } from '../i18n'

const STATE_CLASS: Record<string, string> = {
  submitted: 'blue',
  queued: 'blue',
  dispatched: 'blue',
  waiting_context: 'blue',
  running: 'yellow',
  review: 'yellow',
  done: 'green',
  failed: 'red',
  cancelled: '',
  expired: '',
}

/** Canonical task-state badge; states are wire-stable (internal/core/state.go). */
export function StateBadge({ state }: { state: string }) {
  const cls = STATE_CLASS[state] ?? ''
  return <span class={`badge${cls ? ` ${cls}` : ''}`}>{t(`state.${state}`, state)}</span>
}
