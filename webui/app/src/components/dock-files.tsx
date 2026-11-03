// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's file panel: a directory to walk, and the file you stopped on.
//
// The transcript's drawer could show one file and nothing around it — a path
// arrived, the file appeared, the path was the whole navigation. Beside a
// thread the useful unit is the tree: the agent says "internal/bus/route.go",
// and the next question is always "what else is in there". /api/fs/files has
// answered that since the composer's file picker needed it; nothing in the
// console had asked it yet.
//
// The view stays read-only on purpose. Editing belongs to `panda edit`, and a
// console that can write to a worktree while an agent is working in it is a
// merge conflict with a text box.

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, type FsFileEntry, type FsFileResult } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'

/** Lines of context the panel asks for on either side of a focus line, and how
 *  much of the file it shows when the reader is just looking. */
const FOCUS_CONTEXT = 80
const WHOLE_FILE_LINES = 600

export interface DockFilesProps {
  /** The thread whose worktree relative paths resolve against. */
  session: string | null
  /** A file the transcript asked for. Changing it opens that file; the panel
   *  keeps its own directory otherwise. */
  request: { path: string; line: number } | null
  /** Bumped by the panel's `+`: back to the workspace root. */
  resetSignal?: number
  /** Put a reference back into the composer; absent leaves the action out. */
  onInsert?: (ref: string) => void
}

export function DockFiles(props: DockFilesProps) {
  const [dir, setDir] = useState('')
  const [entries, setEntries] = useState<FsFileEntry[] | null>(null)
  const [root, setRoot] = useState('')
  const [err, setErr] = useState('')
  const [file, setFile] = useState<{ path: string; line: number } | null>(null)

  // A transcript click wins over whatever the panel was showing. The request
  // object is rebuilt per click, so clicking the same path twice re-opens it.
  useEffect(() => {
    if (props.request) setFile(props.request)
  }, [props.request])

  // The panel's `+` is "start over here": root of the tree, nothing open.
  useEffect(() => {
    if (!props.resetSignal) return
    setFile(null)
    setDir('')
  }, [props.resetSignal])

  useEffect(() => {
    let live = true
    setErr('')
    api
      .fsFiles(dir || undefined)
      .then((r) => {
        if (!live) return
        setEntries(r.entries ?? [])
        setRoot((prev) => prev || r.current)
      })
      .catch((e: unknown) => {
        if (!live) return
        setErr(e instanceof Error ? e.message : String(e))
        setEntries([])
      })
    return () => {
      live = false
    }
  }, [dir])

  const parentOf = (path: string): string => {
    const clean = path.replace(/\/+$/, '')
    const i = clean.lastIndexOf('/')
    return i <= 0 ? '' : clean.slice(0, i)
  }

  const dirs = useMemo(() => (entries ?? []).filter((e) => e.dir), [entries])
  const files = useMemo(() => (entries ?? []).filter((e) => !e.dir), [entries])

  if (file) {
    return (
      <DockFileView
        path={file.path}
        line={file.line}
        session={props.session}
        onBack={() => setFile(null)}
        onInsert={props.onInsert}
      />
    )
  }

  return (
    <div class="dock-files">
      <header class="df-bar">
        <button
          type="button"
          class="icon-btn df-up"
          title={t('dockFiles.up')}
          aria-label={t('dockFiles.up')}
          disabled={dir === ''}
          onClick={() => setDir(parentOf(dir))}
        >
          <Icon name="arrow-up" size={15} />
        </button>
        <span class="df-cwd mono" title={dir || root}>
          {dir || root || t('dockFiles.root')}
        </span>
      </header>

      {err !== '' && <p class="df-note is-error">{err}</p>}

      <div class="df-list">
        {entries !== null && entries.length === 0 && err === '' && (
          <p class="df-note dim">{t('dockFiles.empty')}</p>
        )}
        {dirs.map((e) => (
          <button
            key={e.path}
            type="button"
            class="df-row is-dir"
            onClick={() => setDir(e.path)}
            title={e.path}
          >
            <Icon name="folder" size={15} />
            <span class="df-name">{e.name}</span>
          </button>
        ))}
        {files.map((e) => (
          <button
            key={e.path}
            type="button"
            class="df-row"
            onClick={() => setFile({ path: e.path, line: 0 })}
            title={e.path}
          >
            <Icon name="code" size={15} />
            <span class="df-name">{e.name}</span>
            {typeof e.size === 'number' && <span class="df-size mono">{bytesOf(e.size)}</span>}
          </button>
        ))}
      </div>
    </div>
  )
}

function DockFileView(props: {
  path: string
  line: number
  session: string | null
  onBack(): void
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
    api
      .fsFile({
        path: props.path,
        session: props.session ?? undefined,
        line: props.line > 0 ? props.line : undefined,
        count: props.line > 0 ? FOCUS_CONTEXT : WHOLE_FILE_LINES,
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
  }, [props.path, props.line, props.session])

  // A focus line 80 lines into the window is still off screen in a narrow
  // column; bring it into view once the text lands.
  useEffect(() => {
    const el = bodyRef.current?.querySelector('.is-focus')
    if (el && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'center' })
  }, [view])

  const display = view?.rel || view?.path || props.path
  const lines = view?.content ? view.content.split('\n') : []
  const focus = props.line > 0 ? props.line : 0

  async function copy() {
    try {
      await navigator.clipboard.writeText(focus > 0 ? `${display}:${focus}` : display)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard denied: the path is selectable in the head either way.
    }
  }

  return (
    <div class="dock-file">
      <header class="df-bar">
        <button
          type="button"
          class="icon-btn df-up"
          title={t('dockFiles.back')}
          aria-label={t('dockFiles.back')}
          onClick={props.onBack}
        >
          <Icon name="arrow-left" size={15} />
        </button>
        <span class="df-cwd mono" title={display}>
          {display}
          {focus > 0 && <span class="df-line-no">:{focus}</span>}
        </span>
        <button type="button" class="fv-btn" onClick={copy}>
          {copied ? `✓ ${t('md.copied')}` : t('fileView.copy')}
        </button>
        {props.onInsert && (
          <button
            type="button"
            class="fv-btn"
            onClick={() => props.onInsert?.(focus > 0 ? `${display}:${focus}` : display)}
          >
            {t('fileView.insert')}
          </button>
        )}
      </header>

      {err !== '' && <p class="df-note is-error">{err}</p>}
      {view?.binary && <p class="df-note">{t('fileView.binary')}</p>}

      {view && !view.binary && lines.length > 0 && (
        <div class="df-body" ref={bodyRef}>
          <pre class="df-code">
            {lines.map((text, i) => {
              const n = view.start + i
              return (
                <div key={n} class={n === focus ? 'df-line is-focus' : 'df-line'}>
                  <span class="df-num">{n}</span>
                  <span class="df-text">{text}</span>
                </div>
              )
            })}
          </pre>
        </div>
      )}

      {view && !view.binary && lines.length === 0 && err === '' && (
        <p class="df-note">{t('fileView.empty')}</p>
      )}

      {view && (view.truncated || view.total_lines > 0) && (
        <footer class="df-foot">
          {view.total_lines > 0
            ? t('fileView.linesOf', { shown: String(lines.length), total: String(view.total_lines) })
            : t('fileView.truncated')}
        </footer>
      )}
    </div>
  )
}

function bytesOf(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}
