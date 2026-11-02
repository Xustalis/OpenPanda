// SPDX-License-Identifier: AGPL-3.0-or-later

// Putting a reference back in the composer.
//
// Two surfaces hand text to the chat's input — the file drawer's "insert" and
// the artifact preview's "annotate" — and both live below the session view's
// tree. Threading a callback through every intermediate component would make
// each of them know about a feature they have nothing to do with, so the
// session view publishes the action once and the leaves consume it.

import { createContext } from 'preact'
import { useContext } from 'preact/hooks'

/** insert puts text into the composer without sending it. */
export type ComposerInserter = (text: string) => void

export const ComposerContext = createContext<ComposerInserter | null>(null)

export function useComposerInsert(): ComposerInserter | null {
  return useContext(ComposerContext)
}
