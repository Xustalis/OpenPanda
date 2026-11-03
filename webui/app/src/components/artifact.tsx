// SPDX-License-Identifier: AGPL-3.0-or-later

// ArtifactSection surfaces a task's output artifact inside the orbit: the
// file list from /api/tasks/{id}/artifact/files, inline previews for
// images and text, and downloads for everything — the web half of
// `panda task artifact`. Every byte moves through fetchBlob because the
// Bearer token lives in a header a plain <a href> cannot send.

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { api, fetchBlob, ArtifactFileList, Task } from '../api/client'

const IMAGE_RE = /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i
const TEXT_RE = /\.(txt|md|log|json|ya?ml|csv|xml|go|py|ts|tsx|jsx|js|sh|css|html?)$/i

function fmtSize(n: number): string {
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)} MB`
  if (n >= 1_024) return `${(n / 1_024).toFixed(1)} KB`
  return `${n} B`
}

function baseName(path: string): string {
  const i = path.lastIndexOf('/')
  return i >= 0 ? path.slice(i + 1) : path
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

export function ArtifactSection(props: { task: Task; onPreview?: (task: Task) => void }) {
  const hash: string = props.task.plan_meta?.output_artifact ?? ''
  const taskID = props.task.id
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [list, setList] = useState<ArtifactFileList | null>(null)
  const [err, setErr] = useState('')
  const [preview, setPreview] = useState<{ path: string; kind: 'image' | 'text'; url?: string; text?: string } | null>(null)
  const [remote, setRemote] = useState<{ source?: string; canFetch: boolean } | null>(null)
  const [pulling, setPulling] = useState(false)
  const previewURL = useRef('')

  useEffect(
    () => () => {
      if (previewURL.current) URL.revokeObjectURL(previewURL.current)
    },
    [],
  )

  if (!hash || !taskID) return null

  const fileURL = (p: string, download = false) =>
    `/api/tasks/${encodeURIComponent(taskID)}/artifact/file?path=${encodeURIComponent(p)}${download ? '&download=1' : ''}`

  function clearPreview() {
    if (previewURL.current) {
      URL.revokeObjectURL(previewURL.current)
      previewURL.current = ''
    }
    setPreview(null)
  }

  async function toggle() {
    if (open) {
      clearPreview()
      setOpen(false)
      return
    }
    setOpen(true)
    if (list || loading) return
    setLoading(true)
    setErr('')
    try {
      setList(await api.taskArtifactFiles(taskID))
    } catch (e) {
      // The archive is still on the executor. Ask the preview endpoint where
      // it lives and whether this node can pull it, instead of showing a raw
      // 404 that reads like the task produced nothing.
      if (await detectRemote()) return
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  async function detectRemote(): Promise<boolean> {
    try {
      const meta = await api.previewTaskArtifact(taskID)
      if (meta.state === 'remote') {
        setRemote({ source: meta.source, canFetch: Boolean(meta.can_fetch) })
        return true
      }
    } catch {
      // The preview endpoint may be off; keep the original error.
    }
    return false
  }

  async function pull() {
    setPulling(true)
    setErr('')
    try {
      await api.fetchTaskArtifact(taskID)
      setRemote(null)
      setList(await api.taskArtifactFiles(taskID))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setPulling(false)
    }
  }

  async function openEntry(path: string) {
    setErr('')
    try {
      if (IMAGE_RE.test(path)) {
        const blob = await fetchBlob(fileURL(path))
        const url = URL.createObjectURL(blob)
        if (previewURL.current) URL.revokeObjectURL(previewURL.current)
        previewURL.current = url
        setPreview({ path, kind: 'image', url })
      } else if (TEXT_RE.test(path)) {
        const blob = await fetchBlob(fileURL(path))
        const text = await blob.text()
        setPreview({ path, kind: 'text', text: text.slice(0, 64_000) })
      } else {
        const blob = await fetchBlob(fileURL(path, true))
        saveBlob(blob, baseName(path))
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }

  async function downloadArchive() {
    setErr('')
    try {
      if (remote) {
        await api.fetchTaskArtifact(taskID)
        setRemote(null)
      }
      const blob = await fetchBlob(`/api/tasks/${encodeURIComponent(taskID)}/artifact`)
      saveBlob(blob, `task-${taskID}-${hash.slice(0, 12)}.tar.gz`)
    } catch (e) {
      if (await detectRemote()) {
        setOpen(true)
        return
      }
      setErr(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div class="artifact-section">
      <div class="artifact-bar">
        <button type="button" class="orbit-action-btn" onClick={toggle} aria-expanded={open}>
          {t('artifact.open')} · {hash.slice(0, 12)} {open ? '▴' : '▾'}
        </button>
        <button type="button" class="orbit-action-btn" onClick={downloadArchive}>
          {t('artifact.downloadArchive')}
        </button>
        {/* The page view: the artifact rendered as it was built, on the
            separate preview origin (see dock-browser.tsx). The dock is the one
            preview surface — a modal over the console and a column beside it
            would teach the reader two different places for the same page. */}
        {props.onPreview && (
          <button type="button" class="orbit-action-btn" onClick={() => props.onPreview?.(props.task)}>
            {t('artifactPreview.open')}
          </button>
        )}
      </div>
      {open && (
        <div class="artifact-body">
          {loading && <div class="muted">{t('artifact.loading')}</div>}
          {err && <div class="artifact-error">{err}</div>}
          {remote && (
            <div class="artifact-remote">
              <p>{t('artifact.remote', { source: remote.source ?? '?' })}</p>
              {remote.canFetch ? (
                <button type="button" class="orbit-action-btn" disabled={pulling} onClick={pull}>
                  {pulling ? t('artifact.pulling') : t('artifact.pull')}
                </button>
              ) : (
                <p class="muted">{t('artifact.noFetch', { source: remote.source ?? '?' })}</p>
              )}
            </div>
          )}
          {list && (list.entries ?? []).length === 0 && <div class="muted">{t('artifact.empty')}</div>}
          {list && (list.entries ?? []).length > 0 && (
            <ul class="artifact-list">
              {(list.entries ?? []).map((e) => (
                <li key={e.path}>
                  <button
                    type="button"
                    class="artifact-entry"
                    title={e.path}
                    onClick={() => openEntry(e.path)}
                  >
                    <span class="artifact-name">{baseName(e.path)}</span>
                    <span class="muted artifact-size">{fmtSize(e.size)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {preview && (
            <div class="artifact-preview">
              <div class="artifact-preview-head">
                <span class="mono">{preview.path}</span>
                <button type="button" class="orbit-action-btn" onClick={clearPreview}>×</button>
              </div>
              {preview.kind === 'image'
                ? <img class="artifact-preview-img" src={preview.url} alt={preview.path} />
                : <pre class="artifact-preview-text">{preview.text}</pre>}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
