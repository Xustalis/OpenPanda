// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { makeSSEFrameParser, openSSE } from './client.ts'

async function flush(): Promise<void> {
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

function controlledStream() {
  let controller!: ReadableStreamDefaultController<Uint8Array>
  let cancelled = false
  const body = new ReadableStream<Uint8Array>({
    start(c) { controller = c },
    cancel() { cancelled = true },
  })
  return {
    body,
    push(text: string) { controller.enqueue(new TextEncoder().encode(text)) },
    close() { if (!cancelled) controller.close() },
    get cancelled() { return cancelled },
  }
}

test('disabling the SSE watchdog also disables rearming after a chunk', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const original = globalThis.fetch
  const stream = controlledStream()
  globalThis.fetch = async () => new Response(stream.body)
  const ctrl = new AbortController()
  const seen: string[] = []
  const done = openSSE('/events', {
    signal: ctrl.signal, idleTimeoutMs: 0, onEvent: (_, data) => seen.push(data),
  }).then(() => null, (err: unknown) => err)
  try {
    stream.push('data: first\n\n')
    await flush()
    t.mock.timers.tick(1000)
    await flush()
    assert.equal(stream.cancelled, false, 'a disabled watchdog must not close a healthy stream')
    stream.push('data: second\n\n')
    await flush()
    assert.deepEqual(seen, ['first', 'second'])
    stream.close()
    assert.equal(await done, null)
  } finally {
    ctrl.abort()
    await done
    globalThis.fetch = original
  }
})

test('an abort from onOpen ends a silent SSE stream immediately', async () => {
  const original = globalThis.fetch
  const stream = controlledStream()
  globalThis.fetch = async () => new Response(stream.body)
  const ctrl = new AbortController()
  let settled = false
  const done = openSSE('/events', {
    signal: ctrl.signal, idleTimeoutMs: 0,
    onOpen: () => ctrl.abort(), onEvent: () => assert.fail('aborted stream delivered a frame'),
  }).then(() => { settled = true })
  try {
    await flush()
    assert.equal(stream.cancelled, true, 'the abort must reach the body even during onOpen')
    assert.equal(settled, true)
  } finally {
    stream.close()
    await done
    globalThis.fetch = original
  }
})

for (const callback of ['onOpen', 'onActivity', 'onEvent'] as const) {
  test('SSE cancels and unlocks the body when ' + callback + ' throws', async () => {
    const original = globalThis.fetch
    const stream = controlledStream()
    globalThis.fetch = async () => new Response(stream.body)
    const failure = new Error('subscriber failed')
    try {
      stream.push('data: value\n\n')
      await assert.rejects(openSSE('/events', {
        idleTimeoutMs: 0, onEvent: () => {}, [callback]: () => { throw failure },
      }), (err: unknown) => err === failure)
      assert.equal(stream.cancelled, true, 'a failed consumer must not leave the network body running')
      assert.equal(stream.body.locked, false, 'the reader lock must always be released')
    } finally {
      stream.close()
      globalThis.fetch = original
    }
  })
}

test('SSE treats an AbortError from a pending body read as a clean stop', async () => {
  const original = globalThis.fetch
  const ctrl = new AbortController()
  let controller!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({ start(c) { controller = c } })
  ctrl.signal.addEventListener('abort', () => controller.error(new DOMException('stopped', 'AbortError')))
  globalThis.fetch = async () => new Response(body)
  const done = openSSE('/events', { signal: ctrl.signal, idleTimeoutMs: 0, onEvent: () => {} })
  try {
    await flush()
    ctrl.abort()
    await assert.doesNotReject(done)
    assert.equal(body.locked, false)
  } finally {
    globalThis.fetch = original
  }
})

test('SSE parsing accepts every standard line ending even across chunk boundaries', () => {
  for (const ending of ['\n', '\r\n', '\r']) {
    const seen: [string, string][] = []
    const parser = makeSSEFrameParser((event, data) => seen.push([event, data]))
    const frame = ['\uFEFF: heartbeat', 'event:update', 'data: 一', 'data:two', '', 'data', '', 'event:ignored', '', 'data: final', '', ''].join(ending)
    for (const char of frame) parser.push(char)
    assert.deepEqual(seen, [['update', '一\ntwo'], ['message', ''], ['message', 'final']], JSON.stringify(ending))
  }
})

test('SSE keeps one optional field space, empty data, and incomplete frames intact', () => {
  const seen: [string, string][] = []
  const parser = makeSSEFrameParser((event, data) => seen.push([event, data]))
  parser.push('event:  custom \ndata:  leading space\ndata:\n\n')
  parser.push('event: lost\ndata: incomplete')
  assert.deepEqual(seen, [[' custom ', ' leading space\n']])
})
