// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { api, type WorkConnection, type WorkPolicy, type WorkRun, type WorkProjectBinding } from '../api/client'
import { useWorkRead } from './work-read'
import { t } from '../i18n'

const terminal = new Set(['succeeded', 'failed', 'cancelled', 'superseded', 'paused'])

export function WorkResults({ connection = '', taskKey = '', tick, reload }: { connection?: string; taskKey?: string; tick: number; reload(): void }) {
  const runs = useWorkRead(() => api.workRuns(connection, taskKey), [connection, taskKey], tick)
  return <div class="work-results">
    <h2>{t('work.results')}</h2>
    {runs.error && <p class="work-error" role="alert">{runs.error}</p>}
    {runs.data?.length === 0 && <p class="hint">{t('work.noResults')}</p>}
    {runs.data?.map(run => <RunCard key={run.requestId} id={run.requestId} title={run.title} state={run.state} updatedAt={run.updatedAt} tick={tick} reload={reload} />)}
  </div>
}

function RunCard({ id, title, state, updatedAt, tick, reload }: { id: string; title: string; state: string; updatedAt: number; tick: number; reload(): void }) {
  const [open, setOpen] = useState(false)
  return <details class="card work-result" onToggle={e => setOpen(e.currentTarget.open)}>
    <summary>{title} · {t(`work.state.${state}`)} · {new Date(updatedAt).toLocaleString()}</summary>
    {open && <RunDisclosure id={id} tick={tick} reload={reload} />}
  </details>
}

