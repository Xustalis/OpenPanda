import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  api,
  askSessionStream,
  isAbort,
  type AskResult,
  type FsFileEntry,
  type NodeInfo,
  type Session,
  type SessionDiff,
  type SessionTurn,
  type Task,
} from '../api/client'
import { PandaAscii, PandaMark } from '../brand/panda'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { navigateView } from '../nav'
import { Markdown } from '../md/render'
import { toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { buildCommands, type Command } from '../components/palette'
import { rank } from '../components/fuzzy'
import { patchStreaming, slashQuery } from '../components/chatstate'
import { atQuery, expandFileRefs, exportMarkdown, exportFilename } from '../components/attach'
import { isLiveSession } from '../components/session-guard'
import DecisionOrbit from '../components/orbit'
import FleetTopologyCard from '../components/fleet'
import { EventTimeline } from '../components/event-timeline'
import { acquireKeepAlive } from '../utils/keepalive'

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
  /** @file paths that were expanded into fenced blocks for this prompt —
   *  shown as attachment chips on the user's bubble. */
  files?: string[]
}

/** One row in the composer's completion menu — a slash command, a palette
 *  destination, or a filesystem path. Palette `Command`s satisfy the shape:
 *  a run() that ignores the argument is still a valid (arg?) => void. */
interface CompletionItem {
  id: string
  label: string
  hint?: string
  group?: string
  run(arg?: string): void
}

