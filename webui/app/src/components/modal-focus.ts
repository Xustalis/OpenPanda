// Focus trap for the console's modals (⌘K palette, confirm dialog).
//
// A modal owns the keyboard while it is open: focus moves in on mount, Tab
// cycles inside the dialog instead of escaping to the page underneath, and
// focus returns to whatever opened it once the modal unmounts. One hook, two
// call sites — the trap's contract is spelled out here rather than re-derived
// per overlay.

import { useEffect } from 'preact/hooks'

// Everything a Tab keypress could legally land on, in DOM order.
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

/**
 * Trap focus inside `ref.current` while `active` is true.
 *
 *  - On activate: focuses the first focusable element (falling back to the
 *    container itself, so an empty dialog is still a Tab stop).
 *  - Tab / Shift+Tab wrap around at the edges.
 *  - On deactivate: returns focus to the element that was active before the
 *    trap engaged.
 */
export function useModalFocus(ref: { current: HTMLElement | null }, active: boolean): void {
  useEffect(() => {
    if (!active) return
    const root = ref.current
    if (!root) return

    const restoreTo = (document.activeElement as HTMLElement | null) ?? null
    const focusables = () => Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE))

    ;(focusables()[0] ?? root).focus()

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const els = focusables()
      if (els.length === 0) {
        // Nothing to cycle through — keep the keypress inside anyway.
        e.preventDefault()
        return
      }
      const first = els[0]!
      const last = els[els.length - 1]!
      const current = document.activeElement
      if (e.shiftKey) {
        if (current === first || !root.contains(current)) {
          e.preventDefault()
          last.focus()
        }
      } else {
        if (current === last || !root.contains(current)) {
          e.preventDefault()
          first.focus()
        }
      }
    }
    root.addEventListener('keydown', onKeyDown)
    return () => {
      root.removeEventListener('keydown', onKeyDown)
      restoreTo?.focus?.()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active])
}
