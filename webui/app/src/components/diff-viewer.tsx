// SPDX-License-Identifier: AGPL-3.0-or-later

/** Shared unified-diff renderer — used by the task detail's patch card and the
 *  dock's change panel. Line classification is deliberately minimal:
 *  add/del/hunk/meta, no intra-line highlighting.
 *
 *  `numbered` adds the two gutters a reader needs to say *where* a change is:
 *  the old and new line numbers, advanced from each hunk header. Without it
 *  the renderer stays a plain code block, which is what a whole-patch view
 *  (the task card) wants — one column of line numbers across a patch that
 *  jumps between files would number nothing. */
export function DiffViewer({ patch, numbered = false }: { patch: string; numbered?: boolean }) {
  if (!patch) return null
  const lines = patch.split('\n')
  let oldNo = 0
  let newNo = 0
  let inHunk = false
  return (
    <pre class={`diff-patch diff-patch-view mono${numbered ? ' is-numbered' : ''}`}>
      {lines.map((line, idx) => {
        let cls = 'diff-line'
        let oldLabel = ''
        let newLabel = ''
        if (line.startsWith('diff --git ')) {
          // A new file's block starts the numbering over; a stray hunk header
          // from the previous file must not continue it.
          inHunk = false
          cls += ' diff-line-meta'
        } else if (line.startsWith('@@')) {
          const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
          if (m) {
            oldNo = Number(m[1])
            newNo = Number(m[2])
          }
          inHunk = true
          cls += ' diff-line-hunk'
        } else if (line.startsWith('+') && !line.startsWith('+++')) {
          cls += ' diff-line-add'
          if (inHunk) newLabel = String(newNo++)
        } else if (line.startsWith('-') && !line.startsWith('---')) {
          cls += ' diff-line-del'
          if (inHunk) oldLabel = String(oldNo++)
        } else if (line.startsWith('diff ') || line.startsWith('index ') || line.startsWith('---') || line.startsWith('+++')) {
          cls += ' diff-line-meta'
        } else if (inHunk && !line.startsWith('\\')) {
          // Context: present on both sides.
          oldLabel = String(oldNo++)
          newLabel = String(newNo++)
        }
        return (
          <div key={idx} class={cls}>
            {numbered && <span class="diff-num">{oldLabel}</span>}
            {numbered && <span class="diff-num">{newLabel}</span>}
            <span class="diff-line-content">{line || ' '}</span>
          </div>
        )
      })}
    </pre>
  )
}
