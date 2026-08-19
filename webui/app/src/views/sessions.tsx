import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  askSessionStream,
  type AskResult,
  type Session,
  type SessionDiff,
  type SessionTurn,
} from '../api/client'
import { PandaMark } from '../brand/panda'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

/** A chat message in the transcript: a stored turn, or the in-flight
 * assistant reply being streamed right now. */
interface ChatMsg extends SessionTurn {
  streaming?: boolean
  status?: string
  result?: AskResult
}

/** The sessions view: a thread rail on the left (codex / claude code style)
 * and one worktree-isolated conversation on the right, streaming over SSE. */
export function SessionsView({
  activeId,
  onOpenSession,
  onOpenTask,
}: {
  activeId: string | null
  onOpenSession(id: string): void
  onOpenTask(id: string): void
}) {
  useLocaleRerender()
  const [sessions, setSessions] = useState<Session[]>([])
  const [session, setSession] = useState<Session | null>(null)
  const [msgs, setMsgs] = useState<ChatMsg[]>([])
  const [input, setInput] = useState('')
  const [authorize, setAuthorize] = useState(false)
  const [busy, setBusy] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [diff, setDiff] = useState<SessionDiff | null>(null)
  const [diffOpen, setDiffOpen] = useState(false)
  const [diffError, setDiffError] = useState('')
  const [merging, setMerging] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const composer = useRef<HTMLTextAreaElement>(null)

  // Session list (refresh when the active thread changes — its title may).
  useEffect(() => {
    api
      .sessions()
      .then(setSessions)
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [activeId])

  // Load the active thread's transcript.
  useEffect(() => {
    if (!activeId) {
      setSession(null)
      setMsgs([])
      return
    }
    api
      .session(activeId)
      .then((s) => {
        setSession(s)
        setMsgs((s.turns ?? []).map((turn) => ({ ...turn })))
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [activeId])

  // Keep the transcript pinned to the newest message while streaming.
  useEffect(() => {
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight })
  }, [msgs])

  // Load the session's worktree changes (badge count + drawer contents).
  async function refreshDiff(id: string | null) {
    if (!id) {
      setDiff(null)
      return
    }
    try {
      setDiff(await api.sessionDiff(id))
      setDiffError('')
    } catch (e) {
      // No worktree yet (no prompt run) — that is normal, not an error.
      setDiff(null)
      setDiffError('')
    }
  }

  // Refresh the change badge whenever a reply finishes or the thread changes.
  useEffect(() => {
    refreshDiff(activeId)
  }, [activeId, busy])

  async function mergeChanges() {
    if (!activeId || merging) return
    setMerging(true)
    setDiffError('')
    try {
      await api.sessionMerge(activeId)
      await refreshDiff(activeId)
    } catch (e) {
      setDiffError(e instanceof Error ? e.message : String(e))
    } finally {
      setMerging(false)
    }
  }

  async function newChat() {
    const s = await api.createSession()
    setSessions((ls) => [s, ...ls])
    onOpenSession(s.id)
    composer.current?.focus()
  }

  async function removeChat(id: string) {
    // Deleting a session also drops its worktree and branch — confirm first.
    const ok = await confirmDialog({
      title: t('sessions.deleteTitle'),
      message: t('sessions.deleteMsg'),
      confirmLabel: t('sessions.deleteConfirm'),
    })
    if (!ok) return
    try {
      await api.deleteSession(id)
    } catch (e) {
      toastError(e)
      return
    }
    setSessions((ls) => ls.filter((s) => s.id !== id))
    if (id === activeId) onOpenSession('')
  }

  async function send(e?: Event) {
    e?.preventDefault()
    const prompt = input.trim()
    if (!prompt || busy) return
    let id = activeId
    try {
      // No thread yet: create one titled after the first prompt.
      if (!id) {
        const s = await api.createSession(prompt.slice(0, 48))
        id = s.id
        setSession(s)
        setSessions((ls) => [s, ...ls])
        onOpenSession(s.id)
      }
      setBusy(true)
      setInput('')
      setMsgs((ms) => [...ms, { role: 'user', text: prompt }, { role: 'assistant', text: '', streaming: true }])

      const patch = (fn: (m: ChatMsg) => ChatMsg) =>
        setMsgs((ms) => ms.map((m, i) => (i === ms.length - 1 ? fn(m) : m)))

      await askSessionStream(id, prompt, authorize, {
        onDelta: (text) => patch((m) => ({ ...m, text: m.text + text })),
        onStatus: (text) => patch((m) => ({ ...m, status: text })),
        onResult: (r) => patch((m) => ({ ...m, result: r, status: undefined })),
        onError: (message) => patch((m) => ({ ...m, status: undefined, kind: 'error', text: m.text || `⚠ ${message}` })),
      })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      setMsgs((ms) => [
        ...ms.map((m, i) => (i === ms.length - 1 ? { ...m, streaming: false } : m)),
        { role: 'assistant', kind: 'error', text: `⚠ ${message}` },
      ])
    } finally {
      setBusy(false)
      // Persisted transcript is server-side truth; refresh title + turns.
      if (id) {
        api.session(id).then((s) => {
          setSession(s)
          setSessions((ls) => ls.map((x) => (x.id === s.id ? s : x)))
        }).catch(() => {})
      }
    }
  }

  return (
    <section class="chat">
      <aside class="thread-rail">
        <button class="btn primary thread-new" onClick={newChat}>
          + {t('sessions.new')}
        </button>
        <div class="thread-list">
          {sessions.length === 0 && <p class="thread-empty">{t('sessions.railEmpty')}</p>}
          {sessions.map((s) => (
            <div
              key={s.id}
              class={`thread-item${s.id === activeId ? ' active' : ''}`}
              onClick={() => onOpenSession(s.id)}
            >
              <span class="thread-title">{s.title || t('sessions.untitled')}</span>
              <button
                class="thread-del"
                title={t('sessions.delete')}
                onClick={(e) => {
                  e.stopPropagation()
                  removeChat(s.id)
                }}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      </aside>

      <div class="chat-pane">
        {session && (
          <header class="chat-head">
            <h1 class="chat-title">{session.title || t('sessions.untitled')}</h1>
            {session.branch && (
              <span class="badge green mono" title={session.worktree}>
                ⎇ {session.branch}
              </span>
            )}
            {diff && diff.changes.length > 0 && (
              <button class="btn changes-btn" onClick={() => setDiffOpen(!diffOpen)}>
                ± {diff.changes.length} {t('sessions.changes')}
              </button>
            )}
          </header>
        )}

        {diffOpen && diff && (
          <div class="diff-drawer">
            <div class="diff-drawer-head">
              <strong>
                {t('sessions.worktreeChanges')} — {diff.branch}
              </strong>
              <span>
                <button class="btn primary" disabled={merging} onClick={mergeChanges}>
                  {merging ? t('sessions.merging') : t('sessions.merge')}
                </button>
                <button class="btn" onClick={() => setDiffOpen(false)}>
                  ×
                </button>
              </span>
            </div>
            {diffError && <p class="gate-error">{diffError}</p>}
            <ul class="diff-files">
              {diff.changes.map((c) => (
                <li key={c.path}>
                  <span class={`badge ${c.status === 'D' ? 'red' : c.status === '??' ? 'blue' : 'yellow'}`}>
                    {c.status}
                  </span>
                  <span class="mono">{c.path}</span>
                </li>
              ))}
            </ul>
            {diff.patch && <pre class="diff-patch">{diff.patch}</pre>}
          </div>
        )}

        <div class="chat-log" ref={scroller}>
          {msgs.length === 0 && !busy && <ChatHero onPick={(p) => setInput(p)} />}
          {msgs.map((m, i) => (
            <ChatBubble key={i} msg={m} onOpenTask={onOpenTask} />
          ))}
        </div>

        <form class="composer" onSubmit={send}>
          <textarea
            ref={composer}
            class="input composer-input"
            rows={2}
            placeholder={t('sessions.placeholder')}
            value={input}
            disabled={busy}
            onInput={(e) => setInput((e.target as HTMLTextAreaElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                send()
              }
            }}
          />
          <div class="composer-actions">
            <label class="ask-authorize">
              <input
                type="checkbox"
                checked={authorize}
                onChange={(e) => setAuthorize((e.target as HTMLInputElement).checked)}
              />
              {t('sessions.authorize')}
            </label>
            <button class="btn primary" type="submit" disabled={busy || !input.trim()}>
              {busy ? t('sessions.thinking') : t('sessions.send')}
            </button>
          </div>
        </form>
        {loadError && <p class="gate-error composer-error">{loadError}</p>}
      </div>
    </section>
  )
}

/** Empty-state hero: the panda greets with a few starter prompts. */
function ChatHero({ onPick }: { onPick(prompt: string): void }) {
  return (
    <div class="chat-hero">
      <PandaMark size={72} />
      <h2>{t('sessions.hello')}</h2>
      <p>{t('sessions.hint')}</p>
      <div class="hero-chips">
        {(['s1', 's2', 's3'] as const).map((k) => (
          <button key={k} class="chip" onClick={() => onPick(t(`sessions.${k}`))}>
            {t(`sessions.${k}`)}
          </button>
        ))}
      </div>
    </div>
  )
}

function ChatBubble({ msg, onOpenTask }: { msg: ChatMsg; onOpenTask(id: string): void }) {
  const [chainOpen, setChainOpen] = useState(false)

  if (msg.role === 'user') {
    return (
      <div class="msg user">
        <div class="msg-avatar you">You</div>
        <div class="msg-body">
          <p class="msg-text">{msg.text}</p>
        </div>
      </div>
    )
  }
  return (
    <div class="msg panda">
      <div class="msg-avatar">
        <PandaMark size={22} />
      </div>
      <div class="msg-body">
        {msg.text && <p class="msg-text">{msg.text}</p>}
        {msg.streaming && !msg.text && !msg.status && <span class="status-line">{t('sessions.thinking')}</span>}
        {msg.streaming && msg.status && (
          <span class="status-line">
            <span class="spinner" aria-hidden="true" />
            {msg.status}
          </span>
        )}
        {msg.streaming && msg.text && <span class="cursor" aria-hidden="true" />}
        {msg.kind === 'task' && msg.ref && (
          <div class="task-card">
            <span class="badge blue">{t('state.running') === msg.result?.task_state ? t('sessions.taskCreated') : msg.result?.task_state || t('sessions.taskCreated')}</span>
            {msg.result?.stdout && <pre class="task-out">{msg.result.stdout}</pre>}
            <button class="btn small chain-toggle" onClick={() => setChainOpen((v) => !v)}>
              {chainOpen ? t('sessions.chainHide') : t('sessions.chainShow')}
            </button>
            {chainOpen && <TaskChain taskId={msg.ref} />}
            <a href={`#/task/${encodeURIComponent(msg.ref)}`} onClick={() => onOpenTask(msg.ref!)}>
              {t('sessions.viewTask')} →
            </a>
          </div>
        )}
      </div>
    </div>
  )
}

/** The task's thinking chain: the task_events log replayed live — history
 *  comes from GET /api/tasks/{id}/logs, and the panel's SSE change signal
 *  triggers a refetch while the chain is open (design §5). */
function TaskChain({ taskId }: { taskId: string }) {
  const change = useChangeSignal()
  const { data, error } = useAsync(() => api.logs(taskId), [taskId], change)
  if (error) return <p class="dim chain-empty">{t('common.error')} ({error})</p>
  const events = data?.events ?? []
  if (events.length === 0) return <p class="dim chain-empty">{t('sessions.chainEmpty')}</p>
  return (
    <ol class="timeline chain">
      {events.map((ev, i) => (
        <li key={i}>
          <span class="dim">{new Date(ev.ts * 1000).toLocaleTimeString()}</span> <code>{ev.type}</code>
          {ev.data && ev.data !== '{}' && <pre class="timeline-data">{ev.data}</pre>}
        </li>
      ))}
    </ol>
  )
}
