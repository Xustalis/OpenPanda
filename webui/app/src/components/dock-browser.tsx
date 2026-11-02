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
// A finished task's output is the exception. It lives on the preview origin
// (preview.go) — a separate listener with its own path token — and the browse
// proxy refuses loopback by design, so that page is framed directly. It is
// already sandboxed by the response and its token is scoped to the archive.
//
// Two gestures, one frame. Reading is the default. Pointing at an element is a
// mode you turn on from the head; the picker script is already in the page and
// listens for the switch, so turning it on does not reload what you were
// reading. A pick becomes a draft in the composer, never a message of its own.
//
// Nothing here is silent. A pane that cannot load says why and offers the one
// action that might fix it: retry the page, re-read the frame capability, or
// pull a remote artifact over before showing it.

import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { api } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'

interface PagePick {
  selector: string
  text: string
  tag: string
}

/** One page in the pane's history. `direct` marks a URL the console may frame
 *  as-is (the artifact preview origin) instead of one that must go through
 *  /api/browse. */
interface PageRef {
  url: string
  direct?: boolean
  title?: string
}

/** A page as it is on screen: the history entry plus a reload counter, which
 *  is what makes "the same URL again" a reload rather than a no-op. */
interface ShownPage extends PageRef {
  nonce: number
}

/** A finished task the thread asked to look at. `nonce` distinguishes a second
 *  request for the same artifact from a re-render. */
export interface BrowserArtifact {
  taskId: string
  hash: string
  nonce: number
}

/** What the pane says when it has no page to show. The artifact cases carry
 *  the request they came from, so a retry re-reads that task and not whatever
 *  the newest request happens to be. */
type Notice =
  | { kind: 'remote'; taskId: string; key: string; source: string; canFetch: boolean }
  | { kind: 'error'; taskId?: string; key?: string; error: string; retry: 'artifact' | 'page' }
  | { kind: 'empty'; taskId: string; key: string }

export interface DockBrowserProps {
  /** A URL the transcript asked for (a link in a reply, a task's artifact). */
  request?: string | null
  /** Bumped by the panel's `+`: close the page, keep the history. */
  resetSignal?: number
  /** A finished task's page, already resolved by the thread. */
  artifact?: BrowserArtifact | null
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

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

export function DockBrowser(props: DockBrowserProps) {
  const [draft, setDraft] = useState('')
  const [page, setPage] = useState<ShownPage | null>(null)
  // History as one piece of state: the items and the cursor into them must
  // move together, or a push from mid-history leaves the cursor pointing at
  // the wrong row.
  const [history, setHistory] = useState<{ items: PageRef[]; at: number }>({ items: [], at: -1 })
  const [title, setTitle] = useState('')
  const [liveUrl, setLiveUrl] = useState('')
  const [pick, setPick] = useState(false)
  const [picked, setPicked] = useState<PagePick | null>(null)
  const [comment, setComment] = useState('')
  // The frame's credential. An iframe cannot send the Bearer header, so the
  // pane frames a token-in-path route; the prefix arrives from the server on
  // first use. Until it does, framing would only produce a 401 document, so
  // the frame waits — and if the fetch fails the pane says so and offers the
  // one retry that can help.
  const [prefix, setPrefix] = useState('')
  const [prefixErr, setPrefixErr] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [busy, setBusy] = useState(false)
  // A proxied page is fetched server-side before the frame has anything to
  // show; the bar is the only sign of life between the click and the document.
  const [loading, setLoading] = useState(false)
  const frame = useRef<HTMLIFrameElement>(null)
  /** While the address bar has focus the page may not write over what the
   *  reader is typing; a redirect landing mid-edit must not eat the text. */
  const barFocused = useRef(false)
  /** Guards the async artifact read: a second request supersedes the first,
   *  and the first must not paint over it when it lands. */
  const artifactSeq = useRef(0)
  /** The artifact currently framed. Re-requesting the same one is a no-op, so
   *  a poll that keeps reporting it cannot reload the page under the reader. */
  const shownArtifact = useRef('')
  const pageNonce = useRef(0)

  const loadPrefix = useCallback(() => {
    setPrefixErr('')
    api
      .browseOrigin()
      .then((r) => setPrefix(r.prefix))
      .catch((e: unknown) => {
        setPrefixErr(errText(e))
        // Nothing is going to arrive for the page waiting on this capability.
        setLoading(false)
      })
  }, [])

  useEffect(() => {
    loadPrefix()
  }, [loadPrefix])

  const showPage = useCallback((ref: PageRef, push: boolean) => {
    setNotice(null)
    setPicked(null)
    setPick(false)
    setTitle(ref.title ?? '')
    setLiveUrl(ref.direct ? ref.url : '')
    setDraft(ref.url)
    setLoading(true)
    pageNonce.current += 1
    setPage({ ...ref, nonce: pageNonce.current })
    if (push) {
      setHistory((h) => {
        const kept = h.at >= 0 ? h.items.slice(0, h.at + 1) : []
        const items = [...kept, ref]
        return { items, at: items.length - 1 }
      })
    }
  }, [])

  const go = (raw: string) => {
    const url = normalise(raw)
    if (!url) return
    // The reader asked for this page now: an artifact read still in flight is
    // for the page they just left, and must not replace what they typed.
    artifactSeq.current += 1
    setBusy(false)
    showPage({ url }, true)
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
    // Invalidate an artifact read still in flight: it belongs to the page this
    // reset is closing, and letting it land would put the old thread's site
    // back on screen.
    artifactSeq.current += 1
    setBusy(false)
    setLoading(false)
    setPage(null)
    setDraft('')
    setTitle('')
    setLiveUrl('')
    setPicked(null)
    setPick(false)
    setNotice(null)
    shownArtifact.current = ''
  }, [props.resetSignal])

