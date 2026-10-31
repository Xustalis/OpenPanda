// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  type AgentInfo,
  type AgentTestResult,
  type AppSettings,
  type MCPSettings,
} from '../api/client'
import { useLocaleRerender } from '../hooks'
import { Icon, type IconName } from '../components/icons'
import { toast } from '../components/toast'
import { agentLogo } from '../format/agent-logo'
import { agentInitial } from '../format/agent-role'
import { t } from '../i18n'
import { locale, localeNames, locales, setLocale } from '../i18n'
import { Select } from '../components/select'
import { TermsModal } from '../components/terms'
import { onThemeChange, setTheme, theme } from '../theme'
import { SkillsView } from './skills'
import { SystemView } from './system'
import { ModelsSection } from './models'
import { loadAgentRoster } from '../utils/agent-roster'

export type Section =
  | 'general'
  | 'models'
  | 'policy'
  | 'agents'
  | 'mcp'
  | 'skills'
  | 'system'

/** The rail, grouped the way a reader looks for things: the two preferences
 *  you change on day one, then everything the node can *do*, then the one
 *  section about the node itself. A flat list of eight rows was already at
 *  the edge of scannable; the headings cost two lines and buy the grouping.
 *
 *  记忆 is not here — it is a destination under 团队 in the rail (app.tsx),
 *  because reading what the node learned is not configuration. 技能 sits
 *  directly under MCP 服务器: both answer "what can this node call", one over
 *  stdio tools and one over prompt-level skills. */
const GROUPS: Array<{ label: string; items: Array<{ id: Section; label: string; icon: IconName }> }> = [
  {
    label: 'settings.group.prefs',
    items: [
      { id: 'general', label: 'settings.group.general', icon: 'gear' },
      { id: 'models', label: 'settings.model', icon: 'cpu' },
    ],
  },
  {
    label: 'settings.group.features',
    items: [
      { id: 'policy', label: 'settings.policy', icon: 'shield' },
      { id: 'agents', label: 'settings.group.agents', icon: 'bot' },
      { id: 'mcp', label: 'settings.mcp', icon: 'plug' },
      { id: 'skills', label: 'nav.skills', icon: 'sparkles' },
    ],
  },
  {
    label: 'settings.group.system',
    items: [{ id: 'system', label: 'nav.system', icon: 'activity' }],
  },
]

const SECTIONS = GROUPS.flatMap((g) => g.items)

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
    'skills',
    'system',
  ]
  return valid.includes(raw as Section) ? (raw as Section) : 'general'
}

/** The unified settings & management centre — a sheet over whatever you were
 *  reading, not a page you navigate away from. Memory, skills, system
 *  governance and model configurations are all tabs of it; the rail keeps only
 *  the destinations you work in.
 *
 *  It is a native `<dialog>` opened with `showModal()`, which is the one thing
 *  that puts a surface in the browser's *top layer*: above every z-index on
 *  the page, with the rest of the console inert underneath. Escape is the
 *  browser's default exit from a modal dialog and it is cancelled here on
 *  purpose — this sheet is dismissed by its ✕ and nothing else, so a stray
 *  keypress cannot throw away a half-edited model form. The same rule covers
 *  the backdrop: clicking it does nothing, because there is no handler.
 *
 *  提醒 is deliberately not here: it is a bell in the rail's head
 *  (components/reminders.tsx), because a clock you set several times a day does
 *  not belong behind a sheet you open monthly. */
