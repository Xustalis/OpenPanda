// SPDX-License-Identifier: AGPL-3.0-or-later

// The transcript's read-only file drawer.
//
// Clicking a path in a reply opens this beside the chat: the file the agent
// named, scrolled to the line it named, with the surrounding window and nothing
// that can change it. Editing is deliberately absent — the console is not an
// IDE, and `panda edit path:line` is one keystroke away in the terminal.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type FsFileResult } from '../api/client'
import { t } from '../i18n'

/** How many lines of context the drawer asks for on each side of a focus
 *  line, and how much of the file it shows when there is no focus line. */
const FOCUS_CONTEXT = 60
const WHOLE_FILE_LINES = 400

export function FileViewer({
  path,
  line,
  session,
  onClose,
  onInsert,
}: {
  path: string
  /** 1-based focus line, or 0 for the head of the file. */
  line: number
  /** The thread whose worktree a relative path is read against. */
  session?: string | null
  onClose(): void
  /** Put a reference back into the composer; absent leaves the action out. */
  onInsert?: (ref: string) => void
}) {
  const [view, setView] = useState<FsFileResult | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let live = true
    setView(null)
    setErr('')
    setCopied(false)
    api
      .fsFile({
        path,
        session: session ?? undefined,
        line: line > 0 ? line : undefined,
        count: line > 0 ? FOCUS_CONTEXT : WHOLE_FILE_LINES,
      })
      .then((r) => {
        if (live) setView(r)
      })
      .catch((e: unknown) => {
        if (live) setErr(e instanceof Error ? e.message : String(e))
      })
    return () => {
      live = false
    }
  }, [path, line, session])

  // Bring the focus line into view once the file lands. A 60-line window often
  // fits, but a narrow pane does not scroll to it on its own.
  useEffect(() => {
    const el = bodyRef.current?.querySelector('.is-focus')
    if (el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'center' })
    }
  }, [view])

  async function copy() {
    const ref = line > 0 ? `${displayPath()}:${line}` : displayPath()
    try {
      await navigator.clipboard.writeText(ref)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard denied: the path is selectable in the header either way.
    }
  }

  function displayPath(): string {
    return view?.rel || view?.path || path
  }

  const lines = view?.content ? view.content.split('\n') : []
  const focus = line > 0 ? line : 0

  return (
    <aside class="file-viewer" aria-label={t('fileView.title')}>
      <header class="fv-head">
        <div class="fv-title">
          <span class="fv-name">{displayPath()}</span>
          {focus > 0 && <span class="fv-line-no">:{focus}</span>}
        </div>
        <div class="fv-actions">
          <button type="button" class="fv-btn" onClick={copy}>
            {copied ? `✓ ${t('md.copied')}` : t('fileView.copy')}
          </button>
          {onInsert && (
            <button
              type="button"
              class="fv-btn"
              onClick={() => onInsert(focus > 0 ? `${displayPath()}:${focus}` : displayPath())}
            >
              {t('fileView.insert')}
            </button>
          )}
          <button type="button" class="fv-btn fv-close" onClick={onClose} aria-label={t('fileView.close')}>
            ✕
          </button>
        </div>
      </header>

      {err !== '' && <p class="fv-note is-error">{err}</p>}

      {view?.binary && <p class="fv-note">{t('fileView.binary')}</p>}

      {view && !view.binary && lines.length > 0 && (
        <div class="fv-body" ref={bodyRef}>
          <pre class="fv-code">
            {lines.map((text, i) => {
              const n = view.start + i
              return (
                <div key={n} class={n === focus ? 'fv-row is-focus' : 'fv-row'}>
                  <span class="fv-num">{n}</span>
                  <span class="fv-text">{text}</span>
                </div>
              )
            })}
          </pre>
        </div>
      )}

      {view && !view.binary && lines.length === 0 && err === '' && (
        <p class="fv-note">{t('fileView.empty')}</p>
      )}

      {view && (view.truncated || view.total_lines > 0) && (
        <footer class="fv-foot">
          {view.total_lines > 0
            ? t('fileView.linesOf', { shown: String(lines.length), total: String(view.total_lines) })
            : t('fileView.truncated')}
        </footer>
      )}
    </aside>
  )
}