function RunDisclosure({ id, tick, reload }: { id: string; tick: number; reload(): void }) {
  const result = useWorkRead(() => api.workResult(id), [id], tick)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [consent, setConsent] = useState('')
  const [reason, setReason] = useState('')
  const perform = async (fn: () => Promise<unknown>) => {
    if (busy) return
    setBusy(true); setError(''); setNotice('')
    try { await fn(); reload() } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
  }
  const run = result.data
  const exportResult = (copy: boolean) => perform(async () => {
    const text = await api.exportWork(id)
    if (copy) { await navigator.clipboard.writeText(text); setNotice(t('work.copied')); return }
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
    const a = document.createElement('a'); a.href = url; a.download = `work-${id}.txt`; a.click(); URL.revokeObjectURL(url)
  })
  return <div aria-busy={busy}>
    {(error || result.error) && <p class="work-error" role="alert">{error || result.error}</p>}
    {notice && <p role="status">{notice}</p>}
    {run && <>
      <p class="hint work-code">{run.requestId} · {run.mode}</p>
      {run.error && <p class="work-error">{run.error}</p>}
      <div class="work-actions">
        <button class="btn" disabled={busy} onClick={() => void exportResult(true)}>{t('work.copy')}</button>
        <button class="btn" disabled={busy} onClick={() => void exportResult(false)}>{t('work.export')}</button>
        {!terminal.has(run.state) && <button class="btn" disabled={busy} onClick={() => void perform(() => api.pauseWork(id))}>{t('work.pause')}</button>}
      </div>
      {run.deliveries.length === 0 && <pre class="work-payload">{run.prepared}</pre>}
      {run.deliveries.map((item, index) => <article class="work-delivery" key={`${item.delivery.deliveryId}:${item.delivery.revision}`}>
        <h3>{t('work.delivery')} {item.delivery.revision} · {item.syncState}</h3>
        <p class="hint">{item.delivery.scope.fullGoal ? t('work.fullGoal') : t('work.partialGoal')}</p>
        <pre class="work-payload">{item.delivery.summary}</pre>
        <h4>{t('work.steps')}</h4>
        <ol>{item.delivery.steps.map(s => <li><strong>{s.title}</strong> {s.outcome}</li>)}</ol>
        {item.files.length > 0 && <><h4>{t('work.files')}</h4><ul>{item.files.map(f => <li class="work-code">{f}</li>)}</ul></>}
        <h4>{t('work.verification')}</h4>
        <ul>{item.delivery.verification.map(v => <li>{v.status} · {v.summary} <small>({v.method})</small></li>)}</ul>
        {item.delivery.remaining.length > 0 && <><h4>{t('work.remaining')}</h4><ul>{item.delivery.remaining.map(r => <li>{r}</li>)}</ul></>}
        {item.failure && <p class="work-error">{item.failure}</p>}
        {run.mode === 'after_execution' && index === run.deliveries.length - 1 && run.state === 'awaiting_acceptance' && <div class="work-form">
          <p class="hint">{t('work.acceptHint')}</p>
          <label>{t('work.returnReason')}<textarea value={reason} maxLength={16384} onInput={e => setReason(e.currentTarget.value)} /></label>
          <div class="work-actions">
            <button class="btn primary" disabled={busy} onClick={() => void perform(() => api.decideWorkDelivery(item.delivery.deliveryId, item.delivery.revision, 'accept', reason))}>{t('work.accept')}</button>
            <button class="btn" disabled={busy} onClick={() => void perform(() => api.decideWorkDelivery(item.delivery.deliveryId, item.delivery.revision, 'return', reason))}>{t('work.return')}</button>
          </div>
        </div>}
      </article>)}
      {run.actions.map(action => <article class="work-action card" key={action.actionId}>
        <h3>{t('work.finalAction')} · {t(`work.state.${action.state}`)}</h3>
        <dl class="work-facts">
          <div><dt>{t('work.target')}</dt><dd class="work-code">{action.intent.directory}/{action.intent.filename}</dd></div>
          <div><dt>{t('work.account')}</dt><dd>{action.intent.accountId} / {action.intent.deviceId}</dd></div>
          <div><dt>{t('work.expires')}</dt><dd>{new Date(action.intent.expiresAt).toLocaleString()}</dd></div>
          <div><dt>{t('work.fingerprint')}</dt><dd class="work-code">{action.fingerprint}</dd></div>
          <div><dt>{t('work.payloadHash')}</dt><dd class="work-code">{action.intent.sha256}</dd></div>
          <div><dt>{t('work.inputVersion')}</dt><dd class="work-code">{action.intent.inputRevision} / {action.intent.attemptId}</dd></div>
        </dl>
        <details><summary>{t('work.exactContent')}</summary><pre class="work-payload">{action.intent.content}</pre></details>
        {action.receipt && <p class="work-code">{t('work.receipt')} · {action.receipt.filename} · {action.receipt.sha256} · {action.receipt.method}</p>}
        {run.mode === 'before_submit' && ['prepared', 'approved'].includes(action.state) && !terminal.has(run.state) && <div class="work-form">
          <label class="work-check"><input type="checkbox" checked={consent === action.fingerprint} onChange={e => setConsent(e.currentTarget.checked ? action.fingerprint : '')} />{t('work.consentExact')}</label>
          <div class="work-actions">
            <button class="btn primary" disabled={busy || consent !== action.fingerprint || action.intent.expiresAt <= Date.now()} onClick={() => void perform(() => api.decideWorkAction(action.actionId, action.fingerprint, 'approve'))}>{t('work.approveSubmit')}</button>
            <button class="btn" disabled={busy} onClick={() => void perform(() => api.decideWorkAction(action.actionId, action.fingerprint, 'reject'))}>{t('work.rejectSubmit')}</button>
          </div>
        </div>}
      </article>)}
      <SyncFacts run={run} />
    </>}
  </div>
}

function SyncFacts({ run }: { run: WorkRun }) {
  const waiting = run.sync.some(s => !['sent', 'archived'].includes(s.state))
  return <div class="work-sync"><h3>{t('work.syncStatus')}</h3>
    {waiting && <p role="status">{run.actions.some(a => a.receipt) ? t('work.completedPendingSync') : t('work.pendingSync')}</p>}
    <ul>{run.sync.map(s => <li key={s.eventId}>{s.kind} · {s.state} · {t('work.attempts')} {s.attempts}{s.error && <strong class="work-error"> · {s.error}</strong>}</li>)}</ul>
  </div>
}

