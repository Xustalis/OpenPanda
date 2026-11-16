// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../../i18n'

/** Compact file size for the attachment browser and @file completions. */
export function fmtSize(n?: number): string {
  if (n === undefined) return ''
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)}M`
  if (n >= 1024) return `${(n / 1024).toFixed(1)}k`
  return `${n}B`
}

/** A pill's markup. It has to be a raw string rather than the <Icon>
 *  component because the pill is inserted as a DOM node the framework never
 *  sees — React reconciling children inside a contenteditable would fight the
 *  text nodes the user's typing creates there. The two glyphs mirror
 *  components/icons.tsx on purpose.
 *
 *  The image glyph keeps the outline frame of the Lucide original but swaps
 *  its diagonal mountain stroke for a solid silhouette. At 13px the two
 *  strokes of the original sat a pixel apart and fused into a box with a slash
 *  through it — the universal "missing image" symbol, the opposite of what an
 *  attachment pill says. A filled landscape survives the downscale, and the
 *  frame is what separates it from a person glyph (a dot above a triangle on
 *  its own reads as a head over shoulders). 16px, not 13: the silhouette needs
 *  the extra pixels to show two peaks rather than one lump. */
function chipMarkup(image: boolean): string {
  if (image) {
    return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="none">'
      + '<rect x="3" y="3" width="18" height="18" rx="4.2" stroke="currentColor" stroke-width="1.7"/>'
      + '<path fill="currentColor" d="M4.2 20 8.4 12.2 11.6 16.4 15.2 12.8 19.8 20z"/>'
      + '<circle cx="7.9" cy="7.9" r="1.75" fill="currentColor"/></svg>'
  }
  return '<svg width="13" height="13" viewBox="0 0 24 24" fill="none"'
    + ' stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">'
    + '<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/>'
    + '</svg>'
}

/** The pill's own controls, drawn the same way and for the same reason as
 *  `chipMarkup` above: these nodes live in the editable, outside the
 *  framework. `search` and `x` mirror components/icons.tsx. 14px: at 13 the
 *  two glyphs read a size under the name beside them and looked like debris
 *  rather than controls. */
function chipActMarkup(kind: 'zoom' | 'x'): string {
  const open = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"'
    + ' stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
  return kind === 'zoom'
    ? `${open}<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>`
    : `${open}<path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`
}

/** One file waiting in the composer's attachment pills.
 *
 *  The File is kept rather than uploaded on arrival — the pill needs no
 *  server round-trip to show its name, and a brand-new chat has no session to
 *  upload into yet. The bytes go up at send. */
export interface PendingAttachment {
  key: string
  name: string
  size: number
  image: boolean
  /** Bytes to upload at send. A workspace reference has none — it rides as an
   *  `@path` in the prompt instead, which expandFileRefs reads for the model. */
  file?: File
  /** Workspace path behind a reference pill (the files dock's 插入). */
  ref?: string
}

/** A pill for a pending attachment, inserted at the caret.
 *
 *  A pill is atomic: the caret steps over it as one unit. It carries its own
 *  two controls — enlarge, then remove — the way the reference chip carries
 *  its ×. The enlarge glyph is the extension: a picture in the composer had
 *  no way to be looked at closely before sending, and a 23px pill is not a
 *  preview. Clicking the pill's body means "show me the picture"; taking the
 *  file back out has its own button beside it, so a mis-aimed click no longer
 *  throws an attachment away.
 *
 *  Both controls hold their space at rest. A control that materialises under
 *  the pointer widens the pill, which shoves the rest of the sentence — and
 *  the caret — sideways while the pointer is only passing through. */
export function makeChipNode(p: PendingAttachment): HTMLElement {
  const chip = document.createElement('span')
  chip.className = p.image ? 'attach-pill is-image' : 'attach-pill'
  chip.setAttribute('data-pk', p.key)
  chip.setAttribute('contenteditable', 'false')
  // A picture names itself in the floating copy, and a native tooltip would
  // sit on top of it. A text reference has no copy, so the path stays here.
  if (!p.image) chip.setAttribute('title', p.ref ?? p.name)

  const ico = document.createElement('span')
  ico.className = p.image ? 'attach-pill-ico img' : 'attach-pill-ico'
  ico.setAttribute('aria-hidden', 'true')
  ico.innerHTML = chipMarkup(p.image)
  chip.appendChild(ico)

  const name = document.createElement('span')
  name.className = 'attach-pill-name'
  name.textContent = p.name
  chip.appendChild(name)

  const acts = document.createElement('span')
  acts.className = 'attach-pill-acts'
  const buttons: Array<['zoom' | 'x', string]> = p.image
    ? [['zoom', t('events.imageOpen')], ['x', t('sessions.attachRemove')]]
    : [['x', t('sessions.attachRemove')]]
  for (const [kind, label] of buttons) {
    const act = document.createElement('span')
    act.className = `attach-pill-act attach-pill-${kind}`
    act.setAttribute('role', 'button')
    act.setAttribute('title', label)
    act.setAttribute('aria-label', label)
    act.innerHTML = chipActMarkup(kind)
    acts.appendChild(act)
  }
  chip.appendChild(acts)
  return chip
}