function fmtSize(n?: number): string {
  if (n === undefined) return ''
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)}M`
  if (n >= 1024) return `${(n / 1024).toFixed(1)}k`
  return `${n}B`
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
  const [selectedProject, setSelectedProject] = useState(project || '')
  const [projects, setProjects] = useState<string[]>([])
  const activeProject = project || selectedProject || ''
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
  // Exact server-issued operation identity for the current ask. Stop is scoped
  // to this id; it never scans old transcript task references.
  const inflightOperation = useRef<string | null>(null)
  // Mirror of the activeId prop for send()'s async callbacks: a reply's
  // deltas may only write to the thread that started them. Kept in sync by
  // the effect below; send() fast-forwards it through the create flow.
  const activeIdRef = useRef<string | null>(activeId)
  // Which row of the `/` completion menu is highlighted.
  const [completeCursor, setCompleteCursor] = useState(0)
  // The input as of the last Escape: Escape hides the menu until the text
  // changes again, so the slash prefix alone cannot force it back open.
  const [completeDismissed, setCompleteDismissed] = useState('')
  // Caret position in the composer — @file completion is caret-scoped, not
  // tail-scoped, so editing mid-line still completes the token under it.
  const [caret, setCaret] = useState(0)
  // Filesystem rows for the @file completion menu (debounced query).
  const [atFiles, setAtFiles] = useState<FsFileEntry[]>([])
  // Whether the transcript is parked at the bottom. Autoscrolling on every
  // delta is right while you are watching the reply arrive and wrong the
  // moment you scroll up to re-read something, so follow only when pinned.
  const pinned = useRef(true)
  const [isPinned, setIsPinned] = useState(true)
  const [isEditingTitle, setIsEditingTitle] = useState(false)
  const [titleInput, setTitleInput] = useState('')

  useEffect(() => {
    if (session) setTitleInput(session.title || '')
    setIsEditingTitle(false)
  }, [session?.id])

  // Session list (refresh when the active thread changes — its title may, or when project changes).
  useEffect(() => {
    api
      .sessions(activeProject || undefined)
      .then(setSessions)
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [activeId, activeProject])

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
    }
  }, [project])

  // Boundary guard: if a session is active but does not belong to the newly entered project, unselect it.
  useEffect(() => {
    if (activeId && session && activeProject && session.project && session.project !== activeProject) {
      onOpenSession('')
    }
  }, [activeProject, session, activeId])

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
    inflightOperation.current = null
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

  // When the window regains focus or tab becomes visible, reconcile the active thread
  // in case the server completed a background turn while the tab was hidden or blurred.
  useEffect(() => {
    const onWake = () => {
      if (!activeId || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
      api
        .session(activeId)
        .then((s) => {
          if (activeIdRef.current !== activeId) return
          const turns = s.turns ?? []
          const last = turns[turns.length - 1]
          if (last?.role === 'assistant') {
            setSession(s)
            setMsgs(turns.map((turn, i) => ({ ...turn, k: `srv-${i}` })))
            setBusy(false)
          }
        })
        .catch(() => {})
    }
    addEventListener('visibilitychange', onWake)
    addEventListener('focus', onWake)
    return () => {
      removeEventListener('visibilitychange', onWake)
      removeEventListener('focus', onWake)
    }
  }, [activeId])

  // Unmount: nothing should keep streaming into a dead pane.
  useEffect(() => () => inflight.current?.abort(), [])

  // Opening a thread always starts at its newest message.
  useEffect(() => {
    pinned.current = true
    setIsPinned(true)
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

  /** Abort local rendering and cancel only the exact active server operation. */
  function stop() {
    const ctrl = inflight.current
    const targetSid = inflightSid.current
    const operationID = inflightOperation.current
    ctrl?.abort()
    if (targetSid && operationID) {
      api.cancelSession(targetSid, operationID).catch(() => {})
    }
  }

  async function send(e?: Event) {
    e?.preventDefault()
    const raw = input.trim()
    if (!raw || busy || inflight.current) return
    // A slash line is a command, never a prompt — the REPL's rule.
    if (raw.startsWith('/') && dispatchSlash(raw)) {
      setInput('')
      setCompleteDismissed('')
      return
    }
    // Claim the slot before the async file expansion — a second Enter during
    // the /api/fs/read round-trips must not start a parallel ask.
    const ctrl = new AbortController()
    inflight.current = ctrl
    let id = activeId
    const keepAlive = acquireKeepAlive()
    let patch: (fn: (m: ChatMsg) => ChatMsg) => void = () => {}
    let prompt = raw
    let attached: string[] = []
    try {
      // @file references become inline fenced blocks before the prompt
      // leaves the composer — "explain @main.go" works without pasting the
      // file. Inside try so a rejected expansion still clears inflight.
      const expanded = await expandFileRefs(raw, (p) => api.fsRead(p))
      prompt = expanded.prompt
      attached = expanded.attached
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
        setIsPinned(true)
        setMsgs((ms) => [
          ...ms,
          { role: 'user', text: raw, files: attached.length ? attached : undefined, k: localMsgId() },
          { role: 'assistant', text: '', streaming: true, k: localMsgId() },
        ])
      }

      // Aim every delta at the bubble actually streaming. A refetch racing
      // the stream can leave a stale message at the tail; patching the tail
      // blindly used to write replies into the user's own message. The
      // session guard rides along: a backgrounded reply patches nothing.
      patch = (fn: (m: ChatMsg) => ChatMsg) => {
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
          onOperation: (operationID) => {
            if (inflight.current === ctrl && inflightSid.current === sid) {
              inflightOperation.current = operationID
            }
          },
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
          // The operation snapshot is durable in the session. Follow that
          // exact generation until it terminalizes instead of guessing from a
          // fixed number of transcript polls.
          let resolved = false
          if (id) {
            patch((m) => ({ ...m, status: t('sessions.syncing') }))
            let delay = 500
            while (!ctrl.signal.aborted && isLiveSession(id, activeIdRef.current)) {
              try {
                const s = await api.session(id)
                const operationID = inflightOperation.current
                if (operationID && s.operation?.id === operationID && s.operation.status !== 'running') {
                  setSession(s)
                  setMsgs((s.turns ?? []).map((turn, i) => ({ ...turn, k: `srv-${i}` })))
                  resolved = true
                  break
                }
              } catch {}
              await new Promise((r) => setTimeout(r, delay))
              delay = Math.min(delay * 2, 5000)
            }
          }
          if (!resolved && isLiveSession(id, activeIdRef.current) && !ctrl.signal.aborted) {
            const message = err instanceof Error ? err.message : String(err)
            setMsgs((ms) => [
              ...ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
              { role: 'assistant', kind: 'error', text: `⚠ ${message}`, k: localMsgId() },
            ])
          }
        }
      }
      // Else the user switched threads mid-turn: the pane already belongs to
      // the transcript loader of the new thread, so this reply's failure
      // (or stop) has nowhere to render. The persisted transcript stands.
    } finally {
      keepAlive.release()
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

  // TUI-parity slash commands: the ones that act inside this view (new
  // thread, export, clear) plus short hops to the views backing the REPL's
  // informational verbs (/cost → system, /model → models registry…). Not
  // memoized: several run()s close over live render state (msgs for /export,
  // the thread list for /new) and a stale closure is a menu item that
  // silently does the wrong thing. A dozen small objects — free to rebuild.
  const chatCommands: CompletionItem[] = (() => {
    const grp = t('sessions.cmdGroup')
    const go = (view: string) => () => navigateView(view)
    return [
      { id: 'cmd:new', group: grp, label: '/new', hint: t('sessions.cmdNew'), run: () => void newChat() },
      { id: 'cmd:export', group: grp, label: '/export', hint: t('sessions.cmdExport'), run: exportSession },
      { id: 'cmd:clear', group: grp, label: '/clear', hint: t('sessions.cmdClear'), run: () => setInput('') },
      { id: 'cmd:cost', group: grp, label: '/cost', hint: t('sessions.cmdCost'), run: go('settings:system') },
      { id: 'cmd:context', group: grp, label: '/context', hint: t('sessions.cmdContext'), run: go('settings:system') },
      { id: 'cmd:doctor', group: grp, label: '/doctor', hint: t('sessions.cmdDoctor'), run: go('settings:system') },
      { id: 'cmd:model', group: grp, label: '/model', hint: t('sessions.cmdModel'), run: go('settings:models') },
      { id: 'cmd:tasks', group: grp, label: '/tasks', hint: t('sessions.cmdTasks'), run: go('queue') },
      { id: 'cmd:plans', group: grp, label: '/plans', hint: t('sessions.cmdPlans'), run: go('plans') },
      { id: 'cmd:sessions', group: grp, label: '/sessions', hint: t('sessions.cmdSessions'), run: () => setRailOpen((v) => !v) },
      {
        id: 'cmd:read',
        group: grp,
        label: '/read',
        hint: t('sessions.cmdRead'),
        run: (arg) => setInput(arg ? `@${arg} ` : '@'),
      },
    ]
  })()

  // `/` completion (parity with the CLI REPL's Tab completion): TUI verbs
  // first, then the ⌘K palette's destinations — one slash and a few letters
  // run an action, open a view, flip the theme, or log out.
  const slashToken = slashQuery(input)
  const commands: CompletionItem[] = [
    ...chatCommands,
    ...(buildCommands(onLogout) as CompletionItem[]),
  ]

  // @file completion: the caret sits on an @token → list matching paths.
  const atTok = atQuery(input, caret)
  useEffect(() => {
    if (!atTok) {
      setAtFiles([])
      return
    }
    let alive = true
    const timer = setTimeout(() => {
      api
        .fsFiles(atTok.token)
        .then((r) => alive && setAtFiles(r.entries.slice(0, 12)))
        .catch(() => alive && setAtFiles([]))
    }, 120)
    return () => {
      alive = false
      clearTimeout(timer)
    }
  }, [atTok?.token])

  // Not memoized for the same reason as chatCommands — insertAtRef splices
  // the token against the live input/caret, and a memoized item captures
  // whichever render produced it.
  const fileItems: CompletionItem[] = atFiles.map((f) => ({
    id: `file:${f.path}`,
    label: f.name + (f.dir ? '/' : ''),
    hint: f.dir ? 'dir' : fmtSize(f.size),
    run: () => insertAtRef(f),
  }))

  const completeShown: CompletionItem[] = (() => {
    if (input === completeDismissed) return []
    if (slashToken !== null) {
      return rank(commands, slashToken, (c) => [c.label, c.id.replace(':', ' '), 'alias' in c ? (c as Command).alias ?? '' : ''])
    }
    if (atTok) return fileItems
    return []
  })()
  // Ranking reorders on every keystroke; clamp instead of trusting the old
  // index still points at a row.
  const completeIdx = Math.min(completeCursor, Math.max(completeShown.length - 1, 0))

  /** Replace the @token under the caret with the picked path; directories
   *  keep the token open so the menu refetches one level deeper. */
  function insertAtRef(f: FsFileEntry) {
    if (!atTok) return
    const next = `${input.slice(0, atTok.start)}@${f.path}${f.dir ? '/' : ' '}${input.slice(caret)}`
    const newCaret = atTok.start + f.path.length + (f.dir ? 2 : 2)
    setInput(next)
    setCaret(newCaret)
    setCompleteDismissed('')
    requestAnimationFrame(() => {
      composer.current?.focus()
      composer.current?.setSelectionRange(newCaret, newCaret)
    })
  }

  function acceptCompletion(item: CompletionItem) {
    if (item.id.startsWith('file:')) {
      item.run()
      return
    }
    // Text already typed after the command name is its argument: `/read
    // foo.go` + Enter runs /read with "foo.go", not a bare menu pick that
    // would silently drop the path.
    const arg = /^\/\S+\s+(.+)$/.exec(input)?.[1]?.trim() || undefined
    setInput('')
    setCompleteDismissed('')
    item.run(arg)
  }

  /** `/export`: serialize the live transcript to Markdown and download it —
   *  the web equivalent of the REPL writing chat-<ts>.md to disk. */
  function exportSession() {
    if (msgs.length === 0) return
    const blob = new Blob(
      [exportMarkdown(msgs, { project: session?.project ?? activeProject, title: session?.title })],
      { type: 'text/markdown' },
    )
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = exportFilename(session?.title)
    a.click()
    URL.revokeObjectURL(url)
  }

  /** `/cmd args` typed straight into the box and submitted: run the matching
   *  chat command instead of sending it as a prompt (the REPL's rule — a
   *  slash line is a command, never a question). Returns false when nothing
   *  matched, so send() can fall through to a normal ask. */
  function dispatchSlash(text: string): boolean {
    const m = /^\/(\S+)\s*(.*)$/.exec(text)
    if (!m) return false
    const [, name, arg = ''] = m
    const cmd = chatCommands.find((c) => c.label === `/${name}`)
    if (cmd) {
      cmd.run(arg.trim())
      return true
    }
    // Palette destinations count too — `/queue` navigates like the palette.
    const pal = (buildCommands(onLogout) as CompletionItem[]).find(
      (c) => c.id === `go:${name}` || c.id === `settings:${name}`,
    )
    if (pal) {
      pal.run()
      return true
    }
    return false
  }

  return (
    <section class={`chat${railOpen ? ' rail-open' : ''}`}>
      <aside class="thread-rail">
        <div class="thread-rail-project-picker">
          <select
            class="input thread-project-select"
            value={activeProject}
            onChange={(e) => {
              const val = (e.target as HTMLSelectElement).value
              setSelectedProject(val)
              const hash = val ? `#/chat?project=${encodeURIComponent(val)}` : `#/chat`
              location.hash = hash
            }}
            title={t('sessions.activeProject')}
          >
            <option value="">📂 {t('sessions.allProjects')}</option>
            {projects.map((p) => (
              <option key={p} value={p}>
                📁 {p}
              </option>
            ))}
          </select>
        </div>

        {activeProject && (
          <div class="project-scope-banner">
            <span class="project-scope-label">
              📁 {activeProject}
            </span>
            <button
              class="btn-icon project-scope-clear"
              type="button"
              onClick={() => {
                setSelectedProject('')
                if (onExitProject) onExitProject()
                else location.hash = `#/chat`
              }}
              title={t('projects.exit')}
            >
              ✕
            </button>
          </div>
        )}

        <button class="btn thread-new" onClick={newChat}>
          <span aria-hidden="true">+</span> {t('sessions.new')}
        </button>

        <div class="thread-list">
          {sessions.length === 0 && <p class="thread-empty">{t('sessions.railEmpty')}</p>}
          {sessions.map((s) => (
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
              <div class="thread-item-main">
                <span class="thread-title">{s.title || t('sessions.untitled')}</span>
                {s.project && !activeProject && (
                  <span class="thread-project-pill">📁 {s.project}</span>
                )}
              </div>
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
          <div class="chat-title-group">
            {isEditingTitle && session ? (
              <form
                class="chat-title-form"
                onSubmit={async (e) => {
                  e.preventDefault()
                  const trimmed = titleInput.trim()
                  if (!trimmed || trimmed === session.title) {
                    setIsEditingTitle(false)
                    return
                  }
                  try {
                    const updated = await api.patchSession(session.id, { title: trimmed })
                    setSession(updated)
                    setSessions((ls) => ls.map((item) => (item.id === updated.id ? updated : item)))
                    setIsEditingTitle(false)
                  } catch (err) {
                    toastError(err)
                  }
                }}
              >
                <input
                  class="input chat-title-input"
                  value={titleInput}
                  autoFocus
                  onInput={(e) => setTitleInput((e.target as HTMLInputElement).value)}
                  onBlur={() => setIsEditingTitle(false)}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') setIsEditingTitle(false)
                  }}
                  placeholder={t('sessions.untitled')}
                />
              </form>
            ) : (
              <h1
                class={`chat-title${session ? ' editable' : ''}`}
                title={session ? t('sessions.editTitle') : undefined}
                onClick={() => {
                  if (session) {
                    setTitleInput(session.title || '')
                    setIsEditingTitle(true)
                  }
                }}
              >
                <span class="chat-title-text">{session ? session.title || t('sessions.untitled') : t('sessions.new')}</span>
                {session && <span class="chat-title-edit-icon" aria-hidden="true">✎</span>}
              </h1>
            )}
            {session && (
              <div class="session-project-assigner">
                <span class="dim" aria-hidden="true">📁</span>
                <select
                  class="session-project-badge"
                  value={session.project || ''}
                  onChange={async (e) => {
                    const newProj = (e.target as HTMLSelectElement).value
                    try {
                      const updated = await api.patchSession(session.id, { project: newProj })
                      setSession(updated)
                      setSessions((ls) => ls.map((item) => item.id === updated.id ? updated : item))
                    } catch (err) {
                      toastError(err)
                    }
                  }}
                  title={t('sessions.assignProject')}
                >
                  <option value="">{t('sessions.noProject')}</option>
                  {projects.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </div>
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
          <CostChip />
          {session && msgs.length > 0 && (
            <button
              class="icon-btn"
              onClick={exportSession}
              data-tip={t('sessions.export')}
              aria-label={t('sessions.export')}
            >
              ⤓
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
            {diff.patch && <DiffViewer patch={diff.patch} />}
          </div>
        )}

        <div
          class="chat-log"
          ref={scroller}
          onScroll={(e) => {
            const el = e.currentTarget as HTMLDivElement
            // 48px of slack: "close enough to the bottom" survives the last
            // line of a reply arriving between two scroll events.
            const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48
            pinned.current = atBottom
            setIsPinned(atBottom)
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

        {!isPinned && (
          <button
            type="button"
            class="scroll-bottom-btn"
            title={t('sessions.jumpBottom')}
            onClick={() => {
              pinned.current = true
              setIsPinned(true)
              scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: 'smooth' })
            }}
          >
            <span class="scroll-bottom-icon" aria-hidden="true">↓</span>
            <span>{t('sessions.jumpBottom')}</span>
          </button>
        )}

        {/* Textarea and actions share one bordered box so the composer reads
            as a single control rather than a field with a toolbar loose
            underneath it — the focus ring belongs to the whole thing. */}
        <form class="composer" onSubmit={send}>
          <div class="composer-toolbar">
            <div class="composer-project-pill dim" title={session?.project || activeProject ? `项目: ${session?.project || activeProject}` : t('sessions.noProject')}>
              <span>📁 {session?.project || activeProject || t('sessions.noProject')}</span>
            </div>
          </div>
          <div class={`composer-box${busy ? ' composer-running' : ''}`}>
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
                    onClick={() => acceptCompletion(c)}
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
              placeholder={busy ? '⚡ ' + (t('sessions.placeholderRunning') || 'Agent 正在执行... 可点击停止或调整指令') : t('sessions.placeholder')}
              value={input}
              onInput={(e) => {
                const el = e.target as HTMLTextAreaElement
                setInput(el.value)
                setCaret(el.selectionStart)
                autoGrow(el)
              }}
              onSelect={(e) => setCaret((e.target as HTMLTextAreaElement).selectionStart)}
              onClick={(e) => setCaret((e.target as HTMLTextAreaElement).selectionStart)}
              onKeyDown={(e) => {
                if (completeShown.length > 0) {
                  // While the menu is open the navigation keys belong to it:
                  // Tab/Enter accept, arrows move, Escape hides it until the
                  // input changes again.
                  if (e.key === 'Tab' || e.key === 'Enter') {
                    e.preventDefault()
                    const cmd = completeShown[completeIdx]
                    if (cmd) acceptCompletion(cmd)
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
                if ((e.key === 'Enter' && !e.shiftKey) || ((e.metaKey || e.ctrlKey) && e.key === 'Enter')) {
                  e.preventDefault()
                  send()
                }
              }}
            />
            <div class="composer-actions">
              <span class="composer-shortcut-hint">
                {t('sessions.sendShortcut')}
              </span>
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

/** Session-cost chip in the chat header — the `/cost` rollup, condensed to
 *  the two numbers that matter mid-conversation. Click through to System. */
function CostChip() {
  const { data: cost } = useAsync(() => api.cost().catch(() => null), [])
  if (!cost || cost.calls === 0) return null
  return (
    <a
      class="badge cost-chip mono"
      href="#/settings?tab=system"
      data-tip={t('sessions.costTip')}
    >
      ${cost.total_cost_usd.toFixed(3)} · {fmtSizeTokens(cost.total_tokens)}
    </a>
  )
}

function fmtSizeTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M tok`
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k tok`
  return `${n} tok`
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
          {msg.files && msg.files.length > 0 && (
            <div class="bubble-slot-row slot-meta msg-files">
              {msg.files.map((f) => (
                <span key={f} class="attach-chip mono" title={f}>
                  📎 {f.split('/').pop()}
                </span>
              ))}
            </div>
          )}
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
  return <EventTimeline events={events} className="task-chain-timeline" />
}

/** Chain-of-thought, shown apart from the answer.
 *
 *  Reasoning arrives on its own stream and is display-only (D14).
 *  User-controlled toggle state ensures user's expansion choice is preserved
 *  across stream deltas without snapping shut. */
function ThoughtBlock({ text, live }: { text: string; live: boolean }) {
  const [userExpanded, setUserExpanded] = useState<boolean | null>(null)
  const [copied, setCopied] = useState(false)

  const lines = useMemo(() => text.trim().split('\n'), [text])
  const summary = useMemo(() => {
    const first = lines.find((l) => l.trim().length > 0) || ''
    return first.length > 70 ? first.slice(0, 70) + '…' : first
  }, [lines])

  const isOpen = userExpanded !== null ? userExpanded : live

  const toggle = (e: Event) => {
    e.preventDefault()
    setUserExpanded(!isOpen)
  }

  const copy = (e: Event) => {
    e.stopPropagation()
    navigator.clipboard?.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div class={`thought-card ${isOpen ? 'open' : 'closed'}`}>
      <div
        class="thought-head"
        onClick={toggle}
        role="button"
        tabIndex={0}
        aria-expanded={isOpen}
      >
        <span class="thought-badge">
          {live ? <span class="spinner spinner-inline" aria-hidden="true" /> : <span class="thought-sparkle">✻</span>}
          <span class="thought-label">
            {live ? t('sessions.thinking') : t('sessions.thought')}
          </span>
        </span>
        {!isOpen && summary && (
          <span class="thought-summary-preview dim mono" title={summary}>
            {summary}
          </span>
        )}
        <span class="grow" />
        <span class="thought-meta dim">
          {lines.length} {t('common.lines')}
        </span>
        <button
          type="button"
          class="thought-copy-btn"
          onClick={copy}
          title={t('sessions.copyThought')}
        >
          {copied ? '✓' : '⧉'}
        </button>
        <span class={`thought-chevron ${isOpen ? 'open' : ''}`} aria-hidden="true">
          {isOpen ? '▴' : '▾'}
        </span>
      </div>
      {isOpen && (
        <div class="thought-body mono">
          {text}
        </div>
      )}
    </div>
  )
}

/** DiffViewer renders git patch lines with syntax coloring for added/deleted lines and hunks. */
function DiffViewer({ patch }: { patch: string }) {
  if (!patch) return null
  const lines = patch.split('\n')
  return (
    <pre class="diff-patch diff-patch-view mono">
      {lines.map((line, idx) => {
        let cls = 'diff-line'
        if (line.startsWith('+') && !line.startsWith('+++')) {
          cls += ' diff-line-add'
        } else if (line.startsWith('-') && !line.startsWith('---')) {
          cls += ' diff-line-del'
        } else if (line.startsWith('@@')) {
          cls += ' diff-line-hunk'
        } else if (line.startsWith('diff ') || line.startsWith('index ') || line.startsWith('---') || line.startsWith('+++')) {
          cls += ' diff-line-meta'
        }
        return (
          <div key={idx} class={cls}>
            <span class="diff-line-content">{line || ' '}</span>
          </div>
        )
      })}
    </pre>
  )
}