export function PrivateResearch({ connection, taskKey, tick }: { connection: string; taskKey: string; tick: number }) {
  const [open, setOpen] = useState(false)
  const brief = useWorkRead(() => open ? api.workResearch(connection, taskKey) : Promise.resolve([]), [connection, taskKey, open], tick)
  return <details class="card" onToggle={e => setOpen(e.currentTarget.open)}><summary>{t('work.privateBrief')}</summary>
    <p class="hint">{t('work.privateBriefHint')}</p>
    {brief.error && <p class="work-error" role="status">{brief.error}</p>}
    {brief.data?.length === 0 && <p class="hint">{t('work.noBrief')}</p>}
    {brief.data?.map(b => <div><pre class="work-payload">{b.brief}</pre><small>{b.modelVersion} · {b.reservedTokens} · {new Date(b.expiresAt).toLocaleString()}</small></div>)}
  </details>
}

export function SpacePolicy({ connection, space, name, tick, reload }: { connection: WorkConnection; space: string; name: string; tick: number; reload(): void }) {
  const [open, setOpen] = useState(false)
  const policy = useWorkRead(() => open ? api.workPolicy(connection.connectionId, space) : Promise.resolve(null), [connection.connectionId, connection.authEpoch, space, open], tick)
  return <details class="work-space-policy" onToggle={e => setOpen(e.currentTarget.open)}><summary>{name} · {t('work.automationPolicy')}</summary>
    {policy.error && <p class="work-error" role="alert">{policy.error}</p>}
    {policy.data && <PolicyForm key={JSON.stringify([connection.connectionId, space, open, policy.data])} connection={connection.connectionId} space={space} initial={policy.data} reload={reload} />}
    {open && <ProjectBindings key={`${connection.connectionId}/${connection.authEpoch}/${space}`} connection={connection.connectionId} space={space} tick={tick} reload={reload} />}
  </details>
}

function ProjectBindings({ connection, space, tick, reload }: { connection: string; space: string; tick: number; reload(): void }) {
  const bindings = useWorkRead(async () => {
    const [items, projects] = await Promise.all([api.workBindings(connection, space), api.projects()])
    return { items, projects: projects.projects }
  }, [connection, space], tick)
  const [remote, setRemote] = useState('')
  const [local, setLocal] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async (binding: WorkProjectBinding) => {
    if (busy) return
    setBusy(true); setError('')
    try { await api.setWorkBinding(connection, space, binding); reload() } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
  }
  return <section class="work-form"><h3>{t('work.projectBindings')}</h3>
    <p class="hint">{t('work.projectBindingsHint')}</p>
    {(error || bindings.error) && <p class="work-error" role="alert">{error || bindings.error}</p>}
    {bindings.data && <>
      {bindings.data.items.length === 0 && <p class="hint">{t('work.noBindings')}</p>}
      <ul>{bindings.data.items.map(b => <li key={b.remoteProjectId} class="work-actions"><span>{b.remoteProjectId || t('work.wholeSpace')} → {b.localProject}</span><button class="btn" disabled={busy} onClick={() => void save({ ...b, localProject: '' })}>{t('work.removeBinding')}</button></li>)}</ul>
      <form class="work-form" onSubmit={e => { e.preventDefault(); void save({ remoteProjectId: remote, localProject: local }) }}>
        <label>{t('work.remoteProjectId')}<input value={remote} onInput={e => setRemote(e.currentTarget.value.trim())} /></label>
        <label>{t('work.localProject')}<select required value={local} onChange={e => setLocal(e.currentTarget.value)}><option value="">{t('work.chooseProject')}</option>{bindings.data.projects.map(name => <option key={name} value={name}>{name}</option>)}</select></label>
        <button class="btn" type="submit" disabled={busy || !local}>{t('common.save')}</button>
      </form>
    </>}
  </section>
}

