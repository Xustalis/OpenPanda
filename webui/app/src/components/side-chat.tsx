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
//   - Context: the thread's history as it stands when each question is asked,
//     replayed as reference. The channel's own turns sit below the boundary
//     marker, so a question about "now" reads the present thread rather than
//     the one the panel opened on.
//   - Dispatch: no task_submit in the tool face, and a classification that
//     still lands on a task is answered with a fixed note instead of running.
//     Reading is not dispatch: the observation tools and the read-only
//     workspace family (workspace_read/list/search) stay, which is what makes
//     a side question about live state or code answerable at all.
//   - Output: nothing here is written back. The server files no turn, no
//     operation status — `/api/sessions/{id}/ask` with `side: true` skips every
//     write the thread's own turns go through.
//
// The server stores nothing, so the panel owns the conversation. It persists
// per thread in localStorage: a reload keeps the answers the user just read,
// and the pane's `+` starts a genuinely new one.

import { useEffect, useRef, useState } from 'preact/hooks'
import { askSideStream, isAbort, type AskStatusEvent, type SideTurn } from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { Icon } from './icons'
import { MessageCopyButton } from './copy'

interface Line {
  id: number
  role: 'user' | 'assistant'
  text: string
  live?: boolean
  /** What this answer checked, in order: the tool and its subject. Display
   *  only — the engine never replays it, so it never enters the prompt. */
  tools?: string[]
  /** Billed tokens for the answer, when the provider reported them. */
  tokens?: number
}

export interface SideChatProps {
  /** The thread this conversation sits beside. */
  sessionId: string | null
  /** Bumped by the panel's `+`: start a fresh conversation. */
  resetSignal?: number
  /** Put a reply into the composer — the one crossing back into the thread. */
  onInsert(text: string): void
}

let seq = 0

/** How many completed turns survive a reload. A side conversation is a
 *  thinking space, not a second archive: the tail is what a reader resumes. */
const SIDE_STORE_CAP = 40

const sideKey = (id: string) => `openpanda.side.${id}`

/** The side channel's one-line progress note. Retry and outage milestones are
 *  re-phrased here from the same structured fields the main composer reads, so
 *  two panes in one console do not speak different languages about the same
 *  provider hiccup. */
function sideStatusNote(text: string, ev?: AskStatusEvent): string {
  if (ev?.kind === 'retry') {
    const reason = ev.http_status ? `HTTP ${ev.http_status}` : t('sessions.retry.transport')
    return t('sessions.retry', {
      reason,
      delay: Math.max(0, Math.round((ev.delay_ms ?? 0) / 1000)),
      attempt: ev.attempt ?? 0,
      max: ev.max ?? 0,
    })
  }
  if (ev?.kind === 'offline') {
    const waited = Math.max(0, Math.round((ev.waited_ms ?? 0) / 1000))
    return ev.detail === 'resumed'
      ? t('sessions.offline.resumed', { delay: waited })
      : t('sessions.offline.waiting')
  }
  return text
}

/** Compact token count for the answer foot: 950, 1.2k, 1.1M. */
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

/** Read the stored conversation for one thread. Anything malformed is
 *  discarded rather than trusted: this is browser storage a page can corrupt,
 *  and a bad row must not take the panel down with it. */
function loadSide(id: string): Line[] {
  try {
    const raw = localStorage.getItem(sideKey(id))
    if (!raw) return []
    const rows = JSON.parse(raw) as Array<{ role?: unknown; text?: unknown; tools?: unknown; tokens?: unknown }>
    if (!Array.isArray(rows)) return []
    const out: Line[] = []
    for (const row of rows) {
      if (!row || (row.role !== 'user' && row.role !== 'assistant') || typeof row.text !== 'string') continue
      const tools = Array.isArray(row.tools) ? row.tools.filter((x): x is string => typeof x === 'string') : undefined
      const tokens = typeof row.tokens === 'number' && row.tokens > 0 ? row.tokens : undefined
      out.push({ id: ++seq, role: row.role, text: row.text, tools, tokens })
    }
    return out
  } catch {
    return []
  }
}

/** Persist the completed turns. The live row is skipped — a reload mid-answer
 *  should show the question, not half a sentence frozen as an answer. */
