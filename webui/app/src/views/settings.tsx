// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import {
  api,
  type AgentInfo,
  type AgentTestResult,
  type AppSettings,
  type MCPSettings,
} from '../api/client'
import { useLocaleRerender } from '../hooks'
import { Icon, type IconName } from '../components/icons'
import { t } from '../i18n'
import { locale, localeNames, locales, setLocale } from '../i18n'
import { TermsModal } from '../components/terms'
import { onThemeChange, setTheme, theme } from '../theme'
import { MemoryView } from './memory'
import { SkillsView } from './skills'
import { SystemView } from './system'
import { ModelsSection } from './models'

export type Section =
  | 'general'
  | 'models'
  | 'policy'
  | 'agents'
  | 'mcp'
  | 'memory'
  | 'skills'
  | 'system'

const SECTIONS: Array<{ id: Section; label: string; icon: IconName }> = [
  { id: 'general', label: 'settings.group.general', icon: 'gear' },
  { id: 'models', label: 'settings.model', icon: 'cpu' },
  { id: 'policy', label: 'settings.policy', icon: 'shield' },
  { id: 'agents', label: 'settings.group.agents', icon: 'bot' },
  { id: 'mcp', label: 'settings.mcp', icon: 'plug' },
  { id: 'memory', label: 'nav.memory', icon: 'orbit' },
  { id: 'skills', label: 'nav.skills', icon: 'sparkles' },
  { id: 'system', label: 'nav.system', icon: 'activity' },
]

function normalizeSection(raw?: string): Section {
  if (!raw) return 'general'
  if (raw === 'config' || raw === 'model') return 'models'
  // Promoted views keep working when an old settings deep-link arrives.
  // (nodes/devices moved to the fleet rail; memory/skills used to be
  // redirected here and are now real tabs of this page.)
  if (raw === 'nodes' || raw === 'devices') {
    return 'general'
  }
  // 提醒 moved out of Settings: it is the bell in the rail's head now, so an
  // old `?tab=reminders` link falls back to the page's first tab instead of
  // rendering a section that no longer exists.
  if (raw === 'reminders') return 'general'
  const valid: Section[] = [
    'general',
    'models',
    'policy',
    'agents',
    'mcp',
    'memory',
    'skills',
    'system',
  ]
  return valid.includes(raw as Section) ? (raw as Section) : 'general'
}

/** The unified settings & management centre: memory, skills, system
 *  governance, and model configurations all live here as controlled tabs. The
 *  rail keeps only the destinations you work in — the boards and the thread —
 *  while everything about how the node and its agents are configured or what
 *  they remember sits behind this one page. 提醒 is deliberately not here: it
 *  is a bell in the rail's head (components/reminders.tsx), because a clock you
 *  set several times a day does not belong behind a page you open monthly. */
export function SettingsView(props: {
  initialSection?: string
  onSelectTab?: (tab: Section) => void
}) {
  useLocaleRerender()
  const [section, setSection] = useState<Section>(() => normalizeSection(props.initialSection))

  useEffect(() => {
    if (props.initialSection) {
      setSection(normalizeSection(props.initialSection))
    }
  }, [props.initialSection])

  const handleSelect = (s: Section) => {
    setSection(s)
    props.onSelectTab?.(s)
  }

  return (
    <section class="settings-page">
      <div class="settings-head">
        <h1 class="page-title">{t('settings.title')}</h1>
        <p class="page-sub">{t('settings.subtitle')}</p>
      </div>

      <div class="settings-layout">
        <nav class="settings-nav" aria-label={t('settings.title')}>
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              type="button"
              class={`settings-nav-item${section === s.id ? ' active' : ''}`}
              onClick={() => handleSelect(s.id)}
            >
              <Icon name={s.icon} class="settings-nav-icon" />
              <span class="settings-nav-text">{t(s.label)}</span>
            </button>
          ))}
        </nav>

        <div class="settings-body">
          {section === 'general' && <GeneralSection />}
          {section === 'models' && <ModelsSection />}
          {section === 'policy' && <PolicySection />}
          {section === 'agents' && <AgentsSection />}
          {section === 'mcp' && <MCPSection />}
          {section === 'memory' && <MemoryView />}
          {section === 'skills' && <SkillsView />}
          {section === 'system' && <SystemView />}
        </div>
      </div>
    </section>
  )
}

