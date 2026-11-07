// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { api, type ExecutionSettings } from '../api/client'
import { t } from '../i18n'
import { loadAgentRoster } from '../utils/agent-roster'

/** The execution policy, split by owner.
 *
 *  Global limits still belong to a section of the Agents tab; the per-agent
 *  model/concurrency fields belong to the agent row they describe, inside
 *  that row's settings disclosure. Both halves are one `ExecutionSettings`
 *  object and one `PUT /api/settings/execution`, so dirty tracking, discard
 *  and save behave exactly as they did when the fields sat in one big grid.
 *
 *  Native model ids stay free-text: a CLI's subscription catalogue need not
 *  match Panda's registry. */
export interface ExecutionController {
  form: ExecutionSettings | null
  error: string
  notice: string
  saving: boolean
  dirty: boolean
  /** Every dispatchable agent name — roster, card agents, and names already
   *  present in the saved policy. Rows for names outside the roster are
   *  rendered too, so no configurable item is left without its own entry. */
  names: string[]
  patch(values: Partial<ExecutionSettings>): void
  selectModel(key: 'agent_models' | 'subagent_models', agent: string, text: string): void
  setAgentLimit(agent: string, raw: string): void
  save(): Promise<void>
  discard(): void
}

export function useExecutionSettings(): ExecutionController {
  const [form, setForm] = useState<ExecutionSettings | null>(null)
  const [saved, setSaved] = useState('')
  const [agents, setAgents] = useState<string[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api.getExecutionSettings().then((value) => {
      setForm(value)
      setSaved(JSON.stringify(value))
    }).catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
    loadAgentRoster().then((value) => setAgents((names) => [...new Set([...names, ...value.map((agent) => agent.name)])]))
      .catch(() => {})
    // The registry roster omits custom/renamed card agents (e.g. generic).
    api.card().then((value) => setAgents((names) => [...new Set([...names, ...Object.keys(value.card.agents ?? {})])]))
      .catch(() => {})
  }, [])

  const dirty = !!form && JSON.stringify(form) !== saved
  const names = [...new Set([
    ...agents,
    ...(form ? [
      ...Object.keys(form.agent_models ?? {}),
      ...Object.keys(form.subagent_models ?? {}),
      ...Object.keys(form.agent_concurrency ?? {}),
    ] : []),
  ])].sort()

  function patch(values: Partial<ExecutionSettings>) {
    setForm((f) => (f ? { ...f, ...values } : f))
    setNotice('')
  }

  function selectModel(key: 'agent_models' | 'subagent_models', agent: string, text: string) {
    setForm((f) => {
      if (!f) return f
      const selections = { ...f[key] }
      const models = text.split(',').map((value) => value.trim()).filter(Boolean)
      if (models.length) selections[agent] = models
      else delete selections[agent]
      return { ...f, [key]: selections }
    })
    setNotice('')
  }

  function setAgentLimit(agent: string, raw: string) {
    setForm((f) => {
      if (!f) return f
      const limits = { ...f.agent_concurrency }
      const n = Number(raw)
      if (n === 0) delete limits[agent]
      else limits[agent] = n
      return { ...f, agent_concurrency: limits }
    })
    setNotice('')
  }

  async function save() {
    if (!form || saving) return
    setSaving(true)
    setError('')
    try {
      const next = await api.putExecutionSettings(form)
      setForm(next)
      setSaved(JSON.stringify(next))
      setNotice(t('execution.saved'))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { setSaving(false) }
  }

  function discard() {
    if (!saved) return
    setForm(JSON.parse(saved) as ExecutionSettings)
    setError('')
    setNotice('')
  }

  return { form, error, notice, saving, dirty, names, patch, selectModel, setAgentLimit, save, discard }
}

/** Global execution limits: the only part of the policy that is not owned by
 *  a single agent. Kept as its own group above the roster. */
