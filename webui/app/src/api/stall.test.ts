import assert from 'node:assert/strict'
import { test } from 'node:test'
import { isTaskStalled, STALL_ACTIVE_MS, STALL_QUEUED_MS } from './client.ts'
import type { Task } from './client.ts'

const NOW = 1_800_000_000_000 // fixed "now" so ages are deterministic

const task = (state: string, ageMs: number, extra: Partial<Task> = {}): Task => ({
  id: 't-1',
  parent_id: '',
  project: '',
  title: 'work',
  state,
  owner: 'node-a',
  attempt_id: '',
  created_at: new Date(NOW - ageMs - 1000).toISOString(),
  updated_at: new Date(NOW - ageMs).toISOString(),
  ...extra,
})

test('queued task past the queue threshold is stalled', () => {
  assert.equal(isTaskStalled(task('queued', STALL_QUEUED_MS + 1), NOW), true)
})

test('queued task inside the threshold is not stalled', () => {
  assert.equal(isTaskStalled(task('queued', STALL_QUEUED_MS - 1), NOW), false)
})

test('submitted task without plan metadata stalls like a queued task', () => {
  assert.equal(isTaskStalled(task('submitted', STALL_QUEUED_MS + 1), NOW), true)
})

test('submitted plan stage waiting on its graph is not a stall', () => {
  const stage = task('submitted', STALL_QUEUED_MS * 10, {
    plan_meta: { plan_id: 'p', stage_id: 'b', needs: 'a' },
  })
  assert.equal(isTaskStalled(stage, NOW), false)
})

test('active states use the longer execution threshold', () => {
  for (const state of ['dispatched', 'waiting_context', 'running']) {
    assert.equal(
      isTaskStalled(task(state, STALL_QUEUED_MS + 1), NOW),
      false,
      `${state} should survive past the queue threshold`,
    )
    assert.equal(
      isTaskStalled(task(state, STALL_ACTIVE_MS + 1), NOW),
      true,
      `${state} should stall past the active threshold`,
    )
  }
})

test('review and terminal states are never stalled', () => {
  for (const state of ['review', 'done', 'failed', 'cancelled', 'expired']) {
    assert.equal(isTaskStalled(task(state, STALL_ACTIVE_MS * 10), NOW), false, `${state} must not be flagged`)
  }
})

test('missing or invalid timestamp is not stalled', () => {
  assert.equal(isTaskStalled(task('queued', 0, { updated_at: '' }), NOW), false)
  assert.equal(isTaskStalled(task('queued', 0, { updated_at: 'not-a-date' }), NOW), false)
})
