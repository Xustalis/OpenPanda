// SPDX-License-Identifier: AGPL-3.0-or-later

// "The model registry was written" — one window event, two directions.
//
// The registry lives in the settings dialog, which is an overlay: opening it
// does not unmount the chat view underneath. The composer's model card reads
// GET /api/models once on mount, so a model added up there was invisible down
// here until a full page reload — the write happened, the row existed, and the
// card still listed yesterday's set.
//
// A window event rather than a shared hook, because the two ends sit in
// different trees with no common owner: the writer is a settings view, the
// reader is a composer control. The event carries no payload — every listener
// answers it by re-reading /api/models, which is the single source of truth and
// small enough (a handful of rows) to re-read on the rare occasion somebody
// edits a model.

export const MODEL_SAVED_EVENT = 'openpanda:model-saved'

/** Announce that the registry was written — add, edit, remove, switch, or a
 *  batch import. Callers fire it *after* the server accepted the write, never
 *  before: a listener that re-reads too early re-reads the old rows. */
export function notifyModelSaved(): void {
  // The module is imported by views that a test may load without a DOM.
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent(MODEL_SAVED_EVENT))
}

/** Subscribe to registry writes. Returns the unsubscribe, so a hook can hand it
 *  straight back to `useEffect`. */
export function onModelSaved(fn: () => void): () => void {
  if (typeof window === 'undefined') return () => {}
  window.addEventListener(MODEL_SAVED_EVENT, fn)
  return () => window.removeEventListener(MODEL_SAVED_EVENT, fn)
}
