// SPDX-License-Identifier: AGPL-3.0-or-later

// The OpenPanda brand marks.
//
//   PandaMark      the North Star & Orbit symbol: a four-point polar star
//                  inside an open orbit, on the same 64×64 grid as the
//                  favicon and the shipped lockups. Pure geometry, no raster
//                  asset, and it takes its colours from the theme tokens.
//   PandaWordmark  symbol + name, for a header that wants both.
//   PandaLockup    the official horizontal lockup for the gate screen. The
//                  artwork is the designer's SVG; paper and night are two
//                  files, swapped by the theme attribute rather than recoloured
//                  in code.

/** The symbol: orbit ring, polar star and two nodes. Square, so it drops into
 *  any fixed-size gutter without wrapping its own aspect. */
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
      {/* The orbit is open on purpose — the gap is the adapter surface, not a
          missing stroke. pathLength normalises the dash across renderers. */}
      <g
        fill="none"
        stroke="var(--brand)"
        stroke-width="2.3"
        stroke-linecap="round"
        pathLength="100"
        stroke-dasharray="82 18"
      >
        <ellipse cx="32" cy="32" rx="26" ry="9" transform="rotate(-22 32 32)" />
      </g>
      {/* On paper the star is brand green; on night it is starlight cream.
          --brand-star carries that second tone so one component serves both. */}
      <path
        d="M32 12 L36.6 27.4 L52 32 L36.6 36.6 L32 52 L27.4 36.6 L12 32 L27.4 27.4 Z"
        fill="var(--brand-star, var(--brand))"
      />
      <circle cx="56.1" cy="22.3" r="2.9" fill="var(--brand)" />
      <circle cx="39.2" cy="20.3" r="1.9" fill="var(--brand)" />
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

/** The official horizontal lockup (240×54), used where the brand gets to
 *  speak in its own lettering. Both cuts stay in the DOM and CSS picks one:
 *  flipping data-theme swaps the image without a re-render. */
export function PandaLockup({ width = 240 }: { width?: number }) {
  const height = Math.round((width * 54) / 240.15)
  return (
    <span class="panda-lockup" style={{ width: `${width}px` }}>
      <img
        class="panda-lockup-paper"
        src="/logo/lockup-horizontal-paper.svg"
        width={width}
        height={height}
        alt="OpenPanda"
      />
      <img
        class="panda-lockup-night"
        src="/logo/lockup-horizontal-night.svg"
        width={width}
        height={height}
        alt="OpenPanda"
      />
    </span>
  )
}
