// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's file panel: a directory to walk, and the file you stopped on.
//
// Two reading modes share one window. Source files want line numbers, a focus
// line and enough colour to separate structure from prose; Markdown wants the
// opposite — headings, paragraphs, tables and fenced code as a document. A
// .md file opens rendered, with a source toggle for when the line numbers are
// the point (a transcript's `README.md:42` link lands on the source view).
// The Markdown itself goes through the chat's vnode renderer, so the panel
// gets the same parser, copy buttons and zero-HTML policy as a reply.
//
// The view stays read-only on purpose. Editing belongs to `panda edit`, and a
// console that can write to a worktree while an agent is working in it is a
// merge conflict with a text box.

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, fetchBlob, type FsFileEntry, type FsFileResult } from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import {
  baseName,
  docHeadings,
  extOf,
  highlightLines,
  isDocFile,
  prepareDocText,
  resolveDocPath,
  slugOf,
  sourceLangOf,
} from './dock-file-format'
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
        onNavigate={(path, line) => setFile({ path, line })}
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
            <Icon name={isDocFile(e.path) ? 'file-text' : 'code'} size={15} />
            <span class="df-name">{e.name}</span>
            {isDocFile(e.path) && <span class="df-kind mono">MD</span>}
            {typeof e.size === 'number' && <span class="df-size mono">{bytesOf(e.size)}</span>}
          </button>
        ))}
      </div>
    </div>
  )
}

type ViewMode = 'preview' | 'source'

interface Lightbox {
  url: string
  name: string
  alt: string
}