export function SettingsView(props: {
  initialSection?: string
  onSelectTab?: (tab: Section) => void
  onClose(): void
  /** Drops the panel token and returns to the token gate. It lives in 通用 →
     连接 now: disconnecting is about the session this sheet configures, not a
     second verb for the rail's tail to carry. */
  onLogout(): void
}) {
  useLocaleRerender()
  const [section, setSection] = useState<Section>(() => normalizeSection(props.initialSection))
  const [query, setQuery] = useState('')
  // The endpoint form is opened by the active model's 编辑 button, not by a
  // disclosure at the bottom of the section: "fix my key" should not start
  // with scrolling past the registry to find a summary line.
  const sheet = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    if (props.initialSection) {
      setSection(normalizeSection(props.initialSection))
    }
  }, [props.initialSection])

  // Enter the top layer on mount, leave it on unmount. `cancel` is the
  // browser's Escape path for a modal dialog and is swallowed so the ✕ stays
  // the only way out; `close` on teardown keeps the dialog from being re-shown
  // with a stale scroll position if the component remounts.
  useEffect(() => {
    const el = sheet.current
    if (!el) return
    const holdEscape = (e: Event) => e.preventDefault()
    el.addEventListener('cancel', holdEscape)
    if (!el.open) el.showModal()
    return () => {
      el.removeEventListener('cancel', holdEscape)
      if (el.open) el.close()
    }
  }, [])

  const handleSelect = (s: Section) => {
    setSection(s)
    props.onSelectTab?.(s)
  }

  const needle = query.trim().toLowerCase()
  const match = (label: string) => needle === '' || t(label).toLowerCase().includes(needle)
  const groups = GROUPS.map((g) => ({ ...g, items: g.items.filter((s) => match(s.label)) })).filter(
    (g) => g.items.length > 0,
  )
  const title = SECTIONS.find((s) => s.id === section)?.label ?? 'settings.title'

  return (
    <dialog
      ref={sheet}
      class="settings-dialog"
      aria-label={t('settings.title')}
      onCancel={(e) => e.preventDefault()}
    >
      <div class="settings-shell">
        <aside class="settings-rail">
          <label class="settings-search">
            <Icon name="search" size={14} />
            <input
              type="text"
              value={query}
              placeholder={t('settings.search')}
              aria-label={t('settings.search')}
              onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
            />
          </label>

          <nav class="settings-nav" aria-label={t('settings.title')}>
            {groups.map((g) => (
              <div class="settings-nav-group" key={g.label}>
                <div class="settings-nav-group-title">{t(g.label)}</div>
                {g.items.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    class={`settings-nav-item${section === s.id ? ' active' : ''}`}
                    aria-current={section === s.id ? 'page' : undefined}
                    onClick={() => handleSelect(s.id)}
                  >
                    <Icon name={s.icon} class="settings-nav-icon" />
                    <span class="settings-nav-text">{t(s.label)}</span>
                  </button>
                ))}
              </div>
            ))}
            {groups.length === 0 && <p class="settings-nav-empty">{t('settings.noMatch')}</p>}
          </nav>
        </aside>

        <section class="settings-pane">
          <header class="settings-pane-head">
            <h2 class="settings-pane-title">{t(title)}</h2>
            <button
              type="button"
              class="settings-close"
              aria-label={t('common.close')}
              title={t('common.close')}
              onClick={props.onClose}
            >
              <Icon name="x" size={16} />
            </button>
          </header>

          <div class="settings-pane-body">
            {section === 'general' && <GeneralSection onLogout={props.onLogout} />}
            {section === 'models' && <ModelsSection />}
            {section === 'policy' && <PolicySection />}
            {section === 'agents' && <AgentsSection />}
            {section === 'mcp' && <MCPSection />}
            {section === 'skills' && <SkillsView />}
            {section === 'system' && <SystemView />}
          </div>
        </section>
      </div>
    </dialog>
  )
}

/** General: language + appearance theme (light / dark / follow system), the
 *  always-reachable terms & disclaimer viewer, and — as its own block — the
 *  panel session this browser holds.
 *
 *  断开连接 used to sit in the rail's tail beside 设置, which made a
 *  session-level verb look like a second destination. It is here now, one
 *  block below the preferences, because it acts on exactly what this sheet
 *  describes: the connection to this node. The hint spells out the blast
 *  radius — the token this browser saved is dropped, the node keeps running —
 *  so the button does not read as "stop my node". */
