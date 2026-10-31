// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'

// Global toast — the one place every view reports outcome feedback through
// (P1: "错误友好化 + 全局 toast"). Action errors and confirmations flow
// through here instead of each view improvising its own inline error
// paragraph, so feedback looks and behaves the same everywhere.

export type ToastKind = 'error' | 'success' | 'info'

interface ToastItem {
  id: number
  kind: ToastKind
  message: string
}

let nextId = 1
let push: ((item: ToastItem) => void) | null = null

/** Show a toast. Errors stay until dismissed (the user must know what broke);
 *  success/info auto-dismiss so they never linger as noise. */
export function toast(message: string, kind: ToastKind = 'info'): void {
  push?.({ id: nextId++, kind, message })
}

/** Show an error from an unknown catch — unwraps Error, caps length. */
export function toastError(e: unknown): void {
  const msg = e instanceof Error ? e.message : String(e)
  toast(msg.length > 300 ? msg.slice(0, 300) + '…' : msg, 'error')
}

/** Mount once in the app shell. */
export function ToastHost({ placement = 'bottom-right' }: { placement?: 'bottom-right' | 'top-center' }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const hostRef = useRef<HTMLDivElement>(null)

  // Settings is a modal <dialog>, which lives in the browser's top layer —
  // above every z-index, so a fixed host with z-index: 120 paints underneath
  // it and an error raised from inside Settings is invisible. The host is a
  // manual popover so it can join that layer, and every new toast re-shows it:
  // the most recently shown popover sits above a dialog opened earlier.
  const raise = () => {
    const el = hostRef.current
    if (!el) return
    try {
      if (el.matches(':popover-open')) el.hidePopover()
      el.showPopover()
    } catch {
      /* older engines: keep the plain fixed layer */
    }
  }

  useEffect(() => {
    push = (item) => {
      setItems((cur) => [...cur, item])
      raise()
      if (item.kind !== 'error') {
        setTimeout(() => {
          setItems((cur) => cur.filter((i) => i.id !== item.id))
        }, 3500)
      }
    }
    return () => {
      push = null
    }
  }, [])

  return (
    <div
      ref={hostRef}
      {...{ popover: 'manual' }}
      class={`toast-host toast-host-${placement}`}
      role="status"
      aria-live="polite"
    >
      {items.map((item) => (
        <div key={item.id} class={`toast toast-${item.kind}`}>
          <span class="toast-msg">{item.message}</span>
          {item.kind === 'error' && (
            <button
              class="toast-close"
              aria-label={t('common.close')}
              onClick={() => setItems((cur) => cur.filter((i) => i.id !== item.id))}
            >
              ×
            </button>
          )}
        </div>
      ))}
    </div>
  )
}
