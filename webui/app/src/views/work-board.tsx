// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { workTaskKey, type WorkTask } from '../api/client'
import { t, locale } from '../i18n'
import { Icon } from '../components/icons'

export type Member = { accountId: string; name: string; role: string }
export type Directory = Map<string, Member[] | null>
export function spaceKey(connection: string, workspace: string, space: string) {
  return JSON.stringify([connection, workspace, space])
}

function statusLabel(status: string) {
  if (status === 'waiting') return t('work.waiting')
  const mapped = status === 'open' ? 'pending' : status === 'doing' ? 'running' : status
  return t('todos.status.' + mapped, status)
}

function dueLabel(due: WorkTask['snapshot']['dueAt']) {
  if (!due) return t('work.noDue')
  // Calendar dates and wall times have no offset. Format their fields without
  // converting through the browser timezone (including daylight-saving gaps).
  const wall = due.kind !== 'zoned_datetime' || !!due.timezone
  const date = new Date(wall ? due.value.replace(' ', 'T') + (due.kind === 'date' ? 'T00:00:00Z' : 'Z') : due.value)
  if (Number.isNaN(date.getTime())) return due.value
  const fmt = new Intl.DateTimeFormat(locale(), {dateStyle: 'medium', timeStyle: due.kind === 'date' ? undefined : 'short', timeZone: wall ? 'UTC' : undefined})
  return [fmt.format(date), due.timezone].filter(Boolean).join(' ')
}

export function TaskFacts({ task, directory }: { task: WorkTask; directory?: Directory | null }) {
  const s = task.snapshot
  const members = directory?.get(spaceKey(task.connectionId, s.key.workspaceId, s.key.spaceId))
  const owners = (s.assigneeAccountIds || []).map(id => id === task.accountId ? t('work.me') : members?.find(m => m.accountId === id)?.name || t('work.unknownMember'))
  const source = [task.local ? t('workspace.local') : s.spaceName || 'TSync', s.projectName].filter(Boolean).join(' / ')
  return <dl class="board-facts">
    <div><dt><Icon name="folder" size={15} />{t('work.source')}</dt><dd>{source}</dd></div>
    <div><dt><Icon name="activity" size={15} />{t('work.status')}</dt><dd><span class="board-status" data-status={s.status}>{statusLabel(s.status)}</span></dd></div>
    <div><dt><Icon name="user" size={15} />{t('work.owner')}</dt><dd>{task.local ? t('work.me') : owners.join(t('work.nameSeparator')) || t('work.unassigned')}</dd></div>
    <div><dt><Icon name="clock" size={15} />{t('work.due')}</dt><dd>{s.dueAt ? <time dateTime={s.dueAt.value}>{dueLabel(s.dueAt)}</time> : t('work.noDue')}</dd></div>
  </dl>
}

export function TaskBoard({ tasks, directory, open, ready }: { tasks: WorkTask[]; directory?: Directory | null; open(task: WorkTask): void; ready: boolean }) {
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('')
  const [owner, setOwner] = useState('')
  const needle = query.trim().toLocaleLowerCase()
  const visible = tasks.filter(({snapshot: s, accountId, local}) => {
    if (status && s.status !== status) return false
    const owners = s.assigneeAccountIds || []
    if (owner === 'me' && !local && !owners.includes(accountId)) return false
    if (owner === 'unassigned' && (local || owners.length)) return false
    return [s.title, s.description, s.projectName, s.spaceName].some(value => value?.toLocaleLowerCase().includes(needle))
  })
  return <section class="task-board">
    <div class="board-head"><h2>{t('work.taskList')}</h2><span class="space-count">{visible.length} / {tasks.length}</span></div>
    <div class="board-tools"><label class="board-search"><Icon name="search" size={16} /><input type="search" aria-label={t('work.search')} placeholder={t('work.search')} value={query} onInput={e => setQuery(e.currentTarget.value)} /></label>
      <select aria-label={t('work.status')} value={status} onChange={e => setStatus(e.currentTarget.value)}><option value="">{t('work.allStatuses')}</option>{[...new Set([...tasks.map(v => v.snapshot.status), ...(status ? [status] : [])])].sort().map(s => <option key={s} value={s}>{statusLabel(s)}</option>)}</select>
      <select aria-label={t('work.owner')} value={owner} onChange={e => setOwner(e.currentTarget.value)}><option value="">{t('work.allOwners')}</option><option value="me">{t('work.me')}</option><option value="unassigned">{t('work.unassigned')}</option></select>
    </div>
    {ready && !visible.length && <p class="board-empty">{t('work.noMatches')}</p>}
    <div class="work-grid">{visible.map(task => <article class="card board-task" key={task.connectionId + ':' + workTaskKey(task)}>
      <button class="work-title" onClick={() => open(task)}>{task.snapshot.title}<Icon name="chevron-right" size={16} /></button><TaskFacts task={task} directory={directory} />
    </article>)}</div>
  </section>
}
