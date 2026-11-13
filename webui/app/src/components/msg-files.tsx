// SPDX-License-Identifier: AGPL-3.0-or-later

// What a user turn carried besides its words.
//
// A transcript stores the prompt, not the composer: every upload rides as an
// `@path` list under ATTACH_HEADER (see turn-display.ts), and a reloaded turn
// recovers that list into `ChatMsg.files`. The chat rewrite drew the bubble
// from `msg.text` alone, so a screenshot the author had just pasted left no
// trace once the turn was refiled — the pixels were on disk the whole time.
//
// The shape is the composer's: one pill per file, riding in the sentence. A
// thumbnail here turned a short question into an image block and pushed the
// bubble out to the picture's width; the pill keeps the sentence the subject
// and leaves the picture behind a hover (see ImagePill in image-preview.tsx).

import { fileLabel, isImageFile } from './attach'
import { Icon } from './icons'
import { ImagePill } from './image-preview'

/** Whether the entry is a path the panel can fetch.
 *
 *  The bubble that is on screen before the turn is filed lists a tray file
 *  under its display name, which is not a path; asking /api/fs/image for it
 *  would 404 and print "image unavailable" over a picture that is perfectly
 *  fine. Such an entry stays a plain chip. */
function fetchable(path: string): boolean {
  return /[\\/]/.test(path)
}

/** The formats /api/fs/image will serve — the same table as panel/extras.go.
 *  SVG is absent there on purpose (a script-bearing document), so an .svg
 *  attachment stays a plain chip instead of asking for a preview that 415s. */
const PREVIEWABLE = /\.(png|jpe?g|gif|webp|bmp|avif)$/i

/** The attachments of one turn, as pills. Rendered inside the message's own
 *  paragraph, so the row reads the way the composer did while typing. */
export function UserFiles({ files }: { files: string[] }) {
  if (files.length === 0) return null
  return (
    <span class="msg-files">
      {files.map((f) => {
        const label = fileLabel(f)
        if (fetchable(f) && PREVIEWABLE.test(f)) {
          return <ImagePill key={f} path={f} label={label} />
        }
        // The glyph follows the extension even when the bytes are out of
        // reach: the pill is a name, and a picture named as a picture is
        // still the honest label.
        const img = isImageFile({ name: f })
        return (
          <span key={f} class="attach-chip mono" title={f}>
            <Icon name={img ? 'image' : 'paperclip'} size={img ? 12 : 11} />{' '}
            <span class="msg-file-name">{label}</span>
          </span>
        )
      })}
    </span>
  )
}
