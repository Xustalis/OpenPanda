// SPDX-License-Identifier: AGPL-3.0-or-later

// The arithmetic behind dictation: microphone samples → a WAV the transcription
// endpoint will accept, and the clock the live card counts with. Kept out of
// voice-input.tsx so it can be tested — the component needs a DOM and a
// microphone, this needs neither.

/** The rate the clips are encoded at. Whisper resamples to 16 kHz internally,
 *  and a mono 16-bit 16 kHz WAV is ~2 MB a minute — an order of magnitude less
 *  to upload than the 48 kHz float stream the microphone actually produces. */
export const TARGET_RATE = 16_000

/** Float samples → a 16-bit mono WAV. Hand-rolled because the browser's own
 *  encoder (MediaRecorder) can only emit compressed containers, and a chunk
 *  cut out of the middle of one of those has no header — no decoder will take
 *  it, which is what made piecewise transcription impossible. */
export function encodeWav(samples: Float32Array, rate: number): ArrayBuffer {
  const buf = new ArrayBuffer(44 + samples.length * 2)
  const view = new DataView(buf)
  const ascii = (off: number, s: string) => {
    for (let i = 0; i < s.length; i++) view.setUint8(off + i, s.charCodeAt(i))
  }
  ascii(0, 'RIFF')
  view.setUint32(4, 36 + samples.length * 2, true)
  ascii(8, 'WAVE')
  ascii(12, 'fmt ')
  view.setUint32(16, 16, true) // PCM header size
  view.setUint16(20, 1, true) // format: PCM
  view.setUint16(22, 1, true) // channels: mono
  view.setUint32(24, rate, true)
  view.setUint32(28, rate * 2, true) // byte rate
  view.setUint16(32, 2, true) // block align
  view.setUint16(34, 16, true) // bits per sample
  ascii(36, 'data')
  view.setUint32(40, samples.length * 2, true)
  let off = 44
  for (let i = 0; i < samples.length; i++) {
    // Clamp before scaling: a sample past ±1 would wrap around and arrive as
    // a click at full volume, which Whisper hears as a consonant.
    const s = Math.max(-1, Math.min(1, samples[i] ?? 0))
    view.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true)
    off += 2
  }
  return buf
}

/** Box-filter resample. The context normally opens at 16 kHz itself, so this is
 *  the fallback for browsers that ignore the requested rate; averaging over the
 *  source window is a crude but adequate anti-alias for speech. */
export function resample(input: Float32Array, from: number, to: number): Float32Array {
  if (from === to || input.length === 0) return input
  const ratio = from / to
  const out = new Float32Array(Math.floor(input.length / ratio))
  for (let i = 0; i < out.length; i++) {
    const start = Math.floor(i * ratio)
    const end = Math.min(Math.floor((i + 1) * ratio), input.length)
    let sum = 0
    for (let j = start; j < end; j++) sum += input[j] ?? 0
    out[i] = end > start ? sum / (end - start) : 0
  }
  return out
}

/** Concatenate the chunks a capture tap has accumulated. */
export function mergeChunks(chunks: Float32Array[], total: number): Float32Array {
  const out = new Float32Array(total)
  let at = 0
  for (const c of chunks) {
    out.set(c, at)
    at += c.length
  }
  return out
}

/** mm:ss for the live card's clock — the one thing that proves the microphone
 *  is actually running before the first word has been transcribed. */
export function elapsedLabel(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}
