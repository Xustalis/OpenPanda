// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useId, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { layerAboveOwnsEscape } from './card-dismiss'
import { Icon } from './icons'

/** One choice in a {@link Select}. */
export type SelectOption = {
  value: string
  label: string
  /** A right-aligned aside — a count, a device kind. The list is a table of
   *  choices, and the number is often the reason one is picked over another,
   *  so it rides beside the label instead of inside it. */
  hint?: string
  disabled?: boolean
}

/** A field and its list, both ours.
 *
 *  A native `<select>` is the right control on paper and the wrong one on
 *  screen: the field can be themed, but the list behind it is drawn by the OS
 *  — a square white panel with a system-blue band on the current row. Chrome's
 *  `appearance: base-select` does not rescue it either; on Windows the popup is
 *  still handed to the platform (checked against Chrome 156, headful, not just
 *  headless). So the list is markup: the same card, radius, hover wash and
 *  shadow as `.ctx-menu`, and the current choice carries a tick instead of a
 *  colour band.
 *
 *  Owning the list means owning the behaviour, which is why this is the only
 *  select implementation in the console: click toggles, Arrow/Home/End move,
 *  Enter picks, Escape closes back onto the trigger, and an outside press or
 *  any scroll dismisses — the list is fixed-position, so a scroll would
 *  otherwise leave it floating away from its field. */
export function Select(props: {
  value: string
  options: SelectOption[]
  onChange(value: string): void
  /** The control's name for assistive tech — the field shows the value, not
   *  what the list is for. */
  ariaLabel: string
  class?: string
  disabled?: boolean
  id?: string
}) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const [box, setBox] = useState<{ left: number; top: number; width: number; maxWidth: number } | null>(
    null,
  )
  const rootRef = useRef<HTMLDivElement>(null)
  const btnRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const uid = useId()

  const current = props.options.find((o) => o.value === props.value)

  const close = (refocus = false) => {
    setOpen(false)
    if (refocus) btnRef.current?.focus()
  }

  const openList = () => {
    const at = props.options.findIndex((o) => o.value === props.value && !o.disabled)
    setActive(at >= 0 ? at : Math.max(0, props.options.findIndex((o) => !o.disabled)))
    // Land under the field on the first frame; the layout pass below only
    // corrects the flip when the list would run off the bottom.
    const r = btnRef.current?.getBoundingClientRect()
    setBox(
      r
        ? {
            left: r.left,
            top: r.bottom + 5,
            width: r.width,
            maxWidth: Math.max(160, window.innerWidth - 16),
          }
        : null,
    )
    setOpen(true)
  }

  const pick = (opt: SelectOption | undefined) => {
    if (!opt || opt.disabled) return
    props.onChange(opt.value)
    close(true)
  }

  // Measured after the list exists but before paint: flip it above the field
  // when the space below cannot hold it, keep it inside the viewport, and take
  // the focus the keyboard needs.
  useLayoutEffect(() => {
    if (!open) return
    const btn = btnRef.current
    const menu = menuRef.current
    if (!btn || !menu) return
    const r = btn.getBoundingClientRect()
    const h = menu.offsetHeight
    const gap = 5
    const roomBelow = window.innerHeight - r.bottom - 8
    const roomAbove = r.top - 8
    const up = h > roomBelow && roomAbove > roomBelow
    const width = Math.max(r.width, menu.offsetWidth)
    setBox({
      left: Math.max(8, Math.min(r.left, window.innerWidth - 8 - width)),
      top: up ? Math.max(8, r.top - gap - h) : r.bottom + gap,
      // A full-width field (a settings card) must not produce a full-width
      // menu: the list is sized to its own labels, and only stretches to the
      // field when the field is the narrower of the two.
      width: Math.min(r.width, 320),
      maxWidth: Math.max(160, window.innerWidth - 16),
    })
    menu.focus()
  }, [open, props.options.length])

  // An outside press, a scroll anywhere but inside the list, or a resize ends
  // the choice: the list is anchored to a fixed rectangle, so anything that
  // moves the field underneath it would strand the list.
  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close()
    }
    const onScroll = (e: Event) => {
      if (menuRef.current?.contains(e.target as Node)) return
      close()
    }
    document.addEventListener('pointerdown', onDown, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [open])

  // Keep the keyboard position visible when the list is taller than its box.
  useEffect(() => {
    if (!open) return
    menuRef.current?.querySelector<HTMLElement>('.select-opt.on')?.scrollIntoView({ block: 'nearest' })
  }, [open, active])

  const move = (step: number) => {
    const n = props.options.length
    if (!n) return
    let i = active
    for (let k = 0; k < n; k++) {
      i = (i + step + n) % n
      const opt = props.options[i]
      if (opt && !opt.disabled) {
        setActive(i)
        return
      }
    }
  }

  const onKey = (e: KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        move(1)
        break
      case 'ArrowUp':
        e.preventDefault()
        move(-1)
        break
      case 'Home':
        e.preventDefault()
        setActive(props.options.findIndex((o) => !o.disabled))
        break
      case 'End':
        e.preventDefault()
        for (let i = props.options.length - 1; i >= 0; i--) {
          const opt = props.options[i]
          if (opt && !opt.disabled) {
            setActive(i)
            break
          }
        }
        break
      case 'Enter':
      case ' ':
        e.preventDefault()
        pick(props.options[active])
        break
      case 'Escape':
        // The list is the topmost layer while it is up: Escape closes it and
        // nothing behind it (a header card, a dialog) should also close.
        if (layerAboveOwnsEscape('.select-menu')) return
        e.preventDefault()
        e.stopPropagation()
        close(true)
        break
      case 'Tab':
        close()
        break
    }
  }

  return (
    <div class={`select${props.class ? ` ${props.class}` : ''}`} ref={rootRef}>
      <button
        type="button"
        id={props.id}
        ref={btnRef}
        class={`input select-trigger${open ? ' open' : ''}`}
        aria-label={props.ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={props.disabled}
        onClick={() => (open ? close() : openList())}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault()
            openList()
          }
        }}
      >
        <span class="select-label">{current?.label ?? ''}</span>
        {current?.hint && <span class="select-hint">{current.hint}</span>}
        <Icon name="chevron-down" size={14} class="select-caret" />
      </button>
      {open && (
        <div
          ref={menuRef}
          class="select-menu"
          role="listbox"
          tabindex={-1}
          aria-label={props.ariaLabel}
          aria-activedescendant={props.options[active] ? `${uid}-${active}` : undefined}
          style={{
            left: `${box?.left ?? 0}px`,
            top: `${box?.top ?? 0}px`,
            minWidth: box ? `${box.width}px` : undefined,
            maxWidth: box ? `${box.maxWidth}px` : undefined,
          }}
          onKeyDown={onKey}
        >
          {props.options.map((o, i) => (
            <div
              key={o.value || `opt-${i}`}
              id={`${uid}-${i}`}
              role="option"
              aria-selected={o.value === props.value}
              aria-disabled={o.disabled || undefined}
              class={`select-opt${i === active ? ' on' : ''}`}
              onMouseEnter={() => !o.disabled && setActive(i)}
              onClick={() => pick(o)}
            >
              <span class="select-opt-label">{o.label}</span>
              {o.hint && <span class="select-opt-hint">{o.hint}</span>}
              {/* The slot is always there: the counts stay in one column
                  whether or not the row above them carries the tick. */}
              <span class="select-opt-check">
                {o.value === props.value && <Icon name="check" size={14} />}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
