// SPDX-License-Identifier: AGPL-3.0-or-later

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ATTACH_HEADER, displayUserTurn } from './turn-display.ts'

// The shape the panel persists: the composer's prose, the @path list, then a
// fenced copy of a large (here, truncated) file. This is the turn that filled
// a whole screen with JVM classlist output after a stop + transcript reload.
const uploadPrompt = [
  '读一下这个文件',
  '',
  ATTACH_HEADER,
  '- @.openpanda/uploads/1/1794153294451-replay_pid21696.log',
  '- @notes/todo.md',
  '',
  '`.openpanda/uploads/1/1794153294451-replay_pid21696.log`:',
  '```',
  'version 2\r\ninstanceKlass org/apache/logging/log4j/util/EnvironmentPropertySource',
  '# instanceKla',
  '… (truncated)',
  '```',
  '',
  '`notes/todo.md`:',
  '```markdown',
  '# todo',
  '```',
].join('\n')

test('displayUserTurn recovers the prose and chips from an upload prompt', () => {
  assert.deepEqual(displayUserTurn(uploadPrompt), {
    text: '读一下这个文件',
    files: [
      '.openpanda/uploads/1/1794153294451-replay_pid21696.log',
      'notes/todo.md',
    ],
  })
  assert.ok(displayUserTurn(uploadPrompt).text.length < 20)
})

test('displayUserTurn handles a turn that was only an attached file', () => {
  const only = `${ATTACH_HEADER}\n- @shot.png`
  assert.deepEqual(displayUserTurn(only), { text: '', files: ['shot.png'] })
})

test('displayUserTurn strips fenced blocks from a typed @ref with no header', () => {
  const prompt = [
    'check @src/a.go please',
    '',
    '`src/a.go`:',
    '```go',
    'package a',
    '```',
  ].join('\n')
  assert.deepEqual(displayUserTurn(prompt), {
    text: 'check @src/a.go please',
    files: ['src/a.go'],
  })
})

test('displayUserTurn keeps every path of a multi-file expansion', () => {
  const prompt = [
    'compare them',
    '',
    '`a.go`:',
    '```go',
    'package a',
    '```',
    '',
    '`b.go`:',
    '```go',
    'package b',
    '```',
  ].join('\n')
  assert.deepEqual(displayUserTurn(prompt), {
    text: 'compare them',
    files: ['a.go', 'b.go'],
  })
})

test('displayUserTurn leaves ordinary prose and the answer text untouched', () => {
  assert.deepEqual(displayUserTurn('写一个网站，需要你跟我确认细节后开始制作'), {
    text: '写一个网站，需要你跟我确认细节后开始制作',
    files: [],
  })
  assert.deepEqual(displayUserTurn('任务已被用户取消'), { text: '任务已被用户取消', files: [] })
  assert.deepEqual(displayUserTurn(''), { text: '', files: [] })
})

test('displayUserTurn does not cut a question that only quotes a fenced block', () => {
  const question = [
    'why does this fail?',
    '',
    '```go',
    'package main',
    '```',
  ].join('\n')
  assert.deepEqual(displayUserTurn(question), { text: question, files: [] })
})
