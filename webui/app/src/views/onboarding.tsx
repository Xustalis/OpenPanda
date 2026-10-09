// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from 'preact/hooks'
import {
  api,
  type OnboardingPatch,
  type OnboardingState,
  type ProviderInfo,
} from '../api/client'
import { useLocaleRerender } from '../hooks'
import { locale, localeNames, locales, setLocale, t, type Locale } from '../i18n'
import { toast } from '../components/toast'

// First-run onboarding: `panda web` boots zero-config — the panel is fully
// reachable but chat cannot answer until a model is set up, and the TUI
// additionally walks language → terms → approval before the first prompt.
// The web console runs the same four steps in a wizard modal, persisting
// each answer through POST /api/onboarding so a browser-first install lands
// in the same configured state as a terminal one.

/** Window event fired after a successful model-settings save — from this
 *  wizard or the settings page — so the gate re-checks and hides itself. */
export const MODEL_SAVED_EVENT = 'openpanda:model-saved'

export function notifyModelSaved(): void {
  window.dispatchEvent(new CustomEvent(MODEL_SAVED_EVENT))
}

type Step = 'language' | 'terms' | 'approval' | 'model'
const STEPS: Step[] = ['language', 'terms', 'approval', 'model']

/** Mirrors config.TermsVersionCurrent — bump alongside it on terms changes. */
const TERMS_VERSION_CURRENT = 2

/** Effective consent: accepted AND stamped at the current revision. */
function termsCurrent(state: OnboardingState): boolean {
  return state.terms_accepted && (state.terms_version ?? 0) >= TERMS_VERSION_CURRENT
}

/** First-run gate rendered at the top of the main pane. Fresh installs
 *  (`onboarded`/`terms_accepted` unset) get the full wizard immediately;
 *  an install that finished the wizard but has no usable model gets a
 *  persistent banner whose CTA reopens the wizard on the model step. */
export function OnboardingBanner() {
  useLocaleRerender()
  const [state, setState] = useState<OnboardingState | null>(null)
  const [open, setOpen] = useState(false)
  const [completed, setCompleted] = useState(false)

  useEffect(() => {
    const check = () =>
      api
        .getOnboarding()
        .then(setState)
        .catch(() => setState(null)) // views surface their own errors
    void check()
    window.addEventListener(MODEL_SAVED_EVENT, check)
    return () => window.removeEventListener(MODEL_SAVED_EVENT, check)
  }, [])

  const needsWizard = Boolean(state && (!state.onboarded || !termsCurrent(state)))
  // Fresh installs get the wizard without a click — state setters belong in
  // an effect, not the render path.
  useEffect(() => {
    if (needsWizard) setOpen(true)
  }, [needsWizard])

  if (!state || completed) return null

  const showBanner = !needsWizard && !state.model_configured
  return (
    <>
      {showBanner && (
        <div class="onboarding-banner" role="status">
          <span class="onboarding-banner-text">{t('onboarding.banner')}</span>
          <button class="btn primary small" onClick={() => setOpen(true)}>
            {t('onboarding.cta')}
          </button>
        </div>
      )}
      {open && (
        <OnboardingWizard
          state={state}
          modelOnly={!needsWizard}
          termsOnly={needsWizard && state.onboarded}
          onClose={() => setOpen(false)}
          onDone={() => {
            setCompleted(true)
            setOpen(false)
          }}
        />
      )}
    </>
  )
}

/** The four-step wizard. `modelOnly` jumps straight to the model step for
 *  the "already onboarded, still no model" banner path; `termsOnly` shows
 *  just the license step for installs that accepted a stale terms revision
 *  (the MIT→AGPL change) — accepting completes the wizard. */
