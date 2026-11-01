// SPDX-License-Identifier: AGPL-3.0-or-later

// The model page's DOM-free half: how rows collapse into "connections", and
// the small helpers every card shares. It lives beside the view rather than in
// it because the view is JSX and `node --test` only loads .ts — these are the
// parts worth pinning, and none of them touch the document.

import type { ModelEntry, ModelsResponse, ProviderInfo } from '../api/client'

export interface Member {
  entry: ModelEntry
  /** False for the live model when it was configured outside the registry —
   *  it can be edited and tested, but there is no row to remove. */
  registry: boolean
}

export interface Conn {
  id: string
  provider: string
  providerLabel: string
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

export function refOf(e: ModelEntry): { model: string; base_url: string } {
  return { model: e.model, base_url: e.base_url }
}

export function keyOf(e: ModelEntry): string {
  return `${e.alias}\u0000${e.model}\u0000${e.base_url}`
}

export function connIdOf(e: ModelEntry): string {
  // Group by the key digest, not its last four characters: two accounts of
  // one vendor can share a suffix, and merging them makes "extend this
  // connection" borrow the other account's key. Older panels do not send
  // the digest, so key_hint stays as the fallback.
  return [e.provider ?? '', e.base_url, e.api_type, e.key_fingerprint ?? e.key_hint ?? ''].join('\u0000')
}

export function labelOf(providers: ProviderInfo[], id?: string): string {
  if (!id) return ''
  return providers.find((p) => p.id === id)?.label ?? id
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
  }
  return [...byId.values()].sort((a, b) => {
    const aActive = a.members.some((m) => m.entry.active)
    const bActive = b.members.some((m) => m.entry.active)
    if (aActive !== bActive) return aActive ? -1 : 1
    return (a.providerLabel || a.baseURL).localeCompare(b.providerLabel || b.baseURL)
  })
}
