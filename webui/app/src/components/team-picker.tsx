// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import { type AgentInfo } from '../api/client'
import { agentLogo } from '../format/agent-logo'
import { agentInitial, agentRoles, agentTint } from '../format/agent-role'
import { t } from '../i18n'
import { loadAgentRoster } from '../utils/agent-roster'
import { Icon } from './icons'

/** One employee's face: the official mark on the plate its artwork is drawn
 *  for, or — for a CLI the console has no mark for, and for a mark that fails
 *  to load — the initial on the name's tint. The fallback is state per face,
 *  so one broken file cannot blank the rest of the roster. */
export function AgentFace(props: {
  name: string
  label: string
  mini?: boolean
  onDuty?: boolean
}) {
  const logo = agentLogo(props.name)
  const [broken, setBroken] = useState(false)
  const cls = `team-ava${props.mini ? ' mini' : ''} t${agentTint(props.name)}`
  const dot = props.onDuty ? <span class="team-duty-dot" /> : null
  if (logo && !broken) {
    return (
      <span class={`${cls} has-logo plate-${logo.plate}`} aria-hidden="true">
        <img src={`/agents/${logo.file}`} alt="" onError={() => setBroken(true)} />
        {dot}
      </span>
    )
  }
  return (
    <span class={cls} aria-hidden="true">
      {agentInitial(props.label)}
      {dot}
    </span>
  )
}

/** Who works on this conversation — the roster card itself.
 *
 *  It used to be a chip in the composer's footer with its own trigger. The
 *  trigger now lives on the panda (see composer-pet.tsx) and the card opens
 *  above it, so this component is only the card: it loads the roster, draws
 *  each CLI as an employee (a face, a name, the job they are good at), and
 *  reports picks upward. Open state, outside-click and Escape belong to the
 *  pet, which owns both the trigger and this card.
 *
 *  Empty is a real answer and the default — it means "let the scheduler pick
 *  the best hand". It is the first row of the card, so clearing a team is one
 *  click, not a hunt for a "clear" affordance. */
export function TeamRoster(props: {
  /** Selected agent names. Empty = no restriction. */
  value: string[]
  onChange(names: string[]): void
  onManage?(): void
  /** The card closes itself after 管理员工 — that is a navigation. */
  onClose(): void
}) {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [error, setError] = useState('')
  const picked = props.value

  useEffect(() => {
    let alive = true
    setError('')
    loadAgentRoster()
      .then((list) => {
        if (alive) setAgents(list)
      })
      .catch((err: unknown) => {
        if (!alive) return
        setAgents([])
        setError(err instanceof Error ? err.message : String(err))
      })
    return () => {
      alive = false
    }
  }, [])

  function toggle(name: string) {
    props.onChange(picked.includes(name) ? picked.filter((n) => n !== name) : [...picked, name])
  }

  return (
    <div class="team-panel" role="dialog" aria-label={t('sessions.teamTitle')}>
      <p class="team-panel-label">{t('sessions.teamLabel')}</p>
      <div
        class="team-list"
        role="listbox"
        aria-multiselectable="true"
        aria-busy={agents === null}
        aria-label={t('sessions.teamTitle')}
      >
        <button
          type="button"
          class={`team-row${picked.length === 0 ? ' on' : ''}`}
          role="option"
          aria-selected={picked.length === 0}
          onClick={() => props.onChange([])}
        >
          <span class="team-ava auto" aria-hidden="true">
            <Icon name="sparkles" size={14} />
          </span>
          <span class="team-text">
            <span class="team-name">{t('sessions.teamAuto')}</span>
            <span class="team-role">{t('sessions.teamAutoHint')}</span>
          </span>
          {picked.length === 0 && <Icon name="check" size={14} class="team-check" />}
        </button>
        {(agents ?? []).map((a) => {
          const on = picked.includes(a.name)
          const roles = agentRoles(a.best_at)
            .slice(0, 2)
            .map((r) => t(`sessions.teamRole.${r}`))
          // Only a CLI this machine can run is a hire. The row stays in the
          // list so the operator can see the console knows about it (and hover
          // the install hint), but it takes no pick — a member that cannot
          // start here would only fail a turn later. The one exception is a
          // pick left over from before an uninstall: it is already in the
          // team, so it must stay clickable to be taken back out.
          const pickable = a.installed || on
          return (
            <button
              key={a.name}
              type="button"
              class={`team-row${on ? ' on' : ''}${a.installed ? '' : ' off'}`}
              role="option"
              aria-selected={on}
              aria-disabled={!pickable}
              disabled={!pickable}
              aria-label={`${a.display_name || a.name}${
                a.installed ? ` — ${t('sessions.teamOnDuty')}` : ` — ${t('sessions.teamNotInstalled')}`
              }`}
              title={
                a.installed
                  ? a.version || a.path || a.binary
                  : a.install_hint || a.install_url || t('sessions.teamNotInstalledHint')
              }
              onClick={() => toggle(a.name)}
            >
              {/* The green pip is the roster's "on the floor" mark: this CLI is
                  installed on the node, so this person can take a task today.
                  An uninstalled row keeps the pip off, says so in words, and is
                  disabled — a pick that can never be staffed is a promise the
                  scheduler would have to break at dispatch time. */}
              <AgentFace name={a.name} label={a.display_name || a.name} onDuty={a.installed} />
              <span class="team-text">
                <span class="team-name">{a.display_name || a.name}</span>
                {/* An agent the registry knows but has no role tags for still
                    earns a second line — its binary is what the operator would
                    type to check on it. An uninstalled one gets the reason
                    instead, where the roles would have been. */}
                <span class="team-role">
                  {a.installed
                    ? roles.join(' · ') || a.binary
                    : t('sessions.teamNotInstalledHint')}
                </span>
              </span>
              {!a.installed && <span class="team-tag">{t('sessions.teamNotInstalled')}</span>}
              {on && <Icon name="check" size={14} class="team-check" />}
            </button>
          )
        })}
        {/* The probe runs `<cli> --version` for every registered agent, so the
            first open of the card can take a couple of seconds. Rows in the
            shape of the roster beat a one-line "loading": the card keeps its
            height and the reader sees people arriving rather than a blank
            list. */}
        {agents === null && (
          <div class="team-skel" aria-hidden="true">
            {[0, 1, 2].map((i) => (
              <div class="team-skel-row" key={i}>
                <span class="team-skel-ava" />
                <span class="team-skel-lines">
                  <span class="team-skel-line" style={{ width: i === 1 ? '44%' : '56%' }} />
                  <span class="team-skel-line short" />
                </span>
              </div>
            ))}
          </div>
        )}
        {agents !== null && agents.length === 0 && (
          <p class="team-empty">{error || t('sessions.teamEmpty')}</p>
        )}
      </div>
      <p class="team-hint">{t('sessions.teamHint')}</p>
      {props.onManage && (
        <div class="team-foot">
          <button
            type="button"
            class="wd-action"
            onClick={() => {
              props.onClose()
              props.onManage?.()
            }}
          >
            <Icon name="bot" size={15} />
            {t('sessions.teamManage')}
            <Icon name="chevron-right" size={14} class="node-foot-arrow" />
          </button>
        </div>
      )}
    </div>
  )
}
