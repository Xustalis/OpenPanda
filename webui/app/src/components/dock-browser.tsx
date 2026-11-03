// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's browser panel: a page, its address, and one element you can point
// at.
//
// The frame is /api/browse, never the site. Most of the web refuses to be
// framed (X-Frame-Options, frame-ancestors), so a pane pointed straight at the
// URL renders its own about:blank. The endpoint fetches the page, rewrites its
// base, and serves it under a sandbox without allow-same-origin — the page's
// scripts run, its origin is opaque, and nothing it holds reaches this console
// (see webui/panel/browse.go for what that costs: no cookies, so a logged-in
// site shows its logged-out face).
//
// Two gestures, one frame. Reading is the default. Pointing at an element is a
// mode you turn on from the head; the picker script is already in the page and
// listens for the switch, so turning it on does not reload what you were
// reading. A pick becomes a draft in the composer, never a message of its own.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'

interface PagePick {
  selector: string
  text: string
  tag: string
}

export interface DockBrowserProps {
  /** A URL the transcript asked for (a link in a reply, a task's artifact). */
  request?: string | null
  /** Bumped by the panel's `+`: close the page, keep the history. */
  resetSignal?: number
  /** Put the draft into the composer; absent leaves the annotate action out. */
  onInsert?: (text: string) => void
}

/** A bare host gets https, the way a browser's address bar would. */
function normalise(raw: string): string {
  const text = raw.trim()
  if (!text) return ''
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) return text
  return `https://${text}`
}

