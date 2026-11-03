// SPDX-License-Identifier: AGPL-3.0-or-later

/** Zoom arithmetic for the workspace canvas.
 *
 *  The drawing is a scroller holding cards pinned in its own (unscaled)
 *  coordinates; a zoom is a scale applied to that surface, so the browser can
 *  work out the sizes on its own. The two things it cannot work out are how
 *  far one wheel notch should go, and where the scroll offset has to land so
 *  the point under the pointer stays under the pointer. Both live here, the
 *  same reason workspace-links.ts exists: the math is testable without a DOM.
 */

/** How far in and out the wheel goes. Below half size a tile is a smudge;
 *  past double, one card fills the pane and the map stops being a map. */
export const ZOOM_MIN = 0.5
export const ZOOM_MAX = 2

/** Wheel travel → zoom. One notch is 100px in Chrome, so 0.001 puts a notch
 *  at ~9.5%: a step you can feel, small enough to land on a round number. */
const STEP = 0.001

export function clampZoom(z: number): number {
  if (!Number.isFinite(z)) return 1
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))
}

/** The zoom one wheel event asks for. Exponential, so a notch in and the same
 *  notch back out return to where they started instead of drifting. */
export function nextZoom(current: number, deltaY: number): number {
  if (!Number.isFinite(deltaY) || deltaY === 0) return current
  // Three decimals: finer than the readout can show, coarse enough that a
  // stored value is still a number a human could have typed.
  return Math.round(clampZoom(current * Math.exp(-deltaY * STEP)) * 1000) / 1000
}

/** Where the scroller has to sit after a zoom so the point under the pointer
 *  stays under it.
 *
 *  `pointer` and `pad` are pixels from the scroller's border box — the pointer
 *  from the event, the padding from where the surface actually starts (the map
 *  pays 26px/44px of it for the pet's headroom). `scroll` is the offset the
 *  scroller is at now. All three are in the frame the user sees; the surface's
 *  own coordinates only appear inside the division. */
export function anchorScroll(
  scroll: number,
  pointer: number,
  pad: number,
  from: number,
  to: number,
): number {
  if (!(from > 0) || !(to > 0)) return scroll
  const local = (pointer - pad + scroll) / from
  return local * to - pointer + pad
}
