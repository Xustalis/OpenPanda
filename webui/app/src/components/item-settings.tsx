// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './icons'
import { t } from '../i18n'

/** A per-item settings disclosure.
 *
 *  The button is the only settings entry an item exposes, and the panel that
 *  opens from it is a DOM child of that item — not a sheet, not a route, not a
 *  sibling form living somewhere else on the page. Keeping the panel inside
 *  the item is what makes "which agent does this field belong to" answerable
 *  without a caption: open the row, and the fields are that row's.
 *
 *  Layout note: the wrapper is deliberately *not* positioned. The menu is
 *  absolutely positioned against the item root (`.agent-row`, `.skill-row`,
 *  `.model-tile` are all `position: relative`), so it can span the row's full
 *  width even though the trigger sits in a narrow state column. DOM
 *  containment still holds for outside-click detection because the panel is
 *  a descendant of this wrapper.
 *
 *  Escape closes the menu but never the settings dialog: the dialog swallows
 *  its own `cancel` event, and this listener only touches local state. A menu
 *  opened near the foot of the pane flips above its item instead of being
 *  clipped. */
export function ItemSettings(props: {
  /** Stable id shared by `aria-controls` and the panel. */
  id: string
  /** Accessible label for the trigger, e.g. "Claude Code settings". */
  label: string
  /** Short caption in the panel head; defaults to the generic word. */
  title?: string
  /** Where the panel anchors inside the item. `stretch` spans the row. */
  align?: 'stretch' | 'end'
  class?: string
  children: ComponentChildren
}) {
  const [open, setOpen] = useState(false)
  const [flip, setFlip] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const menu = useRef<HTMLDivElement>(null)

  // A disclosure is a child of the scrollport, so both edges can clip it.
  // Measure the room above and below the trigger, open toward the roomier
  // side, and cap the height to that room: on a short or narrow sheet the
  // fields scroll inside the panel instead of the panel vanishing under the
  // edge of the dialog. The nearest scroll ancestor is the settings pane (or
  // the viewport), so the same arithmetic works on a page and in a dialog.
  useLayoutEffect(() => {
    if (!open) return
    const el = menu.current
    const trigger = root.current
    if (!el || !trigger) return
    let ceiling = 0
    let floor = window.innerHeight
    for (let p = trigger.parentElement; p; p = p.parentElement) {
      const overflow = getComputedStyle(p).overflowY
      if (overflow === 'auto' || overflow === 'scroll' || overflow === 'hidden') {
        const r = p.getBoundingClientRect()
        ceiling = r.top
        floor = r.bottom
        break
      }
    }
    const anchor = trigger.getBoundingClientRect()
    // 24px is the menu's margin plus a guard for the scrollport's own border
    // and the row's hover padding: measuring to the exact edge leaves the
    // panel a few pixels over it and clips the panel header.
    const below = floor - anchor.bottom - 24
    const above = anchor.top - ceiling - 24
    const flip = above > below
    el.style.maxHeight = `${Math.min(440, Math.max(96, flip ? above : below))}px`
    setFlip(flip)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      setOpen(false)
    }
    const onPointer = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('pointerdown', onPointer)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('pointerdown', onPointer)
    }
  }, [open])

  return (
    <div class={`item-settings${props.class ? ` ${props.class}` : ''}`} ref={root}>
      <button
        type="button"
        class={`item-settings-btn${open ? ' on' : ''}`}
        aria-label={props.label}
        title={props.label}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={props.id}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="gear" size={14} />
      </button>
      {open && (
        <div
          id={props.id}
          ref={menu}
          class={`item-settings-menu${props.align === 'end' ? ' align-end' : ''}${flip ? ' flip-up' : ''}`}
          role="group"
          aria-label={props.title || props.label}
        >
          <div class="item-settings-head">
            <span class="item-settings-kicker">{t('settings.itemSettings')}</span>
            {props.title && <strong class="item-settings-title">{props.title}</strong>}
            <button
              type="button"
              class="item-settings-close"
              aria-label={t('common.close')}
              title={t('common.close')}
              onClick={() => setOpen(false)}
            >
              <Icon name="x" size={12} />
            </button>
          </div>
          <div class="item-settings-body">{props.children}</div>
        </div>
      )}
    </div>
  )
}
