import { useEffect, useState } from 'preact/hooks'
import {
  api,
  type AgentInfo,
  type AgentTestResult,
  type AppSettings,
  type MCPSettings,
  type ModelSettings,
} from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { locale, localeNames, locales, setLocale } from '../i18n'
import { onThemeChange, setTheme, theme } from '../theme'

type ApiType = 'anthropic' | 'openai'

const EXAMPLES: Record<ApiType, { base: string; model: string }> = {
  anthropic: { base: 'https://api.anthropic.com', model: 'claude-sonnet-4-5' },
  openai: { base: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
}

/** Settings sections: the left rail groups the page Codex-style instead of
 *  dumping every form on one scroll. "memory" and "devices" are jumps, not
 *  sections — they already live on their own pages. */
type Section = 'general' | 'config' | 'memory' | 'devices' | 'agents'

const SECTIONS: Array<{ id: Section; label: string; jump?: string }> = [
  { id: 'general', label: 'settings.group.general' },
  { id: 'config', label: 'settings.group.config' },
  { id: 'memory', label: 'settings.group.memory', jump: '#/memory' },
  { id: 'devices', label: 'settings.group.devices', jump: '#/nodes' },
  { id: 'agents', label: 'settings.group.agents' },
]

/** The settings page (C1): grouped behind a left rail — general (language +
 *  appearance theme), config (model, MCP, and the four app policies:
 *  injection, routing preference, memory caps, approval gate), plus jumps to
 *  the memory console and the devices page, and the agent CLI roster. */
export function SettingsView() {
  useLocaleRerender()
  const [section, setSection] = useState<Section>('general')

  return (
    <section>
      <h1 class="page-title">{t('settings.title')}</h1>
      <p class="page-sub">{t('settings.subtitle')}</p>

      <div class="settings-layout">
        <nav class="settings-nav" aria-label={t('settings.title')}>
          {SECTIONS.map((s) =>
            s.jump ? (
              <a key={s.id} href={s.jump} class="settings-nav-item">
                {t(s.label)}
              </a>
            ) : (
              <button
                key={s.id}
                type="button"
                class={`settings-nav-item${section === s.id ? ' active' : ''}`}
                onClick={() => setSection(s.id)}
              >
                {t(s.label)}
              </button>
            ),
          )}
        </nav>

        <div class="settings-body">
          {section === 'general' && <GeneralSection />}
          {section === 'config' && (
            <>
              <ModelSection />
              <MCPSection />
              <PolicySection />
            </>
          )}
          {section === 'agents' && <AgentsSection />}
        </div>
      </div>
    </section>
  )
}

/** General: language + appearance theme (light / dark / follow system). */
function GeneralSection() {
  const [, force] = useState(0)
  useEffect(() => onThemeChange(() => force((v) => v + 1)), [])

  return (
    <div class="card settings-card">
      <h2 class="block-title">{t('settings.group.general')}</h2>

      <div class="field-group">
        <label>{t('settings.language')}</label>
        <select
          class="input"
          value={locale()}
          onChange={(e) => setLocale((e.target as HTMLSelectElement).value as never)}
        >
          {locales.map((l) => (
            <option key={l} value={l}>
              {localeNames[l]}
            </option>
          ))}
        </select>
      </div>

      <div class="field-group">
        <label>{t('settings.theme')}</label>
        <div class="segmented" role="radiogroup">
          {(['light', 'dark', 'auto'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={theme() === v}
              class={`seg${theme() === v ? ' on' : ''}`}
              onClick={() => setTheme(v)}
            >
              {t(`settings.theme.${v}`)}
            </button>
          ))}
        </div>
        <p class="hint">{t('settings.themeHelp')}</p>
      </div>
    </div>
  )
}

/** The LLM provider form: Anthropic or OpenAI-wire format, any base URL,
 *  any model. Changes persist to config.yaml and hot-swap the engine. */
function ModelSection() {
  const [form, setForm] = useState<ModelSettings | null>(null)
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; reply?: string; error?: string } | null>(null)

  useEffect(() => {
    api
      .getModelSettings()
      .then((s) => setForm(s))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  if (!form) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('settings.model')}</h2>
        <p class="page-sub dim">{error || t('common.loading')}</p>
      </div>
    )
  }

  const apiType = (form.api_type || 'anthropic') as ApiType
  const dirty = form.base_url.trim() !== '' || form.model.trim() !== ''

  function patch(p: Partial<ModelSettings>) {
    setForm((f) => (f ? { ...f, ...p } : f))
    setNotice('')
    setTestResult(null)
  }

  function payload(): ModelSettings {
    return {
      api_type: apiType,
      base_url: form!.base_url.trim(),
      model: form!.model.trim(),
      max_tokens: Number(form!.max_tokens) || 0,
      api_key: apiKey.trim() || undefined,
    }
  }

  async function test() {
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
    setSaving(true)
    setError('')
    try {
      const saved = await api.putModelSettings(payload())
      setForm(saved)
      setApiKey('')
      setNotice(t('settings.saved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form class="card settings-card" onSubmit={save}>
      <h2 class="block-title">{t('settings.model')}</h2>

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
              onClick={() => patch({ api_type: v })}
            >
              {v === 'anthropic' ? t('settings.anthropic') : t('settings.openai')}
            </button>
          ))}
        </div>
        <p class="hint">{t('settings.apiTypeHelp')}</p>
      </div>

      <div class="field-row">
        <div class="field-group">
          <label for="base-url">{t('settings.baseURL')}</label>
          <input
            id="base-url"
            class="input mono"
            type="url"
            required
            placeholder={EXAMPLES[apiType].base}
            value={form.base_url}
            onInput={(e) => patch({ base_url: (e.target as HTMLInputElement).value })}
          />
          <p class="hint">{t('settings.baseURLHelp')}</p>
        </div>
        <div class="field-group">
          <label for="model">{t('settings.model')}</label>
          <input
            id="model"
            class="input mono"
            type="text"
            required
            placeholder={EXAMPLES[apiType].model}
            value={form.model}
            onInput={(e) => patch({ model: (e.target as HTMLInputElement).value })}
          />
          <p class="hint">{t('settings.modelHelp')}</p>
        </div>
      </div>

      <div class="field-row">
        <div class="field-group">
          <label for="api-key">{t('settings.apiKey')}</label>
          <input
            id="api-key"
            class="input mono"
            type="password"
            autocomplete="off"
            placeholder={form.api_key_set ? `${t('settings.apiKeySet')} ${form.api_key_hint ?? ''}` : 'sk-…'}
            value={apiKey}
            onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
          />
          <p class="hint">{form.api_key_set ? t('settings.apiKeyKeep') : t('settings.apiKeyHelp')}</p>
        </div>
        <div class="field-group">
          <label for="max-tokens">{t('settings.maxTokens')}</label>
          <input
            id="max-tokens"
            class="input"
            type="number"
            min={0}
            step={256}
            value={form.max_tokens || 0}
            onInput={(e) => patch({ max_tokens: Number((e.target as HTMLInputElement).value) || 0 })}
          />
          <p class="hint">{t('settings.maxTokensHelp')}</p>
        </div>
      </div>

      {testResult && (
        <p class={`test-result ${testResult.ok ? 'ok' : 'bad'}`}>
          {testResult.ok
            ? t('settings.testOk', { reply: (testResult.reply || '').slice(0, 80) })
            : `${t('settings.testFail')} ${testResult.error ?? ''}`}
        </p>
      )}
      {error && <p class="gate-error">{error}</p>}
      {notice && <p class="test-result ok">{notice}</p>}

      <div class="settings-actions">
        <button type="button" class="btn" disabled={testing || !dirty} onClick={test}>
          {testing ? t('settings.testing') : t('settings.test')}
        </button>
        <button class="btn primary" type="submit" disabled={saving}>
          {saving ? t('common.save') + '…' : t('common.save')}
        </button>
      </div>
    </form>
  )
}

