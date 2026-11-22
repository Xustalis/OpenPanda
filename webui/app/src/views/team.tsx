// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, ApiError, workTaskKey, type WorkAuthorization, type WorkConnection, type WorkInput, type WorkTask } from '../api/client'
import { useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { navigate } from '../nav'
import { t } from '../i18n'
import { toast } from '../components/toast'
import { Icon } from '../components/icons'
import { basicScopes, SpaceConnectForm } from './space-connect-form'
import { PrivateResearch, SpacePolicy, WorkResults, WorkRunList, RemoteTaskForm } from './workflow'
import { TaskBoard, TaskFacts, spaceKey, type Directory, type Member } from './work-board'
import { workRequestId } from '../api/request-id'
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
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Keep the same key after a lost response. A changed payload gets a new key.
  const pending = useRef<{ signature: string; key: string } | null>(null)
  useEffect(() => { setEditing(false); setError(''); pending.current = null }, [taskKey, connection])
  const mutate = async (signature: string, fn: (key: string) => Promise<unknown>) => {
    if (busy) return
    if (pending.current?.signature !== signature) pending.current = { signature, key: workRequestId() }
    setBusy(true); setError('')
    try { await fn(pending.current.key); pending.current = null; setEditing(false); reload() }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const current = detail.data
  // The space automation policy is the local authorization for automation.
  // A connection grant alone never enables it, so read it before offering a
  // request: the server refuses an unauthorized run, and the reader needs the
  // policy editor rather than a bare failure.
  const remote = !!taskKey && connection !== 'local'
  const policy = useWorkRead(
    () => (remote && current?.snapshot.key.spaceId) ? api.workPolicy(connection, current.snapshot.key.spaceId) : Promise.resolve(null),
    [taskKey, connection, current?.snapshot.key.spaceId], tick + signal)
  const grant = grants.data?.find(c => c.connectionId === connection && c.state === 'connected')
  const space = grant?.spaces?.find(s => s.spaceId === current?.snapshot.key.spaceId && s.workspaceId === current?.snapshot.key.workspaceId)
  const supported = (feature: string, scopes: string[]) => !!grant?.capabilities?.capabilities?.includes(feature) && scopes.every(scope => grant.scopes.includes(scope) && space?.scopes.includes(scope))
  const canEdit = !!current?.snapshot.permissions.edit && (!!current.local || supported('taskWrite', ['tasks:read', 'tasks:write']))
  const canRequest = !!current && !current.local && current.snapshot.permissions.execute && supported('executions', ['tasks:read', 'execution:read', 'execution:claim', 'execution:report']) && supported('deliveries', ['delivery:write'])
  // The policy answer arrives after the task does; hold the button until it
  // lands so a disabled policy never shows an action that can only fail.
  const p = policy.data
  const closed = !!current && ['done', 'archived', 'cancelled'].includes(current.snapshot.status)
  const outsideScope = !!p && !!current && ((!!p.taskIds?.length && !p.taskIds.includes(current.snapshot.key.taskId)) || (!!p.projectId && p.projectId !== current.snapshot.projectId))
  const policyIssue = !p?.execute ? 'work.policyBlocksExecution' : p.expiresAt <= Date.now() ? 'work.policyExpired' : outsideScope ? 'work.policyScope' : null
  const requestRun = (firstDraft: boolean) => {
    if (!current || !taskKey || !grant) return
    void mutate(JSON.stringify([connection, taskKey, grant.authEpoch, current.snapshot.taskRevision, firstDraft]), async key => {
      try {
        const run = firstDraft ? await api.requestFirstDraft(current, grant.authEpoch, key) : await api.requestWork(connection, taskKey, key)
        toast(t('work.requestStatus', { state: t('work.state.' + run.state, run.state) }))
      } catch (e) {
        reload()
        if (e instanceof ApiError && e.code === 'automation_not_authorized') throw new Error(t('work.policyBlocksExecution'))
        throw e
      }
    })
  }
  const tasks = list.data || []
  const mine = tasks.filter(task => task.connectionId === 'local' || (task.snapshot.assigneeAccountIds || []).includes(task.accountId))
  const open = (task: WorkTask) => navigate({ view: 'work', key: workTaskKey(task), connection: task.connectionId })
  const failure = error || list.error || detail.error || history.error || grants.error
  return <section class="tm-page work-page" aria-busy={busy}>
    <header class="tm-head work-head">
      <div><div class="team-feature-heading"><Icon name="users" class="work-head-icon" /><h1 class="page-title">{current?.snapshot.title || t('nav.team')}</h1><span class="badge yellow experimental-badge">{t('team.experimental')}</span></div><p class="page-sub">{t('team.subtitle')}</p></div>
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
        {remote && closed && <p class="hint">{t('work.closedTask')}</p>}
        {remote && !closed && !canRequest && grants.data && <p class="hint">{t(current.snapshot.permissions.execute ? 'work.connectionAccess' : 'work.taskAccess')}</p>}
        {canRequest && !closed && <>
          {policy.error && <p class="work-error" role="alert">{policy.error}</p>}
          {!p && !policy.error && <p role="status">{t('common.loading')}</p>}
          {p && (policyIssue ? <>
            {p.configured === false && grant ? <>
              <p class="hint">{t('work.firstDraftHint')}</p>
              <button class="btn primary" disabled={busy} onClick={() => requestRun(true)}>{t('work.firstDraft')}</button>
            </> : <p class="hint">{t(policyIssue)}</p>}
            {grant && <SpacePolicy connection={grant} space={current.snapshot.key.spaceId} tick={tick + signal} reload={reload} />}
          </> : <><button class="btn primary" disabled={busy} onClick={() => requestRun(false)}>{t('work.requestExecution')}</button><p class="hint">{t('work.requestExecutionHint')}</p></>)}
        </>}
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
    </> : <ReviewInbox tick={tick + signal} reload={reload} />}
  </section>
}

