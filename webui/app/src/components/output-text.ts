// SPDX-License-Identifier: AGPL-3.0-or-later

/** Tool output that lost its encoding before it reached the console.
 *
 *  On Windows a shell writes its OEM code page to a pipe (936 on zh-CN);
 *  the CLI that captured it decoded those bytes as UTF-8, so every Chinese
 *  character arrived here as U+FFFD. The bytes are gone by then — no decoder
 *  in this app can bring them back — and what is left is one line of
 *  replacement glyphs per line of output. In a transcript that reads as
 *  damage, not as the error message it is.
 *
 *  So the row keeps what a reader can act on: ASCII (the exception type, the
 *  exit code, the stack) survives untouched, a run of replacement characters
 *  inside a readable line collapses to one marker, and a line that is mostly
 *  replacement characters is dropped and counted — the caller says how many
 *  lines went missing instead of printing the wreckage. */

const BAD = '\uFFFD'

/** Replacement characters a line needs before it can be called unreadable:
 *  one stray byte in a long line is not a broken line. */
const MIN_BAD = 3

/** …and the share of the line's visible characters they must make up. A
 *  mostly-Chinese line with one lost character stays readable; a line that is
 *  half replacement glyphs is not worth a row. */
const BAD_SHARE = 0.35

export type ReadableOutput = {
  /** The text as it should be shown: unreadable lines removed, runs of
   *  replacement characters collapsed. Equal to the input when it held none. */
  text: string
  /** Lines that were dropped for being undecodable. */
  dropped: number
}

function countChar(line: string, target: string): number {
  let n = 0
  for (const ch of line) if (ch === target) n++
  return n
}

function countVisible(line: string): number {
  let n = 0
  for (const ch of line) if (ch !== ' ' && ch !== '\t' && ch !== '\r') n++
  return n
}

/** Strip the unreadable half of a captured output. */
export function readableOutput(text: string): ReadableOutput {
  if (!text.includes(BAD)) return { text, dropped: 0 }
  const kept: string[] = []
  let dropped = 0
  for (const line of text.split(/\r?\n/)) {
    const bad = countChar(line, BAD)
    if (bad >= MIN_BAD && bad >= countVisible(line) * BAD_SHARE) {
      dropped++
      continue
    }
    // A long run is one lost stretch of text, not one glyph per character.
    kept.push(line.replace(/\uFFFD{2,}/g, BAD))
  }
  return { text: kept.join('\n').trimEnd(), dropped }
}
