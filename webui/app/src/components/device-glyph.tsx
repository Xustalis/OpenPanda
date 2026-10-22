// SPDX-License-Identifier: AGPL-3.0-or-later

/** The six machine classes a device plate can draw. Advertised form factors
 *  normalize into these; anything unrecognised is `unknown`. */
export type Form = 'laptop' | 'desktop' | 'mini' | 'server' | 'vm' | 'unknown'

export const FORMS: Form[] = ['laptop', 'desktop', 'mini', 'server', 'vm', 'unknown']

/** A laptop, a tower for a desktop, a low wide box for a mini, a rack for a
 *  server, a dashed outline for a guest. Stroke only, so the theme's ink
 *  color carries through (the plates are monochrome by design: hue is
 *  reserved for status). Drawn in a -14..14 box. */
export function DeviceGlyph({ form }: { form: Form }) {
  switch (form) {
    case 'laptop':
      return (
        <>
          <rect x={-9} y={-8} width={18} height={12} rx={1.8} />
          <path d="M-12 6.4 H12 L10 9 H-10 Z" />
        </>
      )
    case 'desktop':
      return (
        <>
          <rect x={-7.5} y={-10.5} width={15} height={21} rx={2.4} />
          <path d="M-4.2 -6.8 h8.4" />
          <path d="M-4.2 -3.8 h8.4" />
          <circle cx={-4} cy={6.4} r={1.15} />
        </>
      )
    case 'mini':
      return (
        <>
          <rect x={-10.5} y={-5.4} width={21} height={10.8} rx={3} />
          <path d="M-10.5 -1.8 H10.5" opacity={0.45} />
          <circle cx={6.6} cy={2.6} r={0.95} />
        </>
      )
    case 'server':
      return (
        <>
          <rect x={-8.5} y={-9.5} width={17} height={5.4} rx={1.4} />
          <rect x={-8.5} y={-2.7} width={17} height={5.4} rx={1.4} />
          <rect x={-8.5} y={4.1} width={17} height={5.4} rx={1.4} />
          <circle cx={5} cy={-6.8} r={0.9} />
          <circle cx={5} cy={0} r={0.9} />
          <circle cx={5} cy={6.8} r={0.9} />
        </>
      )
    case 'vm':
      return (
        <>
          <rect x={-9.5} y={-8.5} width={19} height={17} rx={3} stroke-dasharray="3 2.6" />
          <rect x={-4.6} y={-3.6} width={9.2} height={7.2} rx={1.4} />
          <path d="M-9.5 1.5 H-6.4 M6.4 1.5 H9.5" opacity={0.4} />
        </>
      )
    default:
      return (
        <>
          <rect x={-8.5} y={-8.5} width={17} height={17} rx={3.2} stroke-dasharray="2.6 2.6" />
          <text
            x={0}
            y={3.6}
            text-anchor="middle"
            font-size="10"
            font-weight="600"
            fill="currentColor"
            stroke="none"
          >
            ?
          </text>
        </>
      )
  }
}