function ReviewInbox({ tick, reload }: { tick: number; reload(): void }) {
  const runs = useWorkRead(() => api.workRuns(), [], tick)
  const reviews = useWorkRead(() => api.tasks({ state: 'review', brief: true }), [], tick)
  const empty = runs.data?.length === 0 && reviews.data?.length === 0
  const next = reviews.data?.[0]
  return <section class="review-inbox">
    {next && <div class="review-clear review-pending">
      <div class="review-art" aria-hidden="true"><span><Icon name="shield" size={38} /></span></div>
      <div><h2>{t('work.authorizationWaiting')}</h2><p>{next.title || next.id}</p>
        <a class="btn" href={'#/task/' + encodeURIComponent(next.id)}>{t('work.openRequest')}<Icon name="arrow-right" size={15} /></a>
      </div>
    </div>}
    {empty && <div class="review-clear">
      <div class="review-art" aria-hidden="true"><span><Icon name="list-checks" size={42} /></span><i><Icon name="check" size={16} /></i></div>
      <div><h2>{t('work.reviewClear')}</h2><p>{t('work.reviewClearHint')}</p>
        <a class="btn" href="#/team?tab=mine">{t('work.mine')}<Icon name="arrow-right" size={15} /></a>
      </div>
    </div>}
    <div class="review-lanes">
      <section class="review-lane"><header><Icon name="file-text" size={18} /><h2>{t('work.results')}</h2>{runs.data && <small>{runs.data.length}</small>}</header>
        {runs.error ? <p role="alert" class="work-error">{runs.error}</p> : !runs.data ? <p>{t('common.loading')}</p> : runs.data.length ? <WorkRunList runs={runs.data} tick={tick} reload={reload} /> : <p>{t('work.deliveryReviewHint')}</p>}
      </section>
      <section class="review-lane"><header><Icon name="shield" size={18} /><h2>{t('work.operationReview')}</h2>{reviews.data && <small>{reviews.data.length}</small>}</header>
        {reviews.error ? <p role="alert" class="work-error">{reviews.error}</p> : !reviews.data ? <p>{t('common.loading')}</p> : reviews.data.length ? reviews.data.map(task => <a class="review-task" key={task.id} href={'#/task/' + encodeURIComponent(task.id)}>{task.title || task.id}<Icon name="chevron-right" size={15} /></a>) : <p>{t('work.authorizationHint')}</p>}
      </section>
    </div>
    <p class="review-note"><Icon name="shield" size={14} />{t('work.reviewHint')}</p>
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
    if (!id && pending.current?.root !== url) pending.current = { root: url, id: workRequestId() }
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
    {!!connections?.length && <header class="board-head connection-heading"><h2>{t('work.accountsHeading')} <span class="space-count">{connections.length}</span></h2>
      {!grant && <button class="btn" disabled={busy} onClick={() => editor ? setEditor(null) : edit('new')}>{t(editor ? 'common.cancel' : 'work.addAccount')}</button>}</header>}
    {showForm && <SpaceConnectForm key={editor && editor !== 'new' ? editor.connectionId : 'new'} existingRoot={editor && editor !== 'new' ? editor.rootUrl : undefined} scopes={scopes} setScopes={setScopes} busy={busy} connect={url => connect(url, editor && editor !== 'new' ? editor.connectionId : undefined)} />}
    {error && <p role="alert" class="work-error">{error}</p>}
    {grant && <div class="card" role="status">
      <p>{t('work.authorizationCode')} <strong>{grant.auth.userCode}</strong></p>
      <a class="btn primary" href={grant.auth.verificationUriComplete || grant.auth.verificationUri} target="_blank" rel="noopener noreferrer">{t('work.signInTSync')}<Icon name="external" size={15} /></a>
      <p class="hint">{t('work.authorizationExpires')} {new Date(grant.auth.expiresAt * 1000).toLocaleString()}</p>
    </div>}
    {connections?.map(c => <details class="account-disclosure" key={c.connectionId} open={c.state !== 'connected' || !!c.lastError}>
      <summary><Icon name="globe" size={16} /><strong>{c.rootUrl}</strong><span class="space-state" data-state={c.state}>{t('work.connection.' + c.state, c.state)}</span><Icon name="chevron-down" size={15} /></summary>
      <div class="account-bar">
      <div><p class="hint">{t('work.lastSync')} · {c.syncedAt ? new Date(c.syncedAt).toLocaleString() : t('work.noSync')}</p>
        {c.state === 'connected' && c.scopes?.every(scope => basicScopes.includes(scope)) && <p class="hint">{t('work.readOnlySummary')}</p>}
        {c.lastError && <p class="work-error" role="status">{t('work.error.' + c.lastError, c.lastError)}</p>}</div>
      <div class="work-actions">
        {['connected', 'authorizing', 'unsupported'].includes(c.state) && <button class="btn" disabled={busy} onClick={() => void perform(() => api.syncWorkConnection(c.connectionId))}><Icon name="refresh" size={14} />{t('work.sync')}</button>}
        <button class="btn" disabled={busy || !!grant} onClick={() => c.state === 'authorizing' ? void connect(c.rootUrl, c.connectionId, c.scopes) : edit(c)}>{t(c.state === 'authorizing' ? 'work.resumeAuthorization' : c.state === 'connected' ? 'work.manageAccess' : 'work.reconnect')}</button>
        {c.state !== 'disconnected' && <button class="btn" disabled={busy} onClick={() => void perform(() => api.disconnectWork(c.connectionId))}>{t('work.disconnect')}</button>}
      </div></div></details>)}
    {active && <div class="space-layout"><nav class="space-switcher" aria-label={t('work.spaces')}><h2>{t('work.spaces')} <span class="space-count">{spaces.length}</span></h2>{spaces.map(s => <button key={s.key} aria-pressed={s.key === active.key} onClick={() => setSelected(s.key)}><Icon name="folder" size={16} />{s.space.name}</button>)}</nav>
      <SpaceView key={active.key + active.connection.authEpoch} connection={active.connection} space={active.space} members={directory?.get(active.key)} tasks={tasks.filter(v => v.connectionId === active.connection.connectionId && v.snapshot.key.workspaceId === active.space.workspaceId && v.snapshot.key.spaceId === active.space.spaceId)} directory={directory} ready={ready} open={open} tick={tick} reload={reload} />
    </div>}
  </div>
}

