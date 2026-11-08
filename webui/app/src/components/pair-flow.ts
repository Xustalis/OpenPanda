// SPDX-License-Identifier: AGPL-3.0-or-later

/** The pairing panel's list arithmetic, kept out of the component so it can be
 *  tested without a DOM.
 *
 *  Two sources answer "who can I pair with": the discovery beacon table (a
 *  peer that broadcast, with a name and an optional signature) and the active
 *  /24 sweep (a port that answered, with no identity at all). They were
 *  rendered as two separate lists, which made one device look like two and
 *  left the operator to work out that the anonymous row was the same machine
 *  as the named one above it. They are one list here, ordered by how much is
 *  known about each entry. */

export interface DiscoveredDevice {
  id: string
  addr: string
  verified: boolean
}

export interface ScanCandidate {
  addr: string
}

/** Where a row's evidence came from. `both` is the strong case: a signed
 *  beacon *and* a reachable port, so identity and reachability are separately
 *  confirmed. */
export type CandidateSource = 'beacon' | 'scan' | 'both'

export interface PairCandidate {
  /** Dial target: the beacon's name when one spoke, else the address. */
  target: string
  /** What the row is labelled with. Empty for sweep-only hits. */
  name: string
  /** host:port to display and to dial. */
  addr: string
  verified: boolean
  source: CandidateSource
  /** Stable render key — the host, not the port, so a beacon advertising a
   *  different port than the one the sweep found still yields one row. */
  key: string
}

/** Host part of host:port, or the input unchanged when it does not split.
 *  Bracketed IPv6 literals keep their brackets off: `[fd00::1]:7836` → `fd00::1`. */
export function hostOf(addr: string): string {
  const s = addr.trim()
  if (s.startsWith('[')) {
    const end = s.indexOf(']')
    if (end > 0) return s.slice(1, end)
    return s
  }
  const colon = s.lastIndexOf(':')
  if (colon <= 0) return s
  // More than one colon and no brackets: a bare IPv6 literal, not host:port.
  if (s.indexOf(':') !== colon) return s
  return s.slice(0, colon)
}

/** Numeric key so 192.168.1.9 sorts before 192.168.1.10 (string order gets it
 *  backwards, which reads as a randomly shuffled list). Non-IPv4 sorts last. */
export function ipSortKey(addr: string): number {
  const host = hostOf(addr)
  const parts = host.split('.')
  if (parts.length !== 4) return Number.MAX_SAFE_INTEGER
  let n = 0
  for (const p of parts) {
    if (!/^\d{1,3}$/.test(p)) return Number.MAX_SAFE_INTEGER
    const v = Number(p)
    if (v > 255) return Number.MAX_SAFE_INTEGER
    n = n * 256 + v
  }
  return n
}

/** Merge the beacon table and the sweep into one ordered list.
 *
 *  Order is "most is known first": a beacon that also answered (both) beats a
 *  signed beacon that did not (beacon) beats a bare port (scan), and ties break
 *  on address so the list does not reshuffle between polls. */
export function mergeCandidates(
  discovered: DiscoveredDevice[],
  scanned: ScanCandidate[],
): PairCandidate[] {
  const byHost = new Map<string, PairCandidate>()

  for (const d of discovered) {
    const key = hostOf(d.addr)
    if (!key) continue
    const prev = byHost.get(key)
    const cand: PairCandidate = {
      target: d.id,
      name: d.id,
      addr: d.addr,
      verified: d.verified,
      source: 'beacon',
      key,
    }
    // The beacon is the authority on identity, so it always wins the row; a
    // duplicate beacon for the same host (two frames, one device) keeps the
    // first.
    if (!prev) byHost.set(key, cand)
  }

  for (const s of scanned) {
    const key = hostOf(s.addr)
    if (!key) continue
    const prev = byHost.get(key)
    if (prev) {
      prev.source = 'both'
      continue
    }
    byHost.set(key, {
      target: s.addr,
      name: '',
      addr: s.addr,
      verified: false,
      source: 'scan',
      key,
    })
  }

  const rank: Record<CandidateSource, number> = { both: 0, beacon: 1, scan: 2 }
  return [...byHost.values()].sort((a, b) => {
    const r = rank[a.source] - rank[b.source]
    if (r !== 0) return r
    const ip = ipSortKey(a.addr) - ipSortKey(b.addr)
    if (ip !== 0) return ip
    return a.addr < b.addr ? -1 : a.addr > b.addr ? 1 : 0
  })
}

/** mm:ss left before a pairing request expires, or '' when there is no
 *  meaningful deadline (absent, unparseable, or already past — the row itself
 *  already says "expired", and a negative clock there is noise). */
export function countdown(expiresAt: string | undefined, nowMs: number): string {
  if (!expiresAt) return ''
  const end = Date.parse(expiresAt)
  if (Number.isNaN(end)) return ''
  return formatLeft(Math.floor((end - nowMs) / 1000))
}

/** mm:ss for a number of seconds already measured, '' when none are left.
 *
 *  A pairing request's deadline is the daemon's clock, not the browser's, so
 *  the panel counts from the server's `expires_in` and its own arrival time
 *  rather than parsing the timestamp. A laptop a minute behind would otherwise
 *  offer "0:58 left" on a request that had already been swept. */
export function formatLeft(seconds: number): string {
  const left = Math.floor(seconds)
  if (!Number.isFinite(left) || left <= 0) return ''
  const mm = Math.floor(left / 60)
  const ss = left % 60
  return `${mm}:${String(ss).padStart(2, '0')}`
}

/** Seconds left on a request, counted from the moment this copy arrived.
 *
 *  `expires_in` is authoritative when the server sent it; `expires_at` stays
 *  as the fallback for an older daemon that does not. Both paths go through
 *  the same anchor-and-now arithmetic so the two screens agree to the second. */
export function requestLeft(
  req: { expires_in?: number; expires_at?: string },
  arrivedMs: number,
  nowMs: number,
): number {
  if (typeof req.expires_in === 'number') {
    return req.expires_in - (nowMs - arrivedMs) / 1000
  }
  const end = req.expires_at ? Date.parse(req.expires_at) : NaN
  return Number.isNaN(end) ? 0 : (end - nowMs) / 1000
}
