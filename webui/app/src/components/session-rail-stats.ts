// SPDX-License-Identifier: AGPL-3.0-or-later

/** The numbers the session rail prints, kept out of the view so they can be
 *  tested without a DOM.
 *
 *  Two of them are not in the payload the panel sends:
 *
 *  - Per-file `+n −m`. `/api/sessions/{id}/diff` carries a unified patch and
 *    an aggregate stat, not per-file counts (see webui/panel/sessions.go,
 *    `sessionDiff`). The rail wants the count beside the file that got the
 *    edit — that is the whole reason a change list is more useful than a
 *    number — so it is read back off the patch the diff drawer already
 *    holds. No extra request, and no second source of truth.
 *  - The context share. The panel reports tokens per turn; the window size
 *    lives on the model row. The percentage is their quotient, clamped.
 *
 *  Both parsers are deliberately conservative: a number the rail cannot
 *  stand behind is left out rather than guessed. */

export interface PatchFileStat {
  insertions: number
  deletions: number
}

/** The marker `Worktrees.Diff` appends when it cuts the patch at 128 KiB. */
const TRUNCATED = '… (diff truncated)'

/** Git prints a path either bare (`+++ b/src/a.ts`) or C-quoted when it holds
 *  a space or a control character (`+++ "b/src/a b.ts"`). Undo both, then drop
 *  the side prefix: the rest of the console keys paths the way `git status
 *  --porcelain` prints them. */
function cleanPath(raw: string): string {
  let p = raw.trim()
  if (p.length > 1 && p.startsWith('"') && p.endsWith('"')) {
    p = p
      .slice(1, -1)
      .replace(/\\(["\\])/g, '$1')
      .replace(/\\t/g, '\t')
      .replace(/\\n/g, '\n')
  }
  if (p.startsWith('a/') || p.startsWith('b/')) p = p.slice(2)
  return p
}

/** Path → line counts, for every file the patch has a hunk for.
 *
 *  Only the `+++`/`---` pair *before* the first `@@` names a file: inside a
 *  hunk an added line can itself start with `++`, and counting that as a
 *  header would silently split the file in two.
 *
 *  The block the cap cut through is dropped. Its count is a partial one, and
 *  a partial `+12` under a diff that stopped at 128 KiB reads as the file's
 *  real size — worse than showing no number at all. */
export function patchFileStats(patch: string): Map<string, PatchFileStat> {
  const out = new Map<string, PatchFileStat>()
  if (!patch) return out
  let key = ''
  let current: PatchFileStat | null = null
  let inHunk = false
  for (const line of patch.split('\n')) {
    if (line.startsWith('diff --git ')) {
      key = ''
      current = null
      inHunk = false
      continue
    }
    if (!inHunk && line.startsWith('+++ ')) {
      const p = cleanPath(line.slice(4))
      // A deletion's target side is /dev/null; the `---` line named it.
      if (p !== '/dev/null') key = p
      continue
    }
    if (!inHunk && line.startsWith('--- ')) {
      const p = cleanPath(line.slice(4))
      if (!key && p !== '/dev/null') key = p
      continue
    }
    if (line.startsWith('@@')) {
      if (key && !current) {
        current = { insertions: 0, deletions: 0 }
        out.set(key, current)
      }
      inHunk = true
      continue
    }
    if (!inHunk || !current) continue
    if (line.startsWith('+')) current.insertions++
    else if (line.startsWith('-')) current.deletions++
  }
  if (key && patch.includes(TRUNCATED)) out.delete(key)
  return out
}

/** How full the context window is, in whole percent, or null when the window
 *  is unknown (a model row with no catalogue entry and no explicit limit).
 *  Clamped: a provider that bills past its advertised window must not render
 *  a bar wider than its track. */
export function contextShare(used: number, window: number): number | null {
  if (!Number.isFinite(used) || !Number.isFinite(window)) return null
  if (window <= 0 || used <= 0) return null
  return Math.min(100, Math.round((used / window) * 100))
}
