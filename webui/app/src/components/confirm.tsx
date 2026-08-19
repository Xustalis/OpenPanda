import { useEffect, useState } from 'preact/hooks'
import { t } from '../i18n'

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

/** Ask the user to confirm. Resolves false when dismissed or cancelled. */
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return ask ? ask(opts) : Promise.resolve(true)
}

/** Mount once in the app shell. */
export function ConfirmHost() {
  const [current, setCurrent] = useState<{
    opts: ConfirmOptions
    resolve: (v: boolean) => void
  } | null>(null)

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
    <div
      class="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) done(false)
      }}
    >
      <div class="modal" role="alertdialog" aria-modal="true" aria-label={opts.title}>
        <h2 class="modal-title">{opts.title}</h2>
        <p class="modal-msg">{opts.message}</p>
        <div class="modal-actions">
          <button class="btn" onClick={() => done(false)} autofocus>
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
    </div>
  )
}
