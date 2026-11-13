// SPDX-License-Identifier: AGPL-3.0-or-later

// Inline image preview for the transcript. A tool row that touched a picture
// — a `Read` of a screenshot, a capture a browser step dropped on disk — used
// to show nothing but its path. The path stays; the picture now sits under it,
// one click from full screen.
//
// Bytes move through fetchBlob: an <img src> cannot carry the Bearer token, so
// the console fetches /api/fs/image itself and hands the <img> an object URL.

import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { fetchBlob } from '../api/client'
import { t } from '../i18n'
import { Icon } from './icons'
import { useModalFocus } from './modal-focus'

function baseName(path: string): string {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return i >= 0 ? path.slice(i + 1) : path
}

export interface ImagePreviewProps {
  /** Filesystem path served by /api/fs/image. */
  path?: string
  /** Bytes the adapter already inlined — shown without a round trip. */
  dataUrl?: string
  /** What to print under the frame. A transcript caption defaults to the
   *  file's base name; a caller that already knows the reader's name for it
   *  (a user turn's upload stamp, say) passes that instead. */
  label?: string
  className?: string
}

/** A bounded thumbnail plus the lightbox it opens. Rendering the picture is
 *  best-effort by design: a path outside the readable roots (or a file the
 *  agent already deleted) leaves one dim line, never a broken image icon. */
