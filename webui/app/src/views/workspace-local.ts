// SPDX-License-Identifier: AGPL-3.0-or-later

import type { NodeInfo, SelfInfo } from '../api/client'
import { isNodeConnected } from '../format/node-status.ts'

/** Is this directory row the machine this console runs on?
 *
 *  The server answers this outright for the row it recognizes (`is_local`),
 *  but the map also has to place itself before its own heartbeat has filed a
 *  row — and a row can be filed under a spelling this console does not know.
 *  The fallback below fills that gap, and it is deliberately narrow: a vm
 *  node is *meant* to share its host's display name (`RuntimeNodeID` appends
 *  `@vm-<hash>` so only the runtime id tells them apart), so a name-only
 *  match paints every sibling as 本机. Same box means same kind and the same
 *  machine identity — a row that disagrees on either is a sibling, not us. */
export function isLocal(self: SelfInfo | null, n: NodeInfo): boolean {
  if (n.is_local) return true
  if (!self) return false
  if (self.node_id && self.node_id === n.id) return true
  if (self.node && self.node.id === n.id) return true
  const name = self.node_name || self.hostname
  if (!name || (n.name !== name && n.id !== name)) return false
  // The name matches; the box still has to. A vm sibling declares its own
  // identity (or none at all, which `RuntimeNodeID` turns into `name@vm`),
  // never this machine's fingerprint.
  if (self.node_kind && n.node_kind && self.node_kind !== n.node_kind) return false
  if (self.node_identity && n.node_identity && self.node_identity !== n.node_identity) return false
  return true
}

/** One panel per box, not per directory row.
 *
 *  Two rows can describe one machine: a rename leaves the old id behind until
 *  the next registration reconciles it, and a row can outlive the identity it
 *  was filed under. The map keys on (kind, identity) — the hardware fact — and
 *  keeps the row that is local first, then online, then the one still running.
 *  A row with no real fingerprint (an older peer reports none, and the ledger
 *  fills the column with the id itself) proves nothing, so it is never merged:
 *  two unrelated machines must not collapse into one panel. */
export function dedupeNodes(nodes: NodeInfo[]): NodeInfo[] {
  const best = new Map<string, NodeInfo>()
  const out: NodeInfo[] = []
  for (const n of nodes) {
    const key = n.node_identity && n.node_identity !== n.id ? `${n.node_kind ?? ''}|${n.node_identity}` : ''
    if (!key) {
      out.push(n)
      continue
    }
    const prev = best.get(key)
    if (!prev) {
      best.set(key, n)
      out.push(n)
      continue
    }
    if (rank(n) > rank(prev)) {
      const at = out.indexOf(prev)
      if (at >= 0) out[at] = n
      best.set(key, n)
    }
  }
  return out
}

/** Which of two rows for the same box the map should draw. Local outranks
 *  everything: the panel you are reading is the one machine whose state the
 *  console knows first-hand. */
function rank(n: NodeInfo): number {
  return (n.is_local ? 4 : 0) + (isNodeConnected(n.status) ? 2 : 0) + (n.running ? 1 : 0)
}
