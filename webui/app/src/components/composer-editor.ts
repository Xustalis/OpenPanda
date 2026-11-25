// SPDX-License-Identifier: AGPL-3.0-or-later

/** Grow the composer with its content up to a ceiling, then scroll inside.
 *  A fixed two-row box makes pasting a paragraph feel like typing into a
 *  keyhole; unbounded growth would eat the transcript instead. */
const COMPOSER_MAX_PX = 240

/** The composer's editable surface is a contenteditable, not a textarea: an
 *  attachment has to render as a pill *inside* the line the user is typing,
 *  and only a rich surface can host one. Everything the textarea used to give
 *  us for free — the placeholder, the newline character, the caret offset —
 *  is re-derived here. */

/** Chromium's default paragraph separator is a div, which would turn every
 *  Enter into a nested block and double the newlines when read back. Ask for
 *  <br> once per document instead. */
let paragraphSeparatorSet = false
export function useBrParagraphSeparator(): void {
  if (paragraphSeparatorSet || typeof document === 'undefined') return
  try {
    document.execCommand('defaultParagraphSeparator', false, 'br')
    paragraphSeparatorSet = true
  } catch {
    // A browser that refuses it still works; newlines just come back as blocks.
  }
}

export function autoGrow(el: HTMLElement): void {
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, COMPOSER_MAX_PX)}px`
}

/** The editor's text as the model should see it: text nodes verbatim, <br> as
 *  a newline, blocks as a newline before their contents. Attachment pills are
 *  skipped — they travel as the attachment block appended to the prompt, so
 *  listing their names here would put them in twice. */
function editorText(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return (node.textContent ?? '').replace(/\u200b/g, '')
  if (node.nodeType !== Node.ELEMENT_NODE) return ''
  const el = node as HTMLElement
  if (el.classList.contains('attach-pill')) return ''
  if (el.tagName === 'BR') return '\n'
  let inner = ''
  for (const child of el.childNodes) inner += editorText(child)
  if (el.tagName === 'DIV' || el.tagName === 'P') return `\n${inner}`
  return inner
}

/** The text as the user sees it typed, with nothing but a newline counting as
 *  empty — deleting everything leaves a <br> behind, and the placeholder has to
 *  show through that. */
export function editorPlain(text: string): string {
  return text.trim()
}

/** The caret's offset in the plain text, so @file completion can slice the
 *  editor's value the same way it sliced the textarea's. The offset has to
 *  count exactly what editorText counts — Range.toString sees only text
 *  nodes, so it under-reads by the <br> and block newlines editorText
 *  synthesizes and over-reads by the pill names it skips. Both adjustments
 *  land here so caret and `input` stay in the same space. */
export function editorCaretOffset(el: HTMLElement): number {
  const sel = el.ownerDocument.getSelection()
  if (!sel || sel.rangeCount === 0) return editorTextOf(el).length
  const range = sel.getRangeAt(0)
  const head = el.ownerDocument.createRange()
  head.selectNodeContents(el)
  head.setEnd(range.startContainer, range.startOffset)
  return editorTextOf(head.cloneContents()).length
}

export function editorTextOf(el: HTMLElement | DocumentFragment): string {
  let out = ''
  for (const child of el.childNodes) out += editorText(child)
  return out
}

/** Character stream as `input` counts it: text nodes at their length, <br>
 *  and block elements as the one '\n' editorText gives them, nothing for an
 *  attachment pill (REJECT keeps its name out too). Positions along this
 *  walk and offsets into editorTextOf are the same space, which is what
 *  atQuery's [start, caret) addresses. */
export function editorTextWalker(el: HTMLElement): TreeWalker {
  return el.ownerDocument.createTreeWalker(el, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
    acceptNode: (n) => {
      if (n.nodeType === Node.ELEMENT_NODE) {
        const e = n as HTMLElement
        if (e.classList.contains('attach-pill')) return NodeFilter.FILTER_REJECT
        return e.tagName === 'BR' || e.tagName === 'DIV' || e.tagName === 'P'
          ? NodeFilter.FILTER_ACCEPT
          : NodeFilter.FILTER_SKIP
      }
      return NodeFilter.FILTER_ACCEPT
    },
  })
}

/** Remove just the command characters. Walking text nodes independently keeps
 * attachment pills and markup between those characters intact. */
export function removeEditorToken(el: HTMLElement, start: number, end: number, focus = true): void {
  const walker = editorTextWalker(el)
  const caret = el.ownerDocument.createRange()
  let offset = 0
  let anchored = false
  let node: Node | null
  while (offset < end && (node = walker.nextNode())) {
    if (node.nodeType !== Node.TEXT_NODE) { offset++; continue }
    const text = node as Text
    const visible = text.data.replace(/\u200b/g, '').length
    if (offset + visible > start) {
      let from = 0
      let to = 0
      let index = offset
      for (const char of text.data.split('')) {
        if (index >= end) break
        if (index < start) from++
        to++
        if (char !== '\u200b') index++
      }
      text.deleteData(from, to - from)
      if (!anchored) { caret.setStart(text, from); caret.collapse(true); anchored = true }
    }
    offset += visible
  }
  if (anchored && focus) {
    el.focus()
    const selection = el.ownerDocument.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(caret)
  }
}

/** Plain text into the editor at the caret, without the HTML a paste would
 *  bring along. execCommand keeps the browser's own undo stack working, which
 *  a hand-built Range insertion does not. */
export function editorInsertText(text: string): void {
  document.execCommand('insertText', false, text)
}

/** Is the document's selection inside `el`? Every caret read assumes it is:
 *  `editorCaretOffset` builds a Range anchored in the editor, and a selection
 *  parked elsewhere (a button that took the focus, a panel that opened) makes
 *  that Range throw instead of answering. */
function caretInside(el: HTMLElement): boolean {
  const sel = el.ownerDocument.getSelection()
  if (!sel || sel.rangeCount === 0) return false
  return el.contains(sel.getRangeAt(0).startContainer)
}

/** Park the caret at the end of the editor — where a re-focused box lands when
 *  there is no remembered selection to restore. */
function caretToEnd(el: HTMLElement): void {
  const rg = el.ownerDocument.createRange()
  rg.selectNodeContents(el)
  rg.collapse(false)
  const sel = el.ownerDocument.getSelection()
  if (!sel) return
  sel.removeAllRanges()
  sel.addRange(rg)
}

/** Drop plain text in at the caret of a contenteditable composer. Shared by
 *  both composers (the thread's and the map's), because a dictated phrase has
 *  to land the same way in either: the box may have lost its selection to the
 *  button that started the dictation, so the caret is re-established first,
 *  and a space is added when it sits at the end of a word — two phrases in a
 *  row must not arrive welded together. */
export function insertTextAtCaret(el: HTMLElement, text: string): void {
  el.focus()
  if (!caretInside(el)) caretToEnd(el)
  const plain = editorTextOf(el)
  const at = editorCaretOffset(el)
  const needsSpace = at > 0 && !/\s/.test(plain.charAt(at - 1))
  editorInsertText(needsSpace ? ` ${text}` : text)
}

/** Keep the pill from welding to the text after it. */
export function range_or_append(after: Node, node: Node): void {
  if (after.parentNode) after.parentNode.insertBefore(node, after.nextSibling)
}

export function editorClear(el: HTMLElement | null): void {
  if (!el) return
  el.textContent = ''
  // 'auto' under a min-height floor resolves to that floor, but the inline
  // height from the last autoGrow call wins over it, so put the number back
  // explicitly or the box stays the height of the turn just sent.
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, COMPOSER_MAX_PX)}px`
}
