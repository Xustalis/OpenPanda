// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { useModalFocus } from './modal-focus'

// Global confirm dialog (P1: "危险操作二次确认"). Promise-based so call
// sites read like `if (!(await confirmDialog({...}))) return` — no local
// open-state plumbing per view. One host mounts in the app shell.

export interface ConfirmOptions {
  /** Headline — what the operation is, e.g. "删除对话". */
  title: string
  /** Consequence-focused body, e.g. "工作树与分支将被一并删除，无法恢复。". */
  message: string
  /** Confirm button label; defaults to common.confirm. */
  confirmLabel?: string
  /** Danger styling for destructive operations (default true). */
  danger?: boolean
}

let ask: ((opts: ConfirmOptions) => Promise<boolean>) | null = null

/** Ask the user to confirm. Resolves false when dismissed or cancelled —
 *  and also when no host is mounted: a destructive action must default to
 *  "no" rather than silently proceeding. */
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return ask ? ask(opts) : Promise.resolve(false)
}

/** Mount once in the app shell.
 *
 *  A native `<dialog>` opened with `showModal()`: a confirmation can be raised
 *  from anywhere, including from inside another sheet (设置 → 记忆 asks before
 *  deleting a topic), and only the top layer guarantees it paints over that
 *  sheet instead of behind it. */
export function ConfirmHost() {
  const [current, setCurrent] = useState<{
    opts: ConfirmOptions
    resolve: (v: boolean) => void
  } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const sheet = useRef<HTMLDialogElement>(null)

  // Focus trap: focus moves into the dialog on open (the cancel button is
  // the first focusable, which is the safe default for a destructive ask),
  // Tab cycles inside, and closing restores the opener's focus.
  useModalFocus(box, current !== null)

  useEffect(() => {
    const el = sheet.current
    if (!el || el.open) return
    el.showModal()
  }, [current])

  useEffect(() => {
    ask = (opts) =>
      new Promise<boolean>((resolve) => {
        setCurrent({ opts, resolve })
      })
    return () => {
      ask = null
    }
  }, [])

  // Resolve pending promises on unmount so callers never hang.
  useEffect(() => {
    return () => current?.resolve(false)
  }, [current])

  function done(v: boolean) {
    current?.resolve(v)
    setCurrent(null)
  }

  if (!current) return null
  const { opts } = current

  return (
    <dialog
      ref={sheet}
      class="modal confirm-modal"
      role="alertdialog"
      aria-label={opts.title}
      onCancel={(e) => {
        // Escape is a legitimate "no" here — unlike 设置, this dialog's whole
        // job is a yes/no, and the safe answer is the one a stray keypress
        // picks.
        e.preventDefault()
        done(false)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Escape') {
          e.stopPropagation()
          done(false)
        }
      }}
    >
      <div ref={box}>
        <h2 class="modal-title">{opts.title}</h2>
        <p class="modal-msg">{opts.message}</p>
        <div class="modal-actions">
          <button class="btn" onClick={() => done(false)}>
            {t('common.cancel')}
          </button>
          <button
            class={`btn ${opts.danger === false ? 'primary' : 'danger'}`}
            onClick={() => done(true)}
          >
            {opts.confirmLabel ?? t('common.confirm')}
          </button>
        </div>
      </div>
    </dialog>
  )
}
