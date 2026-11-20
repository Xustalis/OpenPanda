// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RefObject } from 'preact'
import {
  autoGrow,
  editorCaretOffset,
  editorInsertText,
  editorPlain,
  editorTextOf,
  range_or_append,
  useBrParagraphSeparator,
} from '../../components/composer-editor'
import { t } from '../../i18n'

/** One row in the composer's completion menu — a slash command, a palette
 *  destination, or a filesystem path. Palette `Command`s satisfy the shape:
 *  a run() that ignores the argument is still a valid (arg?) => void. */
export interface CompletionItem {
  id: string
  label: string
  /** The palette's English keywords, carried through so the `/` menu can match
   *  `/queue` to 队列 in a locale whose label says 队列 (see slashWords). */
  alias?: string
  hint?: string
  /** The palette marks the live value with a check icon instead of a hint;
   *  here it degrades to the same ✓ the hint used to carry. */
  checked?: boolean
  group?: string
  run(arg?: string): void
}

interface ComposerInputProps {
  composer: RefObject<HTMLDivElement>
  input: string
  pendingCount: number
  placeholder: string
  completeShown: CompletionItem[]
  completeIdx: number
  setInput(value: string): void
  setCaret(value: number): void
  setCompleteCursor(value: number): void
  setCompleteDismissed(value: string): void
  acceptCompletion(item: CompletionItem): void
  syncPending(el: HTMLElement): void
  removePending(key: string): void
  zoomPill(chip: HTMLElement): void
  peekPill(chip: HTMLElement): void
  unpeek(): void
  addFiles(files: File[]): void
  send(): void
}

/** The rich editor and its completion menu. The session owns the draft and
 *  upload queue; this surface owns DOM, caret, paste and keyboard handling. */
