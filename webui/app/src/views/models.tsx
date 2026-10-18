// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, type ModelEntry, type ModelsResponse, type ProviderInfo } from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ModelPicker } from '../components/model-picker'
import { useModalFocus } from '../components/modal-focus'

/** The multi-model registry — web parity with the TUI's `/model` verb table:
 *  list, switch, add (provider catalogue wizard), remove, fetch remote model
 *  ids, and connectivity test. API keys are write-only: the panel answers
 *  `key_set`/`key_hint`, never the secret.
 *  `onOpenAdvanced` is wired by the settings view: a failed test of the
 *  *active* model means its key/endpoint is broken, and the only repair
 *  surface is the advanced form behind a disclosure — the button jumps to it. */
export function ModelsSection(props: { onOpenAdvanced?: () => void }) {
  useLocaleRerender()
  const [data, setData] = useState<ModelsResponse | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const [wizardOpen, setWizardOpen] = useState(false)
  const [results, setResults] = useState<Record<string, string>>({})

  function reload() {
    api
      .models()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }
  useEffect(reload, [])

  async function run(key: string, fn: () => Promise<unknown>) {
    if (busy) return
    setBusy(key)
    setNotice('')
    setError('')
    try {
      await fn()
      reload()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function test(alias: string) {
    setBusy(`test:${alias}`)
    try {
      const r = await api.testModelRef({ alias })
      setResults((rs) => ({
        ...rs,
        [alias]: r.ok ? `ok:${(r.reply ?? '').slice(0, 60)}` : `err:${r.error ?? ''}`,
      }))
    } catch (e: unknown) {
      setResults((rs) => ({ ...rs, [alias]: `err:${e instanceof Error ? e.message : String(e)}` }))
    } finally {
      setBusy('')
    }
  }

  if (error && !data) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('models.title')}</h2>
        <p class="gate-error">{error}</p>
      </div>
    )
  }
  if (!data) return null

  const active = data.active

  return (
    <div class="models-section">
      <div class="card settings-card model-active-card">
        <div class="model-active-head">
          <div>
            <div class="section-title">{t('models.active')}</div>
            <div class="model-active-name">
              <span class="mono">{active.model || '—'}</span>
              <span class={`badge ${active.key_set || active.no_auth ? 'green' : 'yellow'}`}>
                {active.no_auth
                  ? t('models.noAuth')
                  : active.key_set
                    ? t('models.keySet')
                    : t('models.keyMissing')}
              </span>
            </div>
            <div class="dim model-active-sub">
              {active.provider_label || active.provider || '—'} · {active.alias} ·{' '}
              <span class="mono">{active.base_url || '—'}</span>
              {active.context_window ? ` · ${fmtTokens(active.context_window)} ctx` : ''}
              {active.max_tokens ? ` · ${fmtTokens(active.max_tokens)} ${t('models.out')}` : ''}
              {active.thinking ? ` · ${t(`models.thinking.${active.thinking}`)}` : ''}
            </div>
          </div>
          <div class="model-active-actions">
            <button
              class="btn small"
              disabled={busy !== ''}
              onClick={() => test(active.alias)}
            >
              {busy === `test:${active.alias}` ? t('models.testing') : t('models.test')}
            </button>
          </div>
        </div>
        {results[active.alias] && <ModelResult line={results[active.alias]!} />}
        {results[active.alias]?.startsWith('err:') && props.onOpenAdvanced && (
          <button class="btn small" onClick={props.onOpenAdvanced}>
            {t('models.fixKey')}
          </button>
        )}
      </div>

      <div class="card settings-card">
        <div class="models-head">
          <h2 class="block-title">{t('models.registry')}</h2>
          <button class="btn primary small" onClick={() => setWizardOpen(true)}>
            {t('models.add')}
          </button>
        </div>
        <p class="hint">{t('models.registryHelp')}</p>

        {data.models.length === 0 && <p class="dim">{t('models.empty')}</p>}
        {data.models.length > 0 && (
          <div class="model-rows">
            {data.models.map((m) => (
              <ModelRow
                key={m.alias}
                m={m}
                busy={busy}
                result={results[m.alias]}
                onUse={() => run(`use:${m.alias}`, () => api.useModel(m.alias))}
                onTest={() => test(m.alias)}
                onRemove={() =>
                  run(`rm:${m.alias}`, async () => {
                    await api.removeModel(m.alias)
                    setNotice(t('models.removed', { alias: m.alias }))
                  })
                }
              />
            ))}
          </div>
        )}
        {notice && <p class="test-result ok">{notice}</p>}
        {error && <p class="gate-error">{error}</p>}
      </div>

      {wizardOpen && (
        <AddModelWizard
          providers={data.providers}
          onClose={() => setWizardOpen(false)}
          onAdded={() => {
            setWizardOpen(false)
            setNotice(t('models.added'))
            reload()
          }}
        />
      )}
    </div>
  )
}

