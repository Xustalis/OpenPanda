import { useEffect, useState } from 'preact/hooks'
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
export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([])

  useEffect(() => {
    push = (item) => {
      setItems((cur) => [...cur, item])
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
    <div class="toast-host" role="status" aria-live="polite">
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
