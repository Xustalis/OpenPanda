// SPDX-License-Identifier: AGPL-3.0-or-later
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { onCompletionAcknowledged } from './goal-dismissal.ts'

test('a completed goal stays visible until its message is accepted', () => {
  const goal = { id: 'goal', generation: 2, rounds: 4, status: 'complete' as const }
  const dismissed: string[] = []
  const accepted = onCompletionAcknowledged(() => goal, key => dismissed.push(key))
  assert.deepEqual(dismissed, [])
  accepted?.()
  assert.deepEqual(dismissed, ['goal:2:4'])
})

test('an old acknowledgement cannot dismiss another completion or a new generation', () => {
  let goal = { id: 'goal', generation: 2, rounds: 4, status: 'complete' as const }
  const dismiss = () => assert.fail('dismissed another completion')
  const earlierRound = onCompletionAcknowledged(() => goal, dismiss)
  goal = { ...goal, rounds: 5 }
  earlierRound?.()
  const earlierGeneration = onCompletionAcknowledged(() => goal, dismiss)
  goal = { ...goal, generation: 3 }
  earlierGeneration?.()
  assert.equal(onCompletionAcknowledged(() => ({ ...goal, status: 'active' }), dismiss), undefined)
})