function OnboardingWizard(props: {
  state: OnboardingState
  modelOnly: boolean
  termsOnly: boolean
  onClose(): void
  onDone(): void
}) {
  useLocaleRerender()
  const steps: Step[] = props.termsOnly ? ['terms'] : STEPS
  const [stepIdx, setStepIdx] = useState(props.modelOnly ? steps.length - 1 : 0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const step = steps[stepIdx]

  async function save(patch: OnboardingPatch): Promise<boolean> {
    setBusy(true)
    setError('')
    try {
      await api.postOnboarding(patch)
      return true
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return false
    } finally {
      setBusy(false)
    }
  }

  async function next() {
    if (step === 'terms' && !(await save({ terms_accepted: true }))) return
    if (stepIdx < steps.length - 1) {
      setStepIdx(stepIdx + 1)
    } else if (props.termsOnly) {
      // Re-consent path: the license step is also the last step — the
      // install was already onboarded.
      props.onDone()
    }
  }

  async function finish() {
    if (await save({ onboarded: true })) props.onDone()
  }

  // Backdrop click / Escape close like every other modal — a skipped wizard
  // simply reopens on next launch until terms are accepted.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.key === 'Escape') props.onClose()
    }
    addEventListener('keydown', on)
    return () => removeEventListener('keydown', on)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div
      class="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <div class="modal onboarding-modal wizard" role="dialog" aria-modal="true"
        aria-label={t('onboarding.title')}>
        <div class="wizard-progress" aria-hidden="true">
          {steps.map((s, i) => (
            <span
              key={s}
              class={`wizard-dot${i < stepIdx ? ' done' : ''}${i === stepIdx ? ' on' : ''}`}
            />
          ))}
          <span class="wizard-step-label">{t(`onboarding.step.${step}`)}</span>
        </div>

        {step === 'language' && <LanguageStep />}
        {step === 'terms' && <TermsStep />}
        {step === 'approval' && (
          <ApprovalStep
            initial={props.state.approval_mode}
            onPick={async (mode) => {
              if (await save({ approval_mode: mode })) setStepIdx(stepIdx + 1)
            }}
            busy={busy}
          />
        )}
        {step === 'model' && <ModelStep onSaved={finish} />}

        {error && <p class="gate-error">{error}</p>}

        {step !== 'approval' && step !== 'model' && (
          <div class="modal-actions">
            <button type="button" class="btn" onClick={props.onClose}>
              {t('onboarding.skip')}
            </button>
            <button type="button" class="btn primary" disabled={busy} onClick={next}>
              {step === 'terms' ? t('onboarding.agree') : t('onboarding.next')}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Step 1: language ----

function LanguageStep() {
  const cur = locale()
  return (
    <>
      <h2 class="modal-title">{t('onboarding.langTitle')}</h2>
      <div class="wizard-langs" role="radiogroup">
        {locales.map((l) => (
          <button
            key={l}
            type="button"
            role="radio"
            aria-checked={cur === l}
            class={`wizard-lang${cur === l ? ' on' : ''}`}
            onClick={() => {
              setLocale(l as Locale)
              void api.postOnboarding({ locale: l }).catch(() => {})
            }}
          >
            {localeNames[l]}
          </button>
        ))}
      </div>
    </>
  )
}

// ---- Step 2: terms ----

function TermsStep() {
  return (
    <>
      <h2 class="modal-title">{t('onboarding.termsTitle')}</h2>
      <div class="wizard-terms">
        {([1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const).map((n) => (
          <section key={n} class="wizard-terms-section">
            <h3>{t(`onboarding.terms${n}h`)}</h3>
            <p>{t(`onboarding.terms${n}b`)}</p>
          </section>
        ))}
      </div>
    </>
  )
}

// ---- Step 3: approval mode ----

function ApprovalStep(props: {
  initial: 'always' | 'on-request' | 'never'
  busy: boolean
  onPick(mode: 'always' | 'on-request' | 'never'): void
}) {
  const [mode, setMode] = useState(props.initial)
  return (
    <>
      <h2 class="modal-title">{t('onboarding.approvalTitle')}</h2>
      <p class="modal-msg">{t('onboarding.approvalSub')}</p>
      <div class="wizard-approvals" role="radiogroup">
        {(['always', 'on-request', 'never'] as const).map((m) => (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={mode === m}
            class={`wizard-approval${mode === m ? ' on' : ''}`}
            onClick={() => setMode(m)}
          >
            <span class="wizard-approval-name">{t(`onboarding.approval.${m}`)}</span>
            <span class="wizard-approval-desc">{t(`onboarding.approval.${m}Desc`)}</span>
          </button>
        ))}
      </div>
      <div class="modal-actions">
        <span class="u-flex-1" />
        <button
          type="button"
          class="btn primary"
          disabled={props.busy}
          onClick={() => props.onPick(mode)}
        >
          {t('onboarding.next')}
        </button>
      </div>
    </>
  )
}

// ---- Step 4: model ----

type ApiType = 'anthropic' | 'openai'

const EXAMPLES: Record<ApiType, { base: string; model: string }> = {
  anthropic: { base: 'https://api.anthropic.com', model: 'claude-sonnet-5' },
  openai: { base: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
}

/** The model step mirrors the models page's add wizard: a catalogue card
 *  grid first (endpoints, default models, no-auth flags all prefilled),
 *  then a form that only asks for what the pick still needs — a key for
 *  hosted providers, nothing but a model id for Ollama-style ones, and the
 *  full wire shape for "custom". Saving goes through POST /api/models,
 *  which activates the first entry automatically — exactly the zero-config
 *  path this wizard exists for. */
function ModelStep(props: { onSaved(): void }) {
  const [providers, setProviders] = useState<ProviderInfo[] | null>(null)
  const [provider, setProvider] = useState<ProviderInfo | null>(null)
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [apiType, setApiType] = useState<ApiType>('openai')
  const [baseUrl, setBaseUrl] = useState('')
  const [maxTokens, setMaxTokens] = useState(0)
  const [remoteModels, setRemoteModels] = useState<string[] | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<{
    ok: boolean
    reply?: string
    error?: string
  } | null>(null)

  useEffect(() => {
    api
      .models()
      .then((r) => setProviders(r.providers))
      .catch(() => setProviders([])) // catalogue unreachable → custom form still works
  }, [])

  const regions = useMemo(() => {
    const groups: Record<string, ProviderInfo[]> = { global: [], cn: [] }
    for (const p of providers ?? []) {
      ;(groups[p.region] ?? groups.global)!.push(p)
    }
    return groups
  }, [providers])

  function pick(p: ProviderInfo) {
    setProvider(p)
    setModel(p.default_model)
    setBaseUrl(p.id === 'custom' ? '' : p.base_url)
    if (p.api_type === 'anthropic' || p.api_type === 'openai') setApiType(p.api_type)
    setRemoteModels(null)
    setTestResult(null)
    setError('')
  }

  function refPayload() {
    if (!provider) return {}
    return provider.id === 'custom'
      ? {
          provider: 'custom',
          api_type: apiType,
          base_url: baseUrl.trim(),
          model: model.trim(),
          api_key: apiKey.trim() || undefined,
        }
      : {
          provider: provider.id,
          model: model.trim() || undefined,
          api_key: apiKey.trim() || undefined,
        }
  }

  const needsKey = provider !== null && !provider.no_auth && !provider.key_saved
  const ready =
    provider !== null &&
    (provider.id === 'custom'
      ? baseUrl.trim() !== '' && model.trim() !== ''
      : model.trim() !== '' || provider.default_model !== '') &&
    (!needsKey || apiKey.trim() !== '')

  async function test() {
    if (busy !== '' || !ready) return
    setBusy('test')
    setTestResult(null)
    try {
      const r = await api.testModelRef(refPayload())
      setTestResult(r.ok ? { ok: true, reply: r.reply } : { ok: false, error: r.error })
    } catch (e: unknown) {
      setTestResult({ ok: false, error: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy('')
    }
  }

  async function fetchRemote() {
    if (!provider || busy !== '') return
    setBusy('fetch')
    setError('')
    try {
      const r = await api.fetchModels(refPayload())
      if (r.ok && r.models) setRemoteModels(r.models)
      else setError(r.error ?? t('models.fetchFail'))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function save(e: Event) {
    e.preventDefault()
    if (!ready || busy !== '') return
    setBusy('save')
    setError('')
    try {
      await api.addModel({
        provider: provider!.id,
        model: model.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        ...(provider!.id === 'custom'
          ? { api_type: apiType, base_url: baseUrl.trim(), max_tokens: maxTokens || undefined }
          : {}),
      })
      notifyModelSaved()
      toast(t('onboarding.saved'), 'success')
      props.onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy('')
    }
  }

  if (!providers) return <p class="hint">{t('common.loading')}</p>

  // Phase 1: the catalogue grid — same card markup as the models page.
  if (!provider) {
    return (
      <>
        <h2 class="modal-title">{t('onboarding.modelTitle')}</h2>
        <p class="modal-msg">{t('onboarding.subtitle')}</p>
        <p class="hint">{t('models.pickProvider')}</p>
        <div class="wizard-scroll">
          {(['global', 'cn'] as const).map(
            (r) =>
              regions[r]!.length > 0 && (
                <div key={r}>
                  <div class="section-title wizard-region">{t(`models.region.${r}`)}</div>
                  <div class="provider-grid">
                    {regions[r]!.map((p) => (
                      <button key={p.id} type="button" class="provider-card" onClick={() => pick(p)}>
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
        </div>
        <div class="modal-actions">
          <button type="button" class="btn" onClick={props.onSaved}>
            {t('onboarding.skip')}
          </button>
        </div>
      </>
    )
  }

  // Phase 2: only the fields the pick still needs.
  return (
    <form onSubmit={save}>
      <h2 class="modal-title">{t('onboarding.modelTitle')}</h2>
      <div class="wizard-picked">
        <button class="btn small ghost" type="button" onClick={() => setProvider(null)}>
          ← {provider.label}
        </button>
      </div>

      {provider.id === 'custom' && (
        <>
          <div class="field-group">
            <label>{t('settings.apiType')}</label>
            <div class="segmented" role="radiogroup">
              {(['openai', 'anthropic'] as const).map((v) => (
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
        </>
      )}

      <div class="field-group">
        <label for="onboarding-model">{t('settings.model')}</label>
        <div class="field-row">
          <input
            id="onboarding-model"
            class="input mono u-flex-1"
            type="text"
            required={provider.id === 'custom'}
            placeholder={provider.default_model || EXAMPLES[apiType].model}
            value={model}
            list="onboarding-remote-models"
            onInput={(e) => {
              setModel((e.target as HTMLInputElement).value)
              setTestResult(null)
            }}
          />
          <button type="button" class="btn" disabled={busy !== ''} onClick={fetchRemote}>
            {busy === 'fetch' ? '…' : t('models.fetch')}
          </button>
        </div>
        {remoteModels && (
          <datalist id="onboarding-remote-models">
            {remoteModels.map((id) => (
              <option key={id} value={id} />
            ))}
          </datalist>
        )}
        {remoteModels && <p class="hint">{t('models.fetched', { n: remoteModels.length })}</p>}
      </div>

      {!provider.no_auth && (
        <div class="field-group">
          <label for="onboarding-api-key">{t('settings.apiKey')}</label>
          <input
            id="onboarding-api-key"
            class="input mono"
            type="password"
            autocomplete="off"
            placeholder={provider.key_saved ? t('models.keyReuseHint') : 'sk-…'}
            value={apiKey}
            onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
          />
          {provider.key_saved && <p class="hint">{t('models.keyReuseHelp')}</p>}
        </div>
      )}

      {provider.id === 'custom' && (
        <div class="field-group">
          <label for="onboarding-max-tokens">{t('settings.maxTokens')}</label>
          <input
            id="onboarding-max-tokens"
            class="input"
            type="number"
            min={0}
            value={maxTokens || ''}
            onInput={(e) => setMaxTokens(Number((e.target as HTMLInputElement).value) || 0)}
          />
        </div>
      )}

      {testResult && (
        <p class={`test-result ${testResult.ok ? 'ok' : 'bad'}`}>
          {testResult.ok
            ? t('settings.testOk', { reply: (testResult.reply || '').slice(0, 80) })
            : `${t('settings.testFail')} ${testResult.error ?? ''}`}
        </p>
      )}
      {error && <p class="gate-error">{error}</p>}

      <div class="modal-actions">
        <button type="button" class="btn" onClick={props.onSaved}>
          {t('onboarding.skip')}
        </button>
        <button type="button" class="btn" disabled={busy !== '' || !ready} onClick={test}>
          {busy === 'test' ? t('settings.testing') : t('settings.test')}
        </button>
        <button type="submit" class="btn primary" disabled={!ready || busy === 'save'}>
          {busy === 'save' ? t('common.save') + '…' : t('onboarding.finish')}
        </button>
      </div>
    </form>
  )
}
