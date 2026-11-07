// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { api, type ExecutionSettings } from '../api/client'
import { t } from '../i18n'
import { loadAgentRoster } from '../utils/agent-roster'

/** Uses the existing policy sheet tokens; native model ids remain editable
 *  because a CLI's subscription catalogue need not match Panda's registry. */
export function ExecutionSettingsForm() {
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

  if (!form) return <p class="hint" role="status">{error || t('common.loading')}</p>
  const current = form
  const dirty = JSON.stringify(current) !== saved
  const names = [...new Set([
    ...agents, ...Object.keys(current.agent_models ?? {}),
    ...Object.keys(current.subagent_models ?? {}), ...Object.keys(current.agent_concurrency ?? {}),
  ])].sort()

  function patch(values: Partial<ExecutionSettings>) {
    setForm({ ...current, ...values })
    setNotice('')
  }

  function selectModel(key: 'agent_models' | 'subagent_models', agent: string, text: string) {
    const selections = { ...current[key] }
    const models = text.split(',').map((value) => value.trim()).filter(Boolean)
    if (models.length) selections[agent] = models
    else delete selections[agent]
    patch({ [key]: selections })
  }

  async function save(event: Event) {
    event.preventDefault()
    setSaving(true)
    setError('')
    try {
      const next = await api.putExecutionSettings(current)
      setForm(next)
      setSaved(JSON.stringify(next))
      setNotice(t('execution.saved'))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { setSaving(false) }
  }

  return (
    <form class="policy-form" onSubmit={save}>
      <section class="policy-group">
        <h3 class="policy-group-title">{t('execution.limits')}</h3>
        <p class="hint">{t('execution.limitsHelp')}</p>
        <div class="execution-limits-grid">
          <label class="execution-field">
            <span>{t('execution.maxConcurrent')}</span>
            <input class="input" type="number" min={0} step={1} required value={current.max_concurrent}
              onInput={(event) => patch({ max_concurrent: Number((event.target as HTMLInputElement).value) })} />
          </label>
          {(['max_concurrent', 'fanout', 'max_children', 'max_depth'] as const).map((key) => (
            <label class="execution-field" key={key}>
              <span>{t(`execution.delegation.${key}`)}</span>
              <input class="input" type="number" min={key === 'max_concurrent' ? 0 : 1} step={1} required
                value={current.delegation[key]}
                onInput={(event) => patch({ delegation: {
                  ...current.delegation, [key]: Number((event.target as HTMLInputElement).value),
                } })} />
            </label>
          ))}
        </div>
        <label class="execution-field">
          <span><input type="checkbox" checked={current.delegation.enabled !== false}
            onChange={(event) => patch({ delegation: {
              ...current.delegation, enabled: (event.target as HTMLInputElement).checked,
            } })} /> {t('execution.enabled')}</span>
        </label>
      </section>
      <section class="policy-group">
        <h3 class="policy-group-title">{t('execution.title')}</h3>
        <p class="hint">{t('execution.modelsHelp')}</p>
        <p class="hint">{t('execution.nativeBoundary')}</p>
        <div class="execution-agent-grid">
          {names.map((agent) => (
            <fieldset class="execution-agent" key={agent}>
              <legend>{agent}</legend>
              {(['agent_models', 'subagent_models'] as const).map((key) => (
                <label class="execution-field" key={key}>
                  <span>{t(key === 'agent_models' ? 'execution.rootModel' : 'execution.childModel')}</span>
                  <input class="input" type="text"
                    value={current[key]?.[agent]?.join(', ') ?? ''}
                    placeholder={t(key === 'agent_models' || current[key]?.[agent]?.length === 0 ? 'execution.nativeDefault' : 'execution.inherit')}
                    onChange={(event) => selectModel(key, agent, (event.target as HTMLInputElement).value)} />
                </label>
              ))}
              <label class="execution-field">
                <span>{t('execution.agentLimit')}</span>
                <input class="input" type="number" min={0} step={1}
                  value={current.agent_concurrency?.[agent] ?? 0}
                  onInput={(event) => {
                    const limits = { ...current.agent_concurrency }
                    const n = Number((event.target as HTMLInputElement).value)
                    if (n === 0) delete limits[agent]
                    else limits[agent] = n
                    patch({ agent_concurrency: limits })
                  }} />
              </label>
            </fieldset>
          ))}
        </div>
      </section>
      <div class="policy-actions">
        {error && <p class="gate-error" role="alert">{error}</p>}
        <p class="hint" role="status">{notice}</p>
        <div class="policy-actions-bar">
          <button class="btn ghost" type="button" disabled={!dirty || saving}
            onClick={() => { setForm(JSON.parse(saved) as ExecutionSettings); setError(''); setNotice('') }}>
            {t('policy.discard')}
          </button>
          <button class="btn primary" type="submit" disabled={!dirty || saving}>{t('common.save')}{saving ? '…' : ''}</button>
        </div>
      </div>
    </form>
  )
}
