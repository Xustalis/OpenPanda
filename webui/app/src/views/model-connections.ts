// SPDX-License-Identifier: AGPL-3.0-or-later

// The model page's DOM-free half: how rows collapse into "connections", and
// the small helpers every card shares. It lives beside the view rather than in
// it because the view is JSX and `node --test` only loads .ts — these are the
// parts worth pinning, and none of them touch the document.

import type { ModelEntry, ModelRefWire, ModelsResponse, ProviderInfo } from '../api/client'
import { t } from '../i18n/index.ts'

export interface Member {
  entry: ModelEntry
  /** False for the live model when it was configured outside the registry —
   *  a single-model config an older build wrote. It is still a row: it can be
   *  tested, edited, and deleted (both homes are addressable), only "copy"
   *  stays with registry rows because a duplicate needs a row to duplicate. */
  registry: boolean
}

export interface Conn {
  id: string
  provider: string
  providerLabel: string
  /** The tool this connection's rows were imported from, when they agree on
   *  one. It is provenance, not routing: the card is named after it so a
   *  relay the person never typed does not read as "custom base_url". */
  source: string
  baseURL: string
  apiType: string
  keyHint: string
  keySet: boolean
  noAuth: boolean
  /** The endpoint's plaintext opt-in travels with the row: extending a
   *  plaintext relay must not silently submit allow_plaintext: false, which
   *  the guard reads as a refusal. */
  allowPlaintext: boolean
  members: Member[]
}

/** The address of one row on the wire: the alias from the path, narrowed by the
 *  three fields a legacy config can leave two rows sharing. `key` is the
 *  fingerprint the list handed out — never the secret — and it is the only one
 *  of the four that two accounts of a vendor do not share. */
export function refOf(e: ModelEntry): { model: string; base_url: string; key?: string } {
  return { model: e.model, base_url: e.base_url, key: e.key_fingerprint }
}

/** The row's identity in the DOM: React keys and per-row busy flags. Rows can
 *  share an alias (a config written before names were kept unique), so the key
 *  digest is part of it — without that, two tiles collided on one key and the
 *  second one's spinner landed on the first. */
export function keyOf(e: ModelEntry): string {
  return `${e.alias}\u0000${e.model}\u0000${e.base_url}\u0000${e.key_fingerprint ?? ''}`
}

export function connIdOf(e: ModelEntry): string {
  // Group by the key digest, not its last four characters: two accounts of
  // one vendor can share a suffix, and merging them makes "extend this
  // connection" borrow the other account's key. Older panels do not send
  // the digest, so key_hint stays as the fallback.
  return [e.provider ?? '', e.base_url, e.api_type, e.key_fingerprint ?? e.key_hint ?? ''].join('\u0000')
}

/** The address of every row the card is showing.
 *
 *  Clearing a card is one call, and the call names the rows rather than "this
 *  endpoint": the store keeps a flat list and the grouping is a rendering fact,
 *  so a server-side predicate would be a second, weaker notion of which rows
 *  belong together than the one the card was built from — and one endpoint can
 *  legitimately hold two accounts. A live row that was configured outside the
 *  registry is included, because it is a row on screen too. */
export function connRefs(c: Conn): ModelRefWire[] {
  return c.members.map((m) => ({ alias: m.entry.alias, ...refOf(m.entry) }))
}

export function labelOf(providers: ProviderInfo[], id?: string): string {
  if (!id) return ''
  return providers.find((p) => p.id === id)?.label ?? id
}

/** The discovery collectors that can label a connection. Anything else (a
 *  hand-written row, or a value a newer panel invented) is not a source the
 *  card knows how to name. */
const CONN_SOURCES = new Set(['codex', 'claude', 'opencode', 'local'])

/** The card's heading.
 *
 *  A known vendor names itself ("ChatGPT (OpenAI)", "通义千问 (DashScope)"),
 *  which is the best a card can do. A row imported from another tool is named
 *  after that tool: the person never chose the address a CLI config carried,
 *  so "自定义 / 中转站" plus a loopback port describes the plumbing instead of
 *  the thing they imported. Only a row nobody can place keeps the generic
 *  label — and even then it is the short one, because the catalogue's own
 *  "自定义 / 中转站 (custom base_url)" wraps to two lines in a 232px card. */
