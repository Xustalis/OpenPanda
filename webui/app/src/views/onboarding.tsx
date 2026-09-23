import { useEffect, useState } from 'preact/hooks'
import {
  api,
  type ModelSettings,
  type OnboardingPatch,
  type OnboardingState,
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

  const needsWizard = Boolean(state && (!state.onboarded || !state.terms_accepted))
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
 *  the "already onboarded, still no model" banner path. */
function OnboardingWizard(props: {
  state: OnboardingState
  modelOnly: boolean
  onClose(): void
  onDone(): void
}) {
  useLocaleRerender()
  const [stepIdx, setStepIdx] = useState(props.modelOnly ? 3 : 0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const step = STEPS[stepIdx]

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
    if (stepIdx < STEPS.length - 1) setStepIdx(stepIdx + 1)
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
          {STEPS.map((s, i) => (
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
        {([1, 2, 3, 4] as const).map((n) => (
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

function ModelStep(props: { onSaved(): void }) {
  const [initial, setInitial] = useState<ModelSettings | null>(null)
  const [apiType, setApiType] = useState<ApiType>('anthropic')
  const [baseUrl, setBaseUrl] = useState('')
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<{
    ok: boolean
    reply?: string
    error?: string
  } | null>(null)

  useEffect(() => {
    api
      .getModelSettings()
      .then((s) => {
        setInitial(s)
        setApiType((s.api_type || 'anthropic') as ApiType)
        setBaseUrl(s.base_url)
        setModel(s.model)
      })
      .catch(() => setInitial({} as ModelSettings))
  }, [])

  const ready = baseUrl.trim() !== '' && model.trim() !== ''

  function payload(): ModelSettings {
    return {
      api_type: apiType,
      base_url: baseUrl.trim(),
      model: model.trim(),
      max_tokens: initial?.max_tokens ?? 0,
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
      notifyModelSaved()
      toast(t('onboarding.saved'), 'success')
      props.onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  if (!initial) return <p class="hint">{t('common.loading')}</p>

  return (
    <form onSubmit={save}>
      <h2 class="modal-title">{t('onboarding.modelTitle')}</h2>
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
          {initial?.api_key_set ? t('settings.apiKeyKeep') : t('settings.apiKeyHelp')}
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
        <button type="button" class="btn" onClick={props.onSaved}>
          {t('onboarding.skip')}
        </button>
        <button type="button" class="btn" disabled={testing || !ready} onClick={test}>
          {testing ? t('settings.testing') : t('settings.test')}
        </button>
        <button type="submit" class="btn primary" disabled={!ready || saving}>
          {saving ? t('common.save') + '…' : t('onboarding.finish')}
        </button>
      </div>
    </form>
  )
}