function saveSide(id: string, lines: Line[]) {
  try {
    const rows = lines
      .filter((l) => !l.live && l.text.trim() !== '')
      .slice(-SIDE_STORE_CAP)
      .map((l) => {
        const row: { role: Line['role']; text: string; tools?: string[]; tokens?: number } = {
          role: l.role,
          text: l.text,
        }
        if (l.tools && l.tools.length > 0) row.tools = l.tools
        if (l.tokens) row.tokens = l.tokens
        return row
      })
    if (rows.length === 0) localStorage.removeItem(sideKey(id))
    else localStorage.setItem(sideKey(id), JSON.stringify(rows))
  } catch {
    // Storage can be unavailable (private mode, quota). The panel works
    // without it; only the reload survival is lost.
  }
}

export function SideChat(props: SideChatProps) {
  const [lines, setLines] = useState<Line[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const [err, setErr] = useState('')
  const live = useRef('')
  const abort = useRef<AbortController | null>(null)
  const logRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  // Which ask the visible conversation belongs to. Switching threads (or
  // clearing the pane) bumps it, so callbacks from an aborted ask cannot
  // append their late deltas to the conversation now on screen.
  const run = useRef(0)

  // A new thread loads its own stored conversation; the pane's `+` starts a
  // new one. The old answers were about a different thread (or a snapshot the
  // user deliberately cleared), and keeping them would be a lie about what
  // they saw.
  const seen = useRef<{ session: string | null; reset?: number }>()
  useEffect(() => {
    const before = seen.current
    seen.current = { session: props.sessionId, reset: props.resetSignal }
    const sameThread = before?.session === props.sessionId
    const reset = sameThread && before?.reset !== props.resetSignal
    run.current++
    abort.current?.abort()
    abort.current = null
    setInput('')
    setBusy(false)
    setStatus('')
    setErr('')
    if (!props.sessionId) {
      setLines([])
      return
    }
    if (reset) {
      try {
        localStorage.removeItem(sideKey(props.sessionId))
      } catch {
        // Storage unavailable; there is nothing to clear.
      }
      setLines([])
      return
    }
    setLines(loadSide(props.sessionId))
  }, [props.sessionId, props.resetSignal])

  // Persist completed turns so a reload resumes where the reader left off.
  useEffect(() => {
    if (props.sessionId) saveSide(props.sessionId, lines)
  }, [lines, props.sessionId])

  // An in-flight ask does not outlive the panel: the dock stays mounted once
  // opened, but a session switch or an unmount should not leave a stream
  // feeding a component that is gone.
  useEffect(() => () => abort.current?.abort(), [])

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

  const setLastTokens = (tokens: number) =>
    setLines((prev) => {
      const last = prev[prev.length - 1]
      if (!last || last.role !== 'assistant') return prev
      const next = prev.slice()
      next[next.length - 1] = { ...last, tokens }
      return next
    })

  // appendTool records one tool call on the answer being streamed. The chip is
  // added when the call starts, so the reader watches the channel work
  // (system_status, then workspace_read side.go) instead of a mute spinner.
  const appendTool = (label: string) =>
    setLines((prev) => {
      const last = prev[prev.length - 1]
      if (!last || last.role !== 'assistant' || !last.live) return prev
      const tools = last.tools ?? []
      if (tools.length >= 24 || tools[tools.length - 1] === label) return prev
      const next = prev.slice()
      next[next.length - 1] = { ...last, tools: [...tools, label] }
      return next
    })

  async function ask(prompt: string, history: SideTurn[]) {
    const id = props.sessionId
    if (!id) return
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
    const runId = ++run.current
    const current = () => run.current === runId
    try {
      await askSideStream(
        id,
        prompt,
        history,
        {
          onReasoning: () => {},
          onDelta: (d) => {
            if (!current()) return
            live.current += d
            patchLast(live.current)
          },
          onStatus: (text, ev) => {
            if (!current()) return
            // A tool call is progress worth keeping, not a transient note: it
            // becomes a chip on the answer, and the status line repeats it in
            // the reader's own language while the call runs.
            if (ev?.kind === 'tool' && ev.name) {
              const label = ev.detail ? `${ev.name} ${ev.detail}` : ev.name
              appendTool(label)
              setStatus(t('side.tool', { tool: label }))
              return
            }
            setStatus(sideStatusNote(text, ev))
          },
          onOperation: () => {},
          onResult: (r) => {
            if (!current()) return
            patchLast(r.answer || live.current, true)
            if (typeof r.tokens === 'number' && r.tokens > 0) setLastTokens(r.tokens)
          },
          onError: (message) => {
            if (!current()) return
            setErr(message)
            patchLast(live.current, true)
          },
        },
        ctrl.signal,
      )
    } catch (e) {
      if (current()) {
        if (!isAbort(e)) setErr(e instanceof Error ? e.message : String(e))
        patchLast(live.current, true)
      }
    } finally {
      if (current()) {
        setBusy(false)
        setStatus('')
        abort.current = null
      }
    }
  }

  function send() {
    const prompt = input.trim()
    if (!prompt || !props.sessionId || busy) return
    const history: SideTurn[] = lines.map((l) => ({ role: l.role, content: l.text }))
    void ask(prompt, history)
  }

  // Retry drops the failed exchange and asks the same question again with the
  // history that preceded it. The question is still on screen, so the reader
  // does not have to retype it after a provider hiccup.
  function retry() {
    if (busy || !props.sessionId) return
    let idx = -1
    for (let i = lines.length - 1; i >= 0; i--) {
      if (lines[i]!.role === 'user') {
        idx = i
        break
      }
    }
    if (idx < 0) return
    const prompt = lines[idx]!.text
    const history: SideTurn[] = lines.slice(0, idx).map((l) => ({ role: l.role, content: l.text }))
    setLines(lines.slice(0, idx))
    void ask(prompt, history)
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
              <>
                <p>{t('side.empty')}</p>
                <p class="dim">{t('side.readonly')}</p>
                <p class="dim">{t('side.context')}</p>
                {/* The channel is most useful with a question that names what
                    it can check. These fill the composer rather than sending:
                    the reader edits them to this thread's own words. */}
                <div class="side-starters">
                  <span class="dim">{t('side.starters')}</span>
                  {(['status', 'summary', 'files'] as const).map((k) => (
                    <button
                      type="button"
                      class="side-starter"
                      key={k}
                      onClick={() => {
                        setInput(t(`side.starter.${k}`))
                        inputRef.current?.focus()
                      }}
                    >
                      {t(`side.starter.${k}`)}
                    </button>
                  ))}
                </div>
              </>
            ) : (
              <p>{t('side.noThread')}</p>
            )}
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
                {l.tools && l.tools.length > 0 && (
                  <div class="side-tools">
                    <span class="side-tools-label">{t('side.tools')}</span>
                    {l.tools.map((tool, k) => (
                      <span class="side-tool" key={k} title={tool}>
                        {tool}
                      </span>
                    ))}
                  </div>
                )}
                {!l.live && l.text && (
                  <div class="side-acts">
                    <MessageCopyButton text={l.text} markdown />
                    <button type="button" class="link-btn" onClick={() => props.onInsert(l.text)}>
                      {t('side.insert')}
                    </button>
                    <button
                      type="button"
                      class="link-btn"
                      onClick={() => {
                        // Hand the question back, with what this channel already
                        // found. The main thread would otherwise redo the same
                        // reading; carrying the answer forward is the explicit
                        // bridge back that the isolation is supposed to have.
                        const q = lines[i - 1]?.role === 'user' ? lines[i - 1]!.text : ''
                        if (!q) return
                        props.onInsert(l.text ? `${q}\n\n${t('side.carry')}\n${l.text}` : q)
                      }}
                    >
                      {t('side.toMain')}
                    </button>
                    {l.tokens ? (
                      <span class="side-tokens dim">{formatTokens(l.tokens)} tokens</span>
                    ) : null}
                  </div>
                )}
              </>
            )}
          </div>
        ))}

        {status !== '' && <p class="side-status dim">{status}</p>}
        {err !== '' && (
          <div class="side-error">
            <p class="side-err">{err}</p>
            <button type="button" class="link-btn" onClick={retry} disabled={busy}>
              {t('side.retry')}
            </button>
          </div>
        )}
      </div>

      <form
        class="side-compose"
        onSubmit={(e) => {
          e.preventDefault()
          void send()
        }}
      >
        <textarea
          ref={inputRef}
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
