// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ZOOM_MAX, ZOOM_MIN, anchorScroll, clampZoom, nextZoom } from './workspace-zoom.ts'

test('a notch in and the same notch out return to the same zoom', () => {
  const inOnce = nextZoom(1, -100)
  assert.ok(inOnce > 1, 'a wheel up zooms in')
  assert.ok(Math.abs(nextZoom(inOnce, 100) - 1) < 1e-3, 'the reverse notch undoes it')
})

test('the wheel never leaves the allowed band', () => {
  let z = 1
  for (let i = 0; i < 50; i++) z = nextZoom(z, -100)
  assert.equal(z, ZOOM_MAX)
  for (let i = 0; i < 100; i++) z = nextZoom(z, 100)
  assert.equal(z, ZOOM_MIN)
  // A wheel event with no travel (or a bogus one) is not a zoom.
  assert.equal(nextZoom(1.25, 0), 1.25)
  assert.equal(nextZoom(1.25, Number.NaN), 1.25)
  assert.equal(clampZoom(Number.POSITIVE_INFINITY), 1)
})

test('the point under the pointer keeps its place on the surface', () => {
  // The map's padding is 26px, the pointer sits 300px into the scroller and
  // the surface is scrolled 120px: that is local x = (300 - 26 + 120) / 1.
  const local = (300 - 26 + 120) / 1
  const after = anchorScroll(120, 300, 26, 1, 2)
  assert.equal((300 - 26 + after) / 2, local)

  // Same statement zooming out, and from a zoom that is not 1.
  const out = anchorScroll(after, 300, 26, 2, 0.5)
  assert.ok(Math.abs((300 - 26 + out) / 0.5 - local) < 1e-9)

  // No change in zoom, no change in scroll.
  assert.equal(anchorScroll(120, 300, 26, 1.5, 1.5), 120)
})

test('a pointer parked on the surface origin keeps the surface origin still', () => {
  // Pointer exactly on the first card's corner (padding 26): the surface must
  // not slide out from under it, whatever the zoom does.
  for (const [from, to] of [
    [1, 2],
    [2, 1],
    [1, 0.5],
    [0.5, 1.8],
  ] as const) {
    assert.equal(anchorScroll(0, 26, 26, from, to), 0)
  }
})
