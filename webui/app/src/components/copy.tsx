// SPDX-License-Identifier: AGPL-3.0-or-later

import { render } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './icons'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { toast } from './toast'

// Copying a message, modelled on the TUI's `/copy` (codex-rs/tui):
//
//   * the payload is the *source* text, not what the renderer drew — a reply
//     is copied as Markdown, so bold stays bold when it lands in an editor
//     instead of arriving as pre-wrapped plain text;
//   * a Markdown copy also carries a rendered-HTML flavour, so a paste into a
//     rich surface keeps the structure (`CopyFormat::Markdown` in
//     `clipboard_copy.rs` puts both on the native clipboard);
//   * an empty message is never written — the TUI refuses it with
//     "No agent response to copy" rather than clearing the clipboard;
//   * the outcome is always reported, never swallowed: a silent failure leaves
//     the user pasting the previous clipboard contents.

/** How long the button holds its confirmation before going back to idle. */
const COPIED_MS = 1600

/** The rendered form of a reply, for the HTML clipboard flavour.
 *
 *  There is no HTML serializer for the block tree, so this renders the same
 *  component the transcript uses into a detached node and takes its markup.
 *  The per-code-block copy buttons are stripped first: they are chrome, and
 *  they would paste into a document as stray text.
 *
 *  Returns null when there is nothing to render, so the caller falls back to a
 *  plain-text copy. */
function markdownHtml(text: string): string | null {
  if (!text || typeof document === 'undefined') return null
  const host = document.createElement('div')
  try {
    render(<Markdown text={text} />, host)
    host.querySelectorAll('button').forEach((b) => b.remove())
    const html = host.innerHTML
    return html.trim() ? html : null
  } catch {
    // A reply the block renderer chokes on still copies as text; the HTML
    // flavour is the bonus, never the payload.
    return null
  } finally {
    // Unmounts the tree Preact put in the detached node, so the code-block
    // timers inside it are not left running.
    try {
      render(null, host)
    } catch {
      // Older Preact builds have no unmount-by-null; the node is detached and
      // collected with the string it produced.
    }
  }
}

/** Write `text` to the clipboard, with `html` as a second flavour when the
 *  platform takes one. Throws when nothing was written, so the caller can say
 *  so — `writeText` alone reports nothing when the permission is denied. */
async function writeClipboard(text: string, html: string | null): Promise<void> {
  const clipboard = navigator.clipboard
  if (!clipboard) throw new Error('clipboard unavailable')
  if (html && typeof ClipboardItem !== 'undefined' && clipboard.write) {
    try {
      await clipboard.write([
        new ClipboardItem({
          'text/plain': new Blob([text], { type: 'text/plain' }),
          'text/html': new Blob([html], { type: 'text/html' }),
        }),
      ])
      return
    } catch {
      // Firefox and some Safari versions reject a multi-flavour write. The
      // source text is the payload that matters, so fall through to it.
    }
  }
  await clipboard.writeText(text)
}

/** The copy affordance on one message: an icon that confirms in place.
 *
 *  `markdown` says the text is a reply's Markdown source (so it gets the HTML
 *  flavour); a question is copied exactly as it was sent. */
export function MessageCopyButton({
  text,
  markdown = false,
}: {
  text: string
  markdown?: boolean
}) {
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle')
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])

  const label =
    state === 'done'
      ? t('md.copied')
      : state === 'failed'
        ? t('sessions.copyFailed')
        : t('sessions.copyMessage')

  async function copy() {
    if (!text) return
    window.clearTimeout(timer.current)
    try {
      await writeClipboard(text, markdown ? markdownHtml(text) : null)
      setState('done')
    } catch (e) {
      setState('failed')
      // The TUI answers a failed copy with "Copy failed: <reason>"; the icon
      // alone would not tell the user their clipboard is unchanged.
      const why = e instanceof Error ? e.message : String(e)
      toast(`${t('sessions.copyFailed')}: ${why}`, 'error')
    }
    timer.current = window.setTimeout(() => setState('idle'), COPIED_MS)
  }

  return (
    <button
      type="button"
      class="msg-copy"
      onClick={copy}
      title={label}
      aria-label={label}
    >
      <Icon name={state === 'done' ? 'check' : state === 'failed' ? 'alert' : 'copy'} size={14} />
    </button>
  )
}
