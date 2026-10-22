// SPDX-License-Identifier: AGPL-3.0-or-later

// The console's icon vocabulary: stroke-drawn glyphs on a 24×24 grid that
// replace the emoji pictographs that used to stand in for them.
//
// Emoji were a poor fit — colour glyphs ignore the theme palette, render
// inconsistently across platforms, and read as stickers next to the
// developer-tool geometry the brand mark establishes (brand/panda.tsx).
// These are flat line icons in the Lucide/Feather convention: no fill,
// round caps, currentColor, so they take on whatever tone the surrounding
// text has in either theme.
//
// Every icon is sized 1em × 1em: the call-site's font-size scales them
// exactly the way it scaled the emoji they replaced, so wrapper classes
// like .settings-nav-icon need no sizing changes.

import type { JSX } from 'preact'

export type IconName =
  | 'activity'
  | 'alert'
  | 'arrow-down'
  | 'arrow-left'
  | 'arrow-up'
  | 'bell'
  | 'bell-off'
  | 'bot'
  | 'check'
  | 'chevron-down'
  | 'chevron-right'
  | 'code'
  | 'coins'
  | 'copy'
  | 'cpu'
  | 'devices'
  | 'download'
  | 'folder'
  | 'folder-closed'
  | 'folder-open'
  | 'folder-plus'
  | 'gear'
  | 'git-fork'
  | 'image'
  | 'kanban'
  | 'lightbulb'
  | 'list-checks'
  | 'menu'
  | 'message'
  | 'mic'
  | 'more-horizontal'
  | 'network'
  | 'orbit'
  | 'panel-right'
  | 'paperclip'
  | 'pencil'
  | 'pin'
  | 'pin-off'
  | 'plug'
  | 'plus'
  | 'route'
  | 'search'
  | 'send'
  | 'shield'
  | 'sparkles'
  | 'square-pen'
  | 'trash'
  | 'users'
  | 'x'

