// SPDX-License-Identifier: AGPL-3.0-or-later

/** A menu that opens where the pointer right-clicked.
 *
 *  The console already had dropdowns (`.thread-menu`, the model picker), but
 *  they all hang off a trigger the layout knows about. This one is placed by
 *  the event: it takes viewport coordinates, measures itself once, and flips
 *  inward when the pointer is near the right or bottom edge, so a card in the
 *  last column does not open a menu that runs off the window.
 *
 *  Items are data, not children: the call site describes what a card can do
 *  and the menu stays a menu. Adding an option later is one more object in
 *  that array — which is the whole reason the workspace's cards route their
 *  right-click through here instead of growing their own popovers.
 *
 *  It closes on the four things that make a floating menu stale: a press
 *  outside, a scroll (the surface under it moved), a resize, and Escape. */

import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { Icon, type IconName } from './icons'
import { layerAboveOwnsEscape } from './card-dismiss'

export interface ContextMenuItem {
  id: string
  label: string
  icon?: IconName
  /** Right-aligned aside: the command behind the action, a scope, a caveat. */
  hint?: string
  /** Destructive: the label turns red on hover and keeps its resting tone. */
  danger?: boolean
  disabled?: boolean
  onSelect(): void
}

export interface ContextMenuProps {
  x: number
  y: number
  /** What the items act on — one line at the top, so a menu opened over a
   *  dense row of marks still names its subject. */
  title?: string
  sub?: string
  items: ContextMenuItem[]
  onClose(): void
}

/** Distance from the window edge a menu keeps when it flips inward. */
const GUTTER = 8

export function ContextMenu({ x, y, title, sub, items, onClose }: ContextMenuProps) {
  const root = useRef<HTMLDivElement>(null)
  const rows = useRef<(HTMLButtonElement | null)[]>([])
  const [at, setAt] = useState<{ left: number; top: number } | null>(null)
  const [active, setActive] = useState(-1)
  // The parent's close handler is a fresh closure on every render; keeping it
  // in a ref lets the window listeners mount once instead of re-binding.
  const close = useRef(onClose)
  close.current = onClose

  // Measure, then place. The first paint is hidden rather than jumping: a
  // menu that lands at the pointer and then hops inward reads as a glitch.
  useLayoutEffect(() => {
    const el = root.current
    if (!el) return
    const r = el.getBoundingClientRect()
    // Open away from the edge it would run off: a menu near the bottom of the
    // window grows upward from the pointer, the way a context menu is
    // expected to, instead of being pushed down over the card it names.
    const below = y + r.height + GUTTER <= window.innerHeight
    const right = x + r.width + GUTTER <= window.innerWidth
    setAt({
      left: Math.max(
        GUTTER,
        Math.min(right ? x : x - r.width, window.innerWidth - r.width - GUTTER),
      ),
      top: Math.max(
        GUTTER,
        Math.min(below ? y : y - r.height, window.innerHeight - r.height - GUTTER),
      ),
    })
  }, [x, y])

  // Focus once the menu is actually visible. A `visibility: hidden` box cannot
  // take focus, and the first commit happens before the placement is measured
  // — focusing there left the keys on the card that opened the menu.
  useEffect(() => {
    if (at) root.current?.focus({ preventScroll: true })
  }, [at])

  useEffect(() => {
    const outside = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) close.current()
    }
    const stale = () => close.current()
    // Escape is caught at the document, not on the menu: the menu is the
    // topmost layer and must close whether or not focus landed on it.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape('.ctx-menu')) return
      e.stopPropagation()
      close.current()
    }
    window.addEventListener('pointerdown', outside, true)
    // Capture, because the scrolling element is the map, not the window.
    window.addEventListener('scroll', stale, true)
    window.addEventListener('resize', stale)
    document.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('pointerdown', outside, true)
      window.removeEventListener('scroll', stale, true)
      window.removeEventListener('resize', stale)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [])

  const enabled = items.map((it, i) => (it.disabled ? -1 : i)).filter((i) => i >= 0)

  const step = (dir: 1 | -1) => {
    if (enabled.length === 0) return
    const here = enabled.indexOf(active)
    const next =
      here === -1
        ? dir === 1
          ? enabled[0]!
          : enabled[enabled.length - 1]!
        : enabled[(here + dir + enabled.length) % enabled.length]!
    setActive(next)
    rows.current[next]?.focus({ preventScroll: true })
  }

  const run = (i: number) => {
    const it = items[i]
    if (!it || it.disabled) return
    close.current()
    it.onSelect()
  }

  return (
    <div
      ref={root}
      class="ctx-menu"
      role="menu"
      tabIndex={-1}
      aria-label={title}
      style={{
        left: `${at?.left ?? x}px`,
        top: `${at?.top ?? y}px`,
        visibility: at ? 'visible' : 'hidden',
      }}
      onKeyDown={(e) => {
        if (e.key === 'ArrowDown') {
          e.preventDefault()
          e.stopPropagation()
          step(1)
          return
        }
        if (e.key === 'ArrowUp') {
          e.preventDefault()
          e.stopPropagation()
          step(-1)
          return
        }
        if (e.key === 'Home' || e.key === 'End') {
          e.preventDefault()
          e.stopPropagation()
          const i = e.key === 'Home' ? enabled[0] : enabled[enabled.length - 1]
          if (i !== undefined) {
            setActive(i)
            rows.current[i]?.focus({ preventScroll: true })
          }
          return
        }
        // A menu is not part of the tab order: tabbing out of it closes it
        // rather than walking into whatever happens to be next on the page.
        if (e.key === 'Tab') {
          e.preventDefault()
          close.current()
        }
      }}
    >
      {(title || sub) && (
        <div class="ctx-menu-hd">
          {title && <span class="ctx-menu-title">{title}</span>}
          {sub && <span class="ctx-menu-sub">{sub}</span>}
        </div>
      )}
      {items.map((it, i) => (
        <button
          key={it.id}
          ref={(el) => {
            rows.current[i] = el
          }}
          type="button"
          role="menuitem"
          tabIndex={-1}
          class={`ctx-menu-item${it.danger ? ' danger' : ''}${active === i ? ' on' : ''}`}
          disabled={it.disabled}
          onPointerEnter={() => {
            if (!it.disabled) setActive(i)
          }}
          onClick={() => run(i)}
        >
          {it.icon ? <Icon name={it.icon} size={14} /> : <span class="ctx-menu-gap" />}
          <span class="ctx-menu-label">{it.label}</span>
          {it.hint && <span class="ctx-menu-hint">{it.hint}</span>}
        </button>
      ))}
    </div>
  )
}