/** App policies (C1, GET/PUT /api/settings/app): how Panda injects itself,
 *  which agents routing prefers, the memory caps, and the approval gate.
 *  The sandbox row is GET-only — it describes the confinement every agent
 *  subprocess already runs under, it is not a switch. */
function PolicySection() {
  const [form, setForm] = useState<AppSettings | null>(null)
  const [agentsText, setAgentsText] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getAppSettings()
      .then((s) => {
        setForm(s)
        setAgentsText(s.preferred_agents.join(', '))
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  if (error && !form) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('settings.policy')}</h2>
        <p class="gate-error">{error}</p>
      </div>
    )
  }
  if (!form) return null

  function patch(p: Partial<AppSettings>) {
    setForm((f) => (f ? { ...f, ...p } : f))
    setNotice('')
  }

  async function save(e: Event) {
    e.preventDefault()
    if (saving || !form) return
    setSaving(true)
    setError('')
    try {
      const saved = await api.putAppSettings({
        ...form,
        preferred_agents: agentsText
          .split(/[,，\s]+/)
          .map((s) => s.trim())
          .filter((s) => s !== ''),
      })
      setForm(saved)
      setAgentsText(saved.preferred_agents.join(', '))
      setNotice(t('settings.saved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form class="card settings-card" onSubmit={save}>
      <h2 class="block-title">{t('settings.policy')}</h2>
      <p class="hint">{t('settings.policyHelp')}</p>

      <div class="field-group">
        <label>{t('settings.injection')}</label>
        <div class="segmented" role="radiogroup">
          {(['auto', 'always', 'never'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={form.injection_model === v}
              class={`seg${form.injection_model === v ? ' on' : ''}`}
              onClick={() => patch({ injection_model: v })}
            >
              {t(`settings.injection.${v}`)}
            </button>
          ))}
        </div>
        <p class="hint">{t('settings.injectionHelp')}</p>
      </div>

      <div class="field-group">
        <label for="preferred-agents">{t('settings.preferredAgents')}</label>
        <input
          id="preferred-agents"
          class="input mono"
          type="text"
          placeholder="codex, claude"
          value={agentsText}
          onInput={(e) => {
            setAgentsText((e.target as HTMLInputElement).value)
            setNotice('')
          }}
        />
        <p class="hint">{t('settings.preferredAgentsHelp')}</p>
      </div>

      <div class="field-group">
        <label>{t('settings.memoryLimits')}</label>
        <div class="field-row">
          {(['user', 'memory', 'project'] as const).map((k) => (
            <div class="field-group" key={k}>
              <label for={`limit-${k}`}>{t(`settings.limit.${k}`)}</label>
              <input
                id={`limit-${k}`}
                class="input"
                type="number"
                min={1}
                step={1}
                value={form.memory_limits[k] || 0}
                onInput={(e) =>
                  patch({
                    memory_limits: {
                      ...form.memory_limits,
                      [k]: Number((e.target as HTMLInputElement).value) || 0,
                    },
                  })
                }
              />
            </div>
          ))}
        </div>
        <p class="hint">{t('settings.memoryLimitsHelp')}</p>
      </div>

      <div class="field-group">
        <label>{t('settings.approval')}</label>
        <div class="segmented" role="radiogroup">
          {(['always', 'on-request', 'never'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={form.approval_mode === v}
              class={`seg${form.approval_mode === v ? ' on' : ''}`}
              onClick={() => patch({ approval_mode: v })}
            >
              {t(`settings.approval.${v}`)}
            </button>
          ))}
        </div>
        <p class="hint">{t('settings.approvalHelp')}</p>
      </div>

      {form.sandbox && (
        <div class="field-group">
          <label>{t('settings.sandbox')}</label>
          <p class="hint">
            {t('settings.sandboxDesc')}
            {form.sandbox.work_path && <span class="mono"> {form.sandbox.work_path}</span>}
          </p>
        </div>
      )}

      {error && <p class="gate-error">{error}</p>}
      {notice && <p class="test-result ok">{notice}</p>}

      <div class="settings-actions">
        <button class="btn primary" type="submit" disabled={saving}>
          {saving ? t('common.save') + '…' : t('common.save')}
        </button>
      </div>
    </form>
  )
}

/** Agent CLIs (queue redesign §6): which coding agents the node can drive —
 *  claude code / opencode / codex — with install path, best-effort version,
 *  and a live connectivity test. */
function AgentsSection() {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [error, setError] = useState('')
  const [testing, setTesting] = useState('')
  const [results, setResults] = useState<Record<string, AgentTestResult>>({})

  useEffect(() => {
    api
      .agents()
      .then(setAgents)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  async function test(name: string) {
    if (testing) return
    setTesting(name)
    try {
      const r = await api.testAgent(name)
      setResults((rs) => ({ ...rs, [name]: r }))
    } catch (e: unknown) {
      setResults((rs) => ({
        ...rs,
        [name]: { name, ok: false, error: e instanceof Error ? e.message : String(e) },
      }))
    } finally {
      setTesting('')
    }
  }

  if (error) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('settings.agents')}</h2>
        <p class="gate-error">{error}</p>
      </div>
    )
  }
  if (!agents) return null

  return (
    <div class="card settings-card agents-card">
      <h2 class="block-title">{t('settings.agents')}</h2>
      <p class="hint">{t('settings.agentsHelp')}</p>
      <ul class="agent-list">
        {agents.map((a) => {
          const r = results[a.name]
          return (
            <li key={a.name} class={`agent-row${a.installed ? '' : ' missing'}`}>
              <div class="agent-main">
                <span class="agent-name mono">{a.binary}</span>
                {a.display_name && <span class="agent-version dim">{a.display_name}</span>}
                <span class={`badge ${a.installed ? 'green' : 'red'}`}>
                  {a.installed ? t('settings.agentInstalled') : t('settings.agentMissing')}
                </span>
                {a.installed && a.version && <span class="agent-version dim">{a.version}</span>}
              </div>
              {a.installed && a.path && <div class="agent-path dim mono">{a.path}</div>}
              {!a.installed && (
                <div class="agent-install">
                  {a.install_hint && (
                    <div class="agent-path dim mono">
                      {t('settings.agentInstall')} {a.install_hint}
                    </div>
                  )}
                  {a.install_url && (
                    <a class="btn small" href={a.install_url} target="_blank" rel="noreferrer">
                      {t('settings.agentDownload')}
                    </a>
                  )}
                </div>
              )}
              {r && (
                <div class={`test-result ${r.ok ? 'ok' : 'bad'}`}>
                  {r.ok
                    ? t('settings.agentOk', { version: r.version ?? '' })
                    : `${t('settings.agentFail')} ${r.error ?? ''}`}
                </div>
              )}
              <button class="btn small" disabled={!a.installed || testing !== ''} onClick={() => test(a.name)}>
                {testing === a.name ? t('settings.agentTesting') : t('settings.agentTest')}
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** MCP servers (Model Context Protocol): one stdio server command. Saving
 *  validates it by actually spawning the server and listing its tools, then
 *  hot-swaps the engine — the agent can call the new tools immediately. */
function MCPSection() {
  const [cmd, setCmd] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    api
      .getMCPSettings()
      .then((s: MCPSettings) => {
        setCmd(s.command)
        setLoaded(true)
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  async function save(e: Event) {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError('')
    setNotice('')
    try {
      await api.putMCPSettings({ command: cmd.trim() })
      setNotice(t('settings.mcpSaved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  if (!loaded && !error) return null

  return (
    <form class="card settings-card mcp-card" onSubmit={save}>
      <h2 class="block-title">{t('settings.mcp')}</h2>
      <div class="field-group">
        <label for="mcp-command">{t('settings.mcpCommand')}</label>
        <input
          id="mcp-command"
          class="input mono"
          type="text"
          placeholder={t('settings.mcpPlaceholder')}
          value={cmd}
          onInput={(e) => {
            setCmd((e.target as HTMLInputElement).value)
            setNotice('')
          }}
        />
        <p class="hint">{t('settings.mcpHelp')}</p>
      </div>
      {error && <p class="gate-error">{error}</p>}
      {notice && <p class="test-result ok">{notice}</p>}
      <div class="settings-actions">
        <button class="btn primary" type="submit" disabled={saving}>
          {saving ? t('settings.mcpValidating') : t('common.save')}
        </button>
      </div>
    </form>
  )
}
