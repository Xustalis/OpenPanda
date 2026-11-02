// SPDX-License-Identifier: AGPL-3.0-or-later

import { useMemo } from 'preact/hooks'
import type { Task } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'
import { StateBadge } from './state-badge'

/** The thread's unfinished work as a side column — the web twin of the TUI's
 *  task rail (Ctrl+B). Running work leads, then whatever is waiting on a human,
 *  then the queue: the order is by how soon the reader can act on it, not by
 *  when it was filed.
 *
 *  Clicking a row jumps to that task's card in the transcript. A task the
 *  thread has no card for (dispatched from the bench, or older than the loaded
 *  transcript) opens its task page instead; the row never dead-ends. */

type Group = 'running' | 'waiting' | 'queued'

const GROUPS: { key: Group; states: string[] }[] = [
  { key: 'running', states: ['running', 'dispatched'] },
  { key: 'waiting', states: ['review', 'waiting_context'] },
  { key: 'queued', states: ['submitted', 'queued'] },
]

const DONE = new Set(['done', 'failed', 'cancelled', 'expired'])

function groupOf(state: string): Group | null {
  for (const g of GROUPS) if (g.states.includes(state)) return g.key
  return null
}

function seqOf(task: Task): number {
  return typeof task.seq === 'number' ? task.seq : Number.MAX_SAFE_INTEGER
}

export function TaskRail(props: {
  tasks: Task[]
  onJump(id: string): void
  onClose(): void
}) {
  const groups = useMemo(() => {
    const out: Record<Group, Task[]> = { running: [], waiting: [], queued: [] }
    for (const task of props.tasks) {
      if (DONE.has(task.state)) continue
      const group = groupOf(task.state)
      if (group) out[group].push(task)
    }
    for (const g of GROUPS) {
      out[g.key].sort((a, b) => seqOf(a) - seqOf(b) || a.title.localeCompare(b.title))
    }
    return out
  }, [props.tasks])

  const total = groups.running.length + groups.waiting.length + groups.queued.length

  return (
    <aside class="task-rail" aria-label={t('rail.title')}>
      <header class="task-rail-head">
        <Icon name="list-checks" size={15} />
        <span class="task-rail-title">{t('rail.title')}</span>
        {total > 0 && <span class="task-rail-count mono">{total}</span>}
        <button
          type="button"
          class="icon-btn task-rail-close"
          aria-label={t('common.close')}
          title={t('common.close')}
          onClick={props.onClose}
        >
          <Icon name="x" size={15} />
        </button>
      </header>
      <div class="task-rail-body">
        {total === 0 ? (
          <p class="task-rail-empty dim">{t('rail.empty')}</p>
        ) : (
          GROUPS.map(({ key }) =>
            groups[key].length === 0 ? null : (
              <section class={`task-rail-group is-${key}`} key={key}>
                <p class="task-rail-group-label">
                  {t(`rail.${key}`)}
                  <span class="mono">{groups[key].length}</span>
                </p>
                {groups[key].map((task) => (
                  <button
                    type="button"
                    class="task-rail-item"
                    key={task.id}
                    onClick={() => props.onJump(task.id)}
                    title={task.title || task.id}
                  >
                    <span class="task-rail-item-top">
                      <StateBadge state={task.state} />
                      {task.agent && <span class="task-rail-agent mono">{task.agent}</span>}
                      <span class="task-rail-jump">{t('rail.jump')} →</span>
                    </span>
                    <span class="task-rail-item-title">{task.title || task.id}</span>
                  </button>
                ))}
              </section>
            ),
          )
        )}
      </div>
    </aside>
  )
}