function ModelRow(props: {
  m: ModelEntry
  busy: string
  result?: string
  onUse: () => void
  onTest: () => void
  onRemove: () => void
}) {
  const { m } = props
  const [confirming, setConfirming] = useState(false)
  return (
    <div class={`model-row${m.active ? ' active' : ''}`}>
      <div class="model-row-main">
        <div class="model-row-top">
          <span class="model-alias mono">{m.alias}</span>
          {m.active && <span class="badge green">{t('models.inUse')}</span>}
          {m.no_auth ? (
            <span class="badge">{t('models.noAuth')}</span>
          ) : (
            <span class={`badge ${m.key_set ? 'green' : 'yellow'}`}>
              {m.key_set ? `key ${m.key_hint ?? '✓'}` : t('models.keyMissing')}
            </span>
          )}
        </div>
        <div class="model-row-sub dim">
          <span class="mono">{m.model || '—'}</span>
          {' · '}
          {m.provider_label || m.provider || m.api_type}
          {' · '}
          <span class="mono model-url">{m.base_url || '—'}</span>
          {m.context_window ? ` · ${fmtTokens(m.context_window)} ctx` : ''}
          {m.max_tokens ? ` · ${fmtTokens(m.max_tokens)} ${t('models.out')}` : ''}
          {m.thinking ? ` · ${t(`models.thinking.${m.thinking}`)}` : ''}
        </div>
        {props.result && <ModelResult line={props.result} />}
      </div>
      <div class="model-row-actions">
        {!m.active && (
          <button class="btn small" disabled={props.busy !== ''} onClick={props.onUse}>
            {props.busy === `use:${m.alias}` ? '…' : t('models.use')}
          </button>
        )}
        <button class="btn small" disabled={props.busy !== ''} onClick={props.onTest}>
          {props.busy === `test:${m.alias}` ? '…' : t('models.test')}
        </button>
        {!m.active &&
          (confirming ? (
            <>
              <button class="btn small danger" disabled={props.busy !== ''} onClick={props.onRemove}>
                {t('models.confirmRemove')}
              </button>
              <button class="btn small" onClick={() => setConfirming(false)}>
                {t('common.cancel')}
              </button>
            </>
          ) : (
            <button class="btn small ghost" onClick={() => setConfirming(true)}>
              {t('models.remove')}
            </button>
          ))}
      </div>
    </div>
  )
}

function ModelResult(props: { line: string }) {
  const ok = props.line.startsWith('ok:')
  return (
    <p class={`test-result ${ok ? 'ok' : 'bad'}`}>
      {ok ? t('models.testOk', { reply: props.line.slice(3) }) : `${t('models.testFail')} ${props.line.slice(4)}`}
    </p>
  )
}

/** Add-model wizard: pick a catalogue provider (or custom), then fill model
 *  id, key, alias. Mirrors `/model add` — the same providers.All table. */
