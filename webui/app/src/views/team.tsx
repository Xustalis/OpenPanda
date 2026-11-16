// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, workTaskKey, type WorkAuthorization, type WorkConnection, type WorkInput, type WorkTask } from '../api/client'
import { useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { navigate } from '../nav'
import { t } from '../i18n'
import { PrivateResearch, SpacePolicy, WorkResults, RemoteTaskForm } from './workflow'
import { useWorkRead } from './work-read'

/** Business intentions and their execution history share one source with CLI. */
export function TeamView({ tab = 'mine', taskKey, connection = 'local' }: {
  tab?: 'mine' | 'spaces' | 'review'; taskKey?: string; connection?: string
}) {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const reload = () => setTick(v => v + 1)
  const signal = useChangeSignal()
  useVisibleInterval(reload, 15_000)
  const list = useWorkRead(() => api.workTasks(), [], tick + signal)
  const detail = useWorkRead(() => taskKey ? api.workTask(taskKey, connection) : Promise.resolve(null), [taskKey, connection], tick + signal)
  const history = useWorkRead(() => taskKey ? api.workHistory(taskKey, connection) : Promise.resolve([]), [taskKey, connection], tick + signal)
  const grants = useWorkRead(() => taskKey && connection !== 'local' ? api.workConnections() : Promise.resolve([]), [taskKey, connection], tick + signal)
  const reviews = useWorkRead(() => tab === 'review' ? api.tasks({ state: 'review', brief: true }) : Promise.resolve([]), [tab], tick + signal)
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  // Keep the same key after a lost response. A changed payload gets a new key.
  const pending = useRef<{ signature: string; key: string } | null>(null)
  useEffect(() => { setEditing(false); setError(''); pending.current = null }, [taskKey, connection])
  const mutate = async (signature: string, fn: (key: string) => Promise<unknown>) => {
    if (busy) return
    if (pending.current?.signature !== signature) pending.current = { signature, key: crypto.randomUUID() }
    setBusy(true); setError('')
    try { await fn(pending.current.key); pending.current = null; setEditing(false); reload() }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const current = detail.data
  const grant = grants.data?.find(c => c.connectionId === connection && c.state === 'connected')
  const space = grant?.spaces.find(s => s.spaceId === current?.snapshot.key.spaceId && s.workspaceId === current?.snapshot.key.workspaceId)
  const supported = (feature: string, scopes: string[]) => !!grant?.capabilities?.capabilities.includes(feature) && scopes.every(scope => grant.scopes.includes(scope) && space?.scopes.includes(scope))
  const canEdit = !!current?.snapshot.permissions.edit && (!!current.local || supported('taskWrite', ['tasks:read', 'tasks:write']))
  const canRequest = !!current && !current.local && current.snapshot.permissions.execute && supported('executions', ['tasks:read', 'execution:read', 'execution:claim', 'execution:report'])
  const tasks = list.data || []
  const mine = tasks.filter(task => task.connectionId === 'local' || (task.snapshot.assigneeAccountIds || []).includes(task.accountId))
  const open = (task: WorkTask) => navigate({ view: 'work', key: workTaskKey(task), connection: task.connectionId })
  const failure = error || list.error || detail.error || history.error || grants.error || reviews.error
  return <section class="tm-page work-page" aria-busy={busy}>
    <header class="tm-head work-head">
      <div><div class="team-feature-heading"><h1 class="page-title">{current?.snapshot.title || t('nav.team')}</h1><span class="badge yellow experimental-badge">{t('team.experimental')}</span></div><p class="page-sub">{t('team.subtitle')}</p></div>
      <button class="btn" type="button" onClick={reload} disabled={busy}>{t('common.refresh')}</button>
    </header>
    <nav class="segmented work-tabs" aria-label={t('nav.team')}>
      {(['mine', 'spaces', 'review'] as const).map(k => <button class={`seg${!taskKey && tab === k ? ' on' : ''}`} disabled={busy} onClick={() => navigate({ view: 'team', tab: k })} aria-current={!taskKey && tab === k ? 'page' : undefined}>{t(`work.${k}`)}</button>)}
    </nav>
    {failure && <p class="work-error" role="alert">{failure}</p>}
    {taskKey ? <>
      {!current && !failure && <p>{t('common.loading')}</p>}
      {current && <>
        <TaskFacts task={current} />
        <div class="work-actions">
          {canEdit && <button class="btn" disabled={busy} onClick={() => setEditing(!editing)}>{t('work.edit')}</button>}
          {current.local && current.snapshot.permissions.execute && <button class="btn primary" disabled={busy} onClick={() => mutate(`run:${taskKey}`, async key => { const result = await api.runWork(taskKey, key); if (result.warning) setError(result.warning) })}>{t('work.run')}</button>}
        </div>
        {editing && canEdit && (current.local ? <WorkForm key={taskKey + current.snapshot.taskRevision} task={current} busy={busy} onSave={body => mutate(JSON.stringify([taskKey, current.snapshot.taskRevision, body]), key => api.patchWork(taskKey, current.snapshot.taskRevision, body, key))} /> : <RemoteTaskForm key={taskKey + current.snapshot.taskRevision} task={current} connection={connection} space={current.snapshot.key.spaceId} onSaved={() => { setEditing(false); reload() }} />)}
        {canRequest && <><button class="btn" disabled={busy} onClick={() => void mutate('request:' + taskKey, key => api.requestWork(connection, taskKey, key))}>{t('work.requestExecution')}</button><p class="hint">{t('work.requestExecutionHint')}</p></>}
        <p class="work-description">{current.snapshot.description}</p>
        {!current.local && <>
          {(current.snapshot.assigneeAccountIds || []).length === 0 && <PrivateResearch connection={connection} taskKey={taskKey} tick={tick + signal} />}
          <WorkResults connection={connection} taskKey={taskKey} tick={tick + signal} reload={reload} />
        </>}
        <h2>{t('work.history')}</h2>
        <p class="hint">{t('work.completionHint')}</p>
        {history.data?.length === 0 && <p class="hint">{t('work.noHistory')}</p>}
        <div class="work-history">{history.data?.map((h, i) => <article class="card" key={`${h.requestId}:${h.taskId}:${i}`}>
          <div class="work-card-head"><strong>{h.stageId || h.requestId}</strong><span>{h.executionState}</span></div>
          <p class="hint">{new Date(h.createdAt * 1000).toLocaleString()}</p>
          {h.executionState !== 'unavailable' && h.taskId ? <a href={`#/task/${encodeURIComponent(h.taskId)}`}>{t('work.openExecution')}</a> : <span>{t('work.unavailable')}</span>}
        </article>)}</div>
      </>}
    </> : tab === 'mine' ? <>
      <div class="work-toolbar"><input aria-label={t('work.search')} placeholder={t('work.search')} value={query} onInput={e => setQuery(e.currentTarget.value)} /><button class="btn primary" onClick={() => setEditing(!editing)} disabled={busy}>{editing ? t('common.cancel') : t('work.add')}</button></div>
      {editing && <WorkForm busy={busy} onSave={body => mutate(JSON.stringify(body), async key => { const task = await api.createWork(body, key); open(task) })} />}
      {!list.data && !list.error && <p>{t('common.loading')}</p>}
      {mine.length === 0 && list.data && <p class="hint">{t('work.empty')}</p>}
      <div class="work-grid">{mine.filter(v => `${v.snapshot.title} ${v.snapshot.description}`.toLowerCase().includes(query.toLowerCase())).map(task => <article class="card work-card" key={`${task.connectionId}:${workTaskKey(task)}`}>
        <button class="work-title" onClick={() => open(task)}>{task.snapshot.title}</button><TaskFacts task={task} />
      </article>)}</div>
    </> : tab === 'spaces' ? <>
      <p class="hint">{t('work.policyHint')}</p>
      <ConnectionManager tick={tick + signal} reload={reload} />
      {tasks.some(v => v.connectionId !== 'local') ? <div class="work-grid">{tasks.filter(v => v.connectionId !== 'local').map(task => <article class="card" key={`${task.connectionId}:${workTaskKey(task)}`}><button class="work-title" onClick={() => open(task)}>{task.snapshot.title}</button><TaskFacts task={task} /></article>)}</div> : <p class="hint">{t('work.noConnections')}</p>}
    </> : <>
      <p class="hint">{t('work.reviewHint')}</p>
      <WorkResults tick={tick + signal} reload={reload} />
      <h2>{t('work.operationReview')}</h2>
      {reviews.data?.length === 0 && <p class="hint">{t('work.noReview')}</p>}
      <div class="work-grid">{reviews.data?.map(task => <article class="card" key={task.id}><a href={`#/task/${encodeURIComponent(task.id)}`}>{task.title || task.id}</a></article>)}</div>
    </>}
  </section>
}

function ConnectionManager({ tick, reload }: { tick: number; reload(): void }) {
  const connections = useWorkRead(() => api.workConnections(), [], tick)
  const [root, setRoot] = useState('')
  const [scopes, setScopes] = useState(['tasks:read', 'members:read'])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [grant, setGrant] = useState<{ id: string; auth: WorkAuthorization } | null>(null)
  const grantList = useRef<WorkConnection[] | null>(null)
  const pending = useRef<{ root: string; id: string } | null>(null)
  const perform = async (fn: () => Promise<unknown>) => {
    if (busy) return
    setBusy(true); setError('')
    try { await fn(); reload() }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const connect = (url: string, id?: string) => perform(async () => {
    if (!id && pending.current?.root !== url) pending.current = { root: url, id: crypto.randomUUID() }
    const result = await api.connectWork({ rootUrl: url, connectionId: id || pending.current!.id, scopes })
    grantList.current = connections.data
    setGrant({ id: result.connection.connectionId, auth: result.authorization })
    pending.current = null
  })
  useEffect(() => {
    if (grant && connections.data !== grantList.current && connections.data?.some(c => c.connectionId === grant.id && c.state !== 'authorizing')) setGrant(null)
  }, [connections.data, grant])
  return <div class="work-connections">
    <h2>{t('work.connections')}</h2>
    <form class="card work-form" onSubmit={e => { e.preventDefault(); void connect(root.trim()) }}>
      <label>{t('work.serviceRoot')}<input type="url" required value={root} placeholder="https://example.com/tsync/" onInput={e => setRoot(e.currentTarget.value)} /></label>
      <p class="hint">{t('work.connectHint')}</p>
      <fieldset><legend>{t('work.connectionScopes')}</legend><p class="hint">{t('work.scopeHint')}</p>
        {['tasks:read', 'tasks:write', 'members:read', 'execution:read', 'execution:claim', 'execution:report', 'delivery:write', 'delivery:report-decision'].map(scope => <label class="work-check"><input type="checkbox" checked={scopes.includes(scope)} onChange={e => setScopes(v => e.currentTarget.checked ? [...v, scope] : v.filter(s => s !== scope))} />{scope}</label>)}
      </fieldset>
      <button class="btn primary" disabled={busy} type="submit">{t('work.connect')}</button>
    </form>
    {(error || connections.error) && <p role="alert" class="work-error">{error || connections.error}</p>}
    {grant && <div class="card" role="status">
      <p>{t('work.authorizationCode')} <strong>{grant.auth.userCode}</strong></p>
      <a class="btn" href={grant.auth.verificationUriComplete || grant.auth.verificationUri} target="_blank" rel="noopener noreferrer">{t('work.openAuthorization')}</a>
      <p class="hint">{t('work.authorizationExpires')} {new Date(grant.auth.expiresAt * 1000).toLocaleString()}</p>
    </div>}
    <div class="work-grid">{connections.data?.map(c => <article class="card" key={c.connectionId}>
      <strong>{c.rootUrl}</strong>
      <p>{t(`work.connection.${c.state}`)}{c.accountId ? ` · ${c.accountId}` : ''}</p>
      <p class="hint">{t('work.lastSync')} {c.syncedAt ? new Date(c.syncedAt).toLocaleString() : t('work.noSync')}</p>
      {c.lastError && <p class="work-error" role="status">{c.lastError}</p>}
      <div class="work-actions">
        <button class="btn" disabled={busy} onClick={() => void connect(c.rootUrl, c.connectionId)}>{t(c.state === 'authorizing' ? 'work.resumeAuthorization' : 'work.reconnect')}</button>
        {['connected', 'authorizing', 'unsupported'].includes(c.state) && <button class="btn" disabled={busy} onClick={() => void perform(() => api.syncWorkConnection(c.connectionId))}>{t('work.sync')}</button>}
        {c.state !== 'disconnected' && <button class="btn" disabled={busy} onClick={() => void perform(() => api.disconnectWork(c.connectionId))}>{t('work.disconnect')}</button>}
      </div>
      {c.state === 'connected' && c.scopes.includes('members:read') && c.spaces?.filter(space => space.scopes.includes('members:read')).map(space => <MemberDirectory key={space.spaceId} connection={c} space={space.spaceId} name={space.name} tick={tick} />)}
      {c.state === 'connected' && c.spaces?.map(space => <SpacePolicy key={space.spaceId} connection={c} space={space.spaceId} name={space.name} tick={tick} reload={reload} />)}
    </article>)}</div>
  </div>
}

function MemberDirectory({ connection, space, name, tick }: { connection: WorkConnection; space: string; name: string; tick: number }) {
  const [open, setOpen] = useState(false)
  const members = useWorkRead(() => open ? api.workMembers(connection.connectionId, space) : Promise.resolve([]), [connection.connectionId, space, open], tick)
  return <details onToggle={e => setOpen(e.currentTarget.open)}>
    <summary>{name} · {t('work.members')}</summary>
    {members.error ? <p class="hint">{t('work.membersUnavailable')}</p> : <ul>{members.data?.map(m => <li key={m.accountId}>{m.name || m.accountId} · {m.role}</li>)}</ul>}
  </details>
}

function TaskFacts({ task }: { task: WorkTask }) {
  const s = task.snapshot
  return <dl class="work-facts">
    <div><dt>{t('work.source')}</dt><dd>{s.sourceRef.provider} · {s.spaceName}{s.projectName ? ` / ${s.projectName}` : ''}</dd></div>
    <div><dt>{t('work.status')}</dt><dd>{task.local ? t(`todos.status.${s.status}`) : s.status}</dd></div>
    <div><dt>{t('work.owner')}</dt><dd>{task.local ? t('work.me') : (s.assigneeAccountIds || []).join(', ') || t('work.unassigned')}</dd></div>
    <div><dt>{t('work.due')}</dt><dd>{s.dueAt?.value || t('work.noDue')}</dd></div>
  </dl>
}

function WorkForm({ task, busy, onSave }: { task?: WorkTask; busy: boolean; onSave(body: WorkInput): void }) {
  const [title, setTitle] = useState(task?.snapshot.title || '')
  const [description, setDescription] = useState(task?.snapshot.description || '')
  const [priority, setPriority] = useState<WorkInput['priority']>(task?.local?.priority || 'normal')
  const [project, setProject] = useState(task?.snapshot.projectName || '')
  const [status, setStatus] = useState(task?.snapshot.status || 'pending')
  return <form class="card work-form" onSubmit={e => { e.preventDefault(); onSave({ title, description, priority, project, status }) }}>
    <label>{t('work.title')}<input value={title} required onInput={e => setTitle(e.currentTarget.value)} /></label>
    <label>{t('work.description')}<textarea value={description} rows={4} onInput={e => setDescription(e.currentTarget.value)} /></label>
    <div class="work-form-row">
      <label>{t('work.project')}<input value={project} onInput={e => setProject(e.currentTarget.value)} /></label>
      <label>{t('work.priority')}<select value={priority} onChange={e => setPriority(e.currentTarget.value as WorkInput['priority'])}>{['low', 'normal', 'high'].map(p => <option value={p}>{t(`todos.priority.${p}`)}</option>)}</select></label>
      <label>{t('work.status')}<select value={status} onChange={e => setStatus(e.currentTarget.value)}>{['pending', 'running', 'paused', 'done', 'cancelled'].map(s => <option value={s}>{t(`todos.status.${s}`)}</option>)}</select></label>
    </div>
    <button class="btn primary" type="submit" disabled={busy || !title.trim()}>{t('common.save')}</button>
  </form>
}
