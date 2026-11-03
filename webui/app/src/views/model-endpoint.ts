// SPDX-License-Identifier: AGPL-3.0-or-later

/** True when a model endpoint is plain http on a host that is not loopback —
 *  the one shape the network guard refuses without an explicit per-model
 *  opt-in, because the API key is what travels in the clear. Loopback needs no
 *  consent (a local dev model is the case the exemption exists for), and an
 *  https URL never does.
 *
 *  This mirrors security.NetworkGuard's rule; it decides only whether the form
 *  shows the consent checkbox, never whether the request is allowed — the
 *  backend still refuses an unconsented plaintext endpoint. */
export function needsPlaintextConsent(url: string): boolean {
  // A bracketed IPv6 literal has to be matched whole: its colons would
  // otherwise end the host at "[".
  const m = /^http:\/\/(\[[^\]]*\]|[^/:?#]+)/i.exec(url.trim())
  if (!m?.[1]) return false
  const host = m[1].toLowerCase().replace(/^\[|\]$/g, '')
  if (host === 'localhost' || host === '::1') return false
  // Loopback is 127.0.0.0/8 as a dotted quad. "127.example.com" starts with
  // those digits but is a domain, and its key still travels in the clear —
  // treating it as loopback hid the consent box and let the guard refuse the
  // save with no way to opt in.
  const v4 = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host)
  if (v4) {
    const rest = [v4[1], v4[2], v4[3]].map(Number)
    if (rest.every((n) => n >= 0 && n <= 255)) return false
  }
  return true
}
