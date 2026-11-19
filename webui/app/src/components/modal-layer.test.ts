// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { onModalLayerChange } from './modal-layer.ts'

// A floating surface has to join the *topmost* modal dialog, not merely the one
// that is open: `showModal()` inertises everything below the newest one, so a
// surface parked in a lower dialog is just as unreachable as one parked in the
// shell. This pins that choice — and the fall-back when the top closes.

interface FakeDialog {
  label: string
  open: boolean
  modal: boolean
  matches(sel: string): boolean
}

function fake(label: string): FakeDialog {
  return {
    label,
    open: false,
    modal: true,
    matches(sel: string) {
      return sel === ':modal' ? this.modal : false
    },
  }
}

const dialogs: FakeDialog[] = []
let flush = () => {}

const originalDoc = globalThis.document
const originalMO = globalThis.MutationObserver
let onChange: (() => void) | null = null

;(globalThis as unknown as { MutationObserver: unknown }).MutationObserver = class {
  constructor(cb: () => void) {
    onChange = cb
  }
  observe() {}
  disconnect() {}
}
;(globalThis as unknown as { document: unknown }).document = {
  body: {},
  querySelectorAll: () => dialogs.filter((d) => d.open),
}

flush = () => {
  onChange?.()
}

/** Subscribe, return the values the module reported, newest last. */
function watch(): { seen: (string | null)[]; stop: () => void } {
  const seen: (string | null)[] = []
  const stop = onModalLayerChange((top) => {
    seen.push(top ? (top as unknown as FakeDialog).label : null)
  })
  return { seen, stop }
}

function show(...next: FakeDialog[]): void {
  for (const d of dialogs) d.open = false
  for (const d of next) d.open = true
  flush()
}

describe('modal layer', () => {
  it('reports nothing while no modal dialog is open', () => {
    dialogs.length = 0
    dialogs.push(fake('settings'))
    const { seen, stop } = watch()
    assert.deepEqual(seen, [null])
    stop()
  })

  it('reports the open modal dialog', () => {
    const settings = dialogs[0]!
    const { seen, stop } = watch()
    show(settings)
    assert.equal(seen.at(-1), 'settings')
    stop()
  })

  it('follows the newest dialog, not DOM order', () => {
    const settings = dialogs[0]!
    const confirm = fake('confirm')
    // The confirm dialog is rendered *before* the settings sheet in the DOM;
    // only the order the two were shown decides which one is on top.
    dialogs.length = 0
    dialogs.push(confirm, settings)
    const { seen, stop } = watch()
    show(settings)
    show(settings, confirm)
    assert.equal(seen.at(-1), 'confirm')
    stop()
  })

  it('falls back to the dialog underneath when the top one closes', () => {
    const confirm = dialogs.find((d) => d.label === 'confirm')!
    const { seen, stop } = watch()
    confirm.open = false
    flush()
    assert.equal(seen.at(-1), 'settings')
    stop()
  })

  it('reports nothing again once the last dialog closes', () => {
    const settings = dialogs.find((d) => d.label === 'settings')!
    const { seen, stop } = watch()
    show()
    assert.equal(seen.at(-1), null)
    assert.equal(settings.open, false)
    stop()
  })

  it('ignores a dialog opened with show()', () => {
    const plain = fake('plain')
    plain.modal = false
    dialogs.length = 0
    dialogs.push(plain)
    const { seen, stop } = watch()
    show(plain)
    assert.deepEqual(seen, [null])
    stop()
  })
})

// Restore the shared globals so nothing else in the suite inherits the stubs.
process.on('exit', () => {
  ;(globalThis as unknown as { document: unknown }).document = originalDoc
  ;(globalThis as unknown as { MutationObserver: unknown }).MutationObserver = originalMO
})
