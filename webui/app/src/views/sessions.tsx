import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  api,
  askSessionStream,
  isAbort,
  type AskResult,
  type NodeInfo,
  type Session,
  type SessionDiff,
  type SessionTurn,
  type Task,
} from '../api/client'
import { PandaAscii, PandaMark } from '../brand/panda'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { buildCommands } from '../components/palette'
import { rank } from '../components/fuzzy'
import { patchStreaming, slashQuery } from '../components/chatstate'
import { isLiveSession } from '../components/session-guard'
import DecisionOrbit from '../components/orbit'
import FleetTopologyCard from '../components/fleet'
import { JsonInline } from '../components/json-inline'

/** Grow the composer with its content up to a ceiling, then scroll inside.
 *  A fixed two-row box makes pasting a paragraph feel like typing into a
 *  keyhole; unbounded growth would eat the transcript instead. */
const COMPOSER_MAX_PX = 240

function autoGrow(el: HTMLTextAreaElement): void {
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, COMPOSER_MAX_PX)}px`
}

/** Stable keys for chat bubbles. Server turns are keyed by transcript
 *  position (`srv-N`); locally created (optimistic / error) bubbles get a
 *  unique id from this counter so a streaming bubble is never re-keyed
 *  mid-flight when the list around it shifts. */
let nextLocalMsgId = 0
function localMsgId(): string {
  nextLocalMsgId += 1
  return `opt-${nextLocalMsgId}`
}

/** A chat message in the transcript: a stored turn, or the in-flight
 * assistant reply being streamed right now. */
interface ChatMsg extends SessionTurn {
  /** Stable render key — see `localMsgId` above. */
  k: string
  streaming?: boolean
  status?: string
  result?: AskResult
  /** Chain-of-thought for this reply. It streams on its own event and is
   *  display-only (D14): it is never merged into the answer, and it is not
   *  persisted with the turn, so a thread reloaded from disk comes back
   *  without it. */
  thought?: string
}

/** The sessions view: a thread rail on the left (codex / claude code style)
 * and one worktree-isolated conversation on the right, streaming over SSE. */
export function SessionsView({
  activeId,
  project,
  onOpenSession,
  onExitProject,
  onOpenTask,
  onOpenNodes,
  onLogout,
}: {
  activeId: string | null
  project?: string | null
  onOpenSession(id: string): void
  onExitProject?(): void
  onOpenTask(id: string): void
  /** Where the fleet card's "invite a device" CTA leads. Without it the CTA
   *  paints disabled, which reads as a broken button rather than as an
   *  invitation. */
  onOpenNodes(): void
  /** The ⌘K palette's logout entry, reused by the composer's `/`
   *  completion so both surfaces run the same command list. */
  onLogout(): void
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
  // Which thread the in-flight ask belongs to. The transcript loader must not
  // abort an ask that is painting the very thread it is about to load (the
  // create-then-send path), and send()'s finally guard keys on it too.
  const inflightSid = useRef<string | null>(null)
  // Mirror of the activeId prop for send()'s async callbacks: a reply's
  // deltas may only write to the thread that started them. Kept in sync by
  // the effect below; send() fast-forwards it through the create flow.
  const activeIdRef = useRef<string | null>(activeId)
  // Which row of the `/` completion menu is highlighted.
  const [completeCursor, setCompleteCursor] = useState(0)
  // The input as of the last Escape: Escape hides the menu until the text
  // changes again, so the slash prefix alone cannot force it back open.
  const [completeDismissed, setCompleteDismissed] = useState('')
  // Whether the transcript is parked at the bottom. Autoscrolling on every
  // delta is right while you are watching the reply arrive and wrong the
  // moment you scroll up to re-read something, so follow only when pinned.
  const pinned = useRef(true)

  // Session list (refresh when the active thread changes — its title may, or when project changes).
  useEffect(() => {
    api
      .sessions(project || undefined)
      .then(setSessions)
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [activeId, project])

  // Load projects for the folder/project picker
  useEffect(() => {
    api
      .projects()
      .then((p) => setProjects(p.projects ?? []))
      .catch(() => {})
  }, [])

  // Keep selectedProject in sync with active project prop
  useEffect(() => {
    if (project) {
      setSelectedProject(project)
    } else {
      setSelectedProject('')
    }
  }, [project])

  // Boundary guard: if a session is active but does not belong to the newly entered project, unselect it.
  useEffect(() => {
    if (activeId && session && project && session.project !== project) {
      onOpenSession('')
    }
  }, [project, session, activeId])

  // Live node directory + change signal → re-fetch on SSE changes so the
  // fleet card and single-node orbit collapse both always show current net.
  const nodeTick = useChangeSignal()
  const { data: nodesData } = useAsync<NodeInfo[]>(
    () => api.nodes().catch(() => [] as NodeInfo[]),
    [],
    nodeTick,
  )
  const nodes: NodeInfo[] = nodesData ?? []
  const selfNodeId = nodes.find((n) => n.is_local)?.id
  // Orbit's single-node branch keys on REACHABLE devices: an offline row in
  // the directory is not a routing candidate, so it must not suppress the
  // "add a second device" CTA.
  const onlineNodeCount = nodes.filter((n) => n.status === 'online').length

  // Keep the async guard's mirror in step with the prop.
  useEffect(() => {
    activeIdRef.current = activeId
  }, [activeId])

  // Load the active thread's transcript. Leaving a thread mid-stream first
  // aborts the old ask's client-side consumption — its deltas must not land
  // in another thread's pane. The model turn itself keeps running on the
  // server; the persisted transcript is the eventual truth, and whichever
  // thread comes back on screen refetches it. One exception stays: an
  // in-flight ask that belongs to the thread being opened (create-then-send)
  // paints its own transcript and must not be clobbered by the server's
  // shorter view.
  useEffect(() => {
    if (!activeId) {
      setSession(null)
      setMsgs([])
      setLoading(false)
      return
    }
    if (inflight.current && inflightSid.current === activeId) return
    inflight.current?.abort()
    inflight.current = null
    inflightSid.current = null
    setLoading(true)
    api
      .session(activeId)
      .then((s) => {
        setSession(s)
        setMsgs((s.turns ?? []).map((turn, i) => ({ ...turn, k: `srv-${i}` })))
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [activeId])

  // Unmount: nothing should keep streaming into a dead pane.
  useEffect(() => () => inflight.current?.abort(), [])

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
  // A fresh query also restarts the completion highlight at the top hit.
  useEffect(() => {
    if (composer.current) autoGrow(composer.current)
    setCompleteCursor(0)
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
    const s = await api.createSession(undefined, project || selectedProject || undefined)
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
    let id = activeId
    const ctrl = new AbortController()
    inflight.current = ctrl
    try {
      // No thread yet: create one titled after the first prompt.
      if (!id) {
        const s = await api.createSession(prompt.slice(0, 48), project || selectedProject || undefined)
        id = s.id
        setSession(s)
        setSessions((ls) => [s, ...ls])
        onOpenSession(s.id)
        // The prop mirror lags this synchronous flow by one render; catch it
        // up so the guards below recognize the freshly created thread.
        activeIdRef.current = s.id
      }
      // Race guard: snapshot the thread this reply belongs to. Every write
      // below checks it — if the user switched threads mid-turn the writes
      // are silently dropped. That is safe because the panel persists every
      // turn: the refetch in finally (and the new thread's own loader) is
      // the eventual truth for whichever thread ends up on screen. The
      // stream's rendering stops; the server-side turn is not cancelled.
      const sid = id
      inflightSid.current = sid
      const live = () => isLiveSession(sid, activeIdRef.current)
      setBusy(true)
      if (live()) {
        setInput('')
        // Sending is an explicit "show me the answer", so re-pin the view.
        pinned.current = true
        setMsgs((ms) => [
          ...ms,
          { role: 'user', text: prompt, k: localMsgId() },
          { role: 'assistant', text: '', streaming: true, k: localMsgId() },
        ])
      }

      // Aim every delta at the bubble actually streaming. A refetch racing
      // the stream can leave a stale message at the tail; patching the tail
      // blindly used to write replies into the user's own message. The
      // session guard rides along: a backgrounded reply patches nothing.
      const patch = (fn: (m: ChatMsg) => ChatMsg) => {
        if (!live()) return
        setMsgs((ms) => patchStreaming(ms, fn))
      }

      await askSessionStream(
        id,
        prompt,
        authorize,
        {
          onReasoning: (text) => patch((m) => ({ ...m, thought: (m.thought ?? '') + text })),
          onDelta: (text) => patch((m) => ({ ...m, text: m.text + text })),
          onStatus: (text) => patch((m) => ({ ...m, status: text })),
          onResult: (r) => patch((m) => ({ ...m, result: r, status: undefined })),
          onError: (message) => patch((m) => ({ ...m, status: undefined, kind: 'error', text: m.text || `⚠ ${message}` })),
        },
        ctrl.signal,
      )
      patch((m) => ({ ...m, streaming: false, status: undefined }))
    } catch (err) {
      if (isLiveSession(id, activeIdRef.current)) {
        if (isAbort(err)) {
          // Stopped on purpose: mark the turn done and note why, rather than
          // painting the user's own action as a failure. Aimed at the bubble
          // that was streaming — same target rule as the deltas above.
          setMsgs((ms) =>
            patchStreaming(ms, (m) => ({
              ...m,
              streaming: false,
              status: undefined,
              text: m.text || `· ${t('sessions.stopped')}`,
            })),
          )
        } else {
          const message = err instanceof Error ? err.message : String(err)
          setMsgs((ms) => [
            ...ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
            { role: 'assistant', kind: 'error', text: `⚠ ${message}`, k: localMsgId() },
          ])
        }
      }
      // Else the user switched threads mid-turn: the pane already belongs to
      // the transcript loader of the new thread, so this reply's failure
      // (or stop) has nowhere to render. The persisted transcript stands.
    } finally {
      if (inflight.current === ctrl) {
        inflight.current = null
        inflightSid.current = null
      }
      setBusy(false)
      // Persisted transcript is server-side truth; refresh title + turns.
      if (id) {
        const stillActive = isLiveSession(id, activeIdRef.current)
        api.session(id).then((s) => {
          // The rail entry's title is worth refreshing either way; the pane
          // belongs to whichever thread is active now.
          setSessions((ls) => ls.map((x) => (x.id === s.id ? s : x)))
          if (stillActive) setSession(s)
        }).catch(() => {})
      }
    }
  }

  // `/` completion (parity with the CLI REPL's Tab completion): the composer
  // borrows the ⌘K palette's command list — one slash and a few letters open
  // a view, flip the theme, or log out without leaving the keyboard.
  const slashToken = slashQuery(input)
  const commands = useMemo(() => buildCommands(onLogout), [onLogout])
  const completeShown = useMemo(
    () =>
      slashToken === null || input === completeDismissed
        ? []
        : rank(commands, slashToken, (c) => [c.label, c.alias ?? '', c.id.replace(':', ' ')]),
    [commands, slashToken, input, completeDismissed],
  )
  // Ranking reorders on every keystroke; clamp instead of trusting the old
  // index still points at a row.
  const completeIdx = Math.min(completeCursor, Math.max(completeShown.length - 1, 0))

  function acceptCompletion(run: () => void) {
    setInput('')
    setCompleteDismissed('')
    run()
  }

  return (
    <section class={`chat${railOpen ? ' rail-open' : ''}`}>
      <aside class="thread-rail">
        {project && (
          <div
            class="project-scope-banner"
            style="display: flex; align-items: center; justify-content: space-between; padding: 6px 10px; margin-bottom: 8px; background: var(--bg-hover, #f0f0f0); border-radius: 6px; font-size: 0.85rem;"
          >
            <span style="font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">
              📁 {project}
            </span>
            {onExitProject && (
              <button
                class="btn small"
                type="button"
                onClick={onExitProject}
                title={t('projects.exit')}
                style="padding: 2px 6px; font-size: 0.75rem; margin-left: 6px;"
              >
                ✕
              </button>
            )}
          </div>
        )}
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
          {msgs.length === 0 && !loading && !busy && (
            <ChatEmpty
              nodes={nodes}
              selfNodeId={selfNodeId}
              onPick={(p) => setInput(p)}
              onAddDevice={onOpenNodes}
            />
          )}
          {msgs.map((m) => (
            <ChatBubble
              key={m.k}
              msg={m}
              onOpenTask={onOpenTask}
              onlineNodeCount={onlineNodeCount}
              selfNodeId={selfNodeId}
            />
          ))}
        </div>

        {/* Textarea and actions share one bordered box so the composer reads
            as a single control rather than a field with a toolbar loose
            underneath it — the focus ring belongs to the whole thing. */}
        <form class="composer" onSubmit={send}>
          <div class="composer-toolbar">
            {!project && (
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
            )}
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
            {completeShown.length > 0 && (
              <div class="complete-menu" role="listbox" aria-label={t('palette.title')}>
                {completeShown.map((c, i) => (
                  <button
                    key={c.id}
                    type="button"
                    role="option"
                    aria-selected={i === completeIdx}
                    class={`complete-item${i === completeIdx ? ' active' : ''}`}
                    onMouseMove={() => setCompleteCursor(i)}
                    onClick={() => acceptCompletion(c.run)}
                  >
                    <span class="complete-label">{c.label}</span>
                    <span class="complete-hint">{c.hint ?? c.group}</span>
                  </button>
                ))}
              </div>
            )}
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
                if (completeShown.length > 0) {
                  // While the menu is open the navigation keys belong to it:
                  // Tab/Enter accept, arrows move, Escape hides it until the
                  // input changes again.
                  if (e.key === 'Tab' || e.key === 'Enter') {
                    e.preventDefault()
                    const cmd = completeShown[completeIdx]
                    if (cmd) acceptCompletion(cmd.run)
                    return
                  }
                  if (e.key === 'ArrowDown' || (e.ctrlKey && e.key === 'n')) {
                    e.preventDefault()
                    setCompleteCursor((completeIdx + 1) % completeShown.length)
                    return
                  }
                  if (e.key === 'ArrowUp' || (e.ctrlKey && e.key === 'p')) {
                    e.preventDefault()
                    setCompleteCursor((completeIdx - 1 + completeShown.length) % completeShown.length)
                    return
                  }
                  if (e.key === 'Escape') {
                    e.preventDefault()
                    setCompleteDismissed(input)
                    return
                  }
                }
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

/** Empty-state split-screen (D5). Left 50% paints the FleetTopologyCard so
 *  a single-node user immediately sees the network CTA, while a multi-node
 *  user reads their node directory. Right 50% is the chat "hero" — the
 *  Panda mark + starter prompts. Below 960px the grid collapses to one
 *  column and fleet stacks above the hero. */
function ChatEmpty(props: {
  nodes: NodeInfo[]
  selfNodeId?: string
  onPick(prompt: string): void
  onAddDevice(): void
}) {
  const { nodes, selfNodeId, onPick, onAddDevice } = props
  return (
    <div class="chat-empty">
      <FleetTopologyCard nodes={nodes} selfNodeId={selfNodeId} onAddDevice={onAddDevice} />
      <div class="chat-empty-cta">
        <PandaAscii scale={5} />
        <h1>{t('sessions.hello')}</h1>
        <p>{t('sessions.hint')}</p>
        <div class="hero-chips u-mt-0">
          {(['s1', 's2', 's3'] as const).map((k) => (
            <button key={k} class="chip" onClick={() => onPick(t(`sessions.${k}`))}>
              {t(`sessions.${k}`)}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

function ChatBubble(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  onlineNodeCount?: number
  selfNodeId?: string
}) {
  const { msg, onOpenTask, onlineNodeCount, selfNodeId } = props
  const [chainOpen, setChainOpen] = useState(false)

  if (msg.role === 'user') {
    // What you typed is shown back verbatim: rendering the user's own
    // Markdown would hide the exact text the model received.
    return (
      <div class="msg user">
        <div class="msg-avatar you">You</div>
        <div class="msg-body bubble role-user">
          <div class="bubble-slot-row slot-title" />
          <div class="bubble-slot-row slot-chat">
            <p class="msg-text u-m-0 u-w-100">{msg.text}</p>
          </div>
          <div class="bubble-slot-row slot-meta" />
        </div>
      </div>
    )
  }

  const isTaskKind = msg.kind === 'task' && Boolean(msg.ref)
  const hasTaskStatus = Boolean(msg.streaming && msg.status)
  const hasThinking = Boolean(msg.streaming && !msg.text && !msg.status)

  return (
    <div class="msg panda">
      <div class="msg-avatar">
        <PandaMark size={28} />
      </div>
      <div class="msg-body bubble role-panda">
        {/* — slot-title: orbit strip for task-class messages (collapsed by
              default), or the streaming "thinking / status" so the spinner
              lives in the same lane regardless of whether there's text yet. */}
        <div class="bubble-slot-row slot-title">
          {isTaskKind ? (
            <div class="u-flex-1">
              <TaskOrbit
                taskId={msg.ref!}
                onlineNodeCount={onlineNodeCount}
                selfNodeId={selfNodeId}
              />
            </div>
          ) : (
            <div class="u-flex-1" />
          )}
          {hasThinking && (
            <span class="status-line u-color-tert">
              <span class="spinner" aria-hidden="true" />
              {t('sessions.thinking')}
            </span>
          )}
          {hasTaskStatus && (
            <span class="status-line u-color-tert">
              <span class="spinner" aria-hidden="true" />
              {msg.status}
            </span>
          )}
        </div>

        {/* — slot-thought: chain-of-thought, above the answer the way a
              reasoning model produces it. It starts open while thinking is all
              that is happening and folds once prose arrives, so the reply is
              not pushed down by the working that produced it. */}
        {msg.thought && (
          <div class="bubble-slot-row slot-thought u-w-100">
            <ThoughtBlock text={msg.thought} live={hasThinking} />
          </div>
        )}

        {/* — slot-chat: primary copy (markdown / literal error / empty). */}
        <div class="bubble-slot-row slot-chat u-w-100">
          {msg.text &&
            (msg.kind === 'error' ? (
              <p class="msg-text">{msg.text}</p>
            ) : (
              <Markdown text={msg.text} class="msg-text" />
            ))}
          {msg.streaming && msg.text && <span class="cursor" aria-hidden="true" />}
        </div>

        {/* — slot-meta: task card (link + chain) stays below the copy, inside
              the meta lane so its chrome reads like context, not a separate
              card. */}
        <div class="bubble-slot-row slot-meta">
          {isTaskKind && msg.ref && (
            <div class="task-card u-flex-1 u-min-w-0">
              <span class="badge blue">
                {t('state.running') === msg.result?.task_state
                  ? t('sessions.taskCreated')
                  : msg.result?.task_state || t('sessions.taskCreated')}
              </span>
              {msg.result?.report && <Markdown text={msg.result.report} class="task-report" />}
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
    </div>
  )
}

/** Mounts DecisionOrbit for a single task id. Loads the task once for its
 *  Task.traces / plan_meta / delegation_chain (the initial hydrate), and
 *  leaves SSE tailing to DecisionOrbit → useTraceForTask inside. */
function TaskOrbit(props: {
  taskId: string
  onlineNodeCount?: number
  selfNodeId?: string
}) {
  const change = useChangeSignal()
  const { data: task } = useAsync<Task>(
    () => api.task(props.taskId).catch(() => null as any),
    [props.taskId],
    change,
  )
  return (
    <DecisionOrbit
      task={task ?? undefined}
      onlineNodeCount={props.onlineNodeCount}
      selfNodeId={props.selfNodeId}
      defaultOpen={false}
    />
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
          {ev.data && ev.data !== '{}' && <JsonInline raw={ev.data} />}
        </li>
      ))}
    </ol>
  )
}

/** Chain-of-thought, shown apart from the answer.
 *
 *  Reasoning arrives on its own stream and is display-only (D14): it never
 *  enters the answer text, the stored turn, or a task result. That is why it
 *  gets its own block rather than being prepended to the reply — and why a
 *  thread reloaded from disk comes back without it.
 *
 *  While the model is reasoning it is the only thing happening, so the block
 *  starts open; the caller folds it once prose arrives. */
function ThoughtBlock({ text, live }: { text: string; live: boolean }) {
  return (
    <details class="thought-block" open={live}>
      <summary class="u-color-tert">
        {t('sessions.thought')}
        {live && <span class="spinner" aria-hidden="true" />}
      </summary>
      <div class="thought-body">{text}</div>
    </details>
  )
}
