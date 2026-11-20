// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { useModalFocus } from './modal-focus'

// Global confirm dialog (P1: "危险操作二次确认"). Promise-based so call
// sites read like `if (!(await confirmDialog({...}))) return` — no local
// open-state plumbing per view. One host mounts in the app shell.

/** A checkbox carried by the confirm dialog. A destructive ask often has a
 *  secondary decision attached (keep the memory file? delete the sessions
 *  too?); asking it here is what keeps the answer and the action in one place
 *  instead of splitting them across a form the user has to remember. */
export interface ConfirmChoice {
  /** Stable key, echoed back in the result map. */
  key: string
  label: string
  /** Second line under the label — consequence, count, path. */
  hint?: string
  /** Initial state; unchecked when omitted. */
  checked?: boolean
}

export interface ConfirmOptions {
  /** Headline — what the operation is, e.g. "删除对话". */
  title: string
  /** Consequence-focused body, e.g. "工作树与分支将被一并删除，无法恢复。". */
  message: string
  /** Confirm button label; defaults to common.confirm. */
  confirmLabel?: string
  /** Danger styling for destructive operations (default true). */
  danger?: boolean
  /** Optional checkboxes; read them back with confirmWithChoices. */
  choices?: ConfirmChoice[]
}

export interface ConfirmResult {
  /** False when dismissed, cancelled, or unanswered — never act on it. */
  ok: boolean
  /** Checkbox state at confirm time, keyed by ConfirmChoice.key. */
  choices: Record<string, boolean>
}

let ask: ((opts: ConfirmOptions) => Promise<ConfirmResult>) | null = null

/** Ask the user to confirm. Resolves false when dismissed or cancelled —
 *  and also when no host is mounted: a destructive action must default to
 *  "no" rather than silently proceeding. */
export async function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return (await ask?.(opts))?.ok ?? false
}

/** Confirm with choices. Same dialog, but the caller also gets the checkbox
 *  state — and gets nothing (`ok: false`) unless the user actually confirmed. */
export function confirmWithChoices(opts: ConfirmOptions): Promise<ConfirmResult> {
  return ask ? ask(opts) : Promise.resolve({ ok: false, choices: {} })
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
    resolve: (r: ConfirmResult) => void
  } | null>(null)
  // Choice state lives beside the pending ask, not inside it: the caller owns
  // the defaults (opts.choices), the user owns the toggles.
  const [choices, setChoices] = useState<Record<string, boolean>>({})
  const box = useRef<HTMLDivElement>(null)
  const sheet = useRef<HTMLDialogElement>(null)

  // Focus trap: focus moves into the dialog on open, Tab cycles inside, and
  // closing restores the opener's focus. Cancel takes the initial focus even
  // when checkboxes sit above the actions — the safe answer is the one a
  // stray Enter picks.
  useModalFocus(box, current !== null, '.confirm-cancel')

  useEffect(() => {
    const el = sheet.current
    if (!el || el.open) return
    el.showModal()
  }, [current])

  useEffect(() => {
    ask = (opts) =>
      new Promise<ConfirmResult>((resolve) => {
        setChoices(
          Object.fromEntries((opts.choices ?? []).map((c) => [c.key, c.checked ?? false])),
        )
        setCurrent({ opts, resolve })
      })
    return () => {
      ask = null
    }
  }, [])

  // Resolve pending promises on unmount so callers never hang.
  useEffect(() => {
    return () => current?.resolve({ ok: false, choices: {} })
  }, [current])

  function done(ok: boolean) {
    current?.resolve({ ok, choices })
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
        {opts.choices && opts.choices.length > 0 && (
          <div class="confirm-choices">
            {opts.choices.map((c) => (
              <label class="confirm-choice" key={c.key}>
                <input
                  type="checkbox"
                  class="check"
                  checked={choices[c.key] ?? false}
                  onChange={(e) => {
                    const on = (e.target as HTMLInputElement).checked
                    setChoices((prev) => ({ ...prev, [c.key]: on }))
                  }}
                />
                <span>
                  <span class="confirm-choice-label">{c.label}</span>
                  {c.hint && <span class="confirm-choice-hint">{c.hint}</span>}
                </span>
              </label>
            ))}
          </div>
        )}
        <div class="modal-actions">
          <button class="btn confirm-cancel" onClick={() => done(false)}>
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
