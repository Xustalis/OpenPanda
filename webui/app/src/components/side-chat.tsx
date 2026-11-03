// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's side conversation: a place to ask about the thread without
// becoming part of it.
//
// The everyday interruption is a question, not a task — "what does this
// function do", "which card claims that", "did the plan mention X". Typed into
// the thread, that question either steers a running turn or becomes a turn of a
// thread it had nothing to do with. The TUI has answered this with /side since
// v0.1.4; this is the same conversation on the web, backed by the same engine
// scope (internal/askengine/side.go), which is what makes the boundaries real
// rather than advisory:
//
//   - Context: the thread's history at the moment the panel opened, replayed as
//     reference. The thread may move on; the snapshot does not follow.
//   - Dispatch: no task_submit in the tool face, and a classification that
//     still lands on a task is answered with a fixed note instead of running.
//   - Output: nothing here is written back. The server files no turn, no
//     operation status — `/api/sessions/{id}/ask` with `side: true` skips every
//     write the thread's own turns go through.
//
// Because nothing is stored, the panel owns the history and replays it on each
// question, and a reload starts a new conversation. That is the design, not a
// gap: a persisted side thread would be a /fork with extra steps.

import { useEffect, useRef, useState } from 'preact/hooks'
import { askSideStream, isAbort, type SideTurn } from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { Icon } from './icons'

interface Line {
  id: number
  role: 'user' | 'assistant'
  text: string
  live?: boolean
}

export interface SideChatProps {
  /** The thread the snapshot is taken from. */
  sessionId: string | null
  /** Bumped by the panel's `+`: start a fresh conversation. */
  resetSignal?: number
  /** Put a reply into the composer — the one crossing back into the thread. */
  onInsert(text: string): void
}

let seq = 0

export function SideChat(props: SideChatProps) {
  const [lines, setLines] = useState<Line[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const [err, setErr] = useState('')
  const live = useRef('')
  const abort = useRef<AbortController | null>(null)
  const logRef = useRef<HTMLDivElement>(null)

  // A new thread, or the `+`, is a new conversation: the old answers were about
  // a different snapshot and keeping them would be a lie about what they saw.
  useEffect(() => {
    abort.current?.abort()
    setLines([])
    setInput('')
    setBusy(false)
    setStatus('')
    setErr('')
  }, [props.sessionId, props.resetSignal])

  // Follow the answer as it arrives; a reader who scrolled up stays where they
  // are (the distance check is the same contract the transcript keeps).
  useEffect(() => {
    const el = logRef.current
    if (!el) return
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (near) el.scrollTop = el.scrollHeight
  }, [lines])

  const patchLast = (text: string, done = false) =>
    setLines((prev) => {
      if (prev.length === 0) return prev
      const next = prev.slice()
      const last = next[next.length - 1]!
      next[next.length - 1] = { ...last, text, live: !done }
      return next
    })

  async function send() {
    const prompt = input.trim()
    if (!prompt || !props.sessionId || busy) return
    const history: SideTurn[] = lines.map((l) => ({ role: l.role, content: l.text }))
    setInput('')
    setErr('')
    setStatus('')
    live.current = ''
    setBusy(true)
    setLines((prev) => [
      ...prev,
      { id: ++seq, role: 'user', text: prompt },
      { id: ++seq, role: 'assistant', text: '', live: true },
    ])
    const ctrl = new AbortController()
    abort.current = ctrl
    try {
      await askSideStream(
        props.sessionId,
        prompt,
        history,
        {
          onReasoning: () => {},
          onDelta: (d) => {
            live.current += d
            patchLast(live.current)
          },
          onStatus: (text) => setStatus(text),
          onOperation: () => {},
          onResult: (r) => patchLast(r.answer || live.current, true),
          onError: (message) => {
            setErr(message)
            patchLast(live.current, true)
          },
        },
        ctrl.signal,
      )
    } catch (e) {
      if (!isAbort(e)) setErr(e instanceof Error ? e.message : String(e))
      patchLast(live.current, true)
    } finally {
      setBusy(false)
      setStatus('')
      abort.current = null
    }
  }

  function stop() {
    abort.current?.abort()
    abort.current = null
    setBusy(false)
    setStatus('')
  }

  return (
    <div class="side-chat">
      <div class="side-scroll" ref={logRef}>
        {lines.length === 0 && !busy && (
          <div class="side-empty">
            <Icon name="message" size={20} />
            {/* A brand-new thread has nothing to read, so the whole point of
                this panel is missing — say that instead of showing a box the
                reader can type into and a button that never lights up. */}
            {props.sessionId ? (
              <p>{t('side.empty')}</p>
            ) : (
              <p>{t('side.noThread')}</p>
            )}
            <p class="dim">{t('side.readonly')}</p>
          </div>
        )}

        {lines.map((l, i) => (
          <div class={`side-turn is-${l.role}`} key={l.id}>
            <div class="side-who">{l.role === 'user' ? t('side.you') : t('side.panda')}</div>
            {l.role === 'user' ? (
              <p class="side-q">{l.text}</p>
            ) : (
              <>
                {l.text ? (
                  <Markdown text={l.text} class="side-md" streaming={Boolean(l.live)} />
                ) : (
                  <p class="side-typing dim">{t('side.thinking')}</p>
                )}
                {!l.live && l.text && (
                  <div class="side-acts">
                    <button type="button" class="link-btn" onClick={() => props.onInsert(l.text)}>
                      {t('side.insert')}
                    </button>
                    <button
                      type="button"
                      class="link-btn"
                      onClick={() => {
                        // Retry on the main thread: the question asked here
                        // may be one it must answer with work.
                        const q = lines[i - 1]?.role === 'user' ? lines[i - 1]!.text : ''
                        if (q) props.onInsert(q)
                      }}
                    >
                      {t('side.toMain')}
                    </button>
                  </div>
                )}
              </>
            )}
          </div>
        ))}

        {status !== '' && <p class="side-status dim">{status}</p>}
        {err !== '' && <p class="side-err">{err}</p>}
      </div>

      <form
        class="side-compose"
        onSubmit={(e) => {
          e.preventDefault()
          void send()
        }}
      >
        <textarea
          class="input side-input"
          rows={2}
          value={input}
          placeholder={t('side.placeholder')}
          disabled={!props.sessionId}
          onInput={(e) => setInput((e.currentTarget as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter breaks the line — the composer's rule,
            // because two text boxes in one pane should not disagree.
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              void send()
            }
          }}
        />
        {busy ? (
          <button type="button" class="btn small" onClick={stop}>
            {t('common.stop')}
          </button>
        ) : (
          <button type="submit" class="btn small primary" disabled={!input.trim() || !props.sessionId}>
            <Icon name="send" size={14} />
          </button>
        )}
      </form>
    </div>
  )
}
