// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import type { NodeInfo } from '../api/client'
import { isNodeConnected } from '../format/node-status'
import { t } from '../i18n'
import { layerAboveOwnsEscape } from './card-dismiss'
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
  /** Fired when the card opens or closes, so the pane can make room. */
  onOpenChange?(open: boolean): void
  /** Controlled open state. The pane owns it because the node card and the
   *  orchestration card hang off the same corner and must not both be up;
   *  omitted, the card manages its own like it always did. */
  open?: boolean
}) {
  const [selfOpen, setSelfOpen] = useState(false)
  const open = props.open ?? selfOpen
  const setOpen = (v: boolean) => {
    if (props.open === undefined) setSelfOpen(v)
    props.onOpenChange?.(v)
  }
  const picked = props.nodes.find((n) => n.id === props.value)
  const current = picked ? picked.name || picked.id : t('sessions.nodeAuto')

  // Escape closes the card, and it stops here so it cannot also reach the
  // thread drawer behind it. That and the trigger are the only two ways out:
  // no outside click — a click in the transcript is you reading the answer
  // behind the card, not asking for the card to go away (see `card-dismiss`).
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape()) return
      e.stopPropagation()
      setOpen(false)
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const pick = (id: string) => {
    props.onPick(id)
    setOpen(false)
  }

  return (
    <div class="node-picker">
      <button
        type="button"
        class={`icon-btn tip-down node-pick-btn${open ? ' open' : ''}${props.value ? ' pinned' : ''}`}
        /* The tip is the control's name, not the current choice: the choice is
           already spelled out by the card behind it, and a header tooltip that
           changes with the selection reads as a status line rather than a
           label. The selection is still announced to assistive tech below. */
        data-tip={open ? undefined : t('sessions.nodeBtn')}
        aria-label={`${t('sessions.nodePick')}: ${current}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <Icon name="git-fork" size={19} />
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
              const offline = !isNodeConnected(n.status)
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
        </div>
      )}
    </div>
  )
}
