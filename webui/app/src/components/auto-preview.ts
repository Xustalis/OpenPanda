// SPDX-License-Identifier: AGPL-3.0-or-later

// When a finished task's page should open in the dock by itself.
//
// The transcript polls the thread's task rows, so "a page just became
// available" is a diff between two polls: an artifact hash that was not in the
// previous snapshot. The first snapshot only sets the baseline — a thread
// reopened after the work finished must not pop a preview for something the
// reader has already seen — and a task that regenerates its site carries a new
// hash, which counts as new again.

/** The part of a task row this scan reads. */
export interface PreviewCandidate {
  id: string
  /** The task's current state. An artifact is recorded when work finishes, but
   *  a row still in flight may already carry one from an earlier attempt;
   *  only a settled row means the page is what the reader is about to get. */
  state: string
  plan_meta?: { output_artifact?: string } | null
}

/** Rows whose work is over and whose output is worth opening: `done` and
 *  `review` (the work is produced and waiting for the reader to accept it).
 *  A failed row may carry a partial artifact, and opening it would present a
 *  half-written page as the result. */
const PREVIEWABLE = new Set(['done', 'review'])

/** One task's artifact identity. Empty when the task has produced nothing,
 *  or when a row predates the artifact columns. */
export function artifactKey(task: PreviewCandidate): string {
  const hash = task.plan_meta?.output_artifact ?? ''
  return hash ? `${task.id}:${hash}` : ''
}

export interface ArtifactScan {
  /** The task whose page should open now, or null when nothing is new. The
   *  task read is newest-first, so the first new row is the newest page; with
   *  several new pages in one poll that is the one the dock opens, because
   *  the dock holds one page and the reader just asked for the latest. */
  next: string | null
  /** The snapshot the following scan compares against. */
  seen: Set<string>
}

/** Diff one poll against the previous snapshot.
 *
 *  Keys are kept after their task drops out of the list. The task read is
 *  windowed, and a row that scrolls out and back is not a new artifact.
 */
export function scanArtifacts(
  seen: Set<string> | null,
  tasks: PreviewCandidate[],
): ArtifactScan {
  const current = new Set<string>()
  let next: string | null = null
  for (const task of tasks) {
    if (!PREVIEWABLE.has(task.state)) continue
    const key = artifactKey(task)
    if (!key) continue
    current.add(key)
    if (seen && !seen.has(key) && next === null) next = task.id
  }
  if (seen === null) return { next: null, seen: current }
  for (const key of seen) current.add(key)
  return { next, seen: current }
}
