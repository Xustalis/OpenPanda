import { useEffect, useState } from 'preact/hooks'
import { api, type AgentInfo, type AgentTestResult, type MCPSettings, type ModelSettings } from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'

type ApiType = 'anthropic' | 'openai'

const EXAMPLES: Record<ApiType, { base: string; model: string }> = {
  anthropic: { base: 'https://api.anthropic.com', model: 'claude-sonnet-4-5' },
  openai: { base: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
}

/** The settings page: configure the LLM provider from the web console —
 * Anthropic or OpenAI-wire format, any base URL, any model. Changes persist
 * to config.yaml and hot-swap the engine (no restart needed). */
export function SettingsView() {
  useLocaleRerender()
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
      <section>
        <h1 class="page-title">{t('settings.title')}</h1>
        <p class="page-sub dim">{error || t('common.loading')}</p>
      </section>
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
    <section class="settings">
      <h1 class="page-title">{t('settings.title')}</h1>
      <p class="page-sub">{t('settings.subtitle')}</p>

      <form class="card settings-card" onSubmit={save}>
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

      <MCPSection />
      <AgentsSection />
    </section>
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
                <span class={`badge ${a.installed ? 'green' : 'red'}`}>
                  {a.installed ? t('settings.agentInstalled') : t('settings.agentMissing')}
                </span>
                {a.installed && a.version && <span class="agent-version dim">{a.version}</span>}
              </div>
              {a.installed && a.path && <div class="agent-path dim mono">{a.path}</div>}
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
