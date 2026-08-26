import { useEffect, useState } from 'preact/hooks'
import { api, type ModelSettings } from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { toast } from '../components/toast'

// First-run onboarding (Task 9.2): `panda web` boots zero-config — the panel
// is fully reachable but chat cannot answer until a model is set up. This
// module owns the top banner that says so plus the one-step setup form;
// saving reuses the settings API, whose save hot-loads the engine, so the
// first conversation works immediately — no restart.

/** Window event fired after a successful model-settings save — from this
 *  form or the settings page — so the banner re-checks and hides itself. */
export const MODEL_SAVED_EVENT = 'openpanda:model-saved'

export function notifyModelSaved(): void {
  window.dispatchEvent(new CustomEvent(MODEL_SAVED_EVENT))
}

type ApiType = 'anthropic' | 'openai'

const EXAMPLES: Record<ApiType, { base: string; model: string }> = {
  anthropic: { base: 'https://api.anthropic.com', model: 'claude-sonnet-5' },
  openai: { base: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
}

/** The model can answer once an endpoint AND a key are configured. A fresh
 *  install carries the demo endpoint from config defaults without a key —
 *  that cannot talk to any provider, so it still counts as unconfigured. */
function modelConfigured(s: ModelSettings): boolean {
  return s.base_url.trim() !== '' && !!s.api_key_set
}

/** Persistent banner rendered at the top of the main pane. Self-contained:
 *  it checks the model settings on mount, re-checks on MODEL_SAVED_EVENT,
 *  and renders nothing once a model is usable. */
export function OnboardingBanner() {
  useLocaleRerender()
  const [settings, setSettings] = useState<ModelSettings | null>(null)
  const [open, setOpen] = useState(false)
  // Set when the user completes the form here: the banner goes away for the
  // session even if they deliberately saved a keyless endpoint.
  const [completed, setCompleted] = useState(false)

  useEffect(() => {
    const check = () =>
      api
        .getModelSettings()
        .then(setSettings)
        .catch(() => setSettings(null)) // views surface their own errors
    void check()
    window.addEventListener(MODEL_SAVED_EVENT, check)
    return () => window.removeEventListener(MODEL_SAVED_EVENT, check)
  }, [])

  if (!settings || completed || modelConfigured(settings)) return null

  return (
    <>
      <div class="onboarding-banner" role="status">
        <span class="onboarding-banner-text">{t('onboarding.banner')}</span>
        <button class="btn primary small" onClick={() => setOpen(true)}>
          {t('onboarding.cta')}
        </button>
      </div>
      {open && (
        <OnboardingModal
          initial={settings}
          onClose={() => setOpen(false)}
          onSaved={() => setCompleted(true)}
        />
      )}
    </>
  )
}

/** One-step setup form in a modal: API type / Base URL / model / API key,
 *  a connectivity test against the candidate settings, and save — which
 *  persists to config.yaml and hot-loads the engine server-side. */
function OnboardingModal(props: { initial: ModelSettings; onClose(): void; onSaved(): void }) {
  const [apiType, setApiType] = useState<ApiType>(
    (props.initial.api_type || 'anthropic') as ApiType,
  )
  // Prefill endpoint and model with the current (default) values so the
  // demo-endpoint user only pastes a key.
  const [baseUrl, setBaseUrl] = useState(props.initial.base_url)
  const [model, setModel] = useState(props.initial.model)
  const [apiKey, setApiKey] = useState('')
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<{ ok: boolean; reply?: string; error?: string } | null>(
    null,
  )

  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.key === 'Escape') props.onClose()
    }
    addEventListener('keydown', on)
    return () => removeEventListener('keydown', on)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const ready = baseUrl.trim() !== '' && model.trim() !== ''

  function payload(): ModelSettings {
    return {
      api_type: apiType,
      base_url: baseUrl.trim(),
      model: model.trim(),
      max_tokens: props.initial.max_tokens,
      api_key: apiKey.trim() || undefined, // empty = keep the stored key
    }
  }

  async function test() {
    if (testing || !ready) return
    setTesting(true)
    setTestResult(null)
    try {
      setTestResult(await api.testModelSettings(payload()))
    } catch (e: unknown) {
      setTestResult({ ok: false, error: e instanceof Error ? e.message : String(e) })
    } finally {
      setTesting(false)
    }
  }

  async function save(e: Event) {
    e.preventDefault()
    if (!ready || saving) return
    setSaving(true)
    setError('')
    try {
      await api.putModelSettings(payload())
      toast(t('onboarding.saved'), 'success')
      props.onSaved()
      props.onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div
      class="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <form
        class="modal onboarding-modal"
        onSubmit={save}
        role="dialog"
        aria-modal="true"
        aria-label={t('onboarding.title')}
      >
        <h2 class="modal-title">{t('onboarding.title')}</h2>
        <p class="modal-msg">{t('onboarding.subtitle')}</p>

        <div class="field-group">
          <label>{t('settings.apiType')}</label>
          <div class="segmented" role="radiogroup">
            {(['anthropic', 'openai'] as const).map((v) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={apiType === v}
                class={`seg${apiType === v ? ' on' : ''}`}
                onClick={() => {
                  setApiType(v)
                  setTestResult(null)
                }}
              >
                {v === 'anthropic' ? t('settings.anthropic') : t('settings.openai')}
              </button>
            ))}
          </div>
          <p class="hint">{t('settings.apiTypeHelp')}</p>
        </div>

        <div class="field-group">
          <label for="onboarding-base-url">{t('settings.baseURL')}</label>
          <input
            id="onboarding-base-url"
            class="input mono"
            type="url"
            required
            placeholder={EXAMPLES[apiType].base}
            value={baseUrl}
            onInput={(e) => {
              setBaseUrl((e.target as HTMLInputElement).value)
              setTestResult(null)
            }}
          />
          <p class="hint">{t('settings.baseURLHelp')}</p>
        </div>

        <div class="field-group">
          <label for="onboarding-model">{t('settings.model')}</label>
          <input
            id="onboarding-model"
            class="input mono"
            type="text"
            required
            placeholder={EXAMPLES[apiType].model}
            value={model}
            onInput={(e) => {
              setModel((e.target as HTMLInputElement).value)
              setTestResult(null)
            }}
          />
          <p class="hint">{t('settings.modelHelp')}</p>
        </div>

        <div class="field-group">
          <label for="onboarding-api-key">{t('settings.apiKey')}</label>
          <input
            id="onboarding-api-key"
            class="input mono"
            type="password"
            autocomplete="off"
            placeholder="sk-…"
            value={apiKey}
            onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
          />
          <p class="hint">
            {props.initial.api_key_set ? t('settings.apiKeyKeep') : t('settings.apiKeyHelp')}
          </p>
        </div>

        {testResult && (
          <p class={`test-result ${testResult.ok ? 'ok' : 'bad'}`}>
            {testResult.ok
              ? t('settings.testOk', { reply: (testResult.reply || '').slice(0, 80) })
              : `${t('settings.testFail')} ${testResult.error ?? ''}`}
          </p>
        )}
        {error && <p class="gate-error">{error}</p>}

        <div class="modal-actions">
          <button type="button" class="btn" onClick={props.onClose}>
            {t('onboarding.skip')}
          </button>
          <button type="button" class="btn" disabled={testing || !ready} onClick={test}>
            {testing ? t('settings.testing') : t('settings.test')}
          </button>
          <button type="submit" class="btn primary" disabled={!ready || saving}>
            {saving ? t('common.save') + '…' : t('common.save')}
          </button>
        </div>
      </form>
    </div>
  )
}
