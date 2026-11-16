// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RefObject } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchBlob } from '../../api/client'
import { isImageFile, splitFileRef } from '../../components/attach'
import { autoGrow, editorCaretOffset, editorClear, editorTextOf, range_or_append } from '../../components/composer-editor'
import { t } from '../../i18n'
import { makeChipNode, type PendingAttachment } from './attachment-pills'
import { localMsgId } from './transcript-state'

/** The composer's attachment queue, DOM pills and owned image URLs. Sending
 *  and resetting the session still decide when the queue is cleared. */
export function useComposerAttachments(
  composer: RefObject<HTMLDivElement>,
  setInput: (value: string) => void,
  setCaret: (value: number) => void,
) {
  // Files pasted, dropped or uploaded into the composer, waiting for send.
  const [pending, setPending] = useState<PendingAttachment[]>([])
  // The pill the pointer is resting on, once its bytes have landed: a larger
  // copy floats above the composer. WorkBuddy's chip behaves this way, and it
  // is the only way a 23px pill can be checked before it is sent.
  const [peek, setPeek] = useState<{ key: string; name: string; src: string; anchor: DOMRect } | null>(null)
  // The pill's magnifier: the same bytes, full screen.
  const [shot, setShot] = useState<{ name: string; src: string; path?: string } | null>(null)
  // Object URLs for pending images, minted on first hover and reused until the
  // pill goes away — a screenshot can be peeked at, enlarged and peeked again.
  const shots = useRef(new Map<string, string>())
  const shotsPending = useRef(new Map<string, Promise<string>>())
  const peekTimer = useRef<number | undefined>(undefined)
  const peekKey = useRef('')

  // Unmount: the pills' object URLs go with the composer. Bounded either way,
  // but a screenshot is a megabyte and a thread can collect a dozen.
  useEffect(
    () => () => {
      window.clearTimeout(peekTimer.current)
      for (const url of shots.current.values()) URL.revokeObjectURL(url)
      shots.current.clear()
      shotsPending.current.clear()
    },
    [],
  )

  /** Put the pills in the editable itself, at the caret — the whole point of
   *  a contenteditable. Inserted by hand, never by React: the framework
   *  reconciling children inside an editable would fight the text nodes the
   *  user's own typing puts there. Shared by uploaded files and workspace
   *  references, so the two look and behave identically. */
  function insertChips(added: PendingAttachment[]): void {
    const el = composer.current
    if (!el) return
    el.focus()
    for (const p of added) {
      const chip = makeChipNode(p)
      const sel = el.ownerDocument.getSelection()
      if (sel && sel.rangeCount > 0) {
        const range = sel.getRangeAt(0)
        range.deleteContents()
        range.insertNode(chip)
        range.setStartAfter(chip)
        range.collapse(true)
        sel.removeAllRanges()
        sel.addRange(range)
      } else {
        el.appendChild(chip)
      }
      // A trailing space keeps the pill from welding to whatever follows.
      const gap = el.ownerDocument.createTextNode(' ')
      range_or_append(chip, gap)
    }
    setInput(editorTextOf(el))
    setCaret(editorCaretOffset(el))
    autoGrow(el)
  }

  /** Take pasted, dropped or picked files into the tray. The tile appears at
   *  once — the bytes themselves go up at send, when the thread they belong to
   *  is known and a brand-new chat has one to upload into. */
  function addFiles(files: File[]): void {
    if (files.length === 0) return
    const added: PendingAttachment[] = files.map((file) => ({
      key: localMsgId(),
      name: file.name || t('sessions.attachPasted'),
      size: file.size,
      image: isImageFile(file),
      file,
    }))
    setPending((ps) => [...ps, ...added])
    insertChips(added)
  }

  /** The files dock's 插入: a workspace path becomes the same atomic pill an
   *  attached file gets, instead of a bare filename typed into the prompt.
   *  A `:line` suffix stays in the label (and on the sent bubble) but is not
   *  part of the path expandFileRefs reads at send. */
  function addFileRef(raw: string): void {
    const { path, label } = splitFileRef(raw)
    if (!path) return
    const added: PendingAttachment = {
      key: localMsgId(),
      name: label,
      size: 0,
      image: isImageFile({ name: path }),
      ref: path,
    }
    setPending((ps) => [...ps, added])
    insertChips([added])
  }

  function removePending(key: string): void {
    setPending((ps) => ps.filter((p) => p.key !== key))
    composer.current?.querySelector(`.attach-pill[data-pk="${key}"]`)?.remove()
    forgetShot(key)
    unpeek()
    const el = composer.current
    if (el) {
      setInput(editorTextOf(el))
      autoGrow(el)
    }
  }

  /** The bytes behind a pending image: minted from the File the paste or drop
   *  handed us, fetched for a workspace reference. Minted once per pill and
   *  kept, because a peek, a lightbox and a second peek all want the same
   *  object URL — and revoked by forgetShot when the pill goes away. */
  function shotSrc(p: PendingAttachment): Promise<string> {
    const made = shots.current.get(p.key)
    if (made) return Promise.resolve(made)
    const inflight = shotsPending.current.get(p.key)
    if (inflight) return inflight
    const bytes = p.file
      ? Promise.resolve(URL.createObjectURL(p.file))
      : p.ref
        ? fetchBlob(`/api/fs/image?path=${encodeURIComponent(p.ref)}`).then((b) => URL.createObjectURL(b))
        : Promise.reject(new Error('no bytes'))
    const job = bytes.then((url) => {
      shots.current.set(p.key, url)
      shotsPending.current.delete(p.key)
      return url
    })
    // A failed fetch must not poison the key: a retry should ask again.
    job.catch(() => shotsPending.current.delete(p.key))
    shotsPending.current.set(p.key, job)
    return job
  }

  function forgetShot(key: string): void {
    const url = shots.current.get(key)
    if (!url) return
    shots.current.delete(key)
    shotsPending.current.delete(key)
    URL.revokeObjectURL(url)
  }

  /** The pointer came to rest on a pill: after a beat, float a larger copy
   *  above it — the same dwell the transcript's pills use, so a pointer only
   *  crossing the composer never flashes a picture.
   *
   *  The copy clears the composer box rather than the pill: the pill sits at
   *  the top of the field, so anchoring to it would drop the picture over the
   *  sentence being typed. Centred on the pill, resting on the box's top
   *  edge, it covers the transcript instead — the one place a reader is not
   *  looking at that moment. */
  function peekPill(chip: HTMLElement): void {
    const p = pending.find((x) => x.key === chip.dataset.pk)
    if (!p || !p.image) return
    window.clearTimeout(peekTimer.current)
    peekKey.current = p.key
    peekTimer.current = window.setTimeout(() => {
      const pill = chip.getBoundingClientRect()
      const box = composer.current?.closest('.composer-box')?.getBoundingClientRect()
      const anchor = box ? new DOMRect(pill.left, box.top, pill.width, 0) : pill
      void shotSrc(p)
        .then((src) => {
          // The pointer may have moved on, or the pill been taken out, while
          // the bytes were in flight.
          if (peekKey.current !== p.key) return
          setPeek({ key: p.key, name: p.name, src, anchor })
        })
        .catch(() => {
          /* unreadable reference: no floating copy, and no error either */
        })
    }, 160)
  }

  function unpeek(): void {
    window.clearTimeout(peekTimer.current)
    peekKey.current = ''
    setPeek(null)
  }

  /** The magnifier, and a click on the pill's body: the picture at full size.
   *  A reference the panel cannot read simply does not open. */
  function zoomPill(chip: HTMLElement): void {
    const p = pending.find((x) => x.key === chip.dataset.pk)
    if (!p || !p.image) return
    void shotSrc(p)
      .then((src) => {
        unpeek()
        setShot({ name: p.name, src, path: p.ref })
      })
      .catch(() => {
        /* no bytes, nothing to enlarge */
      })
  }

  /** Drop pending entries whose pill is no longer in the editor. */
  function syncPending(el: HTMLElement): void {
    const keys = livePillKeys(el)
    setPending((ps) => {
      if (!ps.some((p) => !keys.has(p.key))) return ps
      // The pill is already gone from the DOM, so nothing else will release
      // its object URL — Backspace would otherwise leak one per screenshot.
      for (const p of ps) if (!keys.has(p.key)) forgetShot(p.key)
      return ps.filter((p) => keys.has(p.key))
    })
    // A node taken out by Backspace never reports a mouseout, so the copy
    // floating over it would stay up with nothing under it.
    if (peekKey.current && !keys.has(peekKey.current)) unpeek()
  }

  function clearPending(): void {
    for (const key of [...shots.current.keys()]) forgetShot(key)
    unpeek()
    setPending([])
    editorClear(composer.current)
  }

  return {
    pending, setPending, peek, shot, setShot,
    addFiles, addFileRef, removePending, syncPending, clearPending,
    peekPill, unpeek, zoomPill,
  }
}

/** The pill keys actually in the editor right now. The DOM is authoritative:
 *  Backspace or a selection-delete takes the node out without asking, so
 *  the tray drops whatever lost its pill rather than upload a file the user
 *  took back out of the turn. */
export function livePillKeys(el: HTMLElement): Set<string> {
  const keys = new Set<string>()
  el.querySelectorAll('.attach-pill').forEach((n) => {
    const k = (n as HTMLElement).dataset.pk
    if (k) keys.add(k)
  })
  return keys
}

/** `dragover` fires continuously while a file is over the element, and the
 *  drop only happens if this one calls preventDefault — the default action
 *  is the browser navigating away to open the file. */
export function dragHasFiles(e: DragEvent): boolean {
  return [...(e.dataTransfer?.types ?? [])].includes('Files')
}