function PolicyForm({ connection, space, initial, reload }: { connection: string; space: string; initial: WorkPolicy; reload(): void }) {
  const [p, setP] = useState<WorkPolicy>({ ...initial, mode: initial.mode || 'before_submit', maxConcurrent: initial.maxConcurrent || 1, dailyTokens: initial.dailyTokens || 20000, preparationTokens: initial.preparationTokens || 512 })
  const [expires, setExpires] = useState(initial.expiresAt ? localTime(initial.expiresAt) : '')
  const [directory, setDirectory] = useState(initial.publication?.directory || '')
  const [filename, setFilename] = useState(initial.publication?.filename || 'result-{request}.txt')
  const [fullGoal, setFullGoal] = useState(initial.publication?.fullGoal || false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const update = (patch: Partial<WorkPolicy>) => setP(v => ({ ...v, ...patch }))
  const save = async () => {
    if (busy) return
    setBusy(true); setMessage('')
    try {
      const policy: WorkPolicy = { ...p, expiresAt: expires ? new Date(expires).getTime() : 0, capabilities: p.execute ? (p.finalAction ? ['draft', 'file_publish'] : ['draft']) : [], publication: p.finalAction ? { directory, filename, fullGoal } : undefined }
      await api.setWorkPolicy(connection, space, policy); setMessage(t('work.policySaved')); reload()
    } catch (e) { setMessage(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
  }
  return <form class="work-form" onSubmit={e => { e.preventDefault(); void save() }}>
    <p class="hint">{t('work.policyHint')}</p>
    <label class="work-check"><input type="checkbox" checked={p.research} onChange={e => update({ research: e.currentTarget.checked })} />{t('work.enableResearch')}</label>
    <label class="work-check"><input type="checkbox" checked={p.execute} onChange={e => update({ execute: e.currentTarget.checked, finalAction: e.currentTarget.checked && p.finalAction })} />{t('work.enableExecute')}</label>
    <label class="work-check"><input type="checkbox" disabled={!p.execute} checked={p.finalAction} onChange={e => update({ finalAction: e.currentTarget.checked })} />{t('work.enableFinalAction')}</label>
    <label>{t('work.approvalMode')}<select value={p.mode} onChange={e => update({ mode: e.currentTarget.value as WorkPolicy['mode'] })}><option value="before_submit">{t('work.beforeSubmit')}</option><option value="after_execution">{t('work.afterExecution')}</option></select></label>
    <label>{t('work.expires')}<input type="datetime-local" required={p.research || p.execute || p.finalAction} value={expires} onInput={e => setExpires(e.currentTarget.value)} /></label>
    <div class="work-form-row">
      <label>{t('work.dailyTokens')}<input type="number" min={2300} max={1000000} value={p.dailyTokens} onInput={e => update({ dailyTokens: e.currentTarget.valueAsNumber })} /></label>
      <label>{t('work.preparationTokens')}<input type="number" min={256} max={8192} value={p.preparationTokens} onInput={e => update({ preparationTokens: e.currentTarget.valueAsNumber })} /></label>
      <label>{t('work.concurrency')}<input type="number" min={1} max={8} value={p.maxConcurrent} onInput={e => update({ maxConcurrent: e.currentTarget.valueAsNumber })} /></label>
    </div>
    <label>{t('work.scopeTaskIds')}<input value={(p.taskIds || []).join(', ')} onInput={e => update({ taskIds: e.currentTarget.value.split(',').map(v => v.trim()).filter(Boolean) })} /></label>
    <label>{t('work.scopeProject')}<input value={p.projectId} onInput={e => update({ projectId: e.currentTarget.value })} /></label>
    {p.finalAction && <fieldset><legend>{t('work.filePublication')}</legend>
      <p class="hint">{t('work.filePublicationHint')}</p>
      <label>{t('work.directory')}<input required value={directory} onInput={e => setDirectory(e.currentTarget.value)} /></label>
      <label>{t('work.filename')}<input required value={filename} onInput={e => setFilename(e.currentTarget.value)} /></label>
      <label class="work-check"><input type="checkbox" checked={fullGoal} onChange={e => setFullGoal(e.currentTarget.checked)} />{t('work.fullGoalAssertion')}</label>
    </fieldset>}
    <button class="btn primary" type="submit" disabled={busy}>{t('common.save')}</button>
    {message && <p role="status">{message}</p>}
  </form>
}

function localTime(ms: number): string {
  const d = new Date(ms)
  return new Date(ms - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
