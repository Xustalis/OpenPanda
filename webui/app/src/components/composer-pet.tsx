// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { ComposerPanda } from './composer-panda'
import { AgentFace, TeamRoster } from './team-picker'

/** The pet plus the roster it answers for: a single press opens (or closes)
 *  the team card, a quick double press is the squish, and the hired faces hang
 *  along the pet's right side.
 *
 *  The roster used to be a chip in the composer's footer, then a small trigger
 *  button on the pet's shoulder. The button is gone: it made the pet look like
 *  it came with an accessory, and the pet is already the thing that stands for
 *  the console's staff. The two gestures are split inside the pet itself
 *  (composer-panda.tsx): one press for the card, a double for the toy. The
 *  picks are worn on its flank as faces rather than a count.
 *
 *  Open state and the dismissal rules live here: a click on the card must not
 *  count as "outside", Escape closes the card before anything behind it
 *  reacts, and a single press toggles it rather than reopening it. */
export function ComposerPet(props: {
  /** A turn is streaming: the pet cheers instead of bobbing, and a press opens
   *  the card at once instead of waiting out the double-press window. The
   *  roster stays reachable — see the press model in composer-panda.tsx. */
  working: boolean
  /** The thread's roster, as the session carries it. */
  team: string[]
  onTeam(names: string[]): void
  onManage?(): void
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  // A send starts a turn, and the card belongs to the composition that just
  // ended: it closes itself rather than hanging over the streaming answer.
  // Reopening it mid-turn is one press away — a turn does not lock the roster.
  useEffect(() => {
    if (props.working) setOpen(false)
  }, [props.working])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const picked = props.team
  // Three faces, then a count: the row sits beside a 44px sprite and a fourth
  // head would start crowding the input's first glyph.
  const shown = picked.slice(0, 3)

  return (
    <div class="composer-pet" ref={box}>
      <ComposerPanda
        working={props.working}
        expanded={open}
        onPress={() => setOpen((v) => !v)}
        label={`${t('sessions.teamTitle')} · ${t('panda.poke')}`}
      />
      {shown.length > 0 && (
        <span class="pet-roster-faces" aria-hidden="true">
          {shown.map((name) => (
            <AgentFace key={name} name={name} label={name} mini />
          ))}
          {picked.length > shown.length && (
            <span class="team-ava mini more">+{picked.length - shown.length}</span>
          )}
        </span>
      )}
      {open && (
        <TeamRoster
          value={picked}
          onChange={props.onTeam}
          onManage={props.onManage}
          onClose={() => setOpen(false)}
        />
      )}
    </div>
  )
}