const paths: Record<IconName, JSX.Element> = {
  activity: <path d="M22 12h-4l-3 9L9 3l-3 9H2" />,
  alert: (
    <>
      <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" />
      <path d="M12 9v4" />
      <path d="M12 17h.01" />
    </>
  ),
  'arrow-down': (
    <>
      <path d="M12 5v14" />
      <path d="m19 12-7 7-7-7" />
    </>
  ),
  // Back out of a drill-down without leaving the surface — the orchestration
  // card's detail view returns to its two rows with this, not with a chevron.
  'arrow-left': (
    <>
      <path d="m12 19-7-7 7-7" />
      <path d="M19 12H5" />
    </>
  ),
  'arrow-up': (
    <>
      <path d="m5 12 7-7 7 7" />
      <path d="M12 19V5" />
    </>
  ),
  bell: (
    <>
      <path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
      <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
    </>
  ),
  'bell-off': (
    <>
      <path d="M8.7 3A6 6 0 0 1 18 8a21.3 21.3 0 0 0 .6 5" />
      <path d="M17 17H3s3-2 3-9a4.67 4.67 0 0 1 .3-1.7" />
      <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
      <path d="m2 2 20 20" />
    </>
  ),
  bot: (
    <>
      <path d="M12 8V4H8" />
      <rect width="16" height="12" x="4" y="8" rx="2" />
      <path d="M2 14h2" />
      <path d="M20 14h2" />
      <path d="M15 13v2" />
      <path d="M9 13v2" />
    </>
  ),
  check: <path d="M20 6 9 17l-5-5" />,
  'chevron-down': <path d="m6 9 6 6 6-6" />,
  'chevron-right': <path d="m9 18 6-6-6-6" />,
  code: (
    <>
      <path d="m16 18 6-6-6-6" />
      <path d="m8 6-6 6 6 6" />
    </>
  ),
  coins: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M18.09 10.37A6 6 0 1 1 10.34 18" />
    </>
  ),
  copy: (
    <>
      <rect width="14" height="14" x="8" y="8" rx="2" />
      <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
    </>
  ),
  cpu: (
    <>
      <rect x="4" y="4" width="16" height="16" rx="2" />
      <rect x="9" y="9" width="6" height="6" />
      <path d="M15 2v2" />
      <path d="M15 20v2" />
      <path d="M2 15h2" />
      <path d="M2 9h2" />
      <path d="M20 15h2" />
      <path d="M20 9h2" />
      <path d="M9 2v2" />
      <path d="M9 20v2" />
    </>
  ),
  // 工作空间's mark. The `network` tree it replaces is four boxes joined by
  // three connectors — at 16px in the rail that fuses into a blob, and the
  // tree already means "topology" on the node picker and the delegation
  // chips. One screen on a stand reads as "the machines" at any size.
  devices: (
    <>
      <rect x="2.5" y="4" width="19" height="12.6" rx="2.2" />
      <path d="M9 20.6h6" />
      <path d="M12 16.6v4" />
    </>
  ),
  // The header's export mark. It used to be the "⤓" character, which draws at
  // a different weight and baseline in every platform font and sat a pixel
  // high in its button; the tray-and-arrow reads the same at 18px and takes
  // the button's colour like every other glyph in this file.
  download: (
    <>
      <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
      <path d="m7 10 5 5 5-5" />
      <path d="M12 15V3" />
    </>
  ),
  folder: (
    <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
  ),
  // The same folder with the front seam drawn across the body. At 16px the
  // plain folder is an empty outline and the open one is too busy; the seam
  // gives it the mass it needs to sit level with a 15px title.
  'folder-closed': (
    <>
      <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
      <path d="M2 10h20" />
    </>
  ),
  // The same folder, front panel swung open. The back contour and the tab are
  // drawn once and the flap overlaps them, so at 11px the two edges sit a
  // stroke-width apart rather than reading as two separate folders.
  'folder-open': (
    <path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2" />
  ),
  // A closed folder with a plus notched into its face: the workdir menu's
  // "new workspace" row, which is the same chooser the pill used to open
  // directly. Drawn closed so it reads as a folder being made, not as the
  // open one that means "browse".
  'folder-plus': (
    <>
      <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
      <path d="M12 10v6" />
      <path d="M9 13h6" />
    </>
  ),
  gear: (
    <>
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  // The header's node button. One machine above, two below: the glyph says
  // "this run goes to one of several machines" without drawing the three-box
  // tree the `network` icon draws — that tree is now only the fleet/manage
  // affordance, where the point really is the topology.
  'git-fork': (
    <>
      <circle cx="12" cy="18" r="3" />
      <circle cx="6" cy="6" r="3" />
      <circle cx="18" cy="6" r="3" />
      <path d="M18 9v1a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V9" />
      <path d="M12 12v3" />
    </>
  ),
  // The Lucide frame kept, but its diagonal mountain stroke is now a solid
  // silhouette: at 11–16px the two thin strokes fused into a box with a slash
  // through it, i.e. the "missing image" symbol. The frame also keeps the dot
  // and the triangle from reading as a head over shoulders. `stroke="none"` on
  // the filled parts is required — the svg inherits a 2px stroke that would
  // otherwise fatten the silhouette. Mirrored verbatim by chipMarkup() in
  // views/sessions.tsx.
  image: (
    <>
      <rect x="3" y="3" width="18" height="18" rx="4.2" stroke-width="1.7" />
      <path fill="currentColor" stroke="none" d="M4.2 20 8.4 12.2 11.6 16.4 15.2 12.8 19.8 20z" />
      <circle cx="7.9" cy="7.9" r="1.75" fill="currentColor" stroke="none" />
    </>
  ),
  // 队列·任务·项目's mark: the kanban the board itself draws, one column per
  // tab. Bare bars, not three framed boxes — at 16px a boxed three-column grid
  // closes up into a solid brick, while the bars keep the two gaps, and it is
  // the uneven lengths (14/8/12, tops aligned) that say "three boards" instead
  // of "a table". Ink spans 3.5–20.5 × 4–20 with the 2px stroke, so it sits on
  // the same optical centre as `devices` and `network` beside it in the rail.
  kanban: (
    <>
      <path d="M4.5 5v14" />
      <path d="M12 5v8" />
      <path d="M19.5 5v12" />
    </>
  ),
  lightbulb: (
    <>
      <path d="M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5" />
      <path d="M9 18h6" />
      <path d="M10 22h4" />
    </>
  ),
  // 待办: a checklist whose first two rows are already ticked. The rail uses
  // it for the to-do board, which is the one place in the console where a
  // row's whole job is "done or not done".
  'list-checks': (
    <>
      <path d="m3 17 2 2 4-4" />
      <path d="m3 7 2 2 4-4" />
      <path d="M13 6h8" />
      <path d="M13 12h8" />
      <path d="M13 18h8" />
    </>
  ),
  menu: (
    <>
      <path d="M4 6h16" />
      <path d="M4 12h16" />
      <path d="M4 18h16" />
    </>
  ),
  message: <path d="M7.9 20A9 9 0 1 0 4 16.1L2 22Z" />,
  mic: (
    <>
      <path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z" />
      <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
      <path d="M12 19v3" />
    </>
  ),
  'more-horizontal': (
    <>
      <circle cx="12" cy="12" r="1" />
      <circle cx="19" cy="12" r="1" />
      <circle cx="5" cy="12" r="1" />
    </>
  ),
  network: (
    <>
      <rect x="16" y="16" width="6" height="6" rx="1" />
      <rect x="2" y="16" width="6" height="6" rx="1" />
      <rect x="9" y="2" width="6" height="6" rx="1" />
      <path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3" />
      <path d="M12 12V8" />
    </>
  ),
  orbit: (
    <>
      <circle cx="12" cy="12" r="2.4" />
      <ellipse cx="12" cy="12" rx="10" ry="4" transform="rotate(30 12 12)" />
      <ellipse cx="12" cy="12" rx="10" ry="4" transform="rotate(-30 12 12)" />
    </>
  ),
  // The workspace rail's toggle: a pane folded against the right edge — the
  // same shape every editor uses for "show/hide the side panel", so the
  // button needs no label at 16px.
  'panel-right': (
    <>
      <rect x="3" y="4" width="18" height="16" rx="2.4" />
      <path d="M15 4v16" />
    </>
  ),
  paperclip: (
    <path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48" />
  ),
  pencil: (
    <>
      <path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z" />
      <path d="m15 5 4 4" />
    </>
  ),
  // The pending-note row's "send it now" action: a paper plane, the glyph the
  // reference composer uses for inject.
  send: (
    <>
      <path d="M22 2 11 13" />
      <path d="M22 2 15 22l-4-9-9-4Z" />
    </>
  ),
  pin: (
    <>
      <path d="M12 17v5" />
      <path d="M5 17h14v-1.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V6h1a2 2 0 0 0 0-4H8a2 2 0 0 0 0 4h1v4.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24Z" />
    </>
  ),
  'pin-off': (
    <>
      <path d="M12 17v5" />
      <path d="M5 17h9.5" />
      <path d="M16.76 12.55A2 2 0 0 1 15 10.76V6h1a2 2 0 0 0 0-4H8a2 2 0 0 0-2 2" />
      <path d="M5 15.24a2 2 0 0 1 1.11-1.79l1.78-.9A2 2 0 0 0 9 10.76V9.5" />
      <path d="m2 2 20 20" />
    </>
  ),
  plug: (
    <>
      <path d="M12 22v-5" />
      <path d="M9 8V2" />
      <path d="M15 8V2" />
      <path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z" />
    </>
  ),
  // The rail's per-directory "new chat": a bare cross, because it sits in the
  // same 22px hover slot as the "…" beside it and any container would read as
  // a second button instead of the mark itself.
  plus: (
    <>
      <path d="M5 12h14" />
      <path d="M12 5v14" />
    </>
  ),
  // The header's orchestration button. Two endpoints joined by one path that
  // snakes between them: the glyph reads as a run — start, hand-off, arrive —
  // rather than as a shape (the old `workflow` was two boxes and an elbow, which
  // at 19px collapsed into "some boxes") and rather than as machines, which is
  // what the `git-fork` node button beside it means.
  route: (
    <>
      <circle cx="6" cy="19" r="3" />
      <path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15" />
      <circle cx="18" cy="5" r="3" />
    </>
  ),
  // The sidebar head's magnifier: the ⌘K palette used to be a bordered field
  // with a "Ctrl K" chip in the rail, which made the head the heaviest thing
  // in a column of quiet rows. The glyph alone says the same thing in 16px.
  search: (
    <>
      <circle cx="11" cy="11" r="8" />
      <path d="m21 21-4.3-4.3" />
    </>
  ),
  shield: (
    <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
  ),
  sparkles: (
    <>
      <path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z" />
      <path d="M20 3v4" />
      <path d="M22 5h-4" />
      <path d="M4 17v2" />
      <path d="M5 18H3" />
    </>
  ),
  // Lucide's square-pen: the sheet is drawn first and the nib crosses its top
  // right corner, so at 16px it reads as "write something new" rather than as
  // the plain pencil (which is what the composer's edit affordances use).
  'square-pen': (
    <>
      <path d="M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
      <path d="M18.375 2.625a1 1 0 0 1 3 3l-9.013 9.014a2 2 0 0 1-.853.505l-2.873.84a.5.5 0 0 1-.62-.62l.84-2.873a2 2 0 0 1 .506-.852z" />
    </>
  ),
  trash: (
    <>
      <path d="M3 6h18" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
      <path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      <path d="M10 11v6" />
      <path d="M14 11v6" />
    </>
  ),
  // Lucide's users: two heads, the near one solid-lined and the far one drawn
  // as its shoulders. It is the composer's roster mark — "who works on this".
  users: (
    <>
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </>
  ),
  x: (
    <>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </>
  ),
}

/** Inline icon. `size` is in px; defaults to 1em so the surrounding
 *  font-size drives it — the same scaling the emoji had. */
export function Icon({
  name,
  size,
  class: cls,
}: {
  name: IconName
  size?: number
  class?: string
}) {
  return (
    <svg
      width={size ?? '1em'}
      height={size ?? '1em'}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      class={`ico${cls ? ' ' + cls : ''}`}
    >
      {paths[name]}
    </svg>
  )
}
