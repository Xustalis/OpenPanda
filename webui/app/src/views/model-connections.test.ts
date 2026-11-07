// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { ModelEntry, ModelsResponse } from '../api/client.ts'
import { connIdOf, connRefs, copyAlias, groupConnections, hostOf, keyOf, refOf } from './model-connections.ts'

/** A wire entry with only the fields these tests care about; the rest of the
 *  payload is the panel's and is not read here. */
function entry(over: Partial<ModelEntry>): ModelEntry {
  return {
    alias: 'relay',
    model: 'm-1',
    base_url: 'http://127.0.0.1:3065/v1',
    api_type: 'openai',
    key_set: true,
    active: false,
    ...over,
  } as ModelEntry
}

function response(models: ModelEntry[], active?: ModelEntry): ModelsResponse {
  return { models, active: active ?? models[0], providers: [] } as ModelsResponse
}

test('two accounts of one vendor are different connections', () => {
  // Same provider, same endpoint, same last four characters — only the key
  // differs. Grouping them together made "extend this connection" borrow the
  // other account's key.
  const a = entry({ key_fingerprint: 'aaaa1111', key_hint: '1234' })
  const b = entry({ key_fingerprint: 'bbbb2222', key_hint: '1234' })
  assert.notEqual(connIdOf(a), connIdOf(b))
})

test('an older panel without a digest still groups by key_hint', () => {
  const a = entry({ key_hint: '1234' })
  assert.ok(connIdOf(a).includes('1234'))
})

test('grouping keeps the aliases of one connection together', () => {
  const conns = groupConnections(
    response([
      entry({ alias: 'relay', model: 'm-1' }),
      entry({ alias: 'relay-cheap', model: 'm-2' }),
    ]),
  )
  assert.equal(conns.length, 1)
  assert.equal(conns[0]!.members.length, 2)
  assert.equal(conns[0]!.baseURL, 'http://127.0.0.1:3065/v1')
})

test('the live model configured outside the registry gets its own row', () => {
  const live = entry({ alias: 'live', model: 'x', base_url: 'https://elsewhere.example/v1' })
  const conns = groupConnections(response([entry({ alias: 'relay' })], live))
  assert.equal(conns.length, 2)
  assert.ok(conns.some((c) => c.members.some((m) => !m.registry)))
})

test('a registry entry that is also the live model is not duplicated', () => {
  const live = entry({ alias: 'relay', active: true })
  const conns = groupConnections(response([entry({ alias: 'relay', active: true })], live))
  assert.equal(conns.length, 1)
  assert.equal(conns[0]!.members.length, 1)
})

test('the active connection sorts first', () => {
  const idle = entry({ alias: 'zzz', base_url: 'https://b.example/v1' })
  const active = entry({ alias: 'aaa', base_url: 'https://a.example/v1', active: true })
  const conns = groupConnections(response([idle, active], active))
  assert.equal(conns[0]!.members[0]!.entry.alias, 'aaa')
})

test('copyAlias walks past the suffixes already taken', () => {
  assert.equal(copyAlias([], 'relay'), 'relay-2')
  assert.equal(copyAlias(['relay-2'], 'relay'), 'relay-3')
  assert.equal(copyAlias(['relay-2', 'relay-3'], 'relay'), 'relay-4')
})

test('keyOf distinguishes rows that differ only by model', () => {
  assert.notEqual(keyOf(entry({ model: 'a' })), keyOf(entry({ model: 'b' })))
})

test('keyOf distinguishes two accounts that share every visible field', () => {
  // The alias, the model id and the endpoint are what two accounts of one
  // vendor share; the key digest is the one thing that tells them apart, so it
  // is part of the React key and of the per-row busy flag.
  const work = entry({ key_fingerprint: 'aaaa1111' })
  const home = entry({ key_fingerprint: 'bbbb2222' })
  assert.notEqual(keyOf(work), keyOf(home))
})

test('refOf carries the key digest so one account can be addressed', () => {
  const ref = refOf(entry({ key_fingerprint: 'aaaa1111' }))
  assert.equal(ref.key, 'aaaa1111')
  assert.equal(ref.model, 'm-1')
  // A row with no key must not send an empty narrowing parameter: the server
  // would treat "key=" as no key at all, which is right, but the query string
  // should stay clean.
  assert.equal(refOf(entry({ key_fingerprint: undefined })).key, undefined)
})

test('hostOf prints an unparsable address as typed', () => {
  assert.equal(hostOf('http://127.0.0.1:3065/v1'), '127.0.0.1:3065')
  assert.equal(hostOf('not a url'), 'not a url')
  assert.equal(hostOf('  '), '')
})

test('connRefs addresses every row of the card and nothing else', () => {
  // Clearing a card sends the rows it is showing. The neighbour on another
  // endpoint is one card away and must not be in the list: the server deletes
  // what it is told, so an over-broad address takes a working account with it.
  const conns = groupConnections(
    response([
      entry({ alias: 'relay', model: 'm-1', base_url: 'https://a.example/v1', key_fingerprint: 'aaaa' }),
      entry({ alias: 'relay-2', model: 'm-2', base_url: 'https://a.example/v1', key_fingerprint: 'aaaa' }),
      entry({ alias: 'spare', model: 's', base_url: 'https://b.example/v1', key_fingerprint: 'bbbb' }),
    ]),
  )
  const refs = connRefs(conns.find((c) => c.baseURL === 'https://a.example/v1')!)
  assert.deepEqual(refs, [
    { alias: 'relay', model: 'm-1', base_url: 'https://a.example/v1', key: 'aaaa' },
    { alias: 'relay-2', model: 'm-2', base_url: 'https://a.example/v1', key: 'aaaa' },
  ])
})

test('connRefs includes the live row that has no registry entry', () => {
  // That row is on screen — the page offers it edit and delete — so a card
  // clear that skipped it would leave the card standing with one row in it.
  const live = entry({ alias: 'live', model: 'x', base_url: 'https://elsewhere.example/v1' })
  const conns = groupConnections(response([entry({ alias: 'relay' })], live))
  const refs = connRefs(conns.find((c) => c.baseURL === 'https://elsewhere.example/v1')!)
  assert.deepEqual(
    refs.map((r) => r.alias),
    ['live'],
  )
})