export function connName(c: Conn, providers: ProviderInfo[]): string {
  const known = providers.find((p) => p.id === c.provider)
  if (known && c.provider !== 'custom') return known.label
  if (CONN_SOURCES.has(c.source)) return t(`models.discover.source.${c.source}`)
  return t('models.customProvider')
}

/** Whether the card should say it was imported. Provenance is the one thing
 *  the heading cannot show once a known vendor names itself: "ChatGPT (OpenAI)"
 *  is a brand, and the person still deserves to know the row came from their
 *  own Codex login rather than from a key typed into this page. */
export function connImported(c: Conn): boolean {
  return CONN_SOURCES.has(c.source)
}

/** Whether a base URL points at this machine. Same rule the daemon applies to
 *  its own loopback rows, kept local so the card can decide without a request:
 *  a port on 127.0.0.1 is plumbing, not an address a person recognises. */
export function isLoopbackURL(url: string): boolean {
  try {
    const host = new URL(url.trim()).hostname.toLowerCase()
    return host === 'localhost' || host === '::1' || host === '[::1]' || host.startsWith('127.')
  } catch {
    return false
  }
}

/** The line under the heading: the host a person could recognise, or the word
 *  for "this is on your own machine". A loopback port is deliberately not
 *  printed — `127.0.0.1:7896` tells the reader nothing they can act on, and it
 *  is the one piece of a relay's inner wiring this page should not leak. */
export function connHost(c: Conn): string {
  if (isLoopbackURL(c.baseURL)) return t('models.localEndpoint')
  return hostOf(c.baseURL)
}

/** The host part of an endpoint, for tooltips where the scheme and the path
 *  are noise. An unparsable address is printed as-is — a half-typed one is
 *  exactly what a reader needs to see. */
export function hostOf(url: string): string {
  const raw = url.trim()
  if (!raw) return ''
  try {
    return new URL(raw).host
  } catch {
    return raw
  }
}

/** A free alias for a duplicate: `name-2`, `name-3`, … The registry keys on
 *  the name, so a copy that kept it would be refused; a numeric suffix is the
 *  one thing a reader can predict without looking it up. */
export function copyAlias(taken: string[], alias: string): string {
  const used = new Set(taken)
  // The source always holds its own name, so the search starts at -2.
  for (let n = 2; n < 1000; n++) {
    const candidate = `${alias}-${n}`
    if (!used.has(candidate)) return candidate
  }
  return `${alias}-copy`
}

export function groupConnections(data: ModelsResponse): Conn[] {
  const members: Member[] = data.models.map((m) => ({ entry: m, registry: true }))
  // A live model configured outside the registry (the old single-model form
  // wrote one) still belongs on the page — it is the row in use. When the
  // registry already holds it, the registry row is the one to paint, because
  // that is the row a delete addresses.
  const activeKey = keyOf(data.active)
  const registered = data.models.some((m) => keyOf(m) === activeKey)
  if (!registered && (data.active.base_url || data.active.model)) {
    members.push({ entry: data.active, registry: false })
  }

  const byId = new Map<string, Conn>()
  for (const m of members) {
    const id = connIdOf(m.entry)
    let conn = byId.get(id)
    if (!conn) {
      conn = {
        id,
        provider: m.entry.provider ?? '',
        providerLabel: m.entry.provider_label ?? m.entry.provider ?? '',
        source: m.entry.source ?? '',
        baseURL: m.entry.base_url,
        apiType: m.entry.api_type,
        keyHint: m.entry.key_hint ?? '',
        keySet: m.entry.key_set,
        noAuth: m.entry.no_auth ?? false,
        allowPlaintext: m.entry.allow_plaintext ?? false,
        members: [],
      }
      byId.set(id, conn)
    }
    if (!conn.members.some((x) => keyOf(x.entry) === keyOf(m.entry))) {
      conn.members.push(m)
    }
    // One imported row is enough to say where the account came from: a row the
    // person added by hand to the same account does not erase that.
    if (!conn.source && m.entry.source) conn.source = m.entry.source
  }
  return [...byId.values()].sort((a, b) => {
    const aActive = a.members.some((m) => m.entry.active)
    const bActive = b.members.some((m) => m.entry.active)
    if (aActive !== bActive) return aActive ? -1 : 1
    return (a.providerLabel || a.baseURL).localeCompare(b.providerLabel || b.baseURL)
  })
}