  const loadArtifact = useCallback(
    async (taskId: string, key: string) => {
      const seq = ++artifactSeq.current
      setBusy(true)
      setNotice(null)
      try {
        const meta = await api.previewTaskArtifact(taskId)
        if (seq !== artifactSeq.current) return
        if (meta.state === 'ready' && meta.kind === 'none') {
          setPage(null)
          setDraft('')
          setTitle('')
          setLiveUrl('')
          setLoading(false)
          setNotice({ kind: 'empty', taskId, key })
          return
        }
        if (meta.state === 'ready' && meta.url) {
          shownArtifact.current = key
          showPage({ url: meta.url, direct: true, title: meta.entry ?? '' }, true)
          return
        }
        if (meta.state !== 'remote') {
          // "ready" without a URL is a payload this pane cannot frame; saying
          // so beats falling through to the remote wording, which would name a
          // node that has nothing to do with it.
          setPage(null)
          setDraft('')
          setTitle('')
          setLiveUrl('')
          setLoading(false)
          setNotice({ kind: 'error', taskId, key, error: t('dockBrowser.unknown'), retry: 'artifact' })
          return
        }
        // The bytes are still on the node that ran the task. Say that, and
        // offer the pull; the page appears without a second request.
        setPage(null)
        setDraft('')
        setTitle('')
        setLiveUrl('')
        setLoading(false)
        setNotice({
          kind: 'remote',
          taskId,
          key,
          source: meta.source ?? '',
          canFetch: meta.can_fetch !== false,
        })
      } catch (e) {
        if (seq !== artifactSeq.current) return
        setPage(null)
        setDraft('')
        setTitle('')
        setLiveUrl('')
        setLoading(false)
        setNotice({ kind: 'error', taskId, key, error: errText(e), retry: 'artifact' })
      } finally {
        if (seq === artifactSeq.current) setBusy(false)
      }
    },
    [showPage],
  )

