// Formatting tests for the JSON shown on the task detail page.
//
// The reason these exist: the whole point of the result viewer is that a raw
// blob is unreadable. A formatter that throws, swallows, or mangles non-JSON
// text would put the console back where it started — and would do so silently,
// because the failure looks like "no result".

import assert from 'node:assert/strict'
import { test } from 'node:test'

import { flattenJson, prettifyJson } from './json.ts'

test('prettifyJson indents an object across lines', () => {
  const out = prettifyJson('{"ok":true,"exit_code":0}')
  assert.ok(out.includes('\n'), 'expected the payload to be broken across lines')
  assert.ok(out.includes('"ok": true'), out)
  assert.ok(out.includes('"exit_code": 0'), out)
})

test('prettifyJson leaves non-JSON text alone', () => {
  // Older rows hold plain text; showing it verbatim beats showing nothing.
  assert.equal(prettifyJson('deleted 3 files'), 'deleted 3 files')
  assert.equal(prettifyJson(''), '')
})

test('prettifyJson survives input that is JSON but not an object', () => {
  // A bare scalar or array is still valid JSON and still worth indenting.
  assert.equal(prettifyJson('[1,2]'), '[\n  1,\n  2\n]')
  assert.equal(prettifyJson('7'), '7')
})

test('flattenJson collapses the indentation back to one line', () => {
  const flat = flattenJson('{"ok":true,"stdout":"a\\nb"}')
  assert.ok(!flat.includes('\n'), `expected a single line, got: ${flat}`)
  assert.ok(flat.includes('"ok": true'), flat)
})

test('flattenJson does not invent content for non-JSON text', () => {
  assert.equal(flattenJson('plain text'), 'plain text')
})
