// SPDX-License-Identifier: AGPL-3.0-or-later

import test from 'node:test'
import assert from 'node:assert/strict'
import { elapsedLabel, encodeWav, mergeChunks, resample, TARGET_RATE } from './voice-pcm.ts'

/** Read the ASCII chunk id at `off`. */
function tag(view: DataView, off: number): string {
  return String.fromCharCode(
    view.getUint8(off),
    view.getUint8(off + 1),
    view.getUint8(off + 2),
    view.getUint8(off + 3),
  )
}

test('encodeWav writes a header the endpoint can trust', () => {
  const samples = new Float32Array([0, 0.5, -0.5, 1, -1])
  const view = new DataView(encodeWav(samples, TARGET_RATE))

  assert.equal(tag(view, 0), 'RIFF')
  assert.equal(tag(view, 8), 'WAVE')
  assert.equal(tag(view, 12), 'fmt ')
  assert.equal(tag(view, 36), 'data')
  // Sizes are the ones a reader cross-checks before decoding: RIFF chunk size
  // counts everything after its own 8 bytes, and data counts the samples.
  assert.equal(view.getUint32(4, true), 36 + samples.length * 2)
  assert.equal(view.getUint32(40, true), samples.length * 2)
  assert.equal(view.getUint16(20, true), 1, 'PCM format tag')
  assert.equal(view.getUint16(22, true), 1, 'mono')
  assert.equal(view.getUint32(24, true), TARGET_RATE)
  assert.equal(view.getUint32(28, true), TARGET_RATE * 2, 'byte rate')
  assert.equal(view.getUint16(32, true), 2, 'block align')
  assert.equal(view.getUint16(34, true), 16, 'bits per sample')
})

test('encodeWav clamps instead of wrapping', () => {
  // A sample past ±1 that wrapped would arrive as a full-volume click, which
  // a recogniser hears as a consonant.
  const view = new DataView(encodeWav(new Float32Array([2, -2]), TARGET_RATE))
  assert.equal(view.getInt16(44, true), 32767)
  assert.equal(view.getInt16(46, true), -32768)
})

test('encodeWav of nothing is a valid, empty file', () => {
  const view = new DataView(encodeWav(new Float32Array(0), TARGET_RATE))
  assert.equal(view.getUint32(4, true), 36)
  assert.equal(view.getUint32(40, true), 0)
  assert.equal(view.byteLength, 44)
})

test('resample halves a 32 kHz stream to the target rate', () => {
  const input = new Float32Array(3200)
  for (let i = 0; i < input.length; i++) input[i] = 0.5
  const out = resample(input, 32_000, TARGET_RATE)
  assert.equal(out.length, 1600)
  // A constant signal must stay constant — a box filter that loses its window
  // would fade the take to silence.
  for (const s of out) assert.ok(Math.abs(s - 0.5) < 1e-6)
})

test('resample is a no-op at the same rate', () => {
  const input = new Float32Array([0.1, 0.2, 0.3])
  assert.equal(resample(input, TARGET_RATE, TARGET_RATE), input)
})

test('mergeChunks joins the tap in order and at the announced length', () => {
  const a = new Float32Array([1, 2])
  const b = new Float32Array([3])
  const out = mergeChunks([a, b], 3)
  assert.deepEqual([...out], [1, 2, 3])
})

test('elapsedLabel counts minutes', () => {
  assert.equal(elapsedLabel(0), '0:00')
  assert.equal(elapsedLabel(9_400), '0:09')
  assert.equal(elapsedLabel(61_000), '1:01')
  // A negative span is what a clock reading a zeroed start looks like.
  assert.equal(elapsedLabel(-500), '0:00')
})
