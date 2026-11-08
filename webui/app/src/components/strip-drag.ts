// SPDX-License-Identifier: AGPL-3.0-or-later

/** Mouse-driven scrolling for a strip that runs off its own edge.
 *
 *  The orchestration card's strips hold more tasks than their width shows: a
 *  narrow dock keeps two 156px tiles in view and leaves the rest off its right
 *  edge; a wider dock simply reveals more of them. A trackpad swipes that, and
 *  touch scrolls it for free — a plain mouse had nothing, which is why the
 *  strip carries a thin scrollbar as well: a target, not an affordance.
 *
 *  This binds the two gestures a mouse actually has:
 *
 *  - press and drag anywhere on the strip (tiles included) to pull it;
 *  - turn the wheel over it and the notch moves the strip — sideways for an
 *    `x` strip, because a vertical wheel does not move a horizontal scroller.
 *    A `y` strip is a plain vertical scroller: the wheel is already its own,
 *    so no listener is bound for it there and the event is left to the
 *    browser.
 *
 *  A drag that starts on a tile must not also open that tile. The click that
 *  ends a drag is swallowed once, at capture, before it can reach the chip's
 *  own button; a press that never crosses `DRAG_THRESHOLD` stays a click and
 *  opens the task as always. */

/** Travel, in px, before a press counts as a drag rather than a click. Big
 *  enough for the wobble of a deliberate click, small enough that a drag that
 *  reads as a drag on screen is already one here. */
export const DRAG_THRESHOLD = 4

/** Which axis a strip scrolls along. `x` needs the wheel translated, `y` does
 *  not; both take the press-drag. */
export type DragAxis = 'x' | 'y'

/** Which way a wheel event should push the strip, or 0 to leave it to the
 *  page.
 *
 *  `shiftKey` and a sideways-dominant delta are already horizontal gestures —
 *  the browser moves the strip natively and doing it twice would double the
 *  distance. A strip with nothing to reveal gets nothing. And a notch that
 *  would push past either end returns 0 instead of a clamped no-op, which is
 *  what hands the card back its vertical scroll at the end of the row. */
export function wheelStep(x: {
  deltaX: number
  deltaY: number
  shiftKey: boolean
  scrollLeft: number
  clientWidth: number
  scrollWidth: number
}): number {
  if (x.shiftKey) return 0
  if (Math.abs(x.deltaX) >= Math.abs(x.deltaY)) return 0
  const room = x.scrollWidth - x.clientWidth
  if (room <= 0) return 0
  const d = x.deltaY
  if (d < 0) return x.scrollLeft > 0 ? d : 0
  if (d > 0) return x.scrollLeft < room - 1 ? d : 0
  return 0
}

/** Attach the drag and wheel gestures to a strip. Returns the unbind. */
export function bindStripDrag(el: HTMLElement, axis: DragAxis = 'x'): () => void {
  let pressed = false
  let dragging = false
  let startCoord = 0
  let startPos = 0
  let pointerId = -1

  // The axis is read through these four so the gesture below stays one piece:
  // the pointer moves along `coord`, the strip follows along `pos`.
  const pos = () => (axis === 'x' ? el.scrollLeft : el.scrollTop)
  const setPos = (v: number) => {
    if (axis === 'x') el.scrollLeft = v
    else el.scrollTop = v
  }
  const room = () => (axis === 'x' ? el.scrollWidth - el.clientWidth : el.scrollHeight - el.clientHeight)
  const coord = (e: PointerEvent) => (axis === 'x' ? e.clientX : e.clientY)

  const onDown = (e: PointerEvent) => {
    // Left button only, and not touch: touch already scrolls this element
    // natively, and a synthetic drag on top of it would fight the compositor.
    if (e.button !== 0 || e.pointerType === 'touch') return
    // Nothing to pull. A strip that fits is not a drag surface, and swallowing
    // the click at the end of a pointless 5px drag would only make a tile feel
    // unresponsive — the press stays an ordinary click instead.
    if (room() <= 1) return
    pressed = true
    dragging = false
    startCoord = coord(e)
    startPos = pos()
    pointerId = e.pointerId
  }

  const onMove = (e: PointerEvent) => {
    if (!pressed || e.pointerId !== pointerId) return
    const d = coord(e) - startCoord
    if (!dragging) {
      if (Math.abs(d) < DRAG_THRESHOLD) return
      dragging = true
      el.classList.add('is-dragging')
    }
    // The strip follows the pointer one-for-one: no easing, no inertia. A
    // 164px tile that lags behind the cursor reads as a broken scrollbar.
    e.preventDefault()
    setPos(startPos - d)
  }

  const onDragStart = (e: Event) => {
    // A drag over a title is text-selection drag to the browser. The strip
    // wants the whole press, so the native one never starts.
    if (pressed) e.preventDefault()
  }

  const end = (e: PointerEvent) => {
    if (!pressed || e.pointerId !== pointerId) return
    pressed = false
    if (dragging) {
      // The press began on a chip, so the browser will fire a click on it when
      // the drag ends — and that click would open the task the reader was only
      // trying to scroll past. Swallow exactly one, at capture, and drop the
      // trap on the next turn in case the pointer left the strip and no click
      // followed at all.
      const swallow = (ev: Event) => {
        ev.stopPropagation()
        ev.preventDefault()
      }
      el.addEventListener('click', swallow, { capture: true, once: true })
      setTimeout(() => el.removeEventListener('click', swallow, { capture: true }), 0)
    }
    dragging = false
    el.classList.remove('is-dragging')
  }

  const onWheel = (e: WheelEvent) => {
    const step = wheelStep({
      deltaX: e.deltaX,
      deltaY: e.deltaY,
      shiftKey: e.shiftKey,
      scrollLeft: el.scrollLeft,
      clientWidth: el.clientWidth,
      scrollWidth: el.scrollWidth,
    })
    if (!step) return
    e.preventDefault()
    el.scrollLeft += step
  }

  el.addEventListener('pointerdown', onDown)
  // Move and release live on the window: a fast drag leaves the strip before
  // it ends, and a strip that only listened to itself would drop the pointer
  // mid-gesture and leave the row stuck to the cursor.
  window.addEventListener('pointermove', onMove, { passive: false })
  window.addEventListener('pointerup', end)
  window.addEventListener('pointercancel', end)
  el.addEventListener('dragstart', onDragStart)
  // A `y` strip scrolls natively on a vertical wheel; translating the notch
  // there would scroll it twice.
  if (axis === 'x') el.addEventListener('wheel', onWheel, { passive: false })

  return () => {
    el.removeEventListener('pointerdown', onDown)
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', end)
    window.removeEventListener('pointercancel', end)
    el.removeEventListener('dragstart', onDragStart)
    el.removeEventListener('wheel', onWheel)
    el.classList.remove('is-dragging')
  }
}
