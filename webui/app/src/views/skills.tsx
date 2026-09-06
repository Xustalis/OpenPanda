import { useState } from 'preact/hooks'
import { api, type HubSkillEntry, type SkillEntry } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

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
          {skill.key ? `:${skill.key}` : ''} · used {skill.use_count}
        </span>
      </div>
      <div class="skill-side">
        <span
          class={`badge ${skill.status === 'active' ? 'green' : skill.status === 'pending' ? 'yellow' : 'red'}`}
        >
          {skill.status}
        </span>
        {skill.builtin && (
          <button
            type="button"
            class="btn"
            style={{ fontSize: '12px', padding: '3px 8px' }}
            disabled={busy}
            title={t('skills.resetTooltip')}
            onClick={() => onReset(skill.name)}
          >
            {t('skills.resetBtn')}
          </button>
        )}
        {skill.status === 'pending' && (
          <span class="skill-actions">
            <button class="btn primary" disabled={busy} onClick={() => onAct(skill.name, true)}>
              {t('skills.approve')}
            </button>
            <button class="btn danger" disabled={busy} onClick={() => onAct(skill.name, false)}>
              {t('skills.reject')}
            </button>
          </span>
        )}
      </div>
    </div>
  )
}

function SkillsHubPanel({ onInstalled }: { onInstalled(): void }) {
  const [query, setQuery] = useState('')
  const [tick, setTick] = useState(0)
  const { data: hubSkills, error } = useAsync(() => api.hubSkills(query), [query, tick])
  const [installing, setInstalling] = useState('')

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

  return (
    <div class="card">
      <input
        type="text"
        class="skills-search-bar"
        placeholder={t('skills.hubSearchPlaceholder')}
        value={query}
        onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
      />

      {error ? (
        <p class="dim">{error}</p>
      ) : !hubSkills || hubSkills.length === 0 ? (
        <p class="dim">{t('skills.hubEmpty')}</p>
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
  return (
    <div class="skill-row">
      <div class="skill-info">
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <span class="skill-name mono">{skill.name}</span>
          {skill.alias && <span class="badge dim" style={{ fontSize: '11px' }}>{skill.alias}</span>}
          {skill.version && <span class="badge dim">{skill.version}</span>}
          {skill.author && <span class="dim" style={{ fontSize: '12px' }}>by {skill.author}</span>}
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
        {skill.installed ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <span class="badge green">{t('skills.installed')}</span>
            <button
              type="button"
              class="btn"
              disabled={installing}
              title={t('skills.resetTooltip')}
              onClick={() => onInstall(true)}
              style={{ fontSize: '12px', padding: '4px 8px' }}
            >
              ↻
            </button>
          </div>
        ) : (
          <button
            type="button"
            class="btn primary"
            disabled={installing}
            onClick={() => onInstall(false)}
          >
            {installing ? '...' : t('skills.install')}
          </button>
        )}
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
      <div class="skill-modal" onClick={(e) => e.stopPropagation()}>
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
              <select value={scope} onChange={(e) => setScope((e.target as HTMLSelectElement).value)}>
                <option value="global">Global</option>
                <option value="project">Project</option>
                <option value="device">Device</option>
              </select>
            </div>
            <div class="skill-form-group">
              <label>Name (Override)</label>
              <input
                type="text"
                placeholder="optional"
                value={name}
                onInput={(e) => setName((e.target as HTMLInputElement).value)}
              />
            </div>
          </div>

          {scope === 'project' && (
            <div class="skill-form-group">
              <label>Project Name</label>
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
              <label>Device Name</label>
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
              Pending approval
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={force}
                onChange={(e) => setForce((e.target as HTMLInputElement).checked)}
              />
              Overwrite existing
            </label>
          </div>

          <div class="skill-modal-actions">
            <button type="button" class="btn" onClick={onClose} disabled={busy}>
              Cancel
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