export function ImagePreview({ path, dataUrl, label, className = '' }: ImagePreviewProps) {
  const [url, setUrl] = useState(dataUrl ?? '')
  const [failed, setFailed] = useState(false)
  const [zoom, setZoom] = useState(false)
  const [near, setNear] = useState(false)
  const holder = useRef<HTMLDivElement>(null)

  // A computer-use run reads dozens of screenshots, and "expand all" on that
  // log used to fire every fetch at once. The bytes are only worth asking for
  // once the thumbnail is about to be seen.
  useEffect(() => {
    if (dataUrl) return
    const el = holder.current
    if (!el || typeof IntersectionObserver === 'undefined') {
      setNear(true)
      return
    }
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setNear(true)
          io.disconnect()
        }
      },
      { rootMargin: '300px 0px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [dataUrl, path])

  useEffect(() => {
    if (dataUrl) {
      setUrl(dataUrl)
      setFailed(false)
      return
    }
    if (!path || !near) return
    let alive = true
    let made = ''
    setUrl('')
    setFailed(false)
    fetchBlob(`/api/fs/image?path=${encodeURIComponent(path)}`)
      .then((blob) => {
        made = URL.createObjectURL(blob)
        if (!alive) {
          URL.revokeObjectURL(made)
          return
        }
        setUrl(made)
      })
      .catch(() => {
        if (alive) setFailed(true)
      })
    return () => {
      alive = false
      if (made) URL.revokeObjectURL(made)
    }
  }, [path, dataUrl, near])

  if (failed) {
    return <div class={`img-miss dim ${className}`}>{t('events.imageUnavailable')}</div>
  }
  if (!url) {
    return <div class={`img-skel ${className}`} aria-hidden="true" ref={holder} />
  }

  const name = label || (path ? baseName(path) : 'image')
  return (
    <div class={`img-preview ${className}`} ref={holder}>
      <button
        type="button"
        class="img-preview-btn"
        onClick={() => setZoom(true)}
        title={t('events.imageOpen')}
        aria-label={`${t('events.imageOpen')}: ${name}`}
      >
        <img src={url} alt={name} />
        <span class="img-preview-hint" aria-hidden="true">
          <Icon name="search" size={13} />
        </span>
      </button>
      {path && <span class="img-preview-name">{name}</span>}
      {zoom && <Lightbox src={url} path={path} label={label} onClose={() => setZoom(false)} />}
    </div>
  )
}

/** A picture as an attachment pill — the composer's own shape, carried into the
 *  transcript.
 *
 *  A pasted screenshot is context for the sentence, not content of its own: as
 *  a thumbnail it pushed the bubble out to the picture's width and turned a
 *  short question into an image block. The pill keeps the sentence the subject,
 *  and the picture stays one hover (a floating copy) and one click (the
 *  lightbox) away. Bytes are fetched on first hover, not on mount: a thread can
 *  hold dozens of pasted screenshots and most are never opened. */
export function ImagePill({ path, label }: { path: string; label: string }) {
  const [url, setUrl] = useState('')
  const [zoom, setZoom] = useState(false)
  const [anchor, setAnchor] = useState<DOMRect | null>(null)
  const pill = useRef<HTMLButtonElement>(null)
  const dwell = useRef<number | undefined>(undefined)
  const made = useRef('')
  const alive = useRef(true)

  useEffect(
    () => () => {
      alive.current = false
      window.clearTimeout(dwell.current)
      if (made.current) URL.revokeObjectURL(made.current)
    },
    [],
  )

  /** The object URL for this picture, fetched at most once. */
  async function bytes(): Promise<string> {
    if (url) return url
    const blob = await fetchBlob(`/api/fs/image?path=${encodeURIComponent(path)}`)
    if (!alive.current) throw new Error('unmounted')
    made.current = URL.createObjectURL(blob)
    setUrl(made.current)
    return made.current
  }

  function enter() {
    window.clearTimeout(dwell.current)
    dwell.current = window.setTimeout(() => {
      const rect = pill.current?.getBoundingClientRect()
      if (!rect) return
      // Place the copy now, fill it when the bytes land: the frame appears
      // under the pointer in one beat instead of after a round trip.
      setAnchor(rect)
      void bytes().catch(() => setAnchor(null))
    }, 160)
  }

  function leave() {
    window.clearTimeout(dwell.current)
    setAnchor(null)
  }

  return (
    <>
      <button
        type="button"
        class="attach-chip msg-file-pill"
        ref={pill}
        // No native tooltip: the floating copy already names the file, and a
        // system tooltip would sit on top of it.
        onMouseEnter={enter}
        onMouseLeave={leave}
        onClick={() => {
          void bytes()
            .then(() => setZoom(true))
            .catch(() => {
              /* unreadable path: the pill simply does not open */
            })
        }}
      >
        <Icon name="image" size={12} class="msg-file-pill-ico" />{' '}
        <span class="msg-file-name">{label}</span>
      </button>
      {url && anchor && !zoom && <HoverPreview src={url} name={label} anchor={anchor} />}
      {url && zoom && <Lightbox src={url} path={path} label={label} onClose={() => setZoom(false)} />}
    </>
  )
}

/** A larger copy of the picture, floating over the page while the pointer
 *  rests on a small frame.
 *
 *  `pointer-events: none` is the whole trick: this is a preview, not a
 *  surface. The click that opens the lightbox still belongs to the thumbnail
 *  underneath, and moving toward the floating copy cannot make it flicker
 *  away. Placement is measured after the first paint because the copy's size
 *  comes from the picture, not from a constant. */
function HoverPreview({ src, name, anchor }: { src: string; name: string; anchor: DOMRect }) {
  const box = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)

  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const w = el.offsetWidth
    const h = el.offsetHeight
    const gap = 10
    // Above the frame by default: a transcript reads downward, so the room
    // that stays free is the one above. Below when the top edge is close.
    let top = anchor.top - h - gap
    if (top < 8) top = Math.min(anchor.bottom + gap, window.innerHeight - h - 8)
    let left = anchor.left + anchor.width / 2 - w / 2
    left = Math.max(8, Math.min(left, window.innerWidth - w - 8))
    setPos({ left, top })
  }, [anchor, src])

  return (
    <div
      ref={box}
      class="img-hover"
      aria-hidden="true"
      style={
        pos
          ? { left: `${pos.left}px`, top: `${pos.top}px` }
          : { left: '0px', top: '0px', visibility: 'hidden' }
      }
    >
      <img src={src} alt={name} />
      {/* The thumbnail carries no caption, so the floating copy names the file
          — the one place a reader asks "which screenshot is this?". */}
      <span class="img-hover-name">{name}</span>
    </div>
  )
}

/** Full-screen view. Rendered inline with position:fixed — the same overlay
 *  convention the confirm dialog and the todo sheet use — and it closes on
 *  Esc, on the × button, and on a click outside the picture. */
function Lightbox({
  src,
  path,
  label,
  onClose,
}: {
  src: string
  path?: string
  label?: string
  onClose: () => void
}) {
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // Capture phase: the chat's own Esc handler must not also fire.
      e.stopPropagation()
      onClose()
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [onClose])

  const name = label || (path ? baseName(path) : 'image')
  return (
    <div
      class="img-lightbox"
      role="dialog"
      aria-modal="true"
      aria-label={name}
      ref={box}
      onClick={onClose}
    >
      <img class="img-lightbox-img" src={src} alt={name} onClick={(e) => e.stopPropagation()} />
      <div class="img-lightbox-bar" onClick={(e) => e.stopPropagation()}>
        {path && (
          <span class="img-lightbox-path" title={path}>
            {path}
          </span>
        )}
        <button
          type="button"
          class="img-lightbox-close"
          onClick={onClose}
          title={t('common.close')}
          aria-label={t('common.close')}
        >
          <Icon name="x" size={16} />
        </button>
      </div>
    </div>
  )
}
