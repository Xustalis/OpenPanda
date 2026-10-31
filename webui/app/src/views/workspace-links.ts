// SPDX-License-Identifier: AGPL-3.0-or-later

/** The device facts the canvas needs to infer a connection. */
export interface LinkDevice {
  id: string
  local: boolean
  neighbors: string[]
  rtt: number
  transport: string
}

/** One connection on the canvas. `mesh` means at least one endpoint advertises
 *  the other in its routing adjacency; `measured` is the local console's live
 *  RTT sample toward a peer that has not advertised that route back. */
export interface WorkspaceEdge {
  a: string
  b: string
  kind: 'mesh' | 'measured'
  rtt: number
  transport: string
}

/** Build the fleet's undirected edge set. Two declarations of the same pair
 *  become one line; a local measurement enriches that line rather than
 *  drawing a second, slightly different connection over it. */
export function buildWorkspaceEdges(devices: LinkDevice[]): WorkspaceEdge[] {
  const known = new Map(devices.map((d) => [d.id, d]))
  const out = new Map<string, WorkspaceEdge>()
  const key = (a: string, b: string) => [a, b].sort().join('\u0000')
  const put = (a: string, b: string, kind: WorkspaceEdge['kind'], rtt: number, transport: string) => {
    const k = key(a, b)
    const prev = out.get(k)
    if (!prev) {
      out.set(k, { a, b, kind, rtt, transport })
      return
    }
    if (kind === 'mesh') prev.kind = 'mesh'
    if (rtt > 0 && prev.rtt === 0) {
      prev.rtt = rtt
      prev.transport = transport
    }
  }
  for (const d of devices) {
    for (const nb of d.neighbors) {
      if (nb === d.id || !known.has(nb)) continue
      put(d.id, nb, 'mesh', 0, '')
    }
  }
  const local = devices.find((d) => d.local)
  if (local) {
    for (const d of devices) {
      if (d.id === local.id || d.rtt <= 0) continue
      put(local.id, d.id, 'measured', d.rtt, d.transport)
    }
  }
  return [...out.values()]
}

/** A card rectangle in the grid's own coordinate system. */
export interface PanelRect {
  x: number
  y: number
  w: number
  h: number
}

export interface WorkspaceLinkLine {
  x1: number
  y1: number
  x2: number
  y2: number
  /** The card this connection has to pass over, if any. The edge still
   *  exists — a measured link does not stop being real because a machine is
   *  drawn in the way — so the caller draws it de-emphasised and names the
   *  blocker instead of dropping it. */
  blockedBy?: PanelRect
}

const LINK_GAP = 2
/** Cards have rounded corners, so a ray that meets one almost exactly at the
 *  corner looks like it is touching the shadow instead of the card. Keep the
 *  endpoint this far along the edge it lands on — enough to clear the radius,
 *  small enough that a short edge still gets a sensible anchor. */
const CORNER = 26

/** Intersect the centre-to-centre ray with a card's boundary, then step just
 *  outside it. The panels sit only one grid gap apart, so a larger inset would
 *  leave the two endpoint dots almost touching and the line unreadable. */
function linkAnchor(from: PanelRect, toward: PanelRect): { x: number; y: number } {
  const cx = from.x + from.w / 2
  const cy = from.y + from.h / 2
  const dx = toward.x + toward.w / 2 - cx
  const dy = toward.y + toward.h / 2 - cy
  const tx = dx === 0 ? Infinity : (from.w / 2 + LINK_GAP) / Math.abs(dx)
  const ty = dy === 0 ? Infinity : (from.h / 2 + LINK_GAP) / Math.abs(dy)
  const t = Math.min(tx, ty)
  let x = cx + dx * t
  let y = cy + dy * t
  // Which edge was hit decides which coordinate is free to slide: a side hit
  // can only move vertically, a top/bottom hit only horizontally.
  if (tx <= ty) {
    const r = Math.min(CORNER, from.h / 2)
    y = Math.min(Math.max(y, from.y + r), from.y + from.h - r)
  } else {
    const r = Math.min(CORNER, from.w / 2)
    x = Math.min(Math.max(x, from.x + r), from.x + from.w - r)
  }
  return { x, y }
}

/** Grow a rect by `pad` on every side. */
function inflate(r: PanelRect, pad: number): PanelRect {
  return { x: r.x - pad, y: r.y - pad, w: r.w + pad * 2, h: r.h + pad * 2 }
}

/** Do two rects share any area? Touching edges do not count — the grid keeps
 *  its cards one gap apart and that boundary must stay drawable. */
function overlaps(a: PanelRect, b: PanelRect): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

/** Does the segment pass through `r`, inflated by `pad` on every side?
 *  Liang–Barsky: clip the segment's parameter range against the four edges
 *  and see whether anything survives. */
export function crossesRect(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  r: PanelRect,
  pad: number,
): boolean {
  const dx = x2 - x1
  const dy = y2 - y1
  let t0 = 0
  let t1 = 1
  const clip = (p: number, q: number) => {
    if (p === 0) return q >= 0
    const t = q / p
    if (p < 0) {
      if (t > t1) return false
      if (t > t0) t0 = t
    } else {
      if (t < t0) return false
      if (t < t1) t1 = t
    }
    return true
  }
  return (
    clip(-dx, x1 - (r.x - pad)) &&
    clip(dx, r.x + r.w + pad - x1) &&
    clip(-dy, y1 - (r.y - pad)) &&
    clip(dy, r.y + r.h + pad - y1)
  )
}

/** The visible part of a card-to-card connection.
 *
 *  Two cards closer than two gaps have no corridor at all: each anchor sits
 *  LINK_GAP outside its own card, which puts it *inside* the other one, and
 *  the line then runs backwards through both corners. Overlapping cards were
 *  already refused; the inflated test also catches the near miss (a gap under
 *  2×LINK_GAP) that the fixed inset used to create. Those pairs get no line —
 *  there is nothing honest to draw.
 *
 *  A *third* card in the way is different: the connection is real, so it is
 *  still returned, marked `blockedBy`, and the caller renders it de-emphasised
 *  with the blocker named. `others` is the whole card set — the two endpoints
 *  are skipped by identity, so a caller can hand over every rect it has. */
export function workspaceLinkLine(
  a: PanelRect,
  b: PanelRect,
  others: Iterable<PanelRect> = [],
): WorkspaceLinkLine | null {
  if (overlaps(inflate(a, LINK_GAP), inflate(b, LINK_GAP))) return null
  const dx = b.x + b.w / 2 - (a.x + a.w / 2)
  const dy = b.y + b.h / 2 - (a.y + a.h / 2)
  if (dx === 0 && dy === 0) return null
  const start = linkAnchor(a, b)
  const end = linkAnchor(b, a)
  if ((end.x - start.x) * dx + (end.y - start.y) * dy <= 0) return null
  let blockedBy: PanelRect | undefined
  for (const o of others) {
    if (o === a || o === b) continue
    // Inflate by the same gap the anchors keep: a line threading a one-gap
    // corridor between two cards still reads as crossing them.
    if (crossesRect(start.x, start.y, end.x, end.y, o, LINK_GAP)) {
      blockedBy = o
      break
    }
  }
  const line = { x1: start.x, y1: start.y, x2: end.x, y2: end.y }
  return blockedBy ? { ...line, blockedBy } : line
}