function AddModelWizard(props: {
  providers: ProviderInfo[]
  onClose: () => void
  onAdded: () => void
}) {
  const [provider, setProvider] = useState<ProviderInfo | null>(null)
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [alias, setAlias] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [apiType, setAPIType] = useState<'anthropic' | 'openai'>('openai')
  const [maxTokens, setMaxTokens] = useState(0)
  const [ctxWindow, setCtxWindow] = useState(0)
  const [thinking, setThinking] = useState<'auto' | 'on' | 'off'>('auto')
  const [thinkBudget, setThinkBudget] = useState(0)
  const [maxRetries, setMaxRetries] = useState(0)
  const [noAuth, setNoAuth] = useState(false)
  const [paramsJSON, setParamsJSON] = useState('')
  const [headersJSON, setHeadersJSON] = useState('')
  const [remoteModels, setRemoteModels] = useState<string[] | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  const regions = useMemo(() => {
    const groups: Record<string, ProviderInfo[]> = { global: [], cn: [] }
    for (const p of props.providers) {
      ;(groups[p.region] ?? groups.global)!.push(p)
    }
    return groups
  }, [props.providers])

  function pick(p: ProviderInfo) {
    setProvider(p)
    setModel(p.default_model)
    setRemoteModels(null)
    setError('')
  }

  async function fetchRemote() {
    if (!provider) return
    setBusy('fetch')
    setError('')
    try {
      const r = await api.fetchModels(
        provider.id === 'custom'
          ? { provider: 'custom', base_url: baseURL.trim(), api_type: apiType, model, api_key: apiKey }
          : { provider: provider.id, model, api_key: apiKey || undefined },
      )
      if (r.ok && r.models) setRemoteModels(r.models)
      else setError(r.error ?? t('models.fetchFail'))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  function parseJSONField(v: string): Record<string, unknown> | undefined {
    if (!v.trim()) return undefined
    const o = JSON.parse(v)
    if (typeof o !== 'object' || o === null || Array.isArray(o)) throw new Error('not an object')
    return o as Record<string, unknown>
  }

  async function submit(e: Event) {
    e.preventDefault()
    if (!provider || busy) return
    setError('')
    let params: Record<string, unknown> | undefined
    let headers: Record<string, string> | undefined
    try {
      params = parseJSONField(paramsJSON)
      headers = parseJSONField(headersJSON) as Record<string, string> | undefined
    } catch {
      setError(t('models.badJSON'))
      return
    }
    setBusy('add')
    try {
      await api.addModel({
        provider: provider.id,
        model: model.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        alias: alias.trim() || undefined,
        max_tokens: maxTokens || undefined,
        context_window: ctxWindow || undefined,
        thinking: thinking !== 'auto' ? thinking : undefined,
        thinking_budget: thinkBudget || undefined,
        max_retries: maxRetries || undefined,
        no_auth: noAuth || undefined,
        params,
        headers,
        ...(provider.id === 'custom' ? { api_type: apiType, base_url: baseURL.trim() } : {}),
      })
      props.onAdded()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy('')
    }
  }

  const needsKey = provider !== null && !provider.no_auth && !provider.key_saved
  const canSubmit =
    provider !== null &&
    (provider.id === 'custom' ? baseURL.trim() !== '' && model.trim() !== '' : true) &&
    (!needsKey || apiKey.trim() !== '')

  return (
    <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
      <div
        ref={box}
        class="modal wizard-modal"
        role="dialog"
        aria-modal="true"
        aria-label={t('models.add')}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            props.onClose()
          }
        }}
      >
        <h3 class="modal-title">{t('models.addTitle')}</h3>

        {!provider && (
          <>
            <p class="hint">{t('models.pickProvider')}</p>
            {(['global', 'cn'] as const).map(
              (r) =>
                regions[r]!.length > 0 && (
                  <div key={r}>
                    <div class="section-title wizard-region">{t(`models.region.${r}`)}</div>
                    <div class="provider-grid">
                      {regions[r]!.map((p) => (
                        <button key={p.id} class="provider-card" onClick={() => pick(p)}>
                          <span class="provider-name">{p.label}</span>
                          <span class="provider-sub dim mono">{p.default_model || p.api_type}</span>
                          <span class="provider-badges">
                            {p.no_auth && <span class="badge">{t('models.noAuth')}</span>}
                            {p.key_saved && <span class="badge green">{t('models.keySaved')}</span>}
                          </span>
                        </button>
                      ))}
                    </div>
                  </div>
                ),
            )}
            <div class="modal-actions">
              <button class="btn" onClick={props.onClose}>
                {t('common.cancel')}
              </button>
            </div>
          </>
        )}

        {provider && (
          <form onSubmit={submit}>
            <div class="wizard-picked">
              <button class="btn small ghost" type="button" onClick={() => setProvider(null)}>
                ← {provider.label}
              </button>
            </div>

            {provider.id === 'custom' && (
              <>
                <div class="field-group">
                  <label>{t('settings.apiType')}</label>
                  <div class="segmented">
                    {(['openai', 'anthropic'] as const).map((v) => (
                      <button
                        key={v}
                        type="button"
                        class={`seg${apiType === v ? ' on' : ''}`}
                        onClick={() => setAPIType(v)}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                </div>
                <div class="field-group">
                  <label>{t('settings.baseURL')}</label>
                  <input
                    class="input mono"
                    value={baseURL}
                    onInput={(e) => setBaseURL((e.target as HTMLInputElement).value)}
                    placeholder="https://…/v1"
                    required
                  />
                </div>
              </>
            )}

            <div class="field-group">
              <label>{t('settings.model')}</label>
              <div class="field-row">
                <ModelPicker
                  value={model}
                  onValue={setModel}
                  options={remoteModels ?? []}
                  placeholder={provider.default_model || 'model-id'}
                  required={provider.id === 'custom'}
                />
                <button type="button" class="btn" disabled={busy !== ''} onClick={fetchRemote}>
                  {busy === 'fetch' ? '…' : t('models.fetch')}
                </button>
              </div>
              {remoteModels && (
                <p class="hint">
                  {t('models.fetched', { n: remoteModels.length })}
                </p>
              )}
            </div>

            {!provider.no_auth && (
              <div class="field-group">
                <label>{t('settings.apiKey')}</label>
                <input
                  class="input mono"
                  type="password"
                  autocomplete="off"
                  value={apiKey}
                  onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
                  placeholder={provider.key_saved ? t('models.keyReuseHint') : 'sk-…'}
                />
                {provider.key_saved && <p class="hint">{t('models.keyReuseHelp')}</p>}
              </div>
            )}

            <div class="field-row">
              <div class="field-group u-flex-1">
                <label>{t('models.alias')}</label>
                <input
                  class="input"
                  value={alias}
                  onInput={(e) => setAlias((e.target as HTMLInputElement).value)}
                  placeholder={provider.id}
                />
              </div>
            </div>

            <details class="wizard-advanced">
              <summary>{t('models.advanced')}</summary>
              <div class="field-row">
                <div class="field-group">
                  <label>{t('settings.maxTokens')}</label>
                  <input
                    class="input"
                    type="number"
                    min={0}
                    value={maxTokens}
                    onInput={(e) => setMaxTokens(Number((e.target as HTMLInputElement).value) || 0)}
                  />
                  <p class="hint">{t('models.autoHint')}</p>
                </div>
                <div class="field-group">
                  <label>{t('settings.contextWindow')}</label>
                  <input
                    class="input"
                    type="number"
                    min={0}
                    value={ctxWindow}
                    onInput={(e) => setCtxWindow(Number((e.target as HTMLInputElement).value) || 0)}
                  />
                  <p class="hint">{t('models.autoHint')}</p>
                </div>
              </div>
              <div class="field-row">
                <div class="field-group">
                  <label>{t('models.thinking')}</label>
                  <div class="segmented">
                    {(['auto', 'on', 'off'] as const).map((v) => (
                      <button
                        key={v}
                        type="button"
                        class={`seg${thinking === v ? ' on' : ''}`}
                        onClick={() => setThinking(v)}
                      >
                        {t(`models.thinking.${v}`)}
                      </button>
                    ))}
                  </div>
                </div>
                <div class="field-group">
                  <label>{t('models.thinkingBudget')}</label>
                  <input
                    class="input"
                    type="number"
                    min={0}
                    value={thinkBudget}
                    onInput={(e) => setThinkBudget(Number((e.target as HTMLInputElement).value) || 0)}
                  />
                  <p class="hint">{t('models.autoHint')}</p>
                </div>
              </div>
              <div class="field-row">
                <div class="field-group">
                  <label>{t('settings.maxRetries')}</label>
                  <input
                    class="input"
                    type="number"
                    min={-1}
                    value={maxRetries}
                    onInput={(e) => setMaxRetries(Number((e.target as HTMLInputElement).value) || 0)}
                  />
                  <p class="hint">{t('settings.maxRetriesHelp')}</p>
                </div>
                <div class="field-group">
                  <label class="checkbox-label">
                    <input
                      type="checkbox"
                      checked={noAuth}
                      onChange={(e) => setNoAuth((e.target as HTMLInputElement).checked)}
                    />{' '}
                    {t('models.noAuth')}
                  </label>
                  <p class="hint">{t('models.noAuthHelp')}</p>
                </div>
              </div>
              <div class="field-group">
                <label>{t('models.params')}</label>
                <input
                  class="input mono"
                  value={paramsJSON}
                  onInput={(e) => setParamsJSON((e.target as HTMLInputElement).value)}
                  placeholder='{"temperature":0.7}'
                />
                <p class="hint">{t('models.paramsHelp')}</p>
              </div>
              <div class="field-group">
                <label>{t('models.headers')}</label>
                <input
                  class="input mono"
                  value={headersJSON}
                  onInput={(e) => setHeadersJSON((e.target as HTMLInputElement).value)}
                  placeholder='{"X-Tenant":"blue"}'
                />
                <p class="hint">{t('models.headersHelp')}</p>
              </div>
            </details>

            {error && <p class="gate-error">{error}</p>}
            <div class="modal-actions">
              <button type="button" class="btn" onClick={props.onClose}>
                {t('common.cancel')}
              </button>
              <button type="submit" class="btn primary" disabled={!canSubmit || busy !== ''}>
                {busy === 'add' ? '…' : t('models.addConfirm')}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`
  return String(n)
}
