// The OpenPanda brand marks.
//
// Two of them, for two different jobs:
//
//   PandaMark      a flat geometric tile — a panda silhouette in negative
//                  space on an accent field. It is a logo, not an
//                  illustration: no blush, no sparkle, no smile, nothing that
//                  reads as a sticker at 22px in a chat gutter.
//   PandaAscii     a block-letter wordmark, drawn in monospace the way the
//                  CLI draws its splash. This is the brand's loud voice —
//                  the sign-in screen and the empty chat — and it says
//                  "developer tool" in a way no round cartoon face can.
//
// Both are pure geometry/text: no raster assets, no external fonts, and they
// take their colours from the theme tokens so light and dark both work.

/** The tile mark: sidebar wordmark and message avatars. Square, so it can be
 *  dropped into any fixed-size gutter without wrapping its own aspect. */
export function PandaMark({ size = 28 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      aria-hidden="true"
      class="panda-mark"
    >
      {/* The field. `--accent` in both themes; the glyph rides on --accent-ink,
          which is dark-on-light in dark mode and light-on-dark in light mode,
          so contrast holds without a second set of tokens. */}
      <rect width="64" height="64" rx="16" fill="var(--accent)" />
      <g fill="var(--accent-ink)">
        {/* ears, then a stadium head that swallows their lower halves */}
        <circle cx="21" cy="23" r="7.5" />
        <circle cx="43" cy="23" r="7.5" />
        <rect x="13" y="26" width="38" height="27" rx="13.5" />
      </g>
      {/* Eyes cut back out in the field colour — negative space instead of a
          third tone keeps the mark readable when it is 22 pixels wide. */}
      <g fill="var(--accent)">
        <circle cx="25" cy="38" r="3.4" />
        <circle cx="39" cy="38" r="3.4" />
      </g>
    </svg>
  )
}

/** Tile + name, for the sidebar header. */
export function PandaWordmark({ size = 26 }: { size?: number }) {
  return (
    <span class="wordmark">
      <PandaMark size={size} />
      <span class="wordmark-text">
        Open<b>Panda</b>
      </span>
    </span>
  )
}

// Block letters on a 5×5 grid, one space between glyphs. Written out rather
// than generated: nine letters is less code than a font table, and it stays
// legible in the diff when someone wants to tweak a stroke.
const ASCII_OPEN = [
  '█████ █████ █████ █   █',
  '█   █ █   █ █     ██  █',
  '█   █ █████ ████  █ █ █',
  '█   █ █     █     █  ██',
  '█████ █     █████ █   █',
]

const ASCII_PANDA = [
  '█████ █████ █   █ ████  █████',
  '█   █ █   █ ██  █ █   █ █   █',
  '█████ █████ █ █ █ █   █ █████',
  '█     █   █ █  ██ █   █ █   █',
  '█     █   █ █   █ ████  █   █',
]

/** The loud wordmark: two stacked lines of block letters.
 *
 *  It is `aria-hidden` with a real text label beside it, because a screen
 *  reader given the raw block characters would announce a wall of noise. The
 *  `scale` prop is a font-size in px for one cell — the whole mark is sized
 *  off it, so callers pick a size rather than a layout. */
export function PandaAscii({ scale = 9 }: { scale?: number }) {
  return (
    <div class="ascii-mark" role="img" aria-label="OpenPanda">
      <pre aria-hidden="true" style={{ fontSize: `${scale}px` }}>
        {ASCII_OPEN.join('\n')}
      </pre>
      <pre aria-hidden="true" class="ascii-mark-lo" style={{ fontSize: `${scale}px` }}>
        {ASCII_PANDA.join('\n')}
      </pre>
    </div>
  )
}
