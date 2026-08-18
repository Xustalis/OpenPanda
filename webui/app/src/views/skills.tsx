import { useState } from 'preact/hooks'
import { api, type SkillEntry } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'

/** The skills view: every skill with its approval status — the web
 *  equivalent of `panda skill list`. Pending entries await the human
 *  sign-off that activates them (generated skills never run unapproved). */
export function SkillsView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const { data: skills, error } = useAsync(() => api.skills(), [], change)
  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState('')

  async function act(name: string, approve: boolean) {
    if (busy) return
    setBusy(name)
    setActionError('')
    try {
      if (approve) await api.approveSkill(name)
      else await api.rejectSkill(name)
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  if (error) return <p class="dim">{t('common.error')} ({error})</p>

  const pending = (skills ?? []).filter((s) => s.status === 'pending')
  const rest = (skills ?? []).filter((s) => s.status !== 'pending')

  return (
    <section>
      <h1 class="page-title">{t('skills.title')}</h1>
      <p class="page-sub">{t('skills.subtitle')}</p>

      {pending.length > 0 && (
        <div class="card skills-pending">
          <h2 class="block-title">
            {t('skills.pending')} ({pending.length})
          </h2>
          {pending.map((s) => (
            <SkillRow key={s.name} skill={s} busy={busy === s.name} onAct={act} />
          ))}
        </div>
      )}

      <div class="card">
        <h2 class="block-title">{t('skills.all')}</h2>
        {rest.length === 0 && pending.length === 0 && <p class="dim">{t('skills.empty')}</p>}
        {rest.length === 0 && pending.length > 0 && <p class="dim">{t('skills.noOthers')}</p>}
        {rest.map((s) => (
          <SkillRow key={s.name} skill={s} busy={busy === s.name} onAct={act} />
        ))}
      </div>
      {actionError && <p class="gate-error">{actionError}</p>}
    </section>
  )
}

function SkillRow({
  skill,
  busy,
  onAct,
}: {
  skill: SkillEntry
  busy: boolean
  onAct(name: string, approve: boolean): void
}) {
  return (
    <div class="skill-row">
      <div class="skill-info">
        <span class="skill-name mono">{skill.name}</span>
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
