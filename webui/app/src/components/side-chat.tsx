// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's side conversation: a full conversation beside the thread without
// becoming part of it.
//
// The everyday interruption is a question or a small piece of work that does
// not belong in the thread. Typed into the thread, it either steers a running
// turn or becomes a turn of a thread it had nothing to do with. This pane is
// the same engine on a separate transcript, modelled on Claude Code's side
// sessions:
//
//   - Capability: the same tool face as the thread, dispatch included. The
//     channel may read the workspace, query live state, write and submit
//     tasks or plans; every action passes the same permission and approval
//     gates. Isolation is the transcript, not a crippled tool set.
//   - Transcript: the thread's history is replayed above a boundary marker as
//     reference; the channel's own turns live in the session record
//     (Session.SideTurns) and never enter the thread's turns or summary.
//   - Background: the ask runs under the panel's lifetime, and the question
//     and answer are filed server-side. The pane can close, reload, or open on
//     another device and still find the conversation; GET /api/sessions/{id}/side
//     is the reconnect surface, and a running operation is followed by polling
//     until the answer lands.
//
// The pane's `+` clears the server-side conversation and cancels a running
// side ask.

import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  askSideStream,
  isAbort,
  type AskStatusEvent,
  type ClarifyAnswer,
  type ClarifySpec,
  type SideConversation,
} from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { Icon } from './icons'
import { MessageCopyButton } from './copy'
import { ClarifyCard } from './clarify-card'

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
  /** The task this answer dispatched, when it started work. */
  taskId?: string
  /** A failed answer's error. The text may carry the partial prose already
   *  streamed before the failure. */
  error?: string
}

export interface SideChatProps {
  /** The thread this conversation sits beside. */
  sessionId: string | null
  /** Bumped by the panel's `+`: start a fresh conversation. */
  resetSignal?: number
  /** Put a reply into the composer — the one crossing back into the thread. */
  onInsert(text: string): void
  /** Open a task the side answer dispatched. */
  onOpenTask?(id: string): void
}

let seq = 0

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

/** Task ids are UUIDv7 strings; the first segment is enough to recognize one
 *  in a chip, and the full id stays in the title. */
function shortId(id: string): string {
  return id.length > 12 ? id.slice(0, 8) : id
}