function GeneralSection(props: { onLogout(): void }) {
  const [, force] = useState(0)
  const [showTerms, setShowTerms] = useState(false)
  useEffect(() => onThemeChange(() => force((v) => v + 1)), [])

  return (
    <>
      <div class="card settings-card">
        <div class="field-group">
          <label>{t('settings.language')}</label>
          <Select
            ariaLabel={t('settings.language')}
            value={locale()}
            options={locales.map((l) => ({ value: l, label: localeNames[l] ?? l }))}
            onChange={(v) => setLocale(v as never)}
          />
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
      </div>

      <div class="card settings-card">
        <h2 class="block-title">{t('settings.session')}</h2>
        <div class="field-group">
          <label>{t('token.label')}</label>
          <div>
            <button type="button" class="btn" onClick={props.onLogout}>
              {t('token.logout')}
            </button>
          </div>
          <p class="hint">{t('settings.sessionHelp')}</p>
        </div>
      </div>

      {showTerms && <TermsModal onClose={() => setShowTerms(false)} />}
    </>
  )
}


/** App policies (C1, GET/PUT /api/settings/app): how Panda injects itself,
 *  which agents routing prefers, the memory caps, and the approval gate.
 *  The sandbox row is GET-only — it describes the confinement every agent
 *  subprocess already runs under, it is not a switch.
 *
 *  The page reads as four questions with answers — 模型注入 / 路由偏好 /
 *  记忆上限 / 安全. They used to be six equal rows in one column, with the
 *  read-only sandbox status sitting between two switches as if it were a
 *  third one; nothing said which rows belonged together. A group header per
 *  question costs one line each and buys that hierarchy.
 *
 *  The other half is the hints: each option explains itself once it is
 *  picked, instead of one paragraph describing all three at once. The action
 *  bar closes the form and carries the only two actions; 保存 stays dead
 *  until something actually differs from what the node holds, so "did that
 *  save?" has an answer that is not a toast you may have missed. */
