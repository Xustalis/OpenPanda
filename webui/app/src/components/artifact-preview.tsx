// SPDX-License-Identifier: AGPL-3.0-or-later

// The artifact preview: a task's output rendered as a page, with the reader
// able to point at the part that is wrong.
//
// It is an iframe onto the preview origin (preview.go) and nothing else — the
// console never parses or re-renders task output. Scripts in the artifact run
// under a server-side CSP sandbox with no allow-same-origin, so they are in an
// opaque origin that holds no console credentials; the only channel back is a
// postMessage carrying a CSS selector, which becomes a draft the user edits
// before sending.

import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { api, type ArtifactPreviewMeta } from '../api/client'
import { t } from '../i18n'
import { useComposerInsert } from './composer-context'

/** What the injected picker reports about one clicked element. */
interface PreviewPick {
  selector: string
  text: string
  tag: string
}

export function ArtifactPreview({ taskId, onClose }: { taskId: string; onClose(): void }) {
  const insert = useComposerInsert()
  const [meta, setMeta] = useState<ArtifactPreviewMeta | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [pick, setPick] = useState<PreviewPick | null>(null)
  const [comment, setComment] = useState('')
  const frame = useRef<HTMLIFrameElement>(null)

  const load = useCallback(async () => {
    setErr('')
    try {
      setMeta(await api.previewTaskArtifact(taskId))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }, [taskId])

  useEffect(() => {
    void load()
  }, [load])

  // The picker talks back from inside the frame. The frame reference is the
  // check that holds: a sandboxed document has an OPAQUE origin, so the browser
  // reports event.origin as the string "null" rather than the preview origin —
  // verifying against the origin alone would drop every real pick. `source` is
  // the browser's own window reference and cannot be forged by a third page.
  useEffect(() => {
    const origin = meta?.origin
    function onMessage(e: MessageEvent) {
      if (!frame.current || e.source !== frame.current.contentWindow) return
      if (origin && e.origin !== 'null' && e.origin !== origin) return
      const data = e.data as { source?: string; selector?: string; text?: string; tag?: string } | null
      if (!data || data.source !== 'openpanda-preview' || !data.selector) return
      setPick({ selector: data.selector, text: data.text ?? '', tag: data.tag ?? '' })
      setComment('')
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [meta?.origin])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  async function pull() {
    setBusy(true)
    setErr('')
    try {
      await api.fetchTaskArtifact(taskId)
      await load()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function sendAnnotation() {
    if (!pick || !meta || !insert) return
    insert(
      t('artifactPreview.draft', {
        task: taskId,
        hash: meta.hash.slice(0, 12),
        selector: pick.selector,
        comment: comment.trim(),
      }),
    )
    onClose()
  }

  const isPDF = /\.pdf$/i.test(meta?.entry ?? '')

  return (
    <div class="art-preview" role="dialog" aria-modal="true" aria-label={t('artifactPreview.title')}>
      <header class="art-preview-head">
        <span class="art-preview-title">
          {t('artifactPreview.title')}
          {meta?.entry && <span class="muted mono"> · {meta.entry}</span>}
        </span>
        <button type="button" class="orbit-action-btn" onClick={onClose} aria-label={t('artifactPreview.close')}>
          ✕
        </button>
      </header>

      {err !== '' && <p class="artifact-error">{err}</p>}

      {meta?.state === 'remote' && (
        <div class="art-preview-remote">
          <p>{t('artifactPreview.remote', { source: meta.source ?? '?' })}</p>
          {meta.can_fetch ? (
            <button type="button" class="orbit-action-btn" disabled={busy} onClick={pull}>
              {busy ? t('artifactPreview.fetching') : t('artifactPreview.fetch')}
            </button>
          ) : (
            <p class="muted">{t('artifactPreview.noFetch')}</p>
          )}
        </div>
      )}

      {meta?.state === 'ready' && meta.url && (
        <div class="art-preview-body">
          {isPDF || meta.kind !== 'media' ? (
            <iframe
              ref={frame}
              class="art-preview-frame"
              src={meta.url}
              sandbox="allow-scripts allow-forms allow-popups"
              title={t('artifactPreview.title')}
            />
          ) : (
            <img class="art-preview-image" src={meta.url} alt={meta.entry ?? ''} />
          )}
          <p class="art-preview-hint">{t('artifactPreview.hint')}</p>
        </div>
      )}

      {pick && (
        <div class="art-preview-annotate" role="dialog" aria-label={t('artifactPreview.annotate')}>
          <div class="art-preview-pick">
            <span class="mono">{pick.selector}</span>
            {pick.text && <span class="muted"> — {pick.text.slice(0, 80)}</span>}
          </div>
          <textarea
            class="art-preview-comment"
            rows={3}
            placeholder={t('artifactPreview.placeholder')}
            value={comment}
            onInput={(e) => setComment((e.currentTarget as HTMLTextAreaElement).value)}
          />
          <div class="art-preview-actions">
            <button type="button" class="orbit-action-btn" onClick={() => setPick(null)}>
              {t('artifactPreview.cancel')}
            </button>
            <button
              type="button"
              class="orbit-action-btn"
              disabled={!comment.trim()}
              onClick={sendAnnotation}
            >
              {t('artifactPreview.send')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
