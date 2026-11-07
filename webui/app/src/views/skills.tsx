// SPDX-License-Identifier: AGPL-3.0-or-later

import { useRef, useState } from 'preact/hooks'
import { api, type HubSkillEntry, type SkillEntry } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { Icon } from '../components/icons'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { useModalFocus } from '../components/modal-focus'
import { Select } from '../components/select'
import { ItemSettings } from '../components/item-settings'

/** DOM ids for the per-skill settings disclosures. Skill names come from disk
 *  and can contain path separators, so they are folded into a safe suffix. */
function skillSettingsId(name: string): string {
  return `skill-settings-${name.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

/** The skills view: every skill with its approval status, plus
 *  quick recommended presets, custom importing and community Skills Hub integration. */
export function SkillsView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const [tab, setTab] = useState<'installed' | 'hub'>('installed')
  const [showImport, setShowImport] = useState(false)

  const { data: skills, error } = useAsync(() => api.skills(), [], change + tick)
  const [busy, setBusy] = useState('')

  async function act(name: string, approve: boolean) {
    if (busy) return
    if (!approve) {
      const ok = await confirmDialog({
        title: t('skills.rejectConfirmTitle'),
        message: t('skills.rejectConfirmMsg', { name }),
        confirmLabel: t('skills.reject'),
      })
      if (!ok) return
    }
    setBusy(name)
    try {
      if (approve) {
        await api.approveSkill(name)
        toast(t('skills.approvedToast', { name }), 'success')
      } else {
        await api.rejectSkill(name)
        toast(t('skills.rejectedToast', { name }), 'info')
      }
      setTick((v) => v + 1)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  async function reset(name: string) {
    if (busy) return
    const ok = await confirmDialog({
      title: t('skills.resetConfirmTitle'),
      message: t('skills.resetConfirmMsg', { name }),
      confirmLabel: t('skills.resetBtn'),
    })
    if (!ok) return
    setBusy(name)
    try {
      await api.resetSkill(name)
      toast(t('skills.resetSuccess', { name }), 'success')
      setTick((v) => v + 1)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  if (error)
    return (
      <ErrorState
        title={t('skills.title')}
        sub={t('skills.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  const pending = (skills ?? []).filter((s) => s.status === 'pending')
  const rest = (skills ?? []).filter((s) => s.status !== 'pending')

  return (
    <section>
      <PageHeader title={t('skills.title')} sub={t('skills.subtitle')} />

      <div class="skills-toolbar">
        <div class="skills-tabs">
          <button
            type="button"
            class={`btn ${tab === 'installed' ? 'primary' : ''}`}
            onClick={() => setTab('installed')}
          >
            {t('skills.tabInstalled')}
          </button>
          <button
            type="button"
            class={`btn ${tab === 'hub' ? 'primary' : ''}`}
            onClick={() => setTab('hub')}
          >
            {t('skills.tabHub')}
          </button>
        </div>

        {tab === 'installed' && (
          <button type="button" class="btn" onClick={() => setShowImport(true)}>
            + {t('skills.importBtn')}
          </button>
        )}
      </div>

      {tab === 'installed' ? (
        <>
          {pending.length > 0 && (
            <div class="card skills-pending">
              <h2 class="block-title">
                {t('skills.pending')} ({pending.length})
              </h2>
              {pending.map((s) => (
                <SkillRow key={s.name} skill={s} busy={busy === s.name} onAct={act} onReset={reset} />
              ))}
            </div>
          )}

          <div class="card">
            <h2 class="block-title">{t('skills.all')}</h2>
            {rest.length === 0 && pending.length === 0 && <p class="dim">{t('skills.empty')}</p>}
            {rest.length === 0 && pending.length > 0 && <p class="dim">{t('skills.noOthers')}</p>}
            {rest.map((s) => (
              <SkillRow key={s.name} skill={s} busy={busy === s.name} onAct={act} onReset={reset} />
            ))}
          </div>
        </>
      ) : (
        <SkillsHubPanel onInstalled={() => setTick((v) => v + 1)} />
      )}

      {showImport && (
        <ImportSkillModal
          onClose={() => setShowImport(false)}
          onSuccess={() => {
            setShowImport(false)
            setTick((v) => v + 1)
          }}
        />
      )}
    </section>
  )
}

function SkillRow({
  skill,
  busy,
  onAct,
  onReset,
}: {
  skill: SkillEntry
  busy: boolean
  onAct(name: string, approve: boolean): void
  onReset(name: string): void
}) {
  return (
    <div class="skill-row">
      <div class="skill-info">
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <span class="skill-name mono">{skill.name}</span>
          {skill.builtin && (
            <span class="badge blue" style={{ fontSize: '11px' }}>
              {t('skills.builtinTag')}
            </span>
          )}
        </div>
        <span class="skill-desc dim">{skill.description}</span>
        <span class="skill-meta dim">
          {skill.scope}
          {skill.key ? `:${skill.key}` : ''} · {t('skills.usedN', { n: skill.use_count })}
        </span>
      </div>
      <div class="skill-side">
        <span
          class={`badge ${skill.status === 'active' ? 'green' : skill.status === 'pending' ? 'yellow' : 'red'}`}
        >
          {t(`skills.status.${skill.status}`, skill.status)}
        </span>
        {/* The row's own settings entry: status stays visible, every verb that
            acts on this skill lives in the disclosure inside the row. */}
        <ItemSettings
          id={skillSettingsId(skill.name)}
          label={t('settings.itemSettingsFor', { name: skill.name })}
          title={skill.name}
          align="end"
        >
          <p class="item-settings-note">
            {t(`skills.status.${skill.status}`, skill.status)} · {skill.scope}
            {skill.key ? `:${skill.key}` : ''} · {t('skills.usedN', { n: skill.use_count })}
          </p>
          <div class="item-settings-actions">
            {skill.builtin && (
              <button
                type="button"
                class="btn small"
                disabled={busy}
                title={t('skills.resetTooltip')}
                onClick={() => onReset(skill.name)}
              >
                <Icon name="refresh" size={13} />
                {t('skills.resetBtn')}
              </button>
            )}
            {skill.status === 'pending' && (
              <>
                <button class="btn small primary" disabled={busy} onClick={() => onAct(skill.name, true)}>
                  {t('skills.approve')}
                </button>
                <button class="btn small danger" disabled={busy} onClick={() => onAct(skill.name, false)}>
                  {t('skills.reject')}
                </button>
              </>
            )}
          </div>
        </ItemSettings>
      </div>
    </div>
  )
}

function SkillsHubPanel({ onInstalled }: { onInstalled(): void }) {
  const [query, setQuery] = useState('')
  const [tick, setTick] = useState(0)
  const { data: hubSkills, error } = useAsync(() => api.hubSkills(query), [query, tick])
  const [installing, setInstalling] = useState('')
  const [discovering, setDiscovering] = useState(false)

  async function install(name: string, force = false) {
    if (installing) return
    setInstalling(name)
    try {
      await api.installHubSkill(name, undefined, force)
      toast(t('skills.installSuccess', { name }), 'success')
      setTick((v) => v + 1)
      onInstalled()
    } catch (e) {
      toastError(e)
    } finally {
      setInstalling('')
    }
  }

  async function discover() {
    const q = query.trim()
    if (!q || discovering) return
    setDiscovering(true)
    try {
      const res = await api.discoverSkill(q)
      if (res.is_new) {
        toast(t('skills.discoverSuccess', { name: res.name }), 'success')
      } else {
        toast(t('skills.discoverAlreadyActive', { name: res.name }), 'info')
      }
      setTick((v) => v + 1)
      onInstalled()
    } catch (e) {
      toastError(e)
    } finally {
      setDiscovering(false)
    }
  }

  return (
    <div class="card">
      <div style={{ display: 'flex', gap: '8px', marginBottom: '16px' }}>
        <input
          type="text"
          class="skills-search-bar"
          style={{ flex: 1, margin: 0 }}
          placeholder={t('skills.hubSearchPlaceholder')}
          value={query}
          onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && query.trim()) {
              e.preventDefault()
              discover()
            }
          }}
        />
        {query.trim() && (
          <button
            type="button"
            class="btn primary"
            disabled={discovering}
            onClick={discover}
            title={t('skills.discoverTooltip')}
            style={{ whiteSpace: 'nowrap' }}
          >
            {discovering ? '...' : <><Icon name="sparkles" size={13} /> {t('skills.discoverBtn')}</>}
          </button>
        )}
      </div>

      {error ? (
        <p class="dim">{error}</p>
      ) : !hubSkills || hubSkills.length === 0 ? (
        <div style={{ padding: '16px 0', textAlign: 'center' }}>
          <p class="dim">{t('skills.hubEmpty')}</p>
          {query.trim() && (
            <button
              type="button"
              class="btn primary"
              style={{ marginTop: '8px' }}
              disabled={discovering}
              onClick={discover}
            >
              {discovering ? '...' : <><Icon name="sparkles" size={13} /> {t('skills.discoverBtn')}: "{query.trim()}"</>}
            </button>
          )}
        </div>
      ) : (
        hubSkills.map((s) => (
          <HubSkillRow
            key={s.name}
            skill={s}
            installing={installing === s.name}
            onInstall={(force) => install(s.name, force)}
          />
        ))
      )}
    </div>
  )
}

function HubSkillRow({
  skill,
  installing,
  onInstall,
}: {
  skill: HubSkillEntry
  installing: boolean
  onInstall(force?: boolean): void
}) {
  const meta = [skill.author && t('skills.byAuthor', { name: skill.author }), skill.version]
    .filter(Boolean)
    .join(' · ')
  return (
    <div class="skill-row">
      <div class="skill-info">
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <span class="skill-name mono">{skill.name}</span>
          {skill.alias && <span class="badge dim" style={{ fontSize: '11px' }}>{skill.alias}</span>}
          {skill.version && <span class="badge dim">{skill.version}</span>}
          {skill.author && <span class="dim" style={{ fontSize: '12px' }}>{t('skills.byAuthor', { name: skill.author })}</span>}
          {skill.recommended && (
            <span class="badge blue" style={{ fontSize: '11px' }}>
              {t('skills.builtinTag')}
            </span>
          )}
        </div>
        <span class="skill-desc dim">{skill.description}</span>
        {skill.tags && skill.tags.length > 0 && (
          <div class="skill-tags">
            {skill.tags.map((tag) => (
              <span key={tag} class="skill-tag">
                #{tag}
              </span>
            ))}
          </div>
        )}
      </div>
      <div class="skill-side">
        {skill.installed && <span class="badge green">{t('skills.installed')}</span>}
        <ItemSettings
          id={`hub-${skillSettingsId(skill.name)}`}
          label={t('settings.itemSettingsFor', { name: skill.name })}
          title={skill.name}
          align="end"
        >
          {meta && <p class="item-settings-note">{meta}</p>}
          <div class="item-settings-actions">
            {skill.installed ? (
              <button
                type="button"
                class="btn small"
                disabled={installing}
                title={t('skills.resetTooltip')}
                onClick={() => onInstall(true)}
              >
                <Icon name="refresh" size={13} />
                {t('skills.reinstall')}
              </button>
            ) : (
              <button
                type="button"
                class="btn small primary"
                disabled={installing}
                onClick={() => onInstall(false)}
              >
                {installing ? '...' : t('skills.install')}
              </button>
            )}
          </div>
        </ItemSettings>
      </div>
    </div>
  )
}

function ImportSkillModal({
  onClose,
  onSuccess,
}: {
  onClose(): void
  onSuccess(): void
}) {
  const [mode, setMode] = useState<'source' | 'content'>('source')
  const [source, setSource] = useState('')
  const [content, setContent] = useState('')
  const [scope, setScope] = useState('global')
  const [project, setProject] = useState('')
  const [device, setDevice] = useState('')
  const [name, setName] = useState('')
  const [pending, setPending] = useState(false)
  const [force, setForce] = useState(false)
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  async function submit(e: Event) {
    e.preventDefault()
    if (mode === 'source' && !source.trim()) return
    if (mode === 'content' && !content.trim()) return

    setBusy(true)
    try {
      await api.importSkill({
        source: mode === 'source' ? source.trim() : undefined,
        content: mode === 'content' ? content.trim() : undefined,
        scope,
        project: scope === 'project' ? project.trim() : undefined,
        device: scope === 'device' ? device.trim() : undefined,
        name: name.trim() || undefined,
        pending,
        force,
      })
      toast(t('skills.importSuccess'), 'success')
      onSuccess()
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="skill-modal-backdrop" onClick={onClose}>
      <div
        ref={box}
        class="skill-modal"
        role="dialog"
        aria-modal="true"
        aria-label={t('skills.importTitle')}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
      >
        <h3>{t('skills.importTitle')}</h3>
        <p class="dim" style={{ fontSize: '13px', margin: 0 }}>
          {t('skills.importSub')}
        </p>

        <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button
              type="button"
              class={`btn ${mode === 'source' ? 'primary' : ''}`}
              onClick={() => setMode('source')}
            >
              {t('skills.importSource')}
            </button>
            <button
              type="button"
              class={`btn ${mode === 'content' ? 'primary' : ''}`}
              onClick={() => setMode('content')}
            >
              {t('skills.importContent')}
            </button>
          </div>

          {mode === 'source' ? (
            <div class="skill-form-group">
              <label>{t('skills.importSource')}</label>
              <input
                type="text"
                placeholder="https://github.com/.../SKILL.md or /path/to/skill"
                value={source}
                onInput={(e) => setSource((e.target as HTMLInputElement).value)}
                required
              />
            </div>
          ) : (
            <div class="skill-form-group">
              <label>{t('skills.importContent')}</label>
              <textarea
                rows={6}
                placeholder="---\nname: my-skill\ndescription: ...\n---\n\n## Steps\n..."
                value={content}
                onInput={(e) => setContent((e.target as HTMLTextAreaElement).value)}
                required
              />
            </div>
          )}

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '10px' }}>
            <div class="skill-form-group">
              <label>{t('skills.importScope')}</label>
              <Select
                ariaLabel={t('skills.importScope')}
                value={scope}
                options={[
                  { value: 'global', label: t('skills.scopeGlobal') },
                  { value: 'project', label: t('skills.scopeProject') },
                  { value: 'device', label: t('skills.scopeDevice') },
                ]}
                onChange={setScope}
              />
            </div>
            <div class="skill-form-group">
              <label>{t('skills.importName')}</label>
              <input
                type="text"
                placeholder={t('skills.optional')}
                value={name}
                onInput={(e) => setName((e.target as HTMLInputElement).value)}
              />
            </div>
          </div>

          {scope === 'project' && (
            <div class="skill-form-group">
              <label>{t('skills.importProject')}</label>
              <input
                type="text"
                value={project}
                onInput={(e) => setProject((e.target as HTMLInputElement).value)}
                required
              />
            </div>
          )}

          {scope === 'device' && (
            <div class="skill-form-group">
              <label>{t('skills.importDevice')}</label>
              <input
                type="text"
                value={device}
                onInput={(e) => setDevice((e.target as HTMLInputElement).value)}
                required
              />
            </div>
          )}

          <div style={{ display: 'flex', gap: '16px', fontSize: '13px' }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={pending}
                onChange={(e) => setPending((e.target as HTMLInputElement).checked)}
              />
              {t('skills.importPending')}
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={force}
                onChange={(e) => setForce((e.target as HTMLInputElement).checked)}
              />
              {t('skills.importForce')}
            </label>
          </div>

          <div class="skill-modal-actions">
            <button type="button" class="btn" onClick={onClose} disabled={busy}>
              {t('common.cancel')}
            </button>
            <button type="submit" class="btn primary" disabled={busy}>
              {busy ? '...' : t('skills.importSubmit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
