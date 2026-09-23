import { useEffect, useMemo, useState } from 'preact/hooks'
import { api, type ModelEntry, type ModelsResponse, type ProviderInfo } from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'

/** The multi-model registry — web parity with the TUI's `/model` verb table:
 *  list, switch, add (provider catalogue wizard), remove, fetch remote model
 *  ids, and connectivity test. API keys are write-only: the panel answers
 *  `key_set`/`key_hint`, never the secret. */
export function ModelsSection() {
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
  const [remoteModels, setRemoteModels] = useState<string[] | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

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

  async function submit(e: Event) {
    e.preventDefault()
    if (!provider || busy) return
    setBusy('add')
    setError('')
    try {
      await api.addModel({
        provider: provider.id,
        model: model.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        alias: alias.trim() || undefined,
        ...(provider.id === 'custom'
          ? { api_type: apiType, base_url: baseURL.trim(), max_tokens: maxTokens || undefined }
          : {}),
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
      <div class="modal wizard-modal" role="dialog" aria-label={t('models.add')}>
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
                <input
                  class="input mono u-flex-1"
                  value={model}
                  onInput={(e) => setModel((e.target as HTMLInputElement).value)}
                  placeholder={provider.default_model || 'model-id'}
                  list="wizard-remote-models"
                  required={provider.id === 'custom'}
                />
                <button type="button" class="btn" disabled={busy !== ''} onClick={fetchRemote}>
                  {busy === 'fetch' ? '…' : t('models.fetch')}
                </button>
              </div>
              {remoteModels && (
                <datalist id="wizard-remote-models">
                  {remoteModels.map((id) => (
                    <option key={id} value={id} />
                  ))}
                </datalist>
              )}
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
              {provider.id === 'custom' && (
                <div class="field-group">
                  <label>{t('settings.maxTokens')}</label>
                  <input
                    class="input"
                    type="number"
                    min={0}
                    value={maxTokens}
                    onInput={(e) => setMaxTokens(Number((e.target as HTMLInputElement).value) || 0)}
                  />
                </div>
              )}
            </div>

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
