// Session-stream race guard, tested apart from the component.
//
// Run with `npm test` (node's built-in runner). A reply streams into
// whichever thread started it; the moment the user switches threads, that
// reply's writes must stop reaching the pane. The transcript the panel
// persists is the eventual truth — send()'s finally refetch reconciles it —
// so dropping the writes is safe. This predicate is the whole decision.

/** True when a write belonging to session `sid` may render against the
 *  currently active thread. A missing sid or a missing active thread never
 *  matches: an unaddressed write has no pane to land in. */
export function isLiveSession(
  sid: string | null | undefined,
  activeId: string | null | undefined,
): boolean {
  return typeof sid === 'string' && sid !== '' && sid === activeId
}
