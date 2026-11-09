// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { layerAboveOwnsEscape } from './card-dismiss.ts'

describe('header card dismissal', () => {
  it('lets a menu or modal own Escape before the header card', () => {
    const original = globalThis.document
    try {
      for (const open of [true, false]) {
        globalThis.document = {
          querySelectorAll: () => (open ? [{}] : []),
        } as unknown as Document
        assert.equal(layerAboveOwnsEscape(), open)
      }
    } finally {
      globalThis.document = original
    }
  })

  it('a floating layer defers only to layers painted after it', () => {
    const original = globalThis.document
    const pop = { matches: (s: string) => s === '.notif-dialog' }
    const modal = { matches: (s: string) => s === '.modal-backdrop' }
    try {
      globalThis.document = {
        querySelectorAll: () => [pop, modal],
      } as unknown as Document
      // The modal sits after the pop in the document → owns Escape over it.
      assert.equal(layerAboveOwnsEscape('.notif-dialog'), true)
      // Nothing comes after the modal → it is the topmost layer.
      assert.equal(layerAboveOwnsEscape('.modal-backdrop'), false)

      globalThis.document = {
        querySelectorAll: () => [pop],
      } as unknown as Document
      // Alone, the pop owns its own Escape — its open panel is not a layer
      // above itself.
      assert.equal(layerAboveOwnsEscape('.notif-dialog'), false)
    } finally {
      globalThis.document = original
    }
  })
})
