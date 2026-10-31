// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

/** The five permission policies a conversation can run under.
 *
 *  They answer the whole consent question: what the thread may do, turn after
 *  turn. bypass consents to everything, manual and plan to nothing, auto and
 *  edits ask on the irreversible tier. There used to be a second control —
 *  a per-turn 授权 switch beside this picker — but it restated the mode
 *  (bypass already means "yes to all of it"), so the picker is now the only
 *  place consent is given. */
export type PermissionMode = 'auto' | 'manual' | 'edits' | 'plan' | 'bypass'

export const PERMISSION_MODES: PermissionMode[] = ['auto', 'manual', 'edits', 'plan', 'bypass']

/** What an unset mode reads as. The server normalizes the same way. */
export function normalizePermission(raw: string | undefined | null): PermissionMode {
  switch (raw) {
    case 'manual':
    case 'edits':
    case 'plan':
    case 'bypass':
      return raw
    default:
      return 'auto'
  }
}

/** The mode chip and its menu.
 *
 *  The trigger lives in the composer's action row: permission is a property of
 *  sending, and a control that changes what Enter means belongs next to the box
 *  it sends from. The menu
 *  opens upward — the composer sits at the bottom of the pane — and every row
 *  carries its own one-line consequence, because "接受编辑" without "shell 命令
 *  仍会询问" is a promise the gate does not keep. */
export function PermissionPicker(props: {
  value: PermissionMode
  onChange(mode: PermissionMode): void
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  /** Where the menu has to sit to stay on screen. The card is 320px wide and
   *  the chip it hangs from is a fraction of that, so neither a fixed `right`
   *  nor a fixed `left` keeps it inside a narrow window: anchored right it runs
   *  off the left edge, anchored left it runs off the right once the action row
   *  wraps. The composer docks to the bottom of the pane, so there is no room
   *  below and the height ceiling is whatever space is left above the chip. */
  const [anchor, setAnchor] = useState<{ left: number; width: number; maxHeight: number } | null>(
    null,
  )

  useLayoutEffect(() => {
    if (!open) {
      setAnchor(null)
      return
    }
    const place = () => {
      const el = root.current
      if (!el) return
      const r = el.getBoundingClientRect()
      // The menu is painted inside the pane's scroll box, not the window, so
      // every edge it is clamped to is that box's, not the viewport's: on a
      // phone the app bar (a docked sidebar) owns the first 51px of the screen,
      // and on a desktop the sidebar owns the left 304px behind which the card
      // would be clipped. Intersect every clipping ancestor that is in the way.
      let top = 0
      let left = 0
      let right = window.innerWidth
      for (let n = el.parentElement; n; n = n.parentElement) {
        const s = getComputedStyle(n)
        if (s.overflowX !== 'visible' || s.overflowY !== 'visible') {
          const b = n.getBoundingClientRect()
          top = Math.max(top, b.top)
          left = Math.max(left, b.left)
          right = Math.min(right, b.right)
        }
      }
      const width = Math.min(320, right - left - 16)
      const x = Math.max(left + 8, Math.min(r.right - width, right - width - 8))
      // The card's bottom edge sits 8px above the chip, and the ceiling leaves
      // 8px more between its top and the box's top edge.
      setAnchor({ left: x - r.left, width, maxHeight: Math.max(80, r.top - top - 16) })
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const mode = props.value
  return (
    <div class="perm-pick" ref={root}>
      <button
        type="button"
        class={`perm-trigger perm-${mode}${open ? ' open' : ''}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t('perm.title')}
        title={t(`perm.${mode}Hint`)}
        disabled={props.disabled}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name={mode === 'bypass' ? 'alert' : 'shield'} size={13} />
        <span>{t(`perm.${mode}`)}</span>
        <Icon name="chevron-down" size={12} />
      </button>
      {open && (
        <div
          class="perm-menu"
          role="listbox"
          aria-label={t('perm.title')}
          style={
            anchor
              ? {
                  left: `${anchor.left}px`,
                  width: `${anchor.width}px`,
                  maxHeight: `${anchor.maxHeight}px`,
                }
              : undefined
          }
        >
          <p class="perm-menu-head">{t('perm.title')}</p>
          {PERMISSION_MODES.map((m) => (
            <button
              key={m}
              type="button"
              role="option"
              aria-selected={m === mode}
              class={`perm-row${m === mode ? ' on' : ''}${m === 'bypass' ? ' danger' : ''}`}
              onClick={() => {
                setOpen(false)
                if (m !== mode) props.onChange(m)
              }}
            >
              <span class="perm-row-text">
                <span class="perm-row-name">{t(`perm.${m}`)}</span>
                <span class="perm-row-hint">{t(`perm.${m}Hint`)}</span>
              </span>
              {m === mode && <Icon name="check" size={14} class="perm-check" />}
            </button>
          ))}
          <p class="perm-foot">{t('perm.foot')}</p>
        </div>
      )}
    </div>
  )
}