  // A finished task's page replaces whatever the pane held. The first request
  // for an artifact is the one that loads it; a poll that repeats the same one
  // does not reload the page the reader is on.
  useEffect(() => {
    const a = props.artifact
    if (!a) return
    const key = `${a.taskId}:${a.hash}`
    if (shownArtifact.current === key) return
    void loadArtifact(a.taskId, key)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.artifact?.taskId, props.artifact?.hash, props.artifact?.nonce])

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (!frame.current || e.source !== frame.current.contentWindow) return
      const data = e.data as
        | {
            source?: string
            title?: string
            url?: string
            error?: string
            selector?: string
            text?: string
            tag?: string
          }
        | null
      if (!data) return
      if (data.source === 'openpanda-page') {
        // A page that rendered is the end of any earlier failure state.
        setNotice(null)
        setLoading(false)
        if (data.title) setTitle(data.title)
        if (data.url) {
          setLiveUrl(data.url)
          // The address bar follows the document, including a server-side
          // redirect the console never saw. Typing wins while the field has
          // focus: a page landing mid-edit must not rewrite the reader.
          if (!barFocused.current) setDraft(data.url)
        }
        return
      }
      // The fetcher answers a failed page with an HTML explanation. It cannot
      // be read from here (opaque origin), so that document reports itself.
      if (data.source === 'openpanda-browse-error') {
        setLoading(false)
        setNotice({
          kind: 'error',
          error: data.error || t('dockBrowser.unknown'),
          retry: 'page',
        })
        return
      }
      // A link inside a proxied page. The pane, not the frame, owns the fetch:
      // navigating the frame directly would leave the proxy for the site and
      // most sites refuse to be framed.
      if (data.source === 'openpanda-navigate' && data.url) {
        go(data.url)
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
    const next = history.at + delta
    const ref = history.items[next]
    if (!ref || next < 0) return
    setHistory((h) => ({ ...h, at: next }))
    showPage(ref, false)
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

  const reload = () => {
    setNotice(null)
    setLoading(true)
    if (page && !page.direct && !prefix) loadPrefix()
    setPage((p) => (p ? { ...p, nonce: p.nonce + 1 } : p))
  }

  // Hand the page to the reader's own browser. A proxied page opens at the
  // site itself — that browser has no reason to go through this console's
  // fetcher — while an artifact page opens on its preview origin, which is
  // where it actually lives.
  const openExternal = () => {
    if (!page) return
    const url = page.direct ? page.url : liveUrl || page.url
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  const retryNotice = (n: Notice) => {
    if (n.kind === 'error' && n.retry === 'artifact' && n.taskId && n.key) {
      void loadArtifact(n.taskId, n.key)
      return
    }
    if (n.kind === 'remote') {
      // Re-read the row: the bytes may have landed since the poll that drew
      // this notice.
      void loadArtifact(n.taskId, n.key)
      return
    }
    if (n.kind === 'empty') {
      void loadArtifact(n.taskId, n.key)
      return
    }
    reload()
  }

  // Pull a remote artifact onto this node so the preview origin has bytes.
  const pull = async (taskId: string, key: string) => {
    setBusy(true)
    try {
      await api.fetchTaskArtifact(taskId)
      await loadArtifact(taskId, key)
    } catch (e) {
      setNotice({ kind: 'error', taskId, key, error: errText(e), retry: 'artifact' })
    } finally {
      setBusy(false)
    }
  }

  const needsPrefix = Boolean(page && !page.direct)
  const showPrefixWait = needsPrefix && !prefix && !prefixErr
  const showPrefixError = needsPrefix && !prefix && Boolean(prefixErr)

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
          disabled={history.at <= 0}
          onClick={() => step(-1)}
        >
          <Icon name="arrow-left" size={15} />
        </button>
        <button
          type="button"
          class="icon-btn db-nav"
          title={t('dockBrowser.forward')}
          aria-label={t('dockBrowser.forward')}
          disabled={history.at < 0 || history.at >= history.items.length - 1}
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
          onFocus={() => {
            barFocused.current = true
          }}
          onBlur={() => {
            barFocused.current = false
          }}
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
        <button
          type="button"
          class="icon-btn db-nav"
          title={t('dockBrowser.external')}
          aria-label={t('dockBrowser.external')}
          disabled={!page}
          onClick={openExternal}
        >
          <Icon name="external" size={15} />
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

      {loading && (
        <div class="db-progress" role="progressbar" aria-label={t('common.loading')} />
      )}

      {page && (title || liveUrl) && (
        <p class="db-title" title={liveUrl || draft}>
          {title || liveUrl || draft}
        </p>
      )}

      {showPrefixError && (
        <div class="db-notice" role="status">
          <p class="db-notice-text">{t('dockBrowser.originFailed', { error: prefixErr })}</p>
          <div class="db-notice-actions">
            <button
              type="button"
              class="btn small"
              onClick={() => {
                setLoading(true)
                loadPrefix()
              }}
            >
              {t('dockBrowser.retry')}
            </button>
          </div>
        </div>
      )}

      {notice && (
        <div class="db-notice" role="status">
          <p class="db-notice-text">
            {notice.kind === 'remote'
              ? t('dockBrowser.artifactRemote', { source: notice.source || '?' })
              : notice.kind === 'empty'
                ? t('dockBrowser.noPage')
                : t('dockBrowser.failed', { error: notice.error })}
          </p>
          <div class="db-notice-actions">
            {notice.kind === 'remote' && notice.canFetch && (
              <button
                type="button"
                class="btn small primary"
                disabled={busy}
                onClick={() => void pull(notice.taskId, notice.key)}
              >
                {busy ? t('dockBrowser.fetching') : t('dockBrowser.fetch')}
              </button>
            )}
            {notice.kind === 'remote' && !notice.canFetch && (
              <>
                <span class="db-notice-hint">{t('dockBrowser.noFetch')}</span>
                <button
                  type="button"
                  class="btn small"
                  disabled={busy}
                  onClick={() => retryNotice(notice)}
                >
                  {t('dockBrowser.retry')}
                </button>
              </>
            )}
            {notice.kind !== 'remote' && (
              <button
                type="button"
                class="btn small"
                disabled={busy}
                onClick={() => retryNotice(notice)}
              >
                {t('dockBrowser.retry')}
              </button>
            )}
          </div>
        </div>
      )}

      {page ? (
        <div class="db-stage">
          {page.direct || prefix ? (
            <iframe
              key={`${page.url}#${page.direct ? 'd' : 'p'}#${page.nonce}`}
              ref={frame}
              class="db-frame"
              src={page.direct ? page.url : `${prefix}?url=${encodeURIComponent(page.url)}`}
              sandbox="allow-scripts allow-forms allow-popups allow-modals"
              referrerpolicy="no-referrer"
              title={t('dockBrowser.frame')}
              onLoad={() => {
                // A document that arrived is no longer loading, even when it
                // has no picker script to say so (an image or a PDF artifact).
                setLoading(false)
                tellFrame()
              }}
            />
          ) : (
            <p class="db-empty-hint">{showPrefixWait ? t('common.loading') : ''}</p>
          )}
          {pick && !picked && <p class="db-pick-hint">{t('dockBrowser.pickHint')}</p>}
        </div>
      ) : busy ? (
        <div class="db-empty">
          <p class="db-empty-hint">{t('common.loading')}</p>
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
