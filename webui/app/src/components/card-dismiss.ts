// SPDX-License-Identifier: AGPL-3.0-or-later

/** Dismissal rules for the two cards that hang off the chat header (the node
 *  card and the orchestration card).
 *
 *  They are not menus and they do not close on an outside click. Both are read
 *  *while* the thread is being used — you open one to pin a machine or watch a
 *  task, then keep reading the answer behind it, select a line, follow a link.
 *  Every click in the transcript used to put the card away, which meant the
 *  panel disappeared at the first sign of reading, and the rule had to grow a
 *  list of exceptions for the composer, then a second one for the popovers the
 *  composer grows. The trigger in the header is now the only click that closes
 *  a card; Escape is the keyboard equivalent. */

/** Whether a layer above the header card owns Escape right now.
 *
 *  Both cards listen for Escape in the capture phase and stop the event, so
 *  without this check one key press closed the card *and* left the thing it
 *  menu — the card has already stopped the event on its way down — or reset
 *  the task detail instead of closing the card. Escape closes the topmost
 *  layer: a menu hanging off the composer, or a modal, takes it first; the
 *  card takes the next press.
 *
 *  A DOM query rather than a shared store because these popovers are three
 *  different components that already own their own state — the alternative was
 *  threading an "is any menu open" flag through the pane and every menu. */
const ESCAPE_LAYERS =
  '.model-panel, .attach-menu, .complete-menu, .modal-backdrop, .settings-dialog, .reminder-pop, .wd-panel, .ctx-menu, .select-menu, .ws-hidden-card'

/** Whether a layer above the caller owns Escape right now.
 *
 *  `own` lets a floating layer name its own panel: the reminder pop and the
 *  workdir menu ARE in the layer list — the header cards defer to them —
 *  but their own Escape handlers must not mistake their own open panel for
 *  a layer above, or they would never be allowed to close. Document order
 *  is paint order for these fixed-position layers, so "above me" means
 *  "comes after my own panel": two floating layers open at once unwind in
 *  order instead of both deferring to each other and never closing. */
export function layerAboveOwnsEscape(own?: string): boolean {
  const els = [...document.querySelectorAll(ESCAPE_LAYERS)]
  if (!own) return els.length > 0
  const me = els.findIndex((el) => el.matches(own))
  if (me === -1) return els.length > 0
  return me < els.length - 1
}