export function ComposerInput({
  composer, input, pendingCount, placeholder, completeShown, completeIdx,
  setInput, setCaret, setCompleteCursor, setCompleteDismissed, acceptCompletion,
  syncPending, removePending, zoomPill, peekPill, unpeek, addFiles, send,
}: ComposerInputProps) {
  return (
    <>
      {completeShown.length > 0 && (
        <div class="complete-menu" role="listbox" aria-label={t('palette.title')}>
          {completeShown.map((c, i) => (
            <button
              key={c.id}
              type="button"
              role="option"
              aria-selected={i === completeIdx}
              class={`complete-item${i === completeIdx ? ' active' : ''}`}
              onMouseMove={() => setCompleteCursor(i)}
              onClick={() => acceptCompletion(c)}
            >
              <span class="complete-label">{c.label}</span>
              <span class="complete-hint">
                {c.hint ?? (c.checked ? '✓' : c.group)}
              </span>
            </button>
          ))}
        </div>
      )}
      {/* The editable surface. Contenteditable rather than a textarea so
          an attachment can render as a pill inside the line being typed,
          with the caret carrying on after it — the behaviour the
          reference shows and a textarea cannot host. Everything the
          textarea gave for free is re-derived in composer-editor.ts. */}
      <div
        ref={(el) => {
          composer.current = el
        }}
        class={`composer-input${editorPlain(input) === '' && pendingCount === 0 ? ' is-empty' : ''}`}
        contenteditable="true"
        role="textbox"
        aria-multiline="true"
        aria-label={t('sessions.placeholder')}
        data-placeholder={placeholder}
        onInput={(e) => {
          useBrParagraphSeparator()
          const el = e.currentTarget as HTMLDivElement
          setInput(editorTextOf(el))
          setCaret(editorCaretOffset(el))
          // Deleting a pill with Backspace or a selection only removes
          // the node — drop the file behind it too, or a send would
          // still upload what the user took out.
          syncPending(el)
          autoGrow(el)
        }}
        onClick={(e) => {
          // One listener for the whole surface, delegated: the pills are
          // built as raw nodes, and a listener per node would have to be
          // torn down by hand every time one leaves the line. The ×
          // takes the file back out; the magnifier — and the pill's own
          // body — asks to see the picture; anywhere else moves the
          // caret.
          const hit = e.target as HTMLElement
          const chip = hit.closest('.attach-pill') as HTMLElement | null
          if (chip) {
            if (hit.closest('.attach-pill-x')) removePending(chip.dataset.pk ?? '')
            else zoomPill(chip)
            return
          }
          setCaret(editorCaretOffset(e.currentTarget as HTMLDivElement))
        }}
        onMouseOver={(e) => {
          // mouseover, not mouseenter: the former bubbles, so one
          // listener covers pills inserted after this render. Moving
          // between the pill's own glyphs is not "entering" it again.
          const chip = (e.target as HTMLElement).closest('.attach-pill') as HTMLElement | null
          if (!chip || chip.contains(e.relatedTarget as Node | null)) return
          peekPill(chip)
        }}
        onMouseOut={(e) => {
          const chip = (e.target as HTMLElement).closest('.attach-pill') as HTMLElement | null
          if (!chip || chip.contains(e.relatedTarget as Node | null)) return
          unpeek()
        }}
        onKeyUp={(e) => setCaret(editorCaretOffset(e.currentTarget as HTMLDivElement))}
        onPaste={(e) => {
          // A screenshot on the clipboard arrives as a file with no
          // path, which is exactly what the pill is for.
          const files = [...(e.clipboardData?.files ?? [])]
          if (files.length > 0) {
            e.preventDefault()
            addFiles(files)
            return
          }
          // Pasted text goes in as <br>-separated lines: the browser's
          // own paste plants literal newline text nodes, which Chromium
          // does not lay out as line breaks in here. A single line goes
          // through insertText — the default paste would bring the
          // source page's HTML styling into a plain-text surface.
          const text = e.clipboardData?.getData('text/plain')
          if (!text) return
          e.preventDefault()
          const el3 = e.currentTarget as HTMLDivElement
          if (!text.includes('\n')) {
            editorInsertText(text)
            requestAnimationFrame(() => {
              setInput(editorTextOf(el3))
              setCaret(editorCaretOffset(el3))
              autoGrow(el3)
            })
            return
          }
          const parts = text.split('\n')
          const frag = el3.ownerDocument.createDocumentFragment()
          parts.forEach((part, i) => {
            if (i > 0) frag.appendChild(el3.ownerDocument.createElement('br'))
            if (part) frag.appendChild(el3.ownerDocument.createTextNode(part))
          })
          const sel3 = document.getSelection()
          if (sel3 && sel3.rangeCount > 0) {
            const rg = sel3.getRangeAt(0)
            rg.deleteContents()
            rg.insertNode(frag)
            rg.collapse(false)
            sel3.removeAllRanges()
            sel3.addRange(rg)
          } else {
            el3.appendChild(frag)
          }
          setInput(editorTextOf(el3))
          setCaret(editorCaretOffset(el3))
          autoGrow(el3)
        }}
        onKeyDown={(e) => {
          if (completeShown.length > 0) {
            // While the menu is open the navigation keys belong to it:
            // Tab/Enter accept, arrows move, Escape hides it until the
            // input changes again.
            if ((e.key === 'Tab' || e.key === 'Enter') && !e.isComposing && e.keyCode !== 229) {
              e.preventDefault()
              const cmd = completeShown[completeIdx]
              if (cmd) acceptCompletion(cmd)
              return
            }
            if (e.key === 'ArrowDown' || (e.ctrlKey && e.key === 'n')) {
              e.preventDefault()
              setCompleteCursor((completeIdx + 1) % completeShown.length)
              return
            }
            if (e.key === 'ArrowUp' || (e.ctrlKey && e.key === 'p')) {
              e.preventDefault()
              setCompleteCursor((completeIdx - 1 + completeShown.length) % completeShown.length)
              return
            }
            if (e.key === 'Escape') {
              e.preventDefault()
              setCompleteDismissed(input)
              return
            }
          }
          // Shift+Enter breaks the line. The browser's own answer is a
          // literal newline text node, which Chromium does not lay out as
          // a line break inside a contenteditable — the row stays one
          // line tall and the caret sits mid-string. Insert the <br>
          // ourselves so the editor actually grows.
          if (e.key === 'Enter' && e.shiftKey && !e.isComposing && e.keyCode !== 229) {
            e.preventDefault()
            const el2 = e.currentTarget as HTMLDivElement
            const br = el2.ownerDocument.createElement('br')
            const sel2 = document.getSelection()
            if (sel2 && sel2.rangeCount > 0) {
              const rg = sel2.getRangeAt(0)
              rg.deleteContents()
              rg.insertNode(br)
              rg.setStartAfter(br)
              rg.collapse(true)
              sel2.removeAllRanges()
              sel2.addRange(rg)
            } else {
              el2.appendChild(br)
            }
            // A zero-width space after the <br> keeps the caret on the
            // new line; a bare <br> at the end of the block leaves the
            // caret before it in some engines.
            const nbsp = el2.ownerDocument.createTextNode('\u200b')
            range_or_append(br, nbsp)
            setInput(editorTextOf(el2))
            setCaret(editorCaretOffset(el2))
            autoGrow(el2)
            return
          }
          // Enter sends — and while a Chinese or Japanese IME is
          // composing, Enter belongs to the composition, not to us.
          // Checking isComposing is the whole reason a contenteditable can
          // replace the textarea here.
          if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229
              && !(e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            send()
          }
          if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) {
            e.preventDefault()
            send()
          }
        }}
      >
      </div>
    </>
  )
}
