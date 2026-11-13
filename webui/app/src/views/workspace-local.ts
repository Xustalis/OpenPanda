// SPDX-License-Identifier: AGPL-3.0-or-later

import type { NodeInfo, SelfInfo } from '../api/client'

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