function DockFileView(props: {
  path: string
  line: number
  session: string | null
  onBack(): void
  /** A relative link inside a Markdown document; opens the file beside it. */
  onNavigate(path: string, line: number): void
  onInsert?: (ref: string) => void
}) {
  const doc = isDocFile(props.path)
  const lang = useMemo(() => sourceLangOf(props.path), [props.path])
  const ext = extOf(props.path)
  const [mode, setMode] = useState<ViewMode>(() => (doc && props.line === 0 ? 'preview' : 'source'))
  const [wrap, setWrap] = useState(true)
  const [view, setView] = useState<FsFileResult | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)
  const [lightbox, setLightbox] = useState<Lightbox | null>(null)
  const [imageError, setImageError] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  const lightboxUrl = useRef<string | null>(null)

  // A new file starts in its natural mode. A mode the reader picked is kept
  // while they stay on the same file (this effect does not depend on mode).
  useEffect(() => {
    setMode(doc && props.line === 0 ? 'preview' : 'source')
    setLightbox(null)
    setImageError(false)
  }, [doc, props.path, props.line, props.session])

  // A rendered document needs the whole window, not the ±80 lines a transcript
  // reference asked for; switching modes re-reads with the other window.
  useEffect(() => {
    let live = true
    setView(null)
    setErr('')
    const whole = doc && mode === 'preview'
    api
      .fsFile({
        path: props.path,
        session: props.session ?? undefined,
        line: whole ? undefined : props.line > 0 ? props.line : undefined,
        count: whole ? WHOLE_FILE_LINES : props.line > 0 ? FOCUS_CONTEXT : WHOLE_FILE_LINES,
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
  }, [props.path, props.line, props.session, doc, mode])

  // A focus line 80 lines into the window is still off screen in a narrow
  // column; bring it into view once the source lands.
  useEffect(() => {
    if (mode !== 'source') return
    const el = bodyRef.current?.querySelector('.is-focus')
    if (el && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'center' })
  }, [view, mode])

  // Object URLs are ours to release, both when the image changes and when the
  // panel unmounts with one open.
  useEffect(() => {
    const prev = lightboxUrl.current
    if (prev && prev !== (lightbox?.url ?? null)) URL.revokeObjectURL(prev)
    lightboxUrl.current = lightbox?.url ?? null
  }, [lightbox])
  useEffect(
    () => () => {
      if (lightboxUrl.current) URL.revokeObjectURL(lightboxUrl.current)
    },
    [],
  )

  const display = view?.rel || view?.path || props.path
  const focus = props.line > 0 ? props.line : 0
  const lines = useMemo(() => {
    if (!view?.content) return []
    const raw = view.content.split('\n').map((l) => l.replace(/\r$/, ''))
    if (!view.truncated && raw.length > 1 && raw[raw.length - 1] === '') raw.pop()
    return raw
  }, [view])
  const highlighted = useMemo(() => highlightLines(lines, lang), [lines, lang])
  const docText = useMemo(() => prepareDocText(view?.content ?? ''), [view])
  const headings = useMemo(() => docHeadings(view?.content ?? ''), [view])

  async function copy() {
    try {
      await navigator.clipboard.writeText(focus > 0 ? `${display}:${focus}` : display)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard denied: the path is selectable in the head either way.
    }
  }

  function scrollToHeading(index: number) {
    const heads = bodyRef.current?.querySelectorAll<HTMLElement>(
      '.df-doc h2, .df-doc h3, .df-doc h4, .df-doc h5, .df-doc h6',
    )
    heads?.[index]?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }

  async function openImage(target: string, label: string) {
    setImageError(false)
    const ref = resolveDocPath(view?.path || props.path, target)
    if (!ref) return
    try {
      const blob = await fetchBlob(`/api/fs/image?path=${encodeURIComponent(ref.path)}`)
      setLightbox({ url: URL.createObjectURL(blob), name: baseName(ref.path), alt: label })
    } catch {
      setImageError(true)
    }
  }

  function onDocClick(e: MouseEvent) {
    const a = (e.target as HTMLElement | null)?.closest?.('a') as HTMLAnchorElement | null
    if (!a) return
    const href = a.getAttribute('href') ?? ''
    if (href.startsWith('#file:')) {
      e.preventDefault()
      const ref = resolveDocPath(view?.path || props.path, decodeTarget(href.slice(6)))
      if (ref) props.onNavigate(ref.path, ref.line)
      return
    }
    if (href.startsWith('#image:')) {
      e.preventDefault()
      void openImage(decodeTarget(href.slice(7)), a.textContent || '')
      return
    }
    if (href.startsWith('#') && href.length > 1) {
      e.preventDefault()
      const slug = decodeTarget(href.slice(1))
      const index = headings.findIndex((h) => slugOf(h.text) === slug)
      if (index >= 0) scrollToHeading(index)
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
        <div class="fv-title" title={display}>
          <span class="fv-name">{baseName(display)}</span>
          {focus > 0 && <span class="fv-line-no">:{focus}</span>}
        </div>
        <span class="df-badge mono">{ext ? ext.toUpperCase() : 'TXT'}</span>
        <div class="fv-actions">
          <button type="button" class="fv-btn" onClick={copy} title={t('fileView.copy')}>
            <Icon name={copied ? 'check' : 'copy'} size={14} />
            <span class="fv-btn-label">{copied ? `✓ ${t('md.copied')}` : t('fileView.copy')}</span>
          </button>
          {props.onInsert && (
            <button
              type="button"
              class="fv-btn"
              onClick={() => props.onInsert?.(focus > 0 ? `${display}:${focus}` : display)}
              title={t('fileView.insert')}
            >
              <Icon name="arrow-right" size={14} />
              <span class="fv-btn-label">{t('fileView.insert')}</span>
            </button>
          )}
        </div>
      </header>

      {doc && (
        <div class="df-tools">
          <div class="df-seg" role="group" aria-label={t('fileView.title')}>
            <button
              type="button"
              class={`df-seg-btn${mode === 'preview' ? ' on' : ''}`}
              aria-pressed={mode === 'preview'}
              title={t('fileView.preview')}
              onClick={() => setMode('preview')}
            >
              <Icon name="eye" size={14} />
              <span>{t('fileView.preview')}</span>
            </button>
            <button
              type="button"
              class={`df-seg-btn${mode === 'source' ? ' on' : ''}`}
              aria-pressed={mode === 'source'}
              title={t('fileView.source')}
              onClick={() => setMode('source')}
            >
              <Icon name="code" size={14} />
              <span>{t('fileView.source')}</span>
            </button>
          </div>
          <span class="df-tools-spacer" />
          {mode === 'source' && (
            <button
              type="button"
              class={`df-tool${wrap ? ' on' : ''}`}
              aria-pressed={wrap}
              title={t('fileView.wrap')}
              aria-label={t('fileView.wrap')}
              onClick={() => setWrap(!wrap)}
            >
              <span class="df-wrap-glyph mono">↵</span>
            </button>
          )}
        </div>
      )}

      {err !== '' && <p class="df-note is-error">{err}</p>}
      {imageError && <p class="df-note is-error">{t('fileView.imageError')}</p>}
      {view?.binary && <p class="df-note">{t('fileView.binary')}</p>}

      {view && !view.binary && lines.length > 0 && mode === 'preview' && doc && (
        <div class="df-body is-doc" ref={bodyRef} onClick={onDocClick}>
          {headings.length >= 3 && (
            <details class="df-toc" open>
              <summary>
                <Icon name="list-checks" size={13} />
                <span>{t('fileView.toc')}</span>
                <span class="df-toc-count mono">{headings.length}</span>
              </summary>
              <nav>
                {headings.slice(0, 40).map((h, i) => (
                  <button
                    key={i}
                    type="button"
                    class={`df-toc-item is-l${Math.min(h.level, 4)}`}
                    title={h.text}
                    onClick={() => scrollToHeading(i)}
                  >
                    {h.text}
                  </button>
                ))}
              </nav>
            </details>
          )}
          <article class="df-doc">
            <Markdown text={docText} class="df-md" />
          </article>
        </div>
      )}

      {view && !view.binary && lines.length > 0 && (mode === 'source' || !doc) && (
        <div class={`df-body${wrap ? ' is-wrap' : ''}`} ref={bodyRef}>
          <pre class="df-code" data-kind={lang.kind}>
            {lines.map((text, i) => {
              const n = (view.start || 1) + i
              const tokens = highlighted[i] ?? []
              return (
                <div key={n} class={n === focus ? 'df-line is-focus' : 'df-line'}>
                  <span class="df-num">{n}</span>
                  <span class="df-text">
                    {tokens.length > 0
                      ? tokens.map((tok, j) => (
                          <span key={j} class={tok.kind === 'plain' ? undefined : `df-tk-${tok.kind}`}>
                            {tok.text}
                          </span>
                        ))
                      : text}
                  </span>
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
          <span>
            {view.total_lines > 0
              ? t('fileView.linesOf', { shown: String(lines.length), total: String(view.total_lines) })
              : t('fileView.truncated')}
          </span>
          {view.truncated && view.total_lines > 0 && (
            <span class="df-foot-more">{t('fileView.truncated')}</span>
          )}
        </footer>
      )}

      {lightbox && (
        <div class="df-lightbox" role="presentation" onClick={() => setLightbox(null)}>
          <div class="df-lightbox-card" onClick={(e) => e.stopPropagation()}>
            <div class="df-lightbox-head">
              <span class="mono">{lightbox.name}</span>
              <button
                type="button"
                class="icon-btn"
                title={t('fileView.close')}
                aria-label={t('fileView.close')}
                onClick={() => setLightbox(null)}
              >
                <Icon name="x" size={15} />
              </button>
            </div>
            <img src={lightbox.url} alt={lightbox.alt} />
          </div>
        </div>
      )}
    </div>
  )
}

/** A link target is percent-encoded when rewritten; a malformed sequence must
 *  not take the click handler down. */
function decodeTarget(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

function bytesOf(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}
