// SPDX-License-Identifier: AGPL-3.0-or-later

/** Client-side mirror of config.ValidatePeerAddr. The server remains the
 *  authority; this only keeps the add/edit form from submitting an address
 *  the daemon will reject, so the error lands under the field instead of in a
 *  toast after a round-trip.
 *
 *  Accepted: host:port, [v6]:port, ws://…/wss://…, punch:<id>. Bare hosts are
 *  rejected — the daemon dials a port, and guessing 7836 would hide a typo. */
export function isPeerAddr(value: string): boolean {
  const s = value.trim()
  if (s === '') return false

  if (s.startsWith('punch:')) {
    return /^\S+$/.test(s.slice('punch:'.length))
  }

  if (s.includes('://')) {
    try {
      const u = new URL(s)
      return (u.protocol === 'ws:' || u.protocol === 'wss:') && u.hostname !== ''
    } catch {
      return false
    }
  }

  const v6 = /^\[([0-9a-fA-F:.]+)\]:(\d{1,5})$/.exec(s)
  if (v6?.[2]) return validPort(v6[2])

  const hostPort = /^([^\s:/?#]+):(\d{1,5})$/.exec(s)
  return hostPort?.[2] !== undefined && validPort(hostPort[2])
}

function validPort(raw: string): boolean {
  const port = Number(raw)
  return Number.isInteger(port) && port > 0 && port <= 65535
}
