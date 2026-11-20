// SPDX-License-Identifier: AGPL-3.0-or-later
import test from 'node:test'
import assert from 'node:assert/strict'
import { goalBudget, goalCommand } from './goal-command.ts'

test('only explicit positive budgets authorize creation or resume', () => {
  assert.deepEqual(goalBudget('verify 100 files'), { text: 'verify 100 files' })
  assert.deepEqual(goalBudget('--tokens 1000 verify the complete objective'), { text: 'verify the complete objective', tokens: 1000 })
  assert.deepEqual(goalBudget('--tokens 2000 resume'), { text: 'resume', tokens: 2000 })
  for (const text of ['--tokens', '--tokens 1', '--tokens 0 objective', '--tokens -1 objective', '--tokens 999999999999999999999 objective']) {
    assert.equal(goalBudget(text).invalid, true, text)
  }
})

test('only whole control words act on a goal, with edit and literal objective syntax', () => {
  for (const text of ['stop the bleeding in CI', 'PAUSE\tall background jobs', 'resume work', 'clear the inbox']) {
    assert.deepEqual(goalCommand(text), { action: 'create', objective: text })
  }
  for (const action of ['stop', 'pause', 'resume', 'clear']) {
    assert.deepEqual(goalCommand(`  ${action.toUpperCase()} `), { action })
  }
  assert.deepEqual(goalCommand('edit revised  objective'), { action: 'edit', objective: 'revised  objective' })
  assert.deepEqual(goalCommand('edit'), { action: 'edit', objective: '' })
  assert.deepEqual(goalCommand(goalBudget('--tokens 12 -- stop').text), { action: 'create', objective: 'stop' })
  assert.deepEqual(goalCommand('-- edit the backlog'), { action: 'create', objective: 'edit the backlog' })
  assert.deepEqual(goalCommand('--'), { action: 'create', objective: '' })
})