function SpaceView({ connection, space, members, tasks, directory, ready, open, tick, reload }: {
  connection: WorkConnection; space: WorkConnection['spaces'][number]; members?: Member[] | null; directory: Directory | null; tasks: WorkTask[]; ready: boolean; open(task: WorkTask): void; tick: number; reload(): void
}) {
  const [creating, setCreating] = useState(false)
  const canCreate = connection.capabilities?.capabilities?.includes('taskWrite') && connection.scopes?.includes('tasks:write') && space.scopes?.includes('tasks:write')
  return <section class="space-content"><div class="board-head space-heading"><h2><Icon name="folder-open" size={20} />{space.name}</h2>
    {canCreate && <button class="btn primary" onClick={() => setCreating(!creating)}>{t(creating ? 'common.cancel' : 'work.addRemote')}</button>}</div>
    {creating && canCreate && <RemoteTaskForm connection={connection.connectionId} space={space.spaceId} onSaved={() => { setCreating(false); reload() }} />}
    <TaskBoard tasks={tasks} directory={directory} open={open} ready={ready} />
    <div class="space-settings">
      {connection.scopes?.includes('members:read') && space.scopes?.includes('members:read') && <details><summary>{t('work.members')} {members && <span class="space-count">{members.length}</span>}</summary>
        {members ? <ul class="board-members">{members.map(m => <li key={m.accountId}><Icon name="user" size={15} /><strong>{m.name || t('work.unknownMember')}</strong><small>{t('work.role.' + m.role, m.role)}</small></li>)}</ul> : <p class="hint">{t(members === null ? 'work.membersUnavailable' : 'common.loading')}</p>}</details>}
      <SpacePolicy connection={connection} space={space.spaceId} tick={tick} reload={reload} />
    </div>

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