export function SideChat(props: SideChatProps) {
  const [lines, setLines] = useState<Line[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  // attached: this pane holds the live SSE stream. When false and busy is
  // true, the answer is being produced in the background and the pane polls.
  const [attached, setAttached] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [status, setStatus] = useState('')
  const [err, setErr] = useState('')
  const [clarify, setClarify] = useState<{ spec: ClarifySpec; operationID: string } | null>(null)
  const live = useRef('')
  const abort = useRef<AbortController | null>(null)
  const logRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  // Which ask the visible conversation belongs to. Switching threads (or
  // clearing the pane) bumps it, so callbacks from an aborted ask cannot
  // append their late deltas to the conversation now on screen.
  const run = useRef(0)
  // The side operation this pane is following, from the live stream or from the
  // stored operation on load. Stop names it to the cancel endpoint.
  const opID = useRef('')

  const applyConversation = (conv: SideConversation) => {
    setLines((conv.turns ?? []).map((turn) => ({
      id: ++seq,
      role: turn.role,
      text: turn.text,
      tools: turn.tools,
      tokens: turn.tokens,
      taskId: turn.task_id,
      error: turn.error,
    })))
    const op = conv.operation
    opID.current = op?.status === 'running' ? op.id : ''
    if (op?.status === 'running') {
      setBusy(true)
      setAttached(false)
      setStatus(t('side.background'))
    } else {
      setBusy(false)
      setAttached(false)
      setStatus('')
      setErr(op && (op.status === 'failed' || op.status === 'cancelled') ? (op.error ?? '') : '')
    }
    setLoaded(true)
  }

  const loadSide = async (id: string) => {
    try {
      applyConversation(await api.sideConversation(id))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
      setLoaded(true)
    }
  }

  // A new thread loads its own stored conversation; the pane's `+` clears the
  // server-side one. The old answers were about a different thread (or a
  // conversation the user deliberately cleared), and keeping them would be a
  // lie about what they saw.
  const seen = useRef<{ session: string | null; reset?: number }>()
  useEffect(() => {
    const before = seen.current
    seen.current = { session: props.sessionId, reset: props.resetSignal }
    const sameThread = before?.session === props.sessionId
    const reset = sameThread && before?.reset !== props.resetSignal
    run.current++
    abort.current?.abort()
    abort.current = null
    opID.current = ''
    setInput('')
    setBusy(false)
    setAttached(false)
    setStatus('')
    setErr('')
    setClarify(null)
    if (!props.sessionId) {
      setLines([])
      setLoaded(true)
      return
    }
    if (reset) {
      setLines([])
      setLoaded(true)
      void api.resetSideConversation(props.sessionId)
        .then(applyConversation)
        .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
      return
    }
    setLoaded(false)
    void loadSide(props.sessionId)
  }, [props.sessionId, props.resetSignal])

  // A background answer: the pane is not holding the stream, so follow the
  // stored operation until it settles and the conversation comes back whole.
  useEffect(() => {
    if (!props.sessionId || !busy || attached) return
    const id = props.sessionId
    const timer = window.setInterval(() => {
      void loadSide(id)
    }, 1500)
    return () => window.clearInterval(timer)
  }, [props.sessionId, busy, attached])

  // An in-flight ask does not outlive the panel: the dock stays mounted once
  // opened, but a session switch or an unmount should not leave a stream
  // feeding a component that is gone. The server keeps the ask (it runs under
  // the panel's lifetime), so the answer is waiting on the next load.
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

  const setLastTask = (taskId: string) =>
    setLines((prev) => {
      const last = prev[prev.length - 1]
      if (!last || last.role !== 'assistant') return prev
      const next = prev.slice()
      next[next.length - 1] = { ...last, taskId }
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

  async function ask(prompt: string) {
    const id = props.sessionId
    if (!id) return
    setInput('')
    setErr('')
    setStatus('')
    setClarify(null)
    live.current = ''
    setBusy(true)
    setAttached(true)
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
          onOperation: (operationID) => {
            if (current()) opID.current = operationID
          },
          onTask: (taskID) => {
            if (current()) setLastTask(taskID)
          },
          onQuestion: (spec, operationID) => {
            if (!current()) return
            setClarify({ spec, operationID })
            setStatus('')
          },
          onResult: (r) => {
            if (!current()) return
            const text = r.answer || live.current
            // A dispatch or plan has no prose answer of its own; name what it
            // started so the live turn is not an empty bubble.
            if (!text && r.plan_id) {
              patchLast(t('side.planStarted', { goal: r.plan_goal ?? '' }), true)
            } else {
              patchLast(text, true)
            }
            if (typeof r.tokens === 'number' && r.tokens > 0) setLastTokens(r.tokens)
            if (r.task_id) setLastTask(r.task_id)
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
        setAttached(false)
        setStatus('')
        abort.current = null
      }
    }
  }

  function send() {
    const prompt = input.trim()
    if (!prompt || !props.sessionId || busy) return
    void ask(prompt)
  }

  // Retry asks the same question again. The failed attempt stays in the
  // transcript (the server owns it); the retry is a new turn, which is the
  // honest record of what happened.
  function retry() {
    if (busy || !props.sessionId) return
    for (let i = lines.length - 1; i >= 0; i--) {
      if (lines[i]!.role === 'user') {
        void ask(lines[i]!.text)
        return
      }
    }
  }

  function stop() {
    const id = props.sessionId
    if (id && opID.current) {
      void api.cancelSession(id, opID.current).catch(() => {})
    }
    abort.current?.abort()
    abort.current = null
    opID.current = ''
    setBusy(false)
    setAttached(false)
    setClarify(null)
    setStatus('')
  }

  function answerClarify(answers: ClarifyAnswer[]) {
    const pending = clarify
    if (!pending || !props.sessionId) return
    // Keep the card until the answer lands: a failed POST must not leave the
    // turn parked behind a card the pane already threw away.
    void api.answerSessionClarify(props.sessionId, pending.operationID, answers)
      .then(() => setClarify(null))
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
  }

  return (
    <div class="side-chat">
      <div class="side-scroll" ref={logRef}>
        {loaded && lines.length === 0 && !busy && (
          <div class="side-empty">
            <Icon name="message" size={20} />
            {/* A thread with no id has nothing to attach to, so the pane is
                missing its anchor — say that instead of offering a box whose
                send button never lights up. */}
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
                {l.text && !(l.error && l.text.startsWith('⚠')) ? (
                  <Markdown text={l.text} class="side-md" streaming={Boolean(l.live)} />
                ) : !l.live && !l.error && !l.taskId ? (
                  <p class="side-typing dim">{t('side.thinking')}</p>
                ) : null}
                {l.error && <p class="side-err">{l.error}</p>}
                {l.taskId && (
                  <div class="side-task-row">
                    {props.onOpenTask ? (
                      <button
                        type="button"
                        class="side-task"
                        title={l.taskId}
                        onClick={() => props.onOpenTask?.(l.taskId!)}
                      >
                        {t('side.task')} <span class="side-task-id">{shortId(l.taskId)}</span>
                      </button>
                    ) : (
                      <span class="side-task" title={l.taskId}>
                        {t('side.task')} <span class="side-task-id">{shortId(l.taskId)}</span>
                      </span>
                    )}
                  </div>
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

      {clarify && (
        <div class="side-clarify">
          <ClarifyCard spec={clarify.spec} onSubmit={answerClarify} onCancel={stop} />
        </div>
      )}

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
            if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
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