/** General: language + appearance theme (light / dark / follow system) and
 *  the always-reachable terms & disclaimer viewer. */
function GeneralSection() {
  const [, force] = useState(0)
  const [showTerms, setShowTerms] = useState(false)
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

      <div class="field-group">
        <label>{t('settings.terms')}</label>
        <div>
          <button type="button" class="btn" onClick={() => setShowTerms(true)}>
            {t('settings.termsView')}
          </button>
        </div>
        <p class="hint">{t('settings.termsHelp')}</p>
      </div>

      {showTerms && <TermsModal onClose={() => setShowTerms(false)} />}
    </div>
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
        <label>{t('policy.tools')}</label>
        <div class="segmented" role="radiogroup">
          {(['minimal', 'extended'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={form.tools_policy === v}
              class={`seg${form.tools_policy === v ? ' on' : ''}`}
              onClick={() => patch({ tools_policy: v })}
            >
              {t(v === 'minimal' ? 'policy.toolsMinimal' : 'policy.toolsExtended')}
            </button>
          ))}
        </div>
        <p class="hint">{t('policy.toolsHelp')}</p>
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
            {form.sandbox.active
              ? t('settings.sandboxDescOn')
              : form.sandbox.mode && form.sandbox.mode !== 'off'
                ? t('settings.sandboxDescNoBackend')
                : t('settings.sandboxDesc')}
            {form.sandbox.mode && form.sandbox.mode !== 'off' && (
              <span class="mono">
                {' '}{form.sandbox.mode}{form.sandbox.backend ? ` (${form.sandbox.backend})` : ''} ·
              </span>
            )}
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
          // The probe column reads as one of four states; the row itself stays
          // identity-only so the eye scans names down the left edge.
          const state = testing === a.name ? 'testing' : !r ? 'idle' : r.ok ? 'ok' : 'bad'
          const label =
            state === 'testing'
              ? t('settings.agentTesting')
              : state === 'ok'
                ? t('settings.agentStatusOk')
                : state === 'bad'
                  ? t('settings.agentStatusFail')
                  : t('settings.agentUntested')
          // The probe reports the version the test actually saw — only worth a
          // line when it disagrees with the one the row already lists.
          const detail =
            state === 'ok'
              ? r?.version && r.version !== a.version
                ? r.version
                : ''
              : state === 'bad'
                ? (r?.error ?? '')
                : ''
          return (
            <li key={a.name} class={`agent-row${a.installed ? '' : ' missing'}`}>
              <div class="agent-body">
                <div class="agent-main">
                  <span class="agent-name mono">{a.binary}</span>
                  {a.display_name && <span class="agent-version dim">{a.display_name}</span>}
                  <span class={`badge ${a.installed ? 'green' : 'red'}`}>
                    {a.installed ? t('settings.agentInstalled') : t('settings.agentMissing')}
                  </span>
                  {a.installed && a.version && <span class="agent-version dim">{a.version}</span>}
                </div>
                {a.installed && a.path && <div class="agent-path dim mono">{a.path}</div>}
                {!a.installed && a.install_hint && (
                  <div class="agent-install">
                    <div class="agent-path dim mono">
                      {t('settings.agentInstall')} {a.install_hint}
                    </div>
                  </div>
                )}
              </div>
              <div class={`agent-probe ${a.installed ? state : 'missing'}`}>
                {a.installed && (
                  <div class="agent-probe-head">
                    <span class="agent-probe-dot" />
                    <span class="agent-probe-label">{label}</span>
                  </div>
                )}
                {a.installed && detail && (
                  <p class="agent-probe-detail" title={detail}>
                    {detail}
                  </p>
                )}
                {a.installed ? (
                  <button
                    class="btn small agent-probe-btn"
                    disabled={testing !== ''}
                    onClick={() => test(a.name)}
                  >
                    <Icon name="activity" />
                    {t('settings.agentTest')}
                  </button>
                ) : (
                  a.install_url && (
                    <a
                      class="btn small agent-probe-btn"
                      href={a.install_url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      <Icon name="download" />
                      {t('settings.agentDownload')}
                    </a>
                  )
                )}
              </div>
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
