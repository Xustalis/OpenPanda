import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  askSessionStream,
  isAbort,
  type AskResult,
  type Session,
  type SessionDiff,
  type SessionTurn,
} from '../api/client'
import { PandaAscii, PandaMark } from '../brand/panda'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

/** Grow the composer with its content up to a ceiling, then scroll inside.
 *  A fixed two-row box makes pasting a paragraph feel like typing into a
 *  keyhole; unbounded growth would eat the transcript instead. */
const COMPOSER_MAX_PX = 240

function autoGrow(el: HTMLTextAreaElement): void {
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, COMPOSER_MAX_PX)}px`
}

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
  const [selectedProject, setSelectedProject] = useState('')
  const [projects, setProjects] = useState<string[]>([])
  const folderInputRef = useRef<HTMLInputElement>(null)
  // True only while a thread's transcript is in flight, so the pane can show
  // its shape instead of an empty box (or worse, the "new chat" hero).
  const [loading, setLoading] = useState(false)
  const [diff, setDiff] = useState<SessionDiff | null>(null)
  const [diffOpen, setDiffOpen] = useState(false)
  const [diffError, setDiffError] = useState('')
  const [merging, setMerging] = useState(false)
  // Below 920px the thread rail is an off-canvas drawer (it has nowhere to
  // sit next to the transcript); above it, this state is inert because the
  // rail is always laid out.
  const [railOpen, setRailOpen] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const composer = useRef<HTMLTextAreaElement>(null)
  // The in-flight ask, so the stop button can abort it.
  const inflight = useRef<AbortController | null>(null)
  // Whether the transcript is parked at the bottom. Autoscrolling on every
  // delta is right while you are watching the reply arrive and wrong the
  // moment you scroll up to re-read something, so follow only when pinned.
  const pinned = useRef(true)

  // Session list (refresh when the active thread changes — its title may).
  useEffect(() => {
    api
      .sessions()
      .then(setSessions)
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [activeId])

  // Load projects for the folder/project picker
  useEffect(() => {
    api
      .projects()
      .then((p) => setProjects(p.projects ?? []))
      .catch(() => {})
  }, [])

  // Load the active thread's transcript.
  useEffect(() => {
    if (!activeId) {
      setSession(null)
      setMsgs([])
      setLoading(false)
      return
    }
    setLoading(true)
    api
      .session(activeId)
      .then((s) => {
        setSession(s)
        setMsgs((s.turns ?? []).map((turn) => ({ ...turn })))
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [activeId])

  // Opening a thread always starts at its newest message.
  useEffect(() => {
    pinned.current = true
  }, [activeId])

  // Picking a thread on a phone should reveal the transcript, not leave the
  // drawer covering the thing you just asked to see.
  useEffect(() => setRailOpen(false), [activeId])

  // Escape closes the drawer — the same key that dismisses every other
  // overlay in the console.
  useEffect(() => {
    if (!railOpen) return
    const on = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setRailOpen(false)
    }
    addEventListener('keydown', on)
    return () => removeEventListener('keydown', on)
  }, [railOpen])

  // Keep the composer's height in step with its value — including the reset
  // to two rows after a send clears it, and after a starter chip fills it.
  useEffect(() => {
    if (composer.current) autoGrow(composer.current)
  }, [input])

  // Follow the newest message while streaming — but only while pinned.
  useEffect(() => {
    if (!pinned.current) return
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

  /** Abort the in-flight reply. The partial text stays on screen — it is a
   *  real partial answer, and the server-side transcript refresh in send()'s
   *  finally block reconciles whatever was actually persisted. */
  function stop() {
    inflight.current?.abort()
  }

  function handleFolderPick(e: Event) {
    const files = (e.target as HTMLInputElement).files
    if (!files || files.length === 0) return
    // Build a readable summary of the picked folder contents
    const paths: string[] = []
    for (let i = 0; i < Math.min(files.length, 50); i++) {
      const f = files[i] as File & { webkitRelativePath?: string }
      if (f.webkitRelativePath) paths.push(f.webkitRelativePath)
      else paths.push(f.name)
    }
    const folderSummary =
      t('sessions.folderSelected', { n: String(files.length) }) + '\n' +
      paths.map((p) => `  - ${p}`).join('\n') +
      (files.length > 50 ? '\n' + t('sessions.folderTruncated', { n: String(files.length - 50) }) : '') +
      '\n\n' + t('sessions.folderFollowUp')
    setInput((prev) => (prev ? `${prev}\n\n${folderSummary}` : folderSummary))
    // Reset so picking the same folder twice still fires change
    if (folderInputRef.current) folderInputRef.current.value = ''
  }

  async function send(e?: Event) {
    e?.preventDefault()
    let prompt = input.trim()
    if (!prompt || busy) return
    // Prepend project context when one is selected
    if (selectedProject) {
      prompt = t('sessions.projectPrefix', { name: selectedProject }) + '\n\n' + prompt
    }
    let id = activeId
    const ctrl = new AbortController()
    inflight.current = ctrl
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
      // Sending is an explicit "show me the answer", so re-pin the view.
      pinned.current = true
      setMsgs((ms) => [...ms, { role: 'user', text: prompt }, { role: 'assistant', text: '', streaming: true }])

      const patch = (fn: (m: ChatMsg) => ChatMsg) =>
        setMsgs((ms) => ms.map((m, i) => (i === ms.length - 1 ? fn(m) : m)))

      await askSessionStream(
        id,
        prompt,
        authorize,
        {
          onDelta: (text) => patch((m) => ({ ...m, text: m.text + text })),
          onStatus: (text) => patch((m) => ({ ...m, status: text })),
          onResult: (r) => patch((m) => ({ ...m, result: r, status: undefined })),
          onError: (message) => patch((m) => ({ ...m, status: undefined, kind: 'error', text: m.text || `⚠ ${message}` })),
        },
        ctrl.signal,
      )
      patch((m) => ({ ...m, streaming: false, status: undefined }))
    } catch (err) {
      if (isAbort(err)) {
        // Stopped on purpose: mark the turn done and note why, rather than
        // painting the user's own action as a failure.
        setMsgs((ms) =>
          ms.map((m, i) =>
            i === ms.length - 1
              ? { ...m, streaming: false, status: undefined, text: m.text || `· ${t('sessions.stopped')}` }
              : m,
          ),
        )
      } else {
        const message = err instanceof Error ? err.message : String(err)
        setMsgs((ms) => [
          ...ms.map((m, i) => (i === ms.length - 1 ? { ...m, streaming: false } : m)),
          { role: 'assistant', kind: 'error', text: `⚠ ${message}` },
        ])
      }
    } finally {
      inflight.current = null
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
    <section class={`chat${railOpen ? ' rail-open' : ''}`}>
      <aside class="thread-rail">
        {/* Not `.primary`: a filled accent slab is the loudest thing on the
            page, and the loudest thing on the page should be the reply you are
            reading, not the button that starts a different conversation. */}
        <button class="btn thread-new" onClick={newChat}>
          <span aria-hidden="true">+</span> {t('sessions.new')}
        </button>
        <div class="thread-list">
          {sessions.length === 0 && <p class="thread-empty">{t('sessions.railEmpty')}</p>}
          {sessions.map((s) => (
            // A row, not a <button>, because it contains its own delete button
            // and nesting buttons is invalid — so the keyboard contract is
            // spelled out by hand instead of inherited.
            <div
              key={s.id}
              class={`thread-item${s.id === activeId ? ' active' : ''}`}
              role="button"
              tabIndex={0}
              aria-current={s.id === activeId}
              onClick={() => onOpenSession(s.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onOpenSession(s.id)
                }
              }}
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

      {/* Scrim behind the drawer: laid out always, painted only in the narrow
          layout while the rail is open. A tap outside the rail closes it. */}
      <div class="rail-scrim" onClick={() => setRailOpen(false)} />

      <div class="chat-pane">
        {/* The header is unconditional so the drawer toggle is reachable even
            on a brand-new, empty thread — that is exactly when a phone user
            needs to get back to the thread list. */}
        <header class="chat-head">
          <button
            class="rail-toggle"
            onClick={() => setRailOpen((v) => !v)}
            aria-label={t('sessions.threads')}
            aria-expanded={railOpen}
          >
            ☰
          </button>
          <h1 class="chat-title">
            {session ? session.title || t('sessions.untitled') : t('sessions.new')}
          </h1>
          {session?.branch && (
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

        <div
          class="chat-log"
          ref={scroller}
          onScroll={(e) => {
            const el = e.currentTarget as HTMLDivElement
            // 48px of slack: "close enough to the bottom" survives the last
            // line of a reply arriving between two scroll events.
            pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48
          }}
        >
          {msgs.length === 0 && loading && <ChatSkeleton />}
          {msgs.length === 0 && !loading && !busy && <ChatHero onPick={(p) => setInput(p)} />}
          {msgs.map((m, i) => (
            <ChatBubble key={i} msg={m} onOpenTask={onOpenTask} />
          ))}
        </div>

        {/* Textarea and actions share one bordered box so the composer reads
            as a single control rather than a field with a toolbar loose
            underneath it — the focus ring belongs to the whole thing. */}
        <form class="composer" onSubmit={send}>
          <div class="composer-toolbar">
            <select
              class="input project-picker"
              value={selectedProject}
              onChange={(e) => setSelectedProject((e.target as HTMLSelectElement).value)}
              title={t('queue.allProjects')}
            >
              <option value="">📂 {t('queue.allProjects')}</option>
              {projects.map((p) => (
                <option key={p} value={p}>
                  📁 {p}
                </option>
              ))}
            </select>
            <button
              class="btn small folder-btn"
              type="button"
              title={t('sessions.folderPick')}
              onClick={() => folderInputRef.current?.click()}
            >
              📁 {t('sessions.folderPick')}
            </button>
            <input
              ref={folderInputRef}
              type="file"
              // @ts-expect-error non-standard attrs for folder picking
              webkitdirectory=""
              directory=""
              multiple
              style="display:none"
              onChange={handleFolderPick}
            />
          </div>
          <div class="composer-box">
            <textarea
              ref={composer}
              class="composer-input"
              rows={2}
              placeholder={t('sessions.placeholder')}
              value={input}
              onInput={(e) => {
                const el = e.target as HTMLTextAreaElement
                setInput(el.value)
                autoGrow(el)
              }}
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
              {busy ? (
                // While a reply streams, the primary action is stopping it —
                // a disabled "Thinking…" button leaves no way out of a bad ask.
                <button class="btn stop-btn" type="button" onClick={stop}>
                  <span class="stop-icon" aria-hidden="true" />
                  {t('sessions.stop')}
                </button>
              ) : (
                <button class="btn primary" type="submit" disabled={!input.trim()}>
                  {t('sessions.send')}
                </button>
              )}
            </div>
          </div>
        </form>
        {loadError && <p class="gate-error composer-error">{loadError}</p>}
      </div>
    </section>
  )
}

/** Transcript placeholder while a thread loads: rows in the shape of the
 *  messages about to replace them. An empty pane reads as a broken request,
 *  and the hero would claim a thread with history is brand new. */
function ChatSkeleton() {
  return (
    <div class="chat-skeleton" aria-hidden="true">
      {[68, 90, 52].map((w, i) => (
        <div class="skel-msg" key={i}>
          <div class="skel-avatar" />
          <div class="skel-lines">
            <div class="skel-line" style={{ width: `${w}%` }} />
            <div class="skel-line" style={{ width: `${Math.max(w - 24, 28)}%` }} />
          </div>
        </div>
      ))}
    </div>
  )
}

/** Empty-state hero: the block wordmark, then a few starter prompts. The mark
 *  is the loud one here — an empty transcript is the only place in the console
 *  with room for it, and it beats a lone illustration for telling you what you
 *  are looking at. */
function ChatHero({ onPick }: { onPick(prompt: string): void }) {
  return (
    <div class="chat-hero">
      <PandaAscii scale={8} />
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
    // What you typed is shown back verbatim: rendering the user's own
    // Markdown would hide the exact text the model received.
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
        <PandaMark size={28} />
      </div>
      <div class="msg-body">
        {/* An error carries no Markdown — keep it literal so a stray `*` in a
            stderr line is not read as emphasis. */}
        {msg.text &&
          (msg.kind === 'error' ? (
            <p class="msg-text">{msg.text}</p>
          ) : (
            <Markdown text={msg.text} class="msg-text" />
          ))}
        {msg.streaming && !msg.text && !msg.status && (
          <span class="status-line">
            <span class="spinner" aria-hidden="true" />
            {t('sessions.thinking')}
          </span>
        )}
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
