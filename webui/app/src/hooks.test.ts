// The unified event bus, tested apart from Preact.
//
// Run with `npm test` (node's built-in runner). These guard the regression
// the bus was built to kill: one shared /api/events?trace=1 fetch-SSE
// connection for the whole app, reference-counted subscribers, a grace
// period before the last subscriber really closes it, and automatic
// reconnects that fan out a synthetic "reconnect" change event.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { TestContext } from 'node:test'
import {
  __busState,
  BUS_GRACE_MS,
  busSubscribeChange,
  busSubscribeTrace,
} from './hooks.ts'
import { clearToken, setToken, type ChangeEvent, type TraceEvent } from './api/client.ts'

const encoder = new TextEncoder()

interface MockStream {
  /** Enqueue one raw SSE frame (`event: …\ndata: …\n\n`). */
  push(frame: string): void
  /** End the stream cleanly (server close). */
  close(): void
}

interface FetchMock {
  calls: Array<{ url: string; headers: Record<string, string> }>
  streams: MockStream[]
}

/** Replace globalThis.fetch with a hand-puppet SSE server. Every call is
 *  recorded and answered with a 200 whose body is a stream the test drives. */
function installSSEFetch(): { mock: FetchMock; restore(): void } {
  const original = globalThis.fetch
  const mock: FetchMock = { calls: [], streams: [] }
  globalThis.fetch = (async (input: any, init?: any) => {
    mock.calls.push({
      url: String(input),
      headers: { ...(init?.headers ?? {}) },
    })
    let ctrl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        ctrl = c
      },
    })
    mock.streams.push({
      push(frame) {
        ctrl.enqueue(encoder.encode(frame))
      },
      close() {
        try {
          ctrl.close()
        } catch {
          // already closed/cancelled — a race the bus must survive
        }
      },
    })
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
  }) as typeof fetch
  return { mock, restore: () => (globalThis.fetch = original) }
}

/** Let pending microtasks settle. */
async function flush(times = 20): Promise<void> {
  for (let i = 0; i < times; i++) await Promise.resolve()
}

/** Shared scaffolding: a fake fetch, a registry of every subscription the
 *  test opens, and a teardown that always leaves the bus closed — a leaking
 *  busStop would silently reuse the previous test's connection. */
function setup(t: TestContext) {
  const { mock, restore } = installSSEFetch()
  const offs: Array<() => void> = []
  const subscribeChange = (fn: (ev: ChangeEvent) => void) => {
    const off = busSubscribeChange(fn)
    offs.push(off)
    return off
  }
  const subscribeTrace = (fn: (ev: TraceEvent) => void) => {
    const off = busSubscribeTrace(fn)
    offs.push(off)
    return off
  }
  t.after(async () => {
    while (offs.length) offs.pop()!()
    t.mock.timers.tick(BUS_GRACE_MS + 100)
    await flush()
    restore()
    clearToken()
  })
  return { mock, subscribeChange, subscribeTrace }
}

test('one shared connection for many change + trace subscribers, token in header never URL', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange, subscribeTrace } = setup(t)
  setToken('sekrit')

  subscribeChange(() => {})
  subscribeChange(() => {})
  subscribeTrace(() => {})
  await flush()

  assert.equal(mock.calls.length, 1, 'exactly one connection for three subscribers')
  assert.equal(__busState().open, true)
  assert.equal(__busState().change, 2)
  assert.equal(__busState().trace, 1)

  const call = mock.calls[0]!
  assert.equal(call.headers.Authorization, 'Bearer sekrit')
  assert.ok(!call.url.includes('token='), 'the token must never ride in the URL')
  assert.ok(call.url.includes('trace=1'), 'the shared bus opts into trace frames')
})

test('reference counting: unsubscribing one of many never touches the link', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange, subscribeTrace } = setup(t)

  const off1 = subscribeChange(() => {})
  const off2 = subscribeChange(() => {})
  const off3 = subscribeTrace(() => {})
  await flush()
  assert.equal(mock.calls.length, 1)

  off1()
  off3()
  await flush()
  assert.equal(__busState().open, true, 'the link survives while one subscriber remains')
  assert.equal(mock.calls.length, 1, 'no reconnect churn on unsubscribe')
  off2()
  await flush()
  assert.equal(__busState().open, true, 'the grace period delays the real close')
})

test('the connection really closes only after the grace period', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange } = setup(t)

  const off = subscribeChange(() => {})
  await flush()
  off()
  await flush()

  // Immediately after the last unsubscribe the link is still up.
  assert.equal(__busState().open, true)
  assert.equal(mock.calls.length, 1)

  // Just before the deadline: still up.
  t.mock.timers.tick(BUS_GRACE_MS - 10)
  assert.equal(__busState().open, true)

  // Past it: closed, and closing never opens a replacement connection.
  t.mock.timers.tick(1000)
  await flush()
  assert.equal(__busState().open, false)
  assert.equal(mock.calls.length, 1)
})

test('re-subscribing during the grace period reuses the live connection', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange, subscribeTrace } = setup(t)

  const off = subscribeChange(() => {})
  await flush()
  off()
  t.mock.timers.tick(BUS_GRACE_MS / 3)
  assert.equal(__busState().open, true, 'grace period keeps the link alive')

  subscribeTrace(() => {})
  await flush()
  t.mock.timers.tick(BUS_GRACE_MS * 2)
  await flush()

  assert.equal(mock.calls.length, 1, 'the existing connection is reused, not re-dialed')
  assert.equal(__busState().open, true, 'the new subscriber cancels the shutdown')
})

test('a dropped stream reconnects and fans out the synthetic reconnect event', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange } = setup(t)

  const changes: ChangeEvent[] = []
  subscribeChange((ev) => changes.push(ev))
  await flush()
  assert.equal(mock.calls.length, 1)

  // Server closes the stream: the bus must re-dial on its own (backoff 1s).
  mock.streams[0]!.close()
  await flush()
  t.mock.timers.tick(1_100)
  await flush()

  assert.equal(mock.calls.length, 2, 'the bus reconnected')
  assert.equal(__busState().open, true)
  assert.ok(
    changes.some((ev) => ev.kinds.includes('reconnect')),
    'subscribers get the synthetic reconnect event so they can refetch',
  )
})

test('change and trace frames fan out to the right listener sets', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { mock, subscribeChange, subscribeTrace } = setup(t)

  const changes: ChangeEvent[] = []
  const traces: TraceEvent[] = []
  subscribeChange((ev) => changes.push(ev))
  subscribeTrace((ev) => traces.push(ev))
  await flush()
  assert.equal(mock.calls.length, 1)

  const s = mock.streams[0]!
  s.push('event: change\ndata: tasks,nodes abc/def/9\n\n')
  s.push('event: trace\ndata: {"id":7,"ts":123,"task_id":"t_1","type":"route_decision","data":{"x":1}}\n\n')
  await flush()

  assert.equal(changes.length, 1)
  assert.deepEqual(changes[0]!.kinds, ['tasks', 'nodes'])
  assert.equal(changes[0]!.taskFP, 'abc')
  assert.equal(changes[0]!.nodeFP, 'def')
  assert.equal(changes[0]!.reminderFP, '9')

  assert.equal(traces.length, 1)
  assert.equal(traces[0]!.id, 7)
  assert.equal(traces[0]!.task_id, 't_1')
  assert.equal(traces[0]!.type, 'route_decision')
  assert.deepEqual(traces[0]!.data, { x: 1 })
})
