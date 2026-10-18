// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import type { NodeInfo } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'

/** Where this turn runs, as a button that opens a card.
 *
 *  It used to be a `<select>` with the choice spelled out beside it. The
 *  problem was not the control but the sentence: "自动（最佳节点）" is a
 *  15-character label sitting in the header for a decision you make rarely,
 *  and a native select cannot show what the choice *means* — which machine is
 *  local, which is offline, which one the scheduler would pick. So the header
 *  keeps a single glyph (the network mark) and the card behind it carries the
 *  list, the same shape Codex and WorkBuddy use for their header popovers:
 *  ghost icon button, rounded card underneath, rows with a leading icon and a
 *  tick on the current one.
 *
 *  Opening it also reserves a lane on the right of the transcript (the pane
 *  reads `nodeOpen` from the parent), so the card sits *beside* the reading
 *  column instead of on top of it. */
export function NodePicker(props: {
  nodes: NodeInfo[]
  /** Empty string = let the scheduler route by capability. */
  value: string
  onPick(v: string): void
  onManage(): void
  /** Fired when the card opens or closes, so the pane can make room. */
  onOpenChange?(open: boolean): void
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const picked = props.nodes.find((n) => n.id === props.value)
  const current = picked ? picked.name || picked.id : t('sessions.nodeAuto')

  useEffect(() => {
    props.onOpenChange?.(open)
  }, [open])

  // Outside click and Escape close the card — the same two ways every other
  // popover in the console closes, and Escape stops here so it cannot also
  // reach the thread drawer behind it.
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

  const pick = (id: string) => {
    props.onPick(id)
    setOpen(false)
  }

  return (
    <div class="node-picker" ref={box}>
      <button
        type="button"
        class={`icon-btn tip-down node-pick-btn${open ? ' open' : ''}${props.value ? ' pinned' : ''}`}
        /* The tip names the current choice, since the header no longer spells
           it out. It goes away while the card is open — the card is the same
           answer, said properly, and a tip on top of it is noise. */
        data-tip={open ? undefined : current}
        aria-label={t('sessions.nodePick')}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="network" size={19} />
      </button>
      {open && (
        <div class="node-panel" role="dialog" aria-label={t('sessions.nodePick')}>
          <p class="node-panel-label">{t('sessions.nodePanelTitle')}</p>
          <div class="node-panel-list">
            <button
              type="button"
              class={`node-row${props.value === '' ? ' on' : ''}`}
              aria-pressed={props.value === ''}
              onClick={() => pick('')}
            >
              <Icon name="sparkles" size={16} class="node-row-ico" />
              <span class="node-row-name">{t('sessions.nodeAuto')}</span>
              {props.value === '' && <Icon name="check" size={14} class="node-row-check" />}
            </button>
            {props.nodes.map((n) => {
              const offline = n.status !== 'online'
              return (
                <button
                  key={n.id}
                  type="button"
                  class={`node-row${props.value === n.id ? ' on' : ''}${offline ? ' off' : ''}`}
                  aria-pressed={props.value === n.id}
                  disabled={offline}
                  onClick={() => pick(n.id)}
                >
                  <Icon name="cpu" size={16} class="node-row-ico" />
                  <span class="node-row-name">{n.name || n.id}</span>
                  {n.is_local && <span class="node-row-tag">{t('nodes.self')}</span>}
                  {offline && <span class="node-row-tag">{t('sessions.nodeOffline')}</span>}
                  {props.value === n.id && <Icon name="check" size={14} class="node-row-check" />}
                </button>
              )
            })}
            {props.nodes.length === 0 && <p class="node-panel-empty">{t('nodes.empty')}</p>}
          </div>
          <p class="node-panel-hint">{t('sessions.nodePickTip')}</p>
          <div class="node-panel-foot">
            <button
              type="button"
              class="node-foot-btn"
              onClick={() => {
                setOpen(false)
                props.onManage()
              }}
            >
              <Icon name="network" size={15} />
              {t('sessions.nodeManage')}
              <Icon name="chevron-right" size={14} class="node-foot-arrow" />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
