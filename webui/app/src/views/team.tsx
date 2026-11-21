// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, ApiError, workTaskKey, type WorkAuthorization, type WorkConnection, type WorkInput, type WorkTask } from '../api/client'
import { useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { navigate } from '../nav'
import { t } from '../i18n'
import { Icon } from '../components/icons'
import { basicScopes, SpaceConnectForm } from './space-connect-form'
import { PrivateResearch, SpacePolicy, WorkResults, RemoteTaskForm } from './workflow'
import { TaskBoard, TaskFacts, spaceKey, type Directory, type Member } from './work-board'
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
  const grants = useWorkRead(() => api.workConnections(), [], tick + signal)
  const readable = (grants.data || []).filter(c => c.state === 'connected' && c.scopes?.includes('members:read'))
    .flatMap(c => (c.spaces || []).filter(s => s.scopes?.includes('members:read')).map(s => ({ id: c.connectionId, account: c.accountId, epoch: c.authEpoch, synced: c.syncedAt, space: s.spaceId, workspace: s.workspaceId })))
  const directories = useWorkRead(async () => {
    const entries = await Promise.all(readable.map(async r => {
      const members = await api.workMembers(r.id, r.space).catch(() => null)
      return [spaceKey(r.id, r.workspace, r.space), members] as const
    }))
    return new Map(entries)
  }, [readable], tick)
  const reviews = useWorkRead(() => tab === 'review' ? api.tasks({ state: 'review', brief: true }) : Promise.resolve([]), [tab], tick + signal)
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
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
  const space = grant?.spaces?.find(s => s.spaceId === current?.snapshot.key.spaceId && s.workspaceId === current?.snapshot.key.workspaceId)
  const supported = (feature: string, scopes: string[]) => !!grant?.capabilities?.capabilities?.includes(feature) && scopes.every(scope => grant.scopes.includes(scope) && space?.scopes.includes(scope))
  const canEdit = !!current?.snapshot.permissions.edit && (!!current.local || supported('taskWrite', ['tasks:read', 'tasks:write']))
  const canRequest = !!current && !current.local && current.snapshot.permissions.execute && supported('executions', ['tasks:read', 'execution:read', 'execution:claim', 'execution:report']) && supported('deliveries', ['delivery:write'])
  const tasks = list.data || []
  const mine = tasks.filter(task => task.connectionId === 'local' || (task.snapshot.assigneeAccountIds || []).includes(task.accountId))
  const open = (task: WorkTask) => navigate({ view: 'work', key: workTaskKey(task), connection: task.connectionId })
  const failure = error || list.error || detail.error || history.error || grants.error || reviews.error
  return <section class="tm-page work-page" aria-busy={busy}>
    <header class="tm-head work-head">
      <div><div class="team-feature-heading"><h1 class="page-title">{current?.snapshot.title || t('nav.team')}</h1><span class="badge yellow experimental-badge">{t('team.experimental')}</span></div><p class="page-sub">{t('team.subtitle')}</p></div>
      <button class="btn" type="button" onClick={reload} disabled={busy}><Icon name="refresh" size={15} />{t('common.refresh')}</button>
    </header>
    <nav class="work-tabs" aria-label={t('nav.team')}>
      {(['mine', 'spaces', 'review'] as const).map(k => <button key={k} disabled={busy} onClick={() => navigate({ view: 'team', tab: k })} aria-current={!taskKey && tab === k ? 'page' : undefined}><Icon name={k === 'mine' ? 'list-checks' : k === 'spaces' ? 'users' : 'shield'} size={16} />{t(`work.${k}`)}</button>)}
    </nav>
    {failure && <p class="work-error" role="alert">{failure}</p>}
    {taskKey ? <>
      {!current && !failure && <p>{t('common.loading')}</p>}
      {current && <>
        <TaskFacts task={current} directory={directories.data} />
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
      <div class="work-toolbar"><button class="btn primary" onClick={() => setEditing(!editing)} disabled={busy}>{editing ? t('common.cancel') : t('work.add')}</button></div>
      {editing && <WorkForm busy={busy} onSave={body => mutate(JSON.stringify(body), async key => { const task = await api.createWork(body, key); open(task) })} />}
      {!list.data && !list.error && <p>{t('common.loading')}</p>}
      <TaskBoard tasks={mine} directory={directories.data} open={open} ready={!!list.data} />
    </> : tab === 'spaces' ? <>
      <ConnectionManager connections={grants.data} directory={directories.data} tasks={tasks} ready={!!list.data} open={open} tick={tick + signal} reload={reload} />
    </> : <>
      <p class="hint">{t('work.reviewHint')}</p>
      <WorkResults tick={tick + signal} reload={reload} />
      <h2>{t('work.operationReview')}</h2>
      {reviews.data?.length === 0 && <p class="hint">{t('work.noReview')}</p>}
      <div class="work-grid">{reviews.data?.map(task => <article class="card" key={task.id}><a href={`#/task/${encodeURIComponent(task.id)}`}>{task.title || task.id}</a></article>)}</div>
    </>}
  </section>
}

function ConnectionManager({ connections, directory, tasks, ready, open, tick, reload }: {
  connections: WorkConnection[] | null; directory: Directory | null; tasks: WorkTask[]; ready: boolean; open(task: WorkTask): void; tick: number; reload(): void
}) {
  const [editor, setEditor] = useState<WorkConnection | 'new' | null>(null)
  const [selected, setSelected] = useState('')
  const [scopes, setScopes] = useState(basicScopes)
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
  const connect = (url: string, id?: string, requestedScopes = scopes) => perform(async () => {
    if (!id && pending.current?.root !== url) pending.current = { root: url, id: crypto.randomUUID() }
    const result = await api.connectWork({ rootUrl: url, connectionId: id || pending.current!.id, scopes: requestedScopes }).catch(e => {
      if (e instanceof ApiError && e.status === 501) throw new Error(t('work.bindUnavailable'))
      throw e
    })
    setEditor(null)
    grantList.current = connections
    setGrant({ id: result.connection.connectionId, auth: result.authorization })
    pending.current = null
  })
  useEffect(() => {
    if (grant && connections !== grantList.current && connections?.some(c => c.connectionId === grant.id && c.state !== 'authorizing')) setGrant(null)
  }, [connections, grant])
  const spaces = (connections || []).filter(c => c.state === 'connected').flatMap(c => (c.spaces || []).map(space => ({
    connection: c, space, key: spaceKey(c.connectionId, space.workspaceId, space.spaceId),
  })))
  const active = spaces.find(s => s.key === selected) || spaces[0]
  const showForm = !grant && connections !== null && (connections.length === 0 || editor !== null)
  const edit = (c: WorkConnection | 'new') => {
    setScopes(c === 'new' || !c.scopes?.length ? basicScopes : [...c.scopes])
    setEditor(c)
  }
  return <div class="work-connections">
    {!connections && <p class="hint">{t('common.loading')}</p>}
    {!!connections?.length && <header class="board-head"><h2>{t('work.spaces')} <span class="space-count">{spaces.length}</span></h2>
      {!grant && <button class="btn" disabled={busy} onClick={() => editor ? setEditor(null) : edit('new')}>{t(editor ? 'common.cancel' : 'work.addAccount')}</button>}</header>}
    {showForm && <SpaceConnectForm key={editor && editor !== 'new' ? editor.connectionId : 'new'} existingRoot={editor && editor !== 'new' ? editor.rootUrl : undefined} scopes={scopes} setScopes={setScopes} busy={busy} connect={url => connect(url, editor && editor !== 'new' ? editor.connectionId : undefined)} />}
    {error && <p role="alert" class="work-error">{error}</p>}
    {grant && <div class="card" role="status">
      <p>{t('work.authorizationCode')} <strong>{grant.auth.userCode}</strong></p>
      <a class="btn primary" href={grant.auth.verificationUriComplete || grant.auth.verificationUri} target="_blank" rel="noopener noreferrer">{t('work.signInTSync')}<Icon name="external" size={15} /></a>
      <p class="hint">{t('work.authorizationExpires')} {new Date(grant.auth.expiresAt * 1000).toLocaleString()}</p>
    </div>}
    {connections?.map(c => <article class="account-bar" key={c.connectionId}>
      <div><strong>{c.rootUrl}</strong><span class="space-state" data-state={c.state}>{t('work.connection.' + c.state, c.state)}</span>
        <p class="hint">{t('work.lastSync')} · {c.syncedAt ? new Date(c.syncedAt).toLocaleString() : t('work.noSync')}</p>
        {c.state === 'connected' && c.scopes?.every(scope => basicScopes.includes(scope)) && <p class="hint">{t('work.readOnlySummary')}</p>}
        {c.lastError && <p class="work-error" role="status">{t('work.error.' + c.lastError, c.lastError)}</p>}</div>
      <div class="work-actions">
        {['connected', 'authorizing', 'unsupported'].includes(c.state) && <button class="btn" disabled={busy} onClick={() => void perform(() => api.syncWorkConnection(c.connectionId))}><Icon name="refresh" size={14} />{t('work.sync')}</button>}
        <button class="btn" disabled={busy || !!grant} onClick={() => c.state === 'authorizing' ? void connect(c.rootUrl, c.connectionId, c.scopes) : edit(c)}>{t(c.state === 'authorizing' ? 'work.resumeAuthorization' : c.state === 'connected' ? 'work.manageAccess' : 'work.reconnect')}</button>
        {c.state !== 'disconnected' && <button class="btn" disabled={busy} onClick={() => void perform(() => api.disconnectWork(c.connectionId))}>{t('work.disconnect')}</button>}
      </div></article>)}
    {active && <><nav class="space-switcher" aria-label={t('work.spaces')}>{spaces.map(s => <button key={s.key} aria-pressed={s.key === active.key} onClick={() => setSelected(s.key)}><Icon name="folder" size={16} />{s.space.name}</button>)}</nav>
      <SpaceView key={active.key + active.connection.authEpoch} connection={active.connection} space={active.space} members={directory?.get(active.key)} tasks={tasks.filter(v => v.connectionId === active.connection.connectionId && v.snapshot.key.workspaceId === active.space.workspaceId && v.snapshot.key.spaceId === active.space.spaceId)} directory={directory} ready={ready} open={open} tick={tick} reload={reload} />
    </>}
  </div>
}

function SpaceView({ connection, space, members, tasks, directory, ready, open, tick, reload }: {
  connection: WorkConnection; space: WorkConnection['spaces'][number]; members?: Member[] | null; directory: Directory | null; tasks: WorkTask[]; ready: boolean; open(task: WorkTask): void; tick: number; reload(): void
}) {
  const [creating, setCreating] = useState(false)
  const canCreate = connection.capabilities?.capabilities?.includes('taskWrite') && connection.scopes?.includes('tasks:write') && space.scopes?.includes('tasks:write')
  return <section class="space-content"><div class="board-head"><h2>{space.name}</h2>
    {canCreate && <button class="btn primary" onClick={() => setCreating(!creating)}>{t(creating ? 'common.cancel' : 'work.addRemote')}</button>}</div>
    {creating && canCreate && <RemoteTaskForm connection={connection.connectionId} space={space.spaceId} onSaved={() => { setCreating(false); reload() }} />}
    <div class="space-settings">
      {connection.scopes?.includes('members:read') && space.scopes?.includes('members:read') && <details><summary>{t('work.members')} {members && <span class="space-count">{members.length}</span>}</summary>
        {members ? <ul class="board-members">{members.map(m => <li key={m.accountId}><Icon name="user" size={15} /><strong>{m.name || t('work.unknownMember')}</strong><small>{t('work.role.' + m.role, m.role)}</small></li>)}</ul> : <p class="hint">{t(members === null ? 'work.membersUnavailable' : 'common.loading')}</p>}</details>}
      <SpacePolicy connection={connection} space={space.spaceId} tick={tick} reload={reload} />
    </div>
    <TaskBoard tasks={tasks} directory={directory} open={open} ready={ready} />
  </section>
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
