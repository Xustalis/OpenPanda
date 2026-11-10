// SPDX-License-Identifier: AGPL-3.0-or-later

// A persisted user turn is the *prompt*, not the sentence the author saw in
// the composer. The panel stores req.Prompt verbatim (sessions.go), and every
// @file the composer expanded rides along as a fenced copy of that file, so
// the thread keeps replayable context for the model. Reloading a transcript —
// after a stop, a focus change, another device's turn, or just a refresh —
// used to replace the short user bubble with tens of thousands of characters
// of file contents. Keep the stored prompt intact and recover the author's
// words for display, the same split core.NoteProse makes for queued notes.

/** Header withAttachments (web) and noteBody (core) stamp before the @path
 *  list. Keep in sync with internal/core/messages.go:noteAttachHeader and
 *  withAttachments in components/attach.ts. */
export const ATTACH_HEADER = 'Attached files (read them from the workspace):'

export interface UserTurnDisplay {
  /** What the user actually typed. */
  text: string
  /** Paths that were inlined into the prompt, for the bubble's attachments. */
  files: string[]
}

/** Read the `- @path` list that follows the header. It stops at the first
 *  line that is not a list entry: the fenced bodies start right after it. */
function attachList(tail: string): string[] {
  const files: string[] = []
  for (const line of tail.split('\n')) {
    const m = /^\s*- @(\S.*?)\s*$/.exec(line)
    if (m) files.push(m[1]!)
    else if (files.length) break
  }
  return files
}

/** Recover the bubble text and attachment chips from a stored user turn.
 *  Text that does not carry an expansion is returned untouched. */
export function displayUserTurn(text: string): UserTurnDisplay {
  if (!text) return { text, files: [] }

  // 1. An upload (or a note): the header introduces the @path list and the
  //    fenced bodies. Everything from it on is generated context, so the
  //    author's words are exactly the prose before it.
  const marker = `\n\n${ATTACH_HEADER}`
  const at = text.indexOf(marker)
  if (at >= 0) {
    return { text: text.slice(0, at).trimEnd(), files: attachList(text.slice(at + marker.length)) }
  }
  // A turn that was only a file, with no prose typed at all.
  if (text.startsWith(ATTACH_HEADER)) {
    return { text: '', files: attachList(text.slice(ATTACH_HEADER.length)) }
  }

  // 2. A typed @ref with no tray has no header: expandFileRefs appends the
  //    fenced blocks straight after the prose. Cut only when the tail really
  //    ends a fenced block and the prose has no half-open fence of its own —
  //    a question that quotes code stays exactly as it was written.
  const head = /\n\n`([^`\n\s]+)`:\n```[^\n]*\n/g
  const paths: string[] = []
  let first = -1
  for (let m = head.exec(text); m; m = head.exec(text)) {
    if (first < 0) first = m.index
    paths.push(m[1]!)
  }
  if (
    first >= 0 &&
    /```[ \t]*$/.test(text.trimEnd()) &&
    (text.slice(0, first).match(/^```/gm) ?? []).length % 2 === 0
  ) {
    return { text: text.slice(0, first).trimEnd(), files: paths }
  }
  return { text, files: [] }
}