export function ExecutionLimitsSection({ ctrl }: { ctrl: ExecutionController }) {
  const current = ctrl.form
  if (!current) return <p class="hint" role="status">{ctrl.error || t('common.loading')}</p>

  return (
    <section class="policy-group execution-limits">
      <h3 class="policy-group-title">{t('execution.limits')}</h3>
      <p class="hint">{t('execution.limitsHelp')}</p>
      <div class="execution-limits-grid">
        <label class="execution-field">
          <span>{t('execution.maxConcurrent')}</span>
          <input class="input" type="number" min={0} step={1} required value={current.max_concurrent}
            onInput={(event) => ctrl.patch({ max_concurrent: Number((event.target as HTMLInputElement).value) })} />
        </label>
        {(['max_concurrent', 'fanout', 'max_children', 'max_depth'] as const).map((key) => (
          <label class="execution-field" key={key}>
            <span>{t(`execution.delegation.${key}`)}</span>
            <input class="input" type="number" min={key === 'max_concurrent' ? 0 : 1} step={1} required
              value={current.delegation[key]}
              onInput={(event) => ctrl.patch({ delegation: {
                ...current.delegation, [key]: Number((event.target as HTMLInputElement).value),
              } })} />
          </label>
        ))}
      </div>
      <label class="execution-field execution-toggle">
        <span><input type="checkbox" checked={current.delegation.enabled !== false}
          onChange={(event) => ctrl.patch({ delegation: {
            ...current.delegation, enabled: (event.target as HTMLInputElement).checked,
          } })} /> {t('execution.enabled')}</span>
      </label>
    </section>
  )
}

/** One agent's three settings, rendered inside that agent's own disclosure.
 *  The two model fields keep their comma-separated native ids; the cap keeps
 *  0 = shared. Nothing here is saved on its own — the tab's save bar owns the
 *  single PUT, exactly as before. */
export function AgentExecutionFields({ ctrl, agent }: { ctrl: ExecutionController; agent: string }) {
  const current = ctrl.form
  if (!current) return <p class="hint" role="status">{ctrl.error || t('common.loading')}</p>

  return (
    <div class="agent-settings-fields">
      <p class="item-settings-note">{t('settings.agentSettingsHelp')}</p>
      {(['agent_models', 'subagent_models'] as const).map((key) => (
        <label class="execution-field" key={key}>
          <span>{t(key === 'agent_models' ? 'execution.rootModel' : 'execution.childModel')}</span>
          <input class="input" type="text"
            value={current[key]?.[agent]?.join(', ') ?? ''}
            placeholder={t(key === 'agent_models' || current[key]?.[agent]?.length === 0 ? 'execution.nativeDefault' : 'execution.inherit')}
            onChange={(event) => ctrl.selectModel(key, agent, (event.target as HTMLInputElement).value)} />
        </label>
      ))}
      <label class="execution-field">
        <span>{t('execution.agentLimit')}</span>
        <input class="input" type="number" min={0} step={1}
          value={current.agent_concurrency?.[agent] ?? 0}
          onInput={(event) => ctrl.setAgentLimit(agent, (event.target as HTMLInputElement).value)} />
      </label>
      <p class="item-settings-note agent-settings-boundary">{t('execution.nativeBoundary')}</p>
    </div>
  )
}

/** The single action bar for the whole policy. Sticky at the foot of the
 *  pane, so editing a field inside a row's menu never hides the two verbs. */
export function ExecutionSaveBar({ ctrl }: { ctrl: ExecutionController }) {
  if (!ctrl.form) return null
  return (
    <div class="policy-actions execution-save-bar">
      {ctrl.error && <p class="gate-error" role="alert">{ctrl.error}</p>}
      {ctrl.notice && <p class="hint" role="status">{ctrl.notice}</p>}
      <div class="policy-actions-bar">
        <span class={`policy-actions-state${ctrl.dirty ? ' dirty' : ''}`} role="status">
          {ctrl.dirty && (
            <>
              <span class="policy-actions-dot" aria-hidden="true" />
              {t('policy.unsaved')}
            </>
          )}
        </span>
        <button class="btn ghost" type="button" disabled={!ctrl.dirty || ctrl.saving} onClick={ctrl.discard}>
          {t('policy.discard')}
        </button>
        <button class="btn primary" type="button" disabled={!ctrl.dirty || ctrl.saving}
          onClick={() => void ctrl.save()}>
          {t('common.save')}{ctrl.saving ? '…' : ''}
        </button>
      </div>
    </div>
  )
}