export function DockBrowser(props: DockBrowserProps) {
  const [draft, setDraft] = useState('')
  const [page, setPage] = useState<{ url: string; nonce: number } | null>(null)
  const [stack, setStack] = useState<string[]>([])
  const [at, setAt] = useState(-1)
  const [title, setTitle] = useState('')
  const [liveUrl, setLiveUrl] = useState('')
  const [pick, setPick] = useState(false)
  const [picked, setPicked] = useState<PagePick | null>(null)
  const [comment, setComment] = useState('')
  // The frame's credential. An iframe cannot send the Bearer header, so the
  // pane frames a token-in-path route; the prefix arrives from the server on
  // first use. Until it does, framing would only produce a 401 document, so
  // the frame waits.
  const [prefix, setPrefix] = useState('')
  const frame = useRef<HTMLIFrameElement>(null)

  useEffect(() => {
    let live = true
    api
      .browseOrigin()
      .then((r) => live && setPrefix(r.prefix))
      .catch(() => undefined)
    return () => {
      live = false
    }
  }, [])

  const go = (raw: string) => {
    const url = normalise(raw)
    if (!url) return
    setDraft(url)
    setTitle('')
    setLiveUrl('')
    setPicked(null)
    setPick(false)
    setStack((prev) => {
      const kept = at >= 0 ? prev.slice(0, at + 1) : []
      const next = [...kept, url]
      setAt(next.length - 1)
      return next
    })
    setPage((p) => ({ url, nonce: (p?.nonce ?? 0) + 1 }))
  }

  // A URL handed in by the transcript replaces whatever the pane was showing.
  useEffect(() => {
    if (props.request) go(props.request)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.request])

  // `+` closes the page. The stack stays: it is where you have been, and a new
  // page from here is a new branch of it, not a different history.
  useEffect(() => {
    if (!props.resetSignal) return
    setPage(null)
    setDraft('')
    setTitle('')
    setLiveUrl('')
    setPicked(null)
    setPick(false)
  }, [props.resetSignal])

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (!frame.current || e.source !== frame.current.contentWindow) return
      const data = e.data as
        | { source?: string; title?: string; url?: string; selector?: string; text?: string; tag?: string }
        | null
      if (!data) return
      if (data.source === 'openpanda-page') {
        if (data.title) setTitle(data.title)
        if (data.url) setLiveUrl(data.url)
        return
      }
      if (data.source !== 'openpanda-preview' || !data.selector) return
      setPicked({ selector: data.selector, text: data.text ?? '', tag: data.tag ?? '' })
      setComment('')
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  // The switch is a message, not a reload. Sent on every change of state and
  // once more when a freshly loaded document arrives, because a postMessage to
  // a document that does not exist yet goes nowhere.
  const tellFrame = () => {
    frame.current?.contentWindow?.postMessage({ source: 'openpanda-pick-mode', on: pick }, '*')
  }
  useEffect(tellFrame, [pick, page])

  const step = (delta: number) => {
    const next = at + delta
    const url = stack[next]
    if (!url) return
    setAt(next)
    setDraft(url)
    setPicked(null)
    setPage((p) => ({ url, nonce: (p?.nonce ?? 0) + 1 }))
  }

  const send = () => {
    if (!picked || !props.onInsert) return
    props.onInsert(
      t('dockBrowser.draft', {
        url: liveUrl || draft,
        selector: picked.selector,
        comment: comment.trim(),
      }),
    )
    setPicked(null)
  }

  const reload = () => setPage((p) => (p ? { ...p, nonce: p.nonce + 1 } : p))

  return (
    <div class="dock-browser">
      <form
        class="db-bar"
        onSubmit={(e) => {
          e.preventDefault()
          go(draft)
        }}
      >
        <button
          type="button"
          class="icon-btn db-nav"
          title={t('dockBrowser.back')}
          aria-label={t('dockBrowser.back')}
          disabled={at <= 0}
          onClick={() => step(-1)}
        >
          <Icon name="arrow-left" size={15} />
        </button>
        <button
          type="button"
          class="icon-btn db-nav"
          title={t('dockBrowser.forward')}
          aria-label={t('dockBrowser.forward')}
          disabled={at < 0 || at >= stack.length - 1}
          onClick={() => step(1)}
        >
          <Icon name="arrow-right" size={15} />
        </button>
        <input
          class="input db-url mono"
          value={draft}
          spellcheck={false}
          placeholder={t('dockBrowser.placeholder')}
          aria-label={t('dockBrowser.placeholder')}
          onInput={(e) => setDraft((e.currentTarget as HTMLInputElement).value)}
        />
        <button type="submit" class="icon-btn db-nav" title={t('dockBrowser.go')} aria-label={t('dockBrowser.go')}>
          <Icon name="arrow-up" size={15} />
        </button>
        <button
          type="button"
          class="icon-btn db-nav"
          title={t('dockBrowser.reload')}
          aria-label={t('dockBrowser.reload')}
          disabled={!page}
          onClick={reload}
        >
          <Icon name="refresh" size={15} />
        </button>
        {props.onInsert && (
          <button
            type="button"
            class={`icon-btn db-nav${pick ? ' on' : ''}`}
            title={pick ? t('dockBrowser.pickOn') : t('dockBrowser.pick')}
            aria-label={pick ? t('dockBrowser.pickOn') : t('dockBrowser.pick')}
            aria-pressed={pick}
            disabled={!page}
            onClick={() => setPick((v) => !v)}
          >
            <Icon name="eye" size={15} />
          </button>
        )}
      </form>

      {page && (title || liveUrl) && (
        <p class="db-title" title={liveUrl || draft}>
          {title || liveUrl || draft}
        </p>
      )}

      {page ? (
        <div class="db-stage">
          {prefix ? (
            <iframe
              key={`${page.url}#${page.nonce}`}
              ref={frame}
              class="db-frame"
              src={`${prefix}?url=${encodeURIComponent(page.url)}`}
              sandbox="allow-scripts allow-forms allow-popups allow-modals"
              referrerpolicy="no-referrer"
              title={t('dockBrowser.frame')}
              onLoad={tellFrame}
            />
          ) : (
            <p class="db-empty-hint">{t('common.loading')}</p>
          )}
          {pick && !picked && <p class="db-pick-hint">{t('dockBrowser.pickHint')}</p>}
        </div>
      ) : (
        <div class="db-empty">
          <Icon name="network" size={22} />
          <p class="db-empty-hint">{t('dockBrowser.empty')}</p>
        </div>
      )}

      {picked && (
        <div class="db-annotate" role="dialog" aria-label={t('dockBrowser.annotate')}>
          <div class="db-pick">
            <span class="mono">{picked.selector}</span>
            {picked.text && <span class="dim"> — {picked.text.slice(0, 80)}</span>}
          </div>
          <textarea
            class="input db-comment"
            rows={3}
            placeholder={t('dockBrowser.comment')}
            value={comment}
            onInput={(e) => setComment((e.currentTarget as HTMLTextAreaElement).value)}
          />
          <div class="db-actions">
            <button type="button" class="btn small" onClick={() => setPicked(null)}>
              {t('common.cancel')}
            </button>
            <button type="button" class="btn small primary" disabled={!comment.trim()} onClick={send}>
              {t('dockBrowser.send')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
