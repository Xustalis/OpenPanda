// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

/** A model-id field with the fetched list built into it — one control, not two.
 *
 *  The input owns the value, so typing a model the provider does not list
 *  still works; a caret inside the field opens the ids the provider just
 *  returned. This is the affordance `<datalist>` was reaching for, except the
 *  browser filters a datalist by whatever the input already holds — so a
 *  prefilled catalogue default left the list empty and the caret dead. Here
 *  the list is ours: it always shows every fetched id, and a pick writes
 *  straight back into the field. */
export function ModelPicker(props: {
  value: string
  onValue(v: string): void
  options: string[]
  placeholder?: string
  required?: boolean
  id?: string
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  // Fixed, not absolute: the field lives inside the add-model sheet, whose
  // body scrolls and clips. An absolutely positioned list was cut off at the
  // sheet's footer, so a 21-model roster showed its first two ids and the rest
  // were unreachable. Same contract as components/select.tsx: anchor to the
  // field's viewport rect, flip above when the room below is short, and close
  // when anything moves underneath it.
  const [box, setBox] = useState<{ left: number; top: number; width: number; maxHeight: number } | null>(
    null,
  )
  const boxRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const hasOptions = props.options.length > 0

  const openList = () => {
    const r = boxRef.current?.getBoundingClientRect()
    if (r) setBox({ left: r.left, top: r.bottom + 4, width: r.width, maxHeight: 224 })
    setOpen(true)
  }

  // Measured after the list exists but before paint: the first frame lands it
  // under the field, this pass flips it up when the space below cannot hold
  // it and keeps it inside the viewport.
  useLayoutEffect(() => {
    if (!open) return
    const anchor = boxRef.current?.getBoundingClientRect()
    const list = listRef.current
    if (!anchor || !list) return
    const gap = 4
    const roomBelow = window.innerHeight - anchor.bottom - 8
    const roomAbove = anchor.top - 8
    // The list wants its content height, capped at 224. Flip only when the
    // room below cannot hold that — and in either direction the cap is the
    // room actually available, or the list grows past the window (it did:
    // flipping up gave it the whole 648px above and it ran off the bottom).
    const want = Math.min(list.scrollHeight + 10, 224)
    const up = want > roomBelow && roomAbove > roomBelow
    const cap = Math.max(120, Math.min(want, up ? roomAbove : roomBelow))
    const width = Math.max(anchor.width, 220)
    setBox({
      left: Math.max(8, Math.min(anchor.left, window.innerWidth - 8 - width)),
      top: up ? Math.max(8, anchor.top - gap - cap) : anchor.bottom + gap,
      width: Math.min(width, window.innerWidth - 16),
      maxHeight: cap,
    })
    list.focus()
  }, [open, props.options.length])

  // Close on an outside click, on any scroll outside the list, on resize, and
  // on Escape without letting the dialog's own Escape handler see it — one
  // Escape closes the list, the next the modal.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onScroll = (e: Event) => {
      if (listRef.current?.contains(e.target as Node)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [open])

  return (
    <div class="model-picker" ref={boxRef}>
      <input
        id={props.id}
        class="input mono"
        type="text"
        value={props.value}
        placeholder={props.placeholder}
        required={props.required}
        disabled={props.disabled}
        onInput={(e) => props.onValue((e.target as HTMLInputElement).value)}
      />
      {hasOptions && (
        <button
          type="button"
          class="model-picker-toggle"
          aria-label={t('models.pickFromList')}
          title={t('models.pickFromList')}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={props.disabled}
          onClick={() => (open ? setOpen(false) : openList())}
        >
          <Icon name="chevron-down" size={14} />
        </button>
      )}
      {open && hasOptions && (
        <div
          ref={listRef}
          class="model-picker-list"
          role="menu"
          tabindex={-1}
          style={{
            left: `${box?.left ?? 0}px`,
            top: `${box?.top ?? 0}px`,
            width: box ? `${box.width}px` : undefined,
            maxHeight: box ? `${box.maxHeight}px` : undefined,
          }}
        >
          {props.options.map((id) => (
            <button
              key={id}
              type="button"
              role="menuitem"
              class={`model-picker-item${id === props.value ? ' on' : ''}`}
              onClick={() => {
                props.onValue(id)
                setOpen(false)
              }}
            >
              {id}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
