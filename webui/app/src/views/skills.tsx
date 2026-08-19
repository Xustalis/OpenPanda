import { useState } from 'preact/hooks'
import { api, type SkillEntry } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

/** The skills view: every skill with its approval status — the web
 *  equivalent of `panda skill list`. Pending entries await the human
 *  sign-off that activates them (generated skills never run unapproved). */
export function SkillsView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: skills, error } = useAsync(() => api.skills(), [], change + tick)
  const [busy, setBusy] = useState('')

  async function act(name: string, approve: boolean) {
    if (busy) return
    // Rejecting a skill is irreversible and buries the generated content —
    // confirm before it happens.
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