function PolicySection() {
  const [form, setForm] = useState<AppSettings | null>(null)
  /** What the node holds, to tell "edited" from "saved" — and to undo. */
  const [saved, setSaved] = useState<AppSettings | null>(null)
  const [roster, setRoster] = useState<AgentInfo[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getAppSettings()
      .then((s) => {
        setForm(s)
        setSaved(s)
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
    // The chips suggest what is actually installed; a failed roster is not an
    // error state for this form, typing a name by hand still works.
    loadAgentRoster()
      .then((list) => setRoster(list.filter((a) => a.installed)))
      .catch(() => setRoster([]))
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
  const current = form

  const dirty = saved ? !samePolicy(current, saved) : false
  const badLimits =
    current.memory_limits.user <= 0 ||
    current.memory_limits.memory <= 0 ||
    current.memory_limits.project <= 0

  function patch(p: Partial<AppSettings>) {
    setForm((f) => (f ? { ...f, ...p } : f))
    setNotice('')
  }

  function setLimit(k: 'user' | 'memory' | 'project', raw: string) {
    const n = Math.max(0, Math.trunc(Number(raw) || 0))
    setForm((f) => (f ? { ...f, memory_limits: { ...f.memory_limits, [k]: n } } : f))
    setNotice('')
  }

  async function save(e: Event) {
    e.preventDefault()
    if (saving || badLimits) return
    setSaving(true)
    setError('')
    try {
      const next = await api.putAppSettings(current)
      setForm(next)
      setSaved(next)
      setNotice(t('settings.saved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form class="policy-form" onSubmit={save}>
      <p class="policy-lead">{t('settings.policyHelp')}</p>

      <PolicyGroup icon="cpu" title={t('policy.group.inject')} note={t('policy.group.injectNote')}>
        <PolicyRow
          label={t('settings.injection')}
          hint={t(`policy.hint.injection.${current.injection_model}`)}
        >
          <PolicySegmented
            label={t('settings.injection')}
            value={current.injection_model}
            options={(['auto', 'always', 'never'] as const).map((v) => ({
              value: v,
              label: t(`settings.injection.${v}`),
            }))}
            onPick={(v) => patch({ injection_model: v })}
          />
        </PolicyRow>

        <PolicyRow label={t('policy.tools')} hint={t(`policy.hint.tools.${current.tools_policy}`)}>
          <PolicySegmented
            label={t('policy.tools')}
            value={current.tools_policy}
            options={(['minimal', 'extended'] as const).map((v) => ({
              value: v,
              label: t(v === 'minimal' ? 'policy.toolsMinimal' : 'policy.toolsExtended'),
            }))}
            onPick={(v) => patch({ tools_policy: v })}
          />
        </PolicyRow>
      </PolicyGroup>

      <PolicyGroup icon="route" title={t('policy.group.route')} note={t('policy.group.routeNote')}>
        <PolicyRow
          label={t('settings.preferredAgents')}
          hint={t('settings.preferredAgentsHelp')}
          wide
        >
          <AgentChips
            value={current.preferred_agents}
            roster={roster}
            onChange={(next) => patch({ preferred_agents: next })}
          />
        </PolicyRow>
      </PolicyGroup>

      <PolicyGroup
        icon="orbit"
        title={t('settings.memoryLimits')}
        note={t('settings.memoryLimitsHelp')}
      >
        <div class="policy-limits">
          {(['user', 'memory', 'project'] as const).map((k) => (
            <label class={`policy-limit${current.memory_limits[k] > 0 ? '' : ' bad'}`} key={k}>
              <span class="policy-limit-name">{t(`settings.limit.${k}`)}</span>
              <span class="policy-num">
                <input
                  class="input"
                  type="number"
                  min={1}
                  step={1}
                  inputMode="numeric"
                  value={current.memory_limits[k] || ''}
                  aria-label={t(`settings.limit.${k}`)}
                  aria-invalid={current.memory_limits[k] <= 0}
                  onInput={(e) => setLimit(k, (e.target as HTMLInputElement).value)}
                />
                <span class="policy-num-suffix">{t('policy.chars')}</span>
              </span>
            </label>
          ))}
        </div>
      </PolicyGroup>

      <PolicyGroup
        icon="shield"
        title={t('policy.group.safety')}
        note={t('policy.group.safetyNote')}
      >
        <PolicyRow
          label={t('settings.approval')}
          hint={t(`policy.hint.approval.${current.approval_mode}`)}
        >
          <PolicySegmented
            label={t('settings.approval')}
            value={current.approval_mode}
            options={(['always', 'on-request', 'never'] as const).map((v) => ({
              value: v,
              label: t(`settings.approval.${v}`),
            }))}
            onPick={(v) => patch({ approval_mode: v })}
          />
        </PolicyRow>
        {current.sandbox && <SandboxCard sandbox={current.sandbox} />}
      </PolicyGroup>

      <div class="policy-actions">
        {error && <p class="gate-error">{error}</p>}
        {notice && <p class="test-result ok">{notice}</p>}
        <div class="policy-actions-bar">
          <span
            class={`policy-actions-state${badLimits ? ' bad' : dirty ? ' dirty' : ''}`}
            role="status"
          >
            <span class="policy-actions-dot" aria-hidden="true" />
            {badLimits
              ? t('policy.limitsInvalid')
              : dirty
                ? t('policy.unsaved')
                : t('policy.inSync')}
          </span>
          <button
            type="button"
            class="btn ghost"
            disabled={!dirty || saving}
            onClick={() => {
              if (saved) setForm(saved)
              setNotice('')
              setError('')
            }}
          >
            {t('policy.discard')}
          </button>
          <button class="btn primary" type="submit" disabled={saving || !dirty || badLimits}>
            {saving ? `${t('common.save')}…` : t('common.save')}
          </button>
        </div>
      </div>
    </form>
  )
}

/** Both settings objects describe the same five editable policy groups; the
 *  sandbox block is GET-only and must not make an untouched form look dirty. */
function samePolicy(a: AppSettings, b: AppSettings) {
  return (
    a.injection_model === b.injection_model &&
    a.tools_policy === b.tools_policy &&
    a.approval_mode === b.approval_mode &&
    a.preferred_agents.join('\u0000') === b.preferred_agents.join('\u0000') &&
    a.memory_limits.user === b.memory_limits.user &&
    a.memory_limits.memory === b.memory_limits.memory &&
    a.memory_limits.project === b.memory_limits.project
  )
}

/** One policy group: an icon, the question it answers, and the rows that
 *  answer it. The heading is what turns a column of switches into four
 *  decisions you can find without reading every label. */
function PolicyGroup(props: {
  icon: IconName
  title: string
  note: string
  children: ComponentChildren
}) {
  return (
    <section class="policy-group">
      <header class="policy-group-head">
        <span class="policy-group-icon" aria-hidden="true">
          <Icon name={props.icon} size={13} />
        </span>
        <h3 class="policy-group-title">{props.title}</h3>
        <span class="policy-group-note">{props.note}</span>
      </header>
      {props.children}
    </section>
  )
}

/** A row of the sheet: label and its live explanation on the left, the
 *  control on the right. `wide` is for controls that are themselves a row
 *  (chips, three number fields) — they take the full width under the label. */
function PolicyRow(props: {
  label: string
  hint?: string
  wide?: boolean
  children: ComponentChildren
}) {
  return (
    <div class={`policy-row${props.wide ? ' wide' : ''}`}>
      <div class="policy-row-label">
        <span class="policy-row-name">{props.label}</span>
        {props.hint && <p class="hint">{props.hint}</p>}
      </div>
      <div class="policy-row-control">{props.children}</div>
    </div>
  )
}

function PolicySegmented<T extends string>(props: {
  label: string
  value: T
  options: Array<{ value: T; label: string }>
  onPick(v: T): void
}) {
  return (
    <div class="segmented" role="radiogroup" aria-label={props.label}>
      {props.options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={props.value === o.value}
          class={`seg${props.value === o.value ? ' on' : ''}`}
          onClick={() => props.onPick(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** Routing preferences as pills. The value is a *set* of agent names, and a
 *  set you edit by retyping a comma-separated line is a text field pretending
 *  to be a list: one typo silently drops an agent. Enter adds, ✕ removes, and
 *  the installed roster is one click away so nobody has to remember a
 *  spelling. Order is not significant — the router gives every listed agent
 *  the same bonus. */
function AgentChips(props: {
  value: string[]
  roster: AgentInfo[]
  onChange(next: string[]): void
}) {
  const [draft, setDraft] = useState('')
  const taken = new Set(props.value.map((v) => v.toLowerCase()))
  const suggestions = props.roster
    .filter((a) => !taken.has(a.name.toLowerCase()))
    .slice(0, 6)

  function add(raw: string) {
    const name = raw.trim()
    if (!name) return
    if (props.value.length >= 16 || name === '.' || name === '..' || /[\\/]/.test(name)) {
      setDraft('')
      return
    }
    if (taken.has(name.toLowerCase())) {
      setDraft('')
      return
    }
    props.onChange([...props.value, name])
    setDraft('')
  }

  return (
    <div class="policy-chips-field">
      <div class="policy-chips">
        {props.value.map((name) => (
          <span class="policy-chip" key={name}>
            <span class="policy-chip-name">{name}</span>
            <button
              type="button"
              class="policy-chip-x"
              aria-label={`${t('common.clear')} ${name}`}
              title={`${t('common.clear')} ${name}`}
              onClick={() => props.onChange(props.value.filter((v) => v !== name))}
            >
              <Icon name="x" size={10} />
            </button>
          </span>
        ))}
        <input
          class="policy-chip-input"
          type="text"
          value={draft}
          placeholder={t('policy.agentsAdd')}
          aria-label={t('policy.agentsAdd')}
          disabled={props.value.length >= 16}
          onInput={(e) => setDraft((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter') return
            e.preventDefault()
            add(draft)
          }}
        />
      </div>

      {suggestions.length > 0 && (
        <div class="policy-suggest">
          <span>{t('policy.agentsSuggest')}</span>
          {suggestions.map((a) => (
            <button
              key={a.name}
              type="button"
              class="policy-suggest-chip"
              onClick={() => add(a.name)}
            >
              <Icon name="plus" size={10} />
              {a.display_name || a.name}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

/** The sandbox block is a status, not a setting: it reports the confinement
 *  the subprocesses actually run under. It used to sit in the same column as
 *  the switches, where it read as a third, oddly worded toggle — so it is a
 *  panel now, with the state as a pill and the path on its own line instead
 *  of a sentence that wraps around a file path. */
function SandboxCard(props: { sandbox: NonNullable<AppSettings['sandbox']> }) {
  const sb = props.sandbox
  const configured = !!sb.mode && sb.mode !== 'off'
  const state = sb.active ? 'on' : configured ? 'warn' : 'off'
  return (
    <div class="policy-sandbox" data-state={state}>
      <div class="policy-sandbox-head">
        <span class="policy-sandbox-dot" aria-hidden="true" />
        <span class="policy-sandbox-title">{t('settings.sandbox')}</span>
        <span class="policy-sandbox-state">
          {state === 'on'
            ? t('policy.sandboxOn')
            : state === 'warn'
              ? t('policy.sandboxNoBackend')
              : t('policy.sandboxOff')}
        </span>
        {configured && (
          <code class="policy-sandbox-mode">
            {sb.mode}
            {sb.backend ? ` · ${sb.backend}` : ''}
          </code>
        )}
      </div>
      <p class="hint">
        {state === 'on'
          ? t('settings.sandboxDescOn')
          : state === 'warn'
            ? t('settings.sandboxDescNoBackend')
            : t('settings.sandboxDesc')}
      </p>
      {sb.work_path && (
        <div class="policy-sandbox-path">
          <span>{t('policy.sandboxPath')}</span>
          <code title={sb.work_path}>{sb.work_path}</code>
        </div>
      )}
    </div>
  )
}

/** One agent's face on the settings list: the official mark on the plate its
 *  artwork is drawn for, and — for a CLI the bundle has no mark for, or a file
 *  that fails to load — the initial in the mono face. Same rule as the roster
 *  (components/team-picker.tsx) and the workspace tiles, so an agent looks the
 *  same wherever it appears. The plate travels with the artwork instead of
 *  following the theme: near-black marks only read on a light plate. */
function AgentFace(props: { name: string; label: string }) {
  const logo = agentLogo(props.name)
  const [broken, setBroken] = useState(false)
  if (logo && !broken) {
    return (
      <span class={`agent-face plate-${logo.plate}`} aria-hidden="true">
        <img
          src={`/agents/${logo.file}`}
          alt=""
          draggable={false}
          onError={() => setBroken(true)}
        />
      </span>
    )
  }
  return (
    <span class="agent-face fallback" aria-hidden="true">
      {agentInitial(props.label)}
    </span>
  )
}

/** The install command is the one string on this page that gets pasted into a
 *  shell, and it is longer than the row it sits in — so it is copied rather
 *  than selected out of an ellipsis. */
function InstallCopy(props: { text: string }) {
  const [done, setDone] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])

  async function copy() {
    window.clearTimeout(timer.current)
    try {
      await navigator.clipboard.writeText(props.text)
      setDone(true)
      timer.current = window.setTimeout(() => setDone(false), 1600)
    } catch (e: unknown) {
      const why = e instanceof Error ? e.message : String(e)
      toast(`${t('sessions.copyFailed')}: ${why}`, 'error')
    }
  }

  const label = done ? t('md.copied') : t('common.copy')
  return (
    <button
      type="button"
      class="agent-copy"
      onClick={copy}
      title={label}
      aria-label={label}
    >
      <Icon name={done ? 'check' : 'copy'} size={13} />
    </button>
  )
}

/** Agent CLIs (queue redesign §6): which coding agents the node can drive —
 *  claude code / opencode / codex — with install path, best-effort version,
 *  and a live connectivity test.
 *
 *  The row reads left to right as face · identity · state, because the face is
 *  what a reader recognises before they parse `dsh` or `agy`. The state column
 *  used to be a bordered well with its own button, which made every row three
 *  nested frames deep; it is now a pill and one action, and a failed probe's
 *  stderr drops to a full-width line underneath instead of being squeezed into
 *  a 236px column. */
function AgentsSection() {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [error, setError] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [testing, setTesting] = useState('')
  const [results, setResults] = useState<Record<string, AgentTestResult>>({})

  useEffect(() => {
    let alive = true
    loadAgentRoster()
      .then((list) => {
        if (!alive) return
        setAgents(list)
        setError('')
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      alive = false
    }
  }, [])

  async function refresh() {
    if (refreshing) return
    setRefreshing(true)
    try {
      const list = await loadAgentRoster(true)
      setAgents(list)
      setError('')
    } catch (e: unknown) {
      const message = e instanceof Error ? e.message : String(e)
      if (agents) toast(`${t('common.refresh')}: ${message}`, 'error')
      else setError(message)
    } finally {
      setRefreshing(false)
    }
  }

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

  if (error && !agents) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('settings.agents')}</h2>
        <p class="gate-error">{error}</p>
        <button type="button" class="btn small" disabled={refreshing} onClick={refresh}>
          <Icon name="refresh" />
          {t('common.retry')}
        </button>
      </div>
    )
  }
  if (!agents) {
    return (
      <div class="card settings-card agents-card" aria-busy="true">
        <div class="agents-head">
          <div class="agents-head-text">
            <h2 class="block-title">{t('settings.agents')}</h2>
            <p class="hint">{t('settings.agentsHelp')}</p>
          </div>
        </div>
        <ul class="agent-list" aria-label={t('common.loading')}>
          {[0, 1, 2].map((i) => (
            <li class="agent-row" key={i} aria-hidden="true">
              <span class="skel-avatar" />
              <span class="skel-lines">
                <span class="skel-line" style={{ width: i === 1 ? '42%' : '56%' }} />
                <span class="skel-line" style={{ width: '72%' }} />
              </span>
            </li>
          ))}
        </ul>
      </div>
    )
  }

  const installed = agents.filter((a) => a.installed).length

  return (
    <div class="card settings-card agents-card">
      <div class="agents-head">
        <div class="agents-head-text">
          <h2 class="block-title">{t('settings.agents')}</h2>
          <p class="hint">{t('settings.agentsHelp')}</p>
        </div>
        {/* Ten rows of "installed" is a column you have to read; the count is
            the same fact at a glance. */}
        <div class="agents-actions">
          <span class="agents-count">
            {t('settings.agentCount', { installed, total: agents.length })}
          </span>
          <button
            type="button"
            class="icon-btn"
            title={t('common.refresh')}
            aria-label={t('common.refresh')}
            aria-busy={refreshing}
            disabled={refreshing}
            onClick={refresh}
          >
            {refreshing ? <span class="spinner" /> : <Icon name="refresh" />}
          </button>
        </div>
      </div>
      <ul class="agent-list">
        {agents.map((a) => {
          const r = results[a.name]
          const state = !a.installed
            ? 'missing'
            : testing === a.name
              ? 'testing'
              : !r
                ? 'idle'
                : r.ok
                  ? 'ok'
                  : 'bad'
          const label =
            state === 'missing'
              ? t('settings.agentMissing')
              : state === 'testing'
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
            <li key={a.name} class={`agent-row ${state}`}>
              <AgentFace name={a.name} label={a.display_name || a.binary} />
              <div class="agent-id">
                <div class="agent-main">
                  <span class="agent-name">{a.display_name || a.binary}</span>
                  {/* The name is how you say it, the binary is how you type
                      it: the chip keeps both without a second line. */}
                  <span class="agent-bin mono">{a.binary}</span>
                  {a.installed && a.version && <span class="agent-version">{a.version}</span>}
                </div>
                {a.installed && a.path && (
                  <div class="agent-path mono" title={a.path}>
                    {a.path}
                  </div>
                )}
                {!a.installed && a.install_hint && (
                  <div class="agent-install">
                    <span class="agent-path mono" title={a.install_hint}>
                      {t('settings.agentInstall')} {a.install_hint}
                    </span>
                    <InstallCopy text={a.install_hint} />
                  </div>
                )}
              </div>
              <div class="agent-state">
                <span class={`agent-pill ${state}`}>
                  <span class="agent-pill-dot" aria-hidden="true" />
                  {label}
                </span>
                {a.installed ? (
                  <button
                    class="btn small agent-test"
                    disabled={testing !== ''}
                    onClick={() => test(a.name)}
                  >
                    <Icon name="activity" />
                    {t('settings.agentTest')}
                  </button>
                ) : (
                  a.install_url && (
                    <a
                      class="btn small agent-test"
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
              {detail && (
                <p class="agent-detail" title={detail}>
                  {detail}
                </p>
              )}
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
