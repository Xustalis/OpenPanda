// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  api,
  ApiError,
  askSessionStream,
  isAbort,
  type ClarifyAnswer,
  type ClarifySpec,
  type DevStatus,
  type FsFileEntry,
  type NodeInfo,
  type ProjectDetail,
  type Session,
  type SessionDiff,
  type SessionNote,
  type Task,
} from '../api/client'
import { Icon } from '../components/icons'
import { Dock, DOCK_MAX_WIDTH, DOCK_MIN_WIDTH, type DockEnv, type DockTab } from '../components/dock'
import { DockBrowser, type BrowserArtifact, type BrowserPageRequest } from '../components/dock-browser'
import { DockFiles } from '../components/dock-files'
import { DockChanges } from '../components/dock-changes'
import { SideChat } from '../components/side-chat'
import { FileViewContext, type FileOpener, type FileRef } from '../components/file-link'
import { ComposerContext } from '../components/composer-context'
import {
  useAsync,
  useChangeSignal,
  useLocaleRerender,
  busSubscribeChange,
} from '../hooks'
import { t } from '../i18n'
import { isNodeConnected } from '../format/node-status'
import { navigate, navigateView } from '../nav'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { ClarifyCard } from '../components/clarify-card'
import { buildCommands } from '../components/palette'
import { rankWords } from '../components/fuzzy'
import { goalArgument, goalBudget, goalCommand } from '../components/goal-command'
import { composerIntent, reconcileTranscript, threadBusy, patchStreaming, slashQuery, slashArgStarted, slashWords, planArgument, type ComposerMode } from '../components/chatstate'
import { atQuery, expandFileRefs, exportMarkdown, exportFilename, fileLabel, withAttachments } from '../components/attach'
import { isLiveSession } from '../components/session-guard'
import { sortSessions, turnsChanged } from '../components/session-list'
import { HoverPreview, Lightbox } from '../components/image-preview'
import { liveTaskIds } from '../components/task-activity'
import { roundStillOpen, trailingRunSummary, prettyAgent } from '../components/chat-speaker'
import { TaskProgress } from '../components/chat-cast'
import { projectTaskRounds } from './sessions/task-round-state'
import { RoundReportPane, SessionTaskRound, type OpenRoundReport, type RoundReportSelection } from './sessions/task-round'
import {
  PermissionPicker,
  normalizePermission,
  type PermissionMode,
} from '../components/permission-picker'
import { NodePicker } from '../components/node-picker'
import { ComposerPet } from '../components/composer-pet'
import { OrchestrateCard } from '../components/orchestrate'
import { DirButton } from '../components/dir-picker'
import { ModelSelect } from '../components/model-select'
import { SessionRail } from '../components/session-rail'
import { agentContextSamples, type ContextAgentView } from '../components/session-rail-stats'
import { VoiceInput } from '../components/voice-input'
import { upsertActivity } from '../components/execution-steps'
import { acquireKeepAlive } from '../utils/keepalive'
import {
  autoGrow,
  editorCaretOffset,
  editorClear,
  editorInsertText,
  editorTextOf,
  editorTextWalker,
  insertTextAtCaret,
} from '../components/composer-editor'
import { dragHasFiles, livePillKeys, useComposerAttachments } from './sessions/use-composer-attachments'
import { ComposerInput, type CompletionItem } from './sessions/composer-input'
import { QueuedNotes } from './sessions/queued-notes'
import { fmtSize, type PendingAttachment } from './sessions/attachment-pills'
import { AttachMenu } from './sessions/attach-menu'
import { GoalBar, goalActive, goalNeedsMoreBudget, legacyGoalFor, useComposerGoals, type ComposerGoal } from './sessions/goal-bar'
import { PlanProposalDock } from './sessions/plan-proposal-dock'
import { GoalBudgetDialog, PlanProposalDialog } from './sessions/conversation-dialogs'
import { projectForDir, waitDevReady } from './sessions/session-project'
import { ChatEmpty, ChatSkeleton, ChatTurn, reportFallbackRefs, RunSummaryRow, useSessionTasks } from './sessions/transcript'
import { hydrateTurns, localMsgId, statusNote, type ChatMsg } from './sessions/transcript-state'

export {
  autoGrow,
  editorCaretOffset,
  editorClear,
  editorPlain,
  editorTextOf,
  insertTextAtCaret,
  range_or_append,
  useBrParagraphSeparator,
} from '../components/composer-editor'

/** How long the pane keeps watching a turn it lost the live stream for.
 *  Comfortably past a slow classify plus a long agent run, and finite: a
 *  watch with no end is a spinner the user cannot dismiss. */
const RECOVER_WATCH_MS = 10 * 60 * 1000

/** How long an idle thread waits before opening a turn for a note that is
 *  still parked server-side. The turn's own exit resumes immediately; this is
 *  the strip's poll retrying a note whose continuation never started (frame
 *  lost, tab elsewhere, provider down). Long enough that a note being edited
 *  is not posted out from under the user, short enough that "queued" still
 *  means "runs next". */
const RESUME_RETRY_MS = 20 * 1000

/** One line for a prompt-rail tick's tooltip: a question can be a paragraph,
 *  and a native tooltip that runs off the screen is worse than a truncated
 *  one. Whitespace is collapsed first — a pasted block would otherwise open
 *  the tooltip on an empty first line. */
function promptLabel(text: string): string {
  const line = text.replace(/\s+/g, ' ').trim()
  return line.length > 60 ? `${line.slice(0, 60)}…` : line
}

/** The sessions view: a thread rail on the left (codex / claude code style)
 * and one worktree-isolated conversation on the right, streaming over SSE. */
export function SessionsView({
  activeId,
  project,
  ws,
  onOpenSession,
  onLogout,
}: {
  activeId: string | null
  project?: string | null
  /** The workspace a brand-new thread was handed in from (the history's
   *  per-directory "+"), by project name. It seeds the composer's directory
   *  pick and nothing else: unlike `project` it does not scope the rail, so
   *  the reader keeps the whole history while the next thread already knows
   *  where it runs. */
  ws?: string | null
  onOpenSession(id: string): void
  /** Kept for the route's project deep-link: leaving a scope used to be a rail
      control, and the prop survives for callers that still pass it. */
  onExitProject?(): void
  /** The ⌘K palette's logout entry, reused by the composer's `/`
   *  completion so both surfaces run the same command list. */
  onLogout(): void
}) {
  useLocaleRerender()
  const [sessions, setSessions] = useState<Session[]>([])
  const [session, setSession] = useState<Session | null>(null)
  const [msgs, setMsgs] = useState<ChatMsg[]>([])
  // Wall-clock of a finished turn, filed by the reply's index in the
  // transcript. It cannot ride on the message: the moment a turn is persisted
  // the transcript is re-fetched and every message object is replaced, which
  // would take a per-message field with it. `msgsRef` exists only so the
  // completion path can name that index without a stale closure.
  const [turnMs, setTurnMs] = useState<Record<number, number>>({})
  // Wall-clock start of the turn that is streaming right now; 0 when nothing
  // is. The reply prints a live timer from it, and the same stamp is what
  // `finishTurn` turns into the final number.
  const [liveFrom, setLiveFrom] = useState(0)
  const msgsRef = useRef<ChatMsg[]>([])
  const turnStartRef = useRef(0)
  const [input, setInput] = useState('')
  // Notes typed mid-turn that no round has folded yet — the bubbles on the
  // pet's cheek. A queue with no list is a queue the user cannot see, correct,
  // or take back, so hovering the bubbles opens the list.
  const [notes, setNotes] = useState<SessionNote[]>([])
  // Whether that list is showing. It is a hover/focus disclosure, not a mode:
  // the queue is invisible until the pointer (or the caret) asks for it.
  const [notesOpen, setNotesOpen] = useState(false)
  // How tall the popover may grow, measured on open. A fixed cap runs off the
  // top of a short window — over the page header — and the queue is exactly
  // the kind of thing you hover in a window that is already tight.
  const noteBubbles = useRef<HTMLDivElement>(null)
  const [noteRoom, setNoteRoom] = useState(0)
  // The list's scroll box, and whether rows are cut off above / below it. The
  // popover caps its height, so the edges that still hold notes fade — a queue
  // that silently dropped its fifth note off the bottom read as "only four
  // were queued".
  const noteList = useRef<HTMLDivElement>(null)
  const noteCount = useRef(0)
  const [noteEdges, setNoteEdges] = useState({ above: false, below: false })
  // The note being edited in the list. Editing happens in place rather than
  // by pulling the text back into the composer: a pull-back would drop the row
  // first (or the text would be sent twice) and the re-send would silently
  // demote an inject back to a queue entry.
  const [editingNote, setEditingNote] = useState('')
  const [noteDraft, setNoteDraft] = useState('')
  // True while a file is over the composer, so the box can say so.
  const [dropping, setDropping] = useState(false)
  // The conversation's permission policy. It is the whole consent answer:
  // auto and edits stop at the tier-2 gate for a one-off approval card, bypass
  // runs everything, manual asks before every change and plan changes nothing.
  // A separate per-turn "authorize risky actions" switch used to sit beside the
  // picker, but it only had meaning in auto/edits, was ignored in manual/plan/
  // bypass, and duplicated what the bypass mode or the approval card already
  // says — so the picker is the one control.
  const [permission, setPermission] = useState<PermissionMode>('auto')
  // The composer's environment pick: empty = let the scheduler route by
  // capability; a node id pins this turn's spawned task to that executor.
  const [nodePref, setNodePref] = useState('')
  // Who works on this thread — the agent roster, picked in the composer's
  // footer when the conversation starts and remembered by the session after
  // that. Empty means no restriction (the scheduler picks the best hand),
  // which is what every thread had before rosters existed.
  const [team, setTeam] = useState<string[]>([])
  // The side dock: one column holding the thread's tools (export, node pick,
  // tasks), its files, a page being read, and a conversation beside it. One
  // `tab` owns all six surfaces — a tool button switches the body to that tool's
  // page, it never opens a floating card over the panel underneath. `tab`
  // survives a collapse so reopening returns to the surface you left; `epoch`
  // is the head's `+`, which every panel reads as "start a fresh one".
  const [dockOpen, setDockOpen] = useState(false)
  const [dockTab, setDockTab] = useState<DockTab>('')
  const [reportSelection, setReportSelection] = useState<RoundReportSelection>()
  const reportOrigin = useRef<HTMLButtonElement | null>(null)
  // The column's width is a preference, not fleet or thread state: it lives in
  // localStorage and never travels. A stored value outside the drag's bounds
  // (an older build, a hand-edited entry) falls back to the default.
  const [dockW, setDockW] = useState(() => {
    try {
      const stored = Number(localStorage.getItem('openpanda.dock.width'))
      if (Number.isFinite(stored) && stored >= DOCK_MIN_WIDTH && stored <= DOCK_MAX_WIDTH) {
        return stored
      }
    } catch {
      // storage blocked; the default width is fine
    }
    return 380
  })
  useEffect(() => {
    try {
      localStorage.setItem('openpanda.dock.width', String(dockW))
    } catch {
      // storage blocked; the width still works for this session
    }
  }, [dockW])
  // The panels are thread-scoped: switching threads bumps this clock so the
  // next thread cannot reopen onto the last thread's page, file or side
  // question (see the effect keyed on activeId). It is NOT the head's `+` —
  // sharing the two is how starting a new page once silently wiped the side
  // chat history. `+` only ever moves sideEpoch below.
  const [dockEpoch, setDockEpoch] = useState(0)
  // `+` has one meaning now: a new conversation beside the thread. It used to
  // double as "reset whichever panel is showing" (files → root, browser →
  // close the page), so a button labelled 新对话 did something else half the
  // time — and nothing visible at all when that panel was already at its start
  // state. Those two panels own their own start-over buttons now.
  const [sideEpoch, setSideEpoch] = useState(0)
  // Bumped when the reader comes back to the 对话 tab. The pane stays mounted
  // behind a hidden tab, so re-read the thread's stored side conversation
  // rather than showing a stale one.
  const [sideReload, setSideReload] = useState(0)
  // A second click on the 对话 tab. The pane no longer keeps a history bar
  // above the transcript — a permanent row for a list that is usually one
  // item long — so the tab is the only door to it: the button that opened the
  // pane opens the list of the other conversations.
  const [sideHistory, setSideHistory] = useState(0)
  // The head's `+` is always mounted now. When it opens the side conversation
  // from a tool page or the start state, that pane may be mounting in the same
  // render as the epoch bump — it needs a mount-time flag to know the new
  // conversation is intentional rather than a reopen.
  const [sideFresh, setSideFresh] = useState(false)
  // Panels mount the first time their tab is picked and stay mounted after
  // that, hidden rather than dropped: a file tree you scrolled, a page you
  // were reading and a half-typed side question are all the reader's place,
  // and a tab switch or a fold is not a reason to lose it. Lazy on first use
  // so an unopened dock costs nothing — the file tree fetches on mount.
  const [dockSeen, setDockSeen] = useState<DockTab[]>([])
  useEffect(() => {
    if (dockTab === '') return
    setDockSeen((prev) => (prev.includes(dockTab) ? prev : [...prev, dockTab]))
  }, [dockTab])
  // The flag is read once by SideChat on mount. Clear it after that render so
  // a later remount cannot inherit someone else's fresh start.
  useEffect(() => {
    if (sideFresh && dockSeen.includes('side')) setSideFresh(false)
  }, [sideFresh, dockSeen])
  // Which artifact keys this thread has already shown. `null` means "no poll
  // has landed yet": the first snapshot is the baseline, so reopening a thread
  // after the work finished does not pop previews for pages already seen.
  // The finished task whose page the dock's browser is asked to show. It is a
  // request into the panel, not a surface of its own: the panel owns the frame
  // and the error state, this only names the task.
  const [pageAsk, setPageAsk] = useState<BrowserArtifact | null>(null)
  // A live page (the project's dev server) the thread asked to show. It and
  // an artifact request are alternatives: opening one clears the other.
  const [directAsk, setDirectAsk] = useState<BrowserPageRequest | null>(null)
  // Bumped on thread switch so a dev-status read or start wait that is still
  // running cannot open a page in the thread the reader has already left.
  const previewSeq = useRef(0)
  // Projects with an auto-start already in flight, so a burst of settled tasks
  // does not launch the same dev command several times.
  const devStarting = useRef(new Set<string>())
  // The file the transcript's latest click asked for. It is a request into the
  // dock's file panel, not a surface of its own: the panel owns the tree, this
  // only names the file to open there.
  const [fileRef, setFileRef] = useState<FileRef | null>(null)
  // One stable opener for every inline code span in the transcript: a new
  // object per render would re-render every clickable mention on each frame.
  // A click on a path is also the dock's file tab being asked for — the reader
  // asked to see a file, so the column that shows files comes up.
  const fileOpener = useMemo<FileOpener>(
    () => ({
      open: (ref) => {
        setDockTab('files')
        setDockOpen(true)
        setFileRef(ref)
      },
    }),
    [],
  )
  // The same text-into-the-composer action for every surface that produces a
  // reference (the file drawer, the artifact preview): the draft lands at the
  // caret and is never sent on its own.
  const composerInsert = useCallback((text: string) => {
    const el = composer.current
    if (!el) return
    insertTextAtCaret(el, text)
    setInput(editorTextOf(el))
    el.focus()
  }, [])
  // The artifact card's own door to the preview, and the automatic open after
  // a task settles. A project that declares a dev server gets that live page;
  // otherwise the packed artifact is the page. The live server is preferred
  // because it is what the app actually serves, cookies and HMR included.
  const openTaskPreview = useCallback((task: Task) => {
    const hash = task.plan_meta?.output_artifact
    const seq = previewSeq.current
    const stillHere = () => seq === previewSeq.current
    const openArtifact = () => {
      if (!hash || !stillHere()) return
      setDirectAsk(null)
        setPageAsk((prev) => ({
          taskId: task.id,
          hash,
          nonce: (prev?.nonce ?? 0) + 1,
        }))
        setDockOpen(true)
        setDockTab('browser')
    }
    const openDirect = (url: string) => {
      if (!stillHere()) return
      setPageAsk(null)
      setDirectAsk((prev) => ({
        url,
        title: task.title,
        nonce: (prev?.nonce ?? 0) + 1,
        sameOrigin: true,
      }))
      setDockOpen(true)
      setDockTab('browser')
    }
    const project = task.project
    if (!project) {
      openArtifact()
      return
    }
    void (async () => {
      let dev: DevStatus | null = null
      try {
        dev = await api.devStatus(project)
      } catch {
        dev = null
      }
      if (!stillHere()) return
      if (!dev?.configured) {
        openArtifact()
        return
      }
      if (dev.state === 'ready' && dev.url) {
        openDirect(dev.url)
        return
      }
      // A server already starting, or one this project opted into starting,
      // is waited for; a stopped server without auto_start falls back to the
      // artifact instead of launching work the user did not ask for.
      if ((dev.state === 'starting' || dev.auto_start) && !devStarting.current.has(project)) {
        devStarting.current.add(project)
        try {
          if (dev.state !== 'starting') {
            dev = await api.startDev(project)
          }
          const ready = await waitDevReady(project, dev.ready_timeout_s ?? 60, stillHere)
          if (ready?.url) {
            openDirect(ready.url)
            return
          }
        } catch (e) {
          toastError(e instanceof Error ? e.message : String(e))
        } finally {
          devStarting.current.delete(project)
        }
      }
      if (!stillHere()) return
      openArtifact()
    })()
  }, [])
  // The task a transcript link asked for. Held here, not in the card, because
  // the click that asks for it is the transcript's: `{ id }` is rebuilt on
  // every request so a second click on the same 查看任务 still re-opens a pane
  // the reader had closed.
  const [focusTask, setFocusTask] = useState<{ id: string } | null>(null)
  /** Open one task where the reader already is — the tasks page in the side
   *  dock, with that task picked. Leaving for the task page used to cost the
   *  conversation: its scroll, its draft, its place. */
  function openTaskInPlace(id: string) {
    setDockOpen(true)
    setDockTab('orch')
    setFocusTask({ id })
  }
  function closeRoundReport() {
    setDockOpen(false)
    const origin = reportOrigin.current
    if (origin?.isConnected) origin.focus({ preventScroll: true })
  }
  const openRoundReport: OpenRoundReport = (key, origin, taskId) => {
    if (dockOpen && dockTab === 'report' && reportSelection?.key === key && reportSelection.taskId === taskId) {
      closeRoundReport()
      return
    }
    reportOrigin.current = origin
    setReportSelection({ key, taskId })
    setDockTab('report')
    setDockOpen(true)
  }
  /** The head's three tools are pages, not popovers: one click swaps the dock
   *  body to that tool, a second click on the same one folds the column (the
   *  tabs on the right behave the same way). */
  function pickTool(key: 'export' | 'node' | 'orch') {
    if (dockOpen && dockTab === key) {
      setDockOpen(false)
      return
    }
    setDockTab(key)
    setDockOpen(true)
  }
  /** The change list has one home now: the dock's 改动 tab. The rail's change
   *  line, the panel's tab and the start row all land on it. */
  function openChanges() {
    setDockTab('changes')
    setDockOpen(true)
    void refreshDiff(activeId)
  }
  // A dock page belongs to the thread it was opened in. Switching threads
  // closes it: the pages are remounted per session.
  useEffect(() => {
    // The dock's panels are thread-scoped in the same way: a file path resolves
    // against this thread's worktree, a page is what this thread was talking
    // about, and a side conversation is a snapshot of it. Carrying any of them
    // across would show the next thread something it never mentioned.
    setDockOpen(false)
    setDockTab('')
    setReportSelection(undefined)
    reportOrigin.current = null
    setFileRef(null)
    // The panels stay mounted behind a hidden tab, so closing the dock is not
    // enough: the next thread would reopen to the last thread's page, file or
    // side question. The epoch is the panels' reset signal.
    setDockEpoch((n) => n + 1)
    previewSeq.current += 1
    setPageAsk(null)
    setDirectAsk(null)
    // The queue belongs to the thread it was typed in: a counter carried over
    // from another thread would make the next note look like a removal, and a
    // note list left in place would show the previous thread's pending queue.
    noteCount.current = 0
    setNotes([])
    setNotesOpen(false)
  }, [activeId])
  // The jobs this thread still has hands on. Read once, before anything that
  // answers from it — the composer's Stop button, the live employee rows and
  // the round's roll-up all want the same list, and a second read would only
  // give them a second, differently-aged answer.
  //
  // The gate is "a thread is open", not "a bubble dispatched something": a
  // turn started elsewhere (the workspace dispatcher, another tab, the TUI)
  // has no bubble here yet, but the thread must still show the employee's live
  // row instead of a bare spinner — and still offer the way out of it.
  const dispatched =
    msgs.some((m) => m.kind === 'task' || m.kind === 'plan' || Boolean(m.liveTask)) ||
    session?.operation?.status === 'running'
  const sessionTasks = useSessionTasks(activeId, Boolean(activeId) || dispatched)
  const roundOpen = sessionTasks ? roundStillOpen(sessionTasks) : false
  // The employees still working, drawn after the last turn: a running job is
  // the newest thing in the thread, whatever the position of the bubble that
  // dispatched it (see `liveTaskIds`).
  const liveIds = sessionTasks ? liveTaskIds(sessionTasks) : []
  const [localBusy, setBusy] = useState(false)
  // A reload loses the local streaming flag, but the session still carries the
  // server's running operation. Keep the Stop button until that operation
  // settles instead of swapping it for Send while the model is answering.
  //
  // A dispatched employee that is still moving counts as busy too. The panel
  // already treats that state as a live thread — what you type goes to the
  // job's board (see `sendSessionMessage`), so the composer must keep offering
  // Stop instead of swapping back to Send while that job is running.
  const busy = threadBusy(localBusy, activeId, session, liveIds.length)
  const goalAction = useRef<string | null>(null)
  const [pendingGoalAction, setPendingGoalAction] = useState<string | null>(null)
  const { goal, state: controlState, refresh: refreshControl, goalFor } = useComposerGoals(activeId, session?.operation, Boolean(pendingGoalAction))
  const legacyGoal = activeId && !goal ? legacyGoalFor(activeId) : undefined
  const [goalEditor, setGoalEditor] = useState<{ sid: string; id: string; text: string } | null>(null)
  const [budgetGoal, setBudgetGoal] = useState<{ sid: string; id: string } | null>(null)
  const [planReview, setPlanReview] = useState<{ sid: string; version: number } | null>(null)
  const proposal = controlState?.planning && controlState.proposal &&
    controlState.proposal_version > controlState.confirmed_version
    ? { text: controlState.proposal, version: controlState.proposal_version, resumesGoal: Boolean(controlState.goal?.paused_by_plan) }
    : null
  // `/goal` and `/plan` are picks, not prefixes: choosing one takes the token
  // out of the editor and leaves a chip beside the actions, so the draft is
  // the objective and nothing repeats the command. The goal bar is the
  // published goal's own readout — it has no draft preview, because a bar
  // sitting there before the task exists reads as a second composer.
  const [armed, setArmed] = useState<ComposerMode | null>(null)
  const draftSession = useRef(activeId)
  const drafts = useRef(new Map<string, {
    text: string
    nodes: Node[]
    mode: ComposerMode | null
    attachments: PendingAttachment[]
  }>())
  // Still parsed so a hand-typed `/goal …` keeps its menu suppressed and its
  // send routed; the chip no longer mirrors it.
  const goalDraft = goalArgument(input)
  const shownGoal: ComposerGoal | undefined = goal?.phase === 'cancelled' ? undefined : goal
  // The pre-execution question the running turn is parked on. Cleared when the
  // browser answers, when the turn ends, or when the user cancels it.
  const [clarify, setClarify] = useState<{
    sessionID: string
    operationID: string
    spec: ClarifySpec
  } | null>(null)
  const [loadError, setLoadError] = useState('')
  // Poll the pending queue only while there is something to watch: a live turn
  // produces notes, and a waiting note disappears the moment a round folds it.
  // An idle thread with an empty queue costs nothing.
  useEffect(() => {
    if (!activeId) {
      setNotes([])
      setNotesOpen(false)
      return
    }
    void refreshNotes()
    if (!busy && notes.length === 0) return
    const id = setInterval(() => void refreshNotes(), 4000)
    return () => clearInterval(id)
  }, [activeId, busy, notes.length])
  // A queue that empties takes its popover with it: leaving `notesOpen` true
  // would pop the list open again the next time a note is queued, without the
  // pointer ever coming near the bubbles.
  useEffect(() => {
    if (notes.length === 0) setNotesOpen(false)
  }, [notes.length])
  // How much room the popover has, measured from the bubbles up to the top of
  // the pane (not the window — the header is not a place to draw over). The
  // list scrolls past whatever does not fit; 240 is the cap a tall window
  // gets, because the queue is a preview and not a second transcript.
  useEffect(() => {
    if (!notesOpen) return
    const el = noteBubbles.current
    if (!el) return
    const top = el.getBoundingClientRect().top
    const pane = el.closest('.chat-pane')?.getBoundingClientRect().top ?? 0
    // 8 of gap above the bubbles, 10 of card border + the popover's own air.
    const room = top - Math.max(pane, 0) - 26
    setNoteRoom(Math.max(72, Math.min(240, Math.round(room))))
  }, [notesOpen, notes.length])
  // Keep the list's scroll box honest: follow a note the user just added
  // (it lands at the end of the queue), leave the list alone when a round
  // folded one away or the user dropped one, and re-measure the edges whenever
  // the queue, its height or its visibility changes — the box does not exist
  // until the popover is open, so opening has to be one of the triggers.
  useEffect(() => {
    const el = noteList.current
    if (!el) return
    if (notes.length > noteCount.current) el.scrollTop = el.scrollHeight
    noteCount.current = notes.length
    const measure = () => {
      const above = el.scrollTop > 2
      const below = el.scrollHeight - el.scrollTop - el.clientHeight > 2
      setNoteEdges((cur) => (cur.above === above && cur.below === below ? cur : { above, below }))
    }
    measure()
    el.addEventListener('scroll', measure, { passive: true })
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    ro?.observe(el)
    return () => {
      el.removeEventListener('scroll', measure)
      ro?.disconnect()
    }
  }, [notes.length, notesOpen])
  const [selectedProject, setSelectedProject] = useState(project || '')
  // The directory a brand-new thread will run in, as the composer's footer
  // shows it. It starts as the scope's own work dir and only becomes a real
  // choice once the user picks one (`dirTouched`): a send then binds the
  // thread to that directory instead of to the scope's.
  const [pickedDir, setPickedDir] = useState('')
  const [dirTouched, setDirTouched] = useState(false)
  // The project a picked directory resolved to. It outranks the route's
  // project, so a directory chosen on the new-chat page really is where the
  // thread it creates runs; it steps aside the moment the pane shows a
  // thread belonging to another project.
  const [dirProject, setDirProject] = useState('')
  const activeProject = dirProject || project || selectedProject || ''
  // True only while a thread's transcript is in flight, so the pane can show
  // its shape instead of an empty box (or worse, the "new chat" hero).
  const [loading, setLoading] = useState(false)
  const [diff, setDiff] = useState<SessionDiff | null>(null)
  const [diffError, setDiffError] = useState('')
  const [merging, setMerging] = useState(false)
  const [reverting, setReverting] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const composer = useRef<HTMLDivElement>(null)
  // The in-flight ask, so the stop button can abort it.
  const inflight = useRef<AbortController | null>(null)
  // Which thread the in-flight ask belongs to. The transcript loader must not
  // abort an ask that is painting the very thread it is about to load (the
  // create-then-send path), and send()'s finally guard keys on it too.
  const inflightSid = useRef<string | null>(null)
  // Exact server-issued operation identity for the current ask. Stop is scoped
  // to this id; it never scans old transcript task references.
  const inflightOperation = useRef<string | null>(null)
  const stopRequested = useRef(false)
  // Notes the finished turn never read, as the server reported them. They are
  // parked server-side, so this is only the trigger to open the continuation
  // turn — the text itself is drained by the server, which is what keeps a
  // reload or a second tab from delivering the same words twice.
  const queuedTurn = useRef('')
  // The note set the last automatic resume opened a turn for, plus how many
  // times and when. A continuation that cannot drain its notes (the server
  // refused, the turn died first) would otherwise ask again the moment it
  // ends — the same words in a loop. A new note carries a new key, so only a
  // genuinely new queue retries; the cooldown and the cap keep a note the
  // model keeps failing on from spending tokens every few seconds.
  const resumeRetry = useRef<{ key: string; at: number; tries: number }>({ key: '', at: 0, tries: 0 })
  // True while the user is editing a queued row: an automatic continuation
  // must not post the half-edited text out from under them.
  const noteEditing = useRef(false)
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
  const {
    pending, setPending, peek, shot, setShot,
    addFiles, addFileRef, removePending, syncPending, clearPending,
    peekPill, unpeek, zoomPill,
  } = useComposerAttachments(composer, setInput, setCaret)
  // Filesystem rows for the @file completion menu (debounced query).
  const [atFiles, setAtFiles] = useState<FsFileEntry[]>([])
  // Whether the transcript is parked at the bottom. Autoscrolling on every
  // delta is right while you are watching the reply arrive and wrong the
  // moment you scroll up to re-read something, so follow only when pinned.
  const pinned = useRef(true)
  const [isPinned, setIsPinned] = useState(true)
  // Which question the viewport is parked on, as an index into the transcript's
  // user turns. The prompt rail reads it to fill exactly one tick.
  const [promptAt, setPromptAt] = useState(0)
  // Whether the rail is showing its list of questions. Hovering the rail opens
  // it: a 13px tick names nothing, and the thread's own map is more useful than
  // a one-line tooltip that a truncated question cannot fit anyway.
  const [railPreview, setRailPreview] = useState(false)
  // rAF handle for the rail's scroll read — a scroll fires per frame and each
  // read walks every user bubble, so one layout pass per frame is enough.
  const railFrame = useRef(0)
  const [isEditingTitle, setIsEditingTitle] = useState(false)
  const [titleInput, setTitleInput] = useState('')
  // Mirrors of state the SSE listener touches — the subscription outlives
  // any single render, so it must read through refs, not stale closures.
  const activeProjectRef = useRef(activeProject)
  const sessionRef = useRef<Session | null>(null)
  // onOpenSession is a fresh closure per render (it captures route.project);
  // the once-subscribed SSE listener must not hold the stale one.
  const onOpenSessionRef = useRef(onOpenSession)
  // The rail renders the sorted projection; `sessions` state stays in
  // server order so every update path (optimistic, refetch, SSE) goes
  // through one sort at render instead of re-sorting each mutation.
  const ordered = sortSessions(sessions)
  // The header's title must name the thread you are looking at, not "新对话" —
  // session state can still be in flight while the rail already knows the
  // title, so the rail is the fallback source of truth for the label.
  const headerTitle =
    session?.title ||
    (activeId ? ordered.find((s) => s.id === activeId)?.title || '' : '') ||
    (activeId ? t('sessions.untitled') : t('sessions.new'))

  useEffect(() => {
    if (session) setTitleInput(session.title || '')
    setIsEditingTitle(false)
  }, [session?.id])

  useEffect(() => {
    sessionRef.current = session
  }, [session])

  useEffect(() => {
    msgsRef.current = msgs
  }, [msgs])

  /** File the wall-clock of the turn that just landed against its reply. The
   *  reply is the tail of the transcript at this point — nothing is appended
   *  after it — so its index is the one the transcript will still have once
   *  the server's copy arrives. */
  function finishTurn(): void {
    const idx = msgsRef.current.length - 1
    setTurnMs((m) => ({ ...m, [idx]: Date.now() - turnStartRef.current }))
    // The reply carries the number from here on; a second timer would keep
    // ticking beside it.
    setLiveFrom(0)
  }

  /** Answer the parked pre-execution question. The same SSE turn resumes, so
   *  the card clears immediately and the stream carries on. */
  async function submitClarify(answers: ClarifyAnswer[]): Promise<void> {
    const pending = clarify
    if (!pending) return
    setClarify(null)
    try {
      await api.answerSessionClarify(pending.sessionID, pending.operationID, answers)
    } catch (err) {
      toastError(err)
    }
  }

  /** Cancel the parked turn: the same request the stop button sends, so the
   *  ask goroutine's question wait is released by the cancelled context. */
  function cancelClarify(): void {
    const pending = clarify
    setClarify(null)
    if (!pending) return
    api.cancelSession(pending.sessionID, pending.operationID).catch(() => {
      /* the stream's own stop path reports a real failure */
    })
  }

  useEffect(() => {
    onOpenSessionRef.current = onOpenSession
  }, [onOpenSession])

  useEffect(() => {
    activeProjectRef.current = activeProject
  }, [activeProject])

  // Session list (refresh when the active thread changes — its title may, or when project changes).
  useEffect(() => {
    let alive = true
    const want = activeProject
    api
      .sessions(want || undefined)
      .then((ls) => {
        // A reply for the project just left must not replace the new one's
        // list — the sidebar and rail would show another project's threads.
        if (alive && activeProjectRef.current === want) setSessions(ls)
      })
      .catch((e: unknown) => {
        if (alive) setLoadError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      alive = false
    }
  }, [activeId, activeProject])

  // The projects table, once: the composer foot's read-only directory pill
  // resolves the open thread's project row to the directory it runs in. The
  // row rarely changes mid-visit, and a stale path is corrected on the next
  // mount — this fetch exists for geometry and orientation, not live state.
  const [threadProjects, setThreadProjects] = useState<ProjectDetail[]>([])
  useEffect(() => {
    api
      .projects()
      .then((pl) => setThreadProjects(pl.detail ?? []))
      .catch(() => {})
  }, [])

  // Live rail: the panel pushes a 'sessions' fingerprint slot on the change
  // feed, so a thread created/pinned/deleted on another tab, device, or the
  // CLI lands here without a manual refresh. The fingerprint comparison is
  // what keeps task-churn frames (which arrive constantly while work runs)
  // from refetching the list every second. On a reconnect we reconcile
  // unconditionally — the gap may hide any number of mutations.
  useEffect(() => {
    let lastFP: string | undefined
    return busSubscribeChange((ev) => {
      const reconnect = ev.kinds.includes('reconnect')
      if (!reconnect && ev.sessionFP === lastFP) return
      lastFP = ev.sessionFP
      const want = activeProjectRef.current
      api
        .sessions(want || undefined)
        .then((ls) => {
          if (activeProjectRef.current === want) setSessions(ls)
        })
        .catch(() => {})
      // The open thread: pull the persisted transcript when it moved and
      // no local ask is streaming into it — a reply arriving on another
      // device (or a TUI turn) then paints itself. A 404 means another
      // surface deleted the thread out from under the pane: leave it
      // rather than keep showing a ghost.
      const sid = activeIdRef.current
      if (!sid) return
      api
        .session(sid)
        .then((s) => {
          if (!isLiveSession(sid, activeIdRef.current)) return
          if (inflight.current && inflightSid.current === sid) return
          const prev = sessionRef.current
          setSession(s)
          if (!prev || prev.id !== s.id || turnsChanged(prev.turns, s.turns)) {
            setMsgs(hydrateTurns(s.turns ?? [], (i) => `srv-${i}`))
          }
        })
        .catch((e: unknown) => {
          if (e instanceof ApiError && e.status === 404 && isLiveSession(sid, activeIdRef.current)) {
            toast(t('sessions.deletedElsewhere'), 'info')
            onOpenSessionRef.current('')
          }
        })
    })
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

  // The footer's directory readout, and the only two things that can seed it:
  // the workspace a new thread was handed in from (`ws`, the history's "+"),
  // or the project this pane was opened *in*. The server's active project used
  // to seed it too, which is why 新建对话 opened with the last project you had
  // visited already filled in — a choice you never made, dressed as one you
  // had. It starts empty now; picking a directory is the reader's call, and a
  // group's "+" is the shortcut that makes it for them.
  useEffect(() => {
    if (activeId) return
    let live = true
    api
      .projects()
      .then((pl) => {
        if (!live) return
        const name = ws || project || ''
        const hit = (pl.detail ?? []).find((d) => d.name === name)
        setPickedDir(hit?.work_dir ?? '')
        // A directory handed in by the rail is a decision already made, so the
        // send binds the thread to it. A scope's own directory is only where
        // the pane happens to sit — it stays the default, not a pick.
        setDirTouched(!!ws && !!hit?.work_dir)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [activeId, project, ws])

  // A thread opened from the rail answers for its own project: once the pane
  // shows a thread of another project the pick's override has done its job
  // and must not keep the rail scoped to a directory nobody is looking at.
  useEffect(() => {
    if (dirProject && session?.project && session.project !== dirProject) setDirProject('')
  }, [dirProject, session?.project])

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
  const onlineNodeCount = nodes.filter((n) => isNodeConnected(n.status)).length

  useLayoutEffect(() => {
    const previous = draftSession.current
    draftSession.current = activeId
    activeIdRef.current = activeId
    if (previous === activeId) return
    setPlanReview(null)
    setGoalEditor(null)
    setBudgetGoal(null)
    // The first send can give the new conversation its ID during submission.
    if (activeId && inflight.current && inflightSid.current === activeId) return
    const el = composer.current
    const text = el ? editorTextOf(el) : ''
    if (previous && el && (text || armed || pending.length)) {
      drafts.current.set(previous, {
        text,
        nodes: Array.from(el.childNodes, (node) => node.cloneNode(true)),
        mode: armed,
        attachments: pending,
      })
    }
    const next = activeId ? drafts.current.get(activeId) : undefined
    if (activeId) drafts.current.delete(activeId)
    clearPending()
    setShot(null)
    if (el && next) {
      el.replaceChildren(...next.nodes)
      autoGrow(el)
    }
    setInput(next?.text ?? '')
    setCaret(next?.text.length ?? 0)
    setPending(next?.attachments ?? [])
    setArmed(next?.mode ?? null)
    setCompleteCursor(0)
    setCompleteDismissed('')
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
    if (activeId && inflight.current && inflightSid.current === activeId) return
    inflight.current?.abort()
    inflight.current = null
    inflightSid.current = null
    inflightOperation.current = null
    stopRequested.current = false
    setBusy(false)
    if (!activeId) {
      setSession(null)
      setMsgs([])
      // A brand-new thread starts with no roster: the pick in the footer is
      // for the conversation about to be created, not the last one.
      setTeam([])
      setPermission('auto')
      // Timings are filed by transcript index, so they belong to the thread
      // that produced them and must not follow the pane to another one.
      setTurnMs({})
      setLiveFrom(0)
      setLoading(false)
      return
    }
    setLoading(true)
    // A stale transcript and a sticky error belong to the thread we are
    // leaving, not the one being opened. Without these resets a failed load
    // (deleted thread, restarted panel) rendered the previous conversation
    // under the new header, and every later successful open kept the old red
    // line because nothing ever cleared it.
    setLoadError('')
    setSession(null)
    setMsgs([])
    setTurnMs({})
    setLiveFrom(0)
    // Guard the response against the thread that is current NOW, not the one
    // this effect closed over. Rapid A→B switching let A's slower reply land
    // after B's: the transcript under B's header showed A's conversation, and
    // a failed A load cleared B's messages.
    let alive = true
    const current = () => alive && activeIdRef.current === activeId
    api
      .session(activeId)
      .then((s) => {
        if (!current()) return
        setSession(s)
        setLoadError('')
        setTeam(s.team ?? [])
        setPermission(normalizePermission(s.permission_mode))
        setTurnMs({})
        setLiveFrom(0)
        setMsgs(hydrateTurns(s.turns ?? [], (i) => `srv-${i}`))
      })
      .catch((e: unknown) => {
        if (!current()) return
        setSession(null)
        setMsgs([])
        setLoadError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [activeId])

  // A turn can be started from a surface that renders no transcript — the
  // workspace dispatcher, another tab, the TUI. It persists its operation on
  // the session before there is any reply to show, so opening the thread
  // mid-flight used to render only the user's own bubble until the answer
  // landed tens of seconds later, which reads as "nothing happened". Mirror
  // that live operation as a streaming placeholder (the same thinking
  // affordance a local send gets) and follow it until it terminalizes.
  useEffect(() => {
    if (!activeId || !session || session.id !== activeId) return
    const op = session.operation
    if (!op || op.status !== 'running') {
      // A paused or stopped background round settles without filing a turn of
      // its own, so nothing else would ever take its placeholder down: the
      // "thinking" spinner kept spinning under a paused goal bar. Drop it as
      // soon as the operation is no longer running.
      setMsgs((ms) => (ms.some((m) => m.streaming && m.k.startsWith('op-'))
        ? ms.filter((m) => !(m.streaming && m.k.startsWith('op-')))
        : ms))
      return
    }
    // A local ask already owns the tail: its own placeholder is there and the
    // poll below would fight the stream for the transcript.
    if (inflight.current && inflightSid.current === activeId) return
    const key = `op-${op.id}`
    const restore = (s: Session) => {
      const current = s.operation
      const rows = hydrateTurns(s.turns ?? [], (i) => `srv-${i}`)
      if (current?.id === op.id && current.status === 'running') {
        // A background round (goal/plan controller) never streams, so its
        // snapshot carries the zero time and the quiet clock read it as
        // 0001-01-01 — "quiet for 17758525h". Absent means "no output yet".
        const lastActivity = current.last_activity ? Date.parse(current.last_activity) : NaN
        rows.push({
          role: 'assistant', text: current.text || '', thought: current.thought, activities: current.activities,
          lastActivity: Number.isFinite(lastActivity) && lastActivity > 0 ? lastActivity : undefined,
          tokens: current.tokens, tokensEstimated: current.estimated,
          status: current.progress ? statusNote(current.progress.text || '', current.progress) : undefined,
          streaming: true, k: key, liveTask: current.task_id,
        })
      }
      setMsgs(rows)
    }
    restore(session)
    const startedAt = Date.parse(op.started_at || op.updated_at)
    if (Number.isFinite(startedAt) && startedAt > 0) setLiveFrom(startedAt)
    let cancelled = false
    const poll = async () => {
      while (!cancelled) {
        await new Promise((r) => setTimeout(r, 2000))
        if (cancelled) return
        try {
          const s = await api.session(activeId)
          if (cancelled) return
          setSession(s)
          restore(s)
          if (s.operation?.id !== op.id || s.operation.status !== 'running') return
        } catch {
          // A transient read failure is not the end of the turn; keep
          // following it rather than leaving the placeholder stranded.
        }
      }
    }
    void poll()
    return () => {
      cancelled = true
    }
  }, [activeId, session?.id, session?.operation?.id, session?.operation?.status])

  // A file dropped anywhere but the composer would otherwise be opened by the
  // browser, throwing the whole console away mid-turn. The composer's own
  // handlers do the attaching; this only cancels the default navigation, and it
  // listens on the window so a near-miss lands harmlessly instead of being the
  // one drop that loses the page.
  useEffect(() => {
    const swallow = (e: DragEvent) => e.preventDefault()
    addEventListener('dragover', swallow)
    addEventListener('drop', swallow)
    return () => {
      removeEventListener('dragover', swallow)
      removeEventListener('drop', swallow)
    }
  }, [])

  // When the window regains focus or tab becomes visible, reconcile the active thread
  // in case the server completed a background turn while the tab was hidden or blurred.
  useEffect(() => {
    const onWake = () => {
      if (!activeId || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
      // A local ask owns the tail: its stream is still painting the bubble,
      // and replacing the transcript underneath it would drop that bubble and
      // aim the next delta at an older message.
      if (inflight.current && inflightSid.current === activeId) return
      api
        .session(activeId)
        .then((s) => {
          if (activeIdRef.current !== activeId) return
          if (inflight.current && inflightSid.current === activeId) return
          const turns = s.turns ?? []
          const last = turns[turns.length - 1]
          if (last?.role === 'assistant') {
            setSession(s)
            setMsgs(hydrateTurns(turns, (i) => `srv-${i}`))
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

  // Keep the composer's height in step with its value — including the reset
  // to two rows after a send clears it, and after a starter chip fills it.
  // A fresh query also restarts the completion highlight at the top hit.
  useEffect(() => {
    if (composer.current) autoGrow(composer.current)
    setCompleteCursor(0)
  }, [input])

  // A thread that just loaded is parked at its newest message, which is the
  // last question. The rail has to say so before the first scroll event, and
  // the log's own scroll-to-bottom happens one frame later.
  useEffect(() => {
    const id = requestAnimationFrame(() => syncPromptAt())
    return () => cancelAnimationFrame(id)
  }, [msgs.length])

  /** The questions in this transcript, in order, each with the one-line label
   *  its tick carries. Every user turn gets a tick, including one that only
   *  carried files: the rail is a map of the thread, and a gap in it would
   *  read as a question that was skipped. */
  const prompts = msgs
    .filter((m) => m.role === 'user')
    .map((m) => ({
      k: m.k,
      label: promptLabel(
        m.text.trim() ||
          (m.files ?? []).map(fileLabel).join(' ') ||
          '…',
      ),
    }))

  /** Which question the top of the viewport has scrolled past. Read off the
   *  DOM, not derived from the message list: a bubble's height depends on its
   *  rendered markdown, so only the layout knows where a question sits. */
  function syncPromptAt(): void {
    const el = scroller.current
    if (!el) return
    const nodes = el.querySelectorAll('.msg.user')
    if (nodes.length === 0) return
    // Parked at the bottom means the last thing you asked is the one you are
    // looking at — even though its bubble can sit half a screen down while the
    // question above it is the one crossing the probe.
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 48) {
      setPromptAt(nodes.length - 1)
      return
    }
    // The probe sits below the top edge by the height of a line or two, so a
    // question counts as "current" the moment it arrives rather than once it
    // has scrolled entirely off.
    const edge = el.getBoundingClientRect().top + 90
    let at = 0
    nodes.forEach((n, i) => {
      if (n.getBoundingClientRect().top <= edge) at = i
    })
    setPromptAt(at)
  }

  /** Scroll a question to the top of the log. Unpins first: the jump is a
   *  deliberate move away from the newest message, and a streaming reply must
   *  not drag the view back down mid-animation. */
  function jumpToPrompt(i: number): void {
    const el = scroller.current
    if (!el) return
    const node = el.querySelectorAll('.msg.user')[i] as HTMLElement | undefined
    if (!node) return
    const top = node.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop
    pinned.current = false
    setIsPinned(false)
    el.scrollTo({ top: Math.max(0, top - 16), behavior: 'smooth' })
  }

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

  async function revertMerge() {
    if (!activeId || reverting) return
    // Destructive — a merge at HEAD resets the checkout; ask first, like
    // the task revert on the detail page already does.
    const ok = await confirmDialog({
      title: t('sessions.revertConfirmTitle'),
      message: t('sessions.revertConfirmMsg'),
      confirmLabel: t('sessions.revert'),
    })
    if (!ok) return
    setReverting(true)
    setDiffError('')
    try {
      await api.sessionRevert(activeId)
      await refreshDiff(activeId)
    } catch (e) {
      setDiffError(e instanceof Error ? e.message : String(e))
    } finally {
      setReverting(false)
    }
  }

  function newChat() {
    // 新对话 mints nothing — the thread comes into being with the first send,
    // the same rule the rail's new-chat row follows.
    onOpenSession('')
    composer.current?.focus()
  }

  /** Change the thread's roster. A new thread has nothing to store yet — the
   *  pick rides createSession on the first send. An existing one is patched
   *  immediately so the roster survives a reload and reaches a second device;
   *  the ask also carries it every turn, so the patch failing (offline, a
   *  restarting daemon) costs the memory, never the current turn. */
  function pickTeam(names: string[]) {
    setTeam(names)
    if (!activeId) return
    api.patchSession(activeId, { team: names }).catch((err: unknown) => toastError(err))
  }

  /** Change the thread's permission policy. Saved on the session so it
   *  survives a reload and a second device; the ask also carries it every
   *  turn, so a failed patch (offline, restarting daemon) costs the memory,
   *  never the current turn. */
  function pickPermission(mode: PermissionMode) {
    const prev = permission
    setPermission(mode)
    // Speak only after the server agrees. Announcing the switch first meant a
    // failed PATCH reverted the chip while the toast still claimed bypass was
    // on — the one message about this feature that must never be wrong.
    const announce = () => {
      if (mode === 'bypass') toast(t('perm.bypassOn'))
      else if (prev === 'bypass') toast(t('perm.bypassOff'))
    }
    if (!activeId) {
      announce()
      return
    }
    api
      .patchSession(activeId, { permission_mode: mode })
      .then(announce)
      .catch((err: unknown) => {
        setPermission(prev)
        toastError(err)
      })
  }

  /** Put a starter prompt in the composer, ready to edit or send. The chip is
   *  a suggestion, not a send button: a replaceable draft is cheaper to
   *  correct than a turn that already left. */
  function useStarter(text: string) {
    clearPending()
    const el = composer.current
    el?.focus()
    if (!el) {
      setInput(text)
      return
    }
    editorInsertText(text)
    setInput(editorTextOf(el))
    autoGrow(el)
  }




  /** Stop what this thread is doing — the exact operation, including one
   *  recovered after a reload, and otherwise the jobs that operation left
   *  running.
   *
   *  The ask comes first: while a reply streams, that is the thing the reader
   *  is watching, and the dispatch it already made is a separate decision
   *  (the button stays up afterwards, so a second press is what stops the
   *  job). Once the ask has settled the thread can still be busy with what it
   *  dispatched, and a stuck adapter is precisely the case with no other way
   *  out of the pane. */
  async function stop() {
    if (goal?.round && activeId) { await changeGoal('stop'); return }
    const ctrl = inflight.current
    const targetSid = inflightSid.current || activeId
    const operationID = inflightOperation.current ||
      (session?.id === targetSid && session.operation?.status === 'running' ? session.operation.id : '')
    if (targetSid && operationID) {
      try {
        await api.cancelSession(targetSid, operationID)
        ctrl?.abort()
        // Re-read the session so its operation row clears and Stop becomes Send.
        const s = await api.session(targetSid)
        if (activeIdRef.current === targetSid) setSession(s)
      } catch (e) { toastError(e) }
      return
    }
    if (liveIds.length === 0) {
      // Do not detach an accepted ask before its operation frame arrives:
      // that loses the only identity the explicit cancellation API can use.
      if (ctrl) stopRequested.current = true
      return
    }
    // One job is one decision, and a stop button that asks first is a stop
    // button the stuck reader cannot use. A batch is the other way round:
    // several agents are several decisions, and a cascade cannot be undone.
    if (liveIds.length > 1) {
      const ok = await confirmDialog({
        title: t('sessions.stopTasksTitle'),
        message: t('sessions.stopTasksMessage', { n: String(liveIds.length) }),
        confirmLabel: t('sessions.stop'),
      })
      if (!ok) return
    }
    try {
      await Promise.all(liveIds.map((id) => api.cancel(id)))
      toast(t('sessions.stopTasksDone', { n: String(liveIds.length) }))
    } catch (e) { toastError(e) }
  }

  /** Type into the turn that is already streaming. Enter while busy used to be
   *  a silent early return — the composer's own placeholder promised "type an
   *  addition" and nothing happened. The text now lands on the session's steer
   *  queue (or the linked task's board), which is the same contract the TUI has
   *  always had for plain text typed mid-turn.
   *
   *  Files ride along: a screenshot dropped into the composer mid-turn uploads
   *  like any other attachment, and its mention is handed to the note. Refusing
   *  them (the old behaviour) meant the one gesture that most needs a note — "look
   *  at this while you work" — was the one gesture that could not be made. */
  async function sendIntoRunningTurn(text: string, tray: PendingAttachment[]) {
    const sid = activeId
    if (!sid || (!text && tray.length === 0)) return
    try {
      // Upload before the note so a failed upload leaves the draft intact.
      let mentions: string[] = []
      const uploads = tray.filter((p) => p.file)
      if (uploads.length > 0) {
        const up = await api.uploadSessionFiles(sid, uploads.flatMap((p) => (p.file ? [p.file] : [])))
        mentions = up.files.map((f) => f.mention)
      }
      // A workspace reference has no bytes to upload: its path rides with the
      // note as an attachment, the same shape an uploaded file's mention takes.
      const refs = tray.flatMap((p) => (p.ref ? [p.ref] : []))
      const r = await api.sendSessionMessage(sid, text, false, [...mentions, ...refs])
      // Only clear once the server accepted it: a refused note is still the
      // user's text, and losing it to an optimistic clear costs more than the
      // round trip.
      setInput('')
      editorClear(composer.current)
      setPending([])
      setCompleteDismissed('')
      // The server's own words when it has a caveat (no queue consumer, or an
      // inject that could not interrupt), else the plain outcome.
      if (r.warning) toast(r.warning)
      else toast(t('sessions.msgQueued'))
      void refreshNotes()
    } catch (err) {
      toastError(err)
    }
  }

  /** The pending strip's list: notes no round has folded yet. Polled rather
   *  than pushed — the round loop's consumption is a row stamp, not an event
   *  every surface subscribes to, and a 4s tick costs one small GET. */
  async function refreshNotes() {
    // Read the CURRENT thread, not the render this function closed over: the
    // old comparison compared a stale value to itself, so a slow reply for the
    // thread just left could install its notes under the new one.
    const sid = activeIdRef.current
    if (!sid) {
      setNotes([])
      return
    }
    try {
      const r = await api.sessionNotes(sid)
      if (activeIdRef.current === sid) {
        setNotes(r.notes)
        // The strip is also the fallback's trigger: a note parked by a turn
        // whose continuation never started is still listed here, and nothing
        // else would ever open a turn for it.
        resumeParkedNotes(sid, r.notes)
      }
    } catch {
      // A note list is advisory: a failed read must not paint an error over
      // the transcript. The next tick retries.
    }
  }

  /** Open the continuation turn for notes no round read.
   *
   *  `queued_turn` fires once, on the turn's way out. A note accepted in the
   *  window between the last round and that exit — or a frame lost with the
   *  stream — leaves the strip holding text nothing will read, which is the
   *  "queued" that never runs. The notes endpoint is the durable truth, so a
   *  turn that ends without the frame asks it once. Board rows are left
   *  alone (the executor reads those).
   *
   *  `immediate` marks the turn's own exit, which is the one attempt that must
   *  not wait: the strip's poll calls it too, so a note stranded by a lost
   *  frame, a closed tab or a continuation that died before it started still
   *  gets a turn — just not on every tick. */
  function resumeParkedNotes(sid: string, listed: SessionNote[], immediate = false) {
    const pend = listed.filter((n) => !n.durable)
    if (pend.length === 0) return
    if (inflight.current || activeIdRef.current !== sid) return
    const goal = goalFor(sid)
    if (goal?.phase === 'paused' || goal?.phase === 'cancelled' || (goal && goalAction.current === goal.id)) return
    // A turn is already streaming this thread: the note just queued belongs
    // to it, and opening a second ask would cancel the one being watched.
    if (inflightSid.current === sid) return
    if (noteEditing.current) return
    // A hidden tab keeps the note parked: the console is not being watched,
    // so a turn opened now would spend tokens on an answer nobody sees.
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    const key = pend.map((n) => n.key).join(',')
    const clock = resumeRetry.current
    if (clock.key !== key) {
      clock.key = key
      clock.at = 0
      clock.tries = 0
    }
    if (!immediate) {
      if (clock.tries >= 3 || Date.now() - clock.at < RESUME_RETRY_MS) return
    }
    clock.at = Date.now()
    clock.tries += 1
    void send(undefined, pend.map((n) => n.text).join('\n\n'), sid)
  }

  async function resumeLeftoverNotes(sid: string) {
    try {
      const r = await api.sessionNotes(sid)
      resumeParkedNotes(sid, r.notes, true)
    } catch {
      // A failed read leaves the notes where they are; the next turn's own
      // start drains them server-side.
    }
  }

  /** Send ▲ on a pending note: escalate it to an inject in place. */
  async function promoteNote(n: SessionNote) {
    const sid = activeId
    if (!sid) return
    try {
      const r = await api.promoteSessionNote(sid, n.key)
      if (r.warning) toast(r.warning)
      else if (r.status === 'interrupted') toast(t('sessions.noteInjected'))
      else toast(t('sessions.noteQueuedAhead'))
      void refreshNotes()
    } catch (e) {
      toastError(e)
    }
  }

  /** Trash on a pending note: take it back out of the queue. */
  async function dropNote(n: SessionNote) {
    const sid = activeId
    if (!sid) return
    try {
      await api.deleteSessionNote(sid, n.key)
      toast(t('sessions.noteDropped'))
      void refreshNotes()
    } catch (e) {
      toastError(e)
    }
  }

  /** Pencil on a pending note: edit the row itself. The slot keeps its queue
   *  position and its mode — the server rewrites the same note, so a round that
   *  has not read it yet sees only the corrected wording. */
  function beginEditNote(n: SessionNote) {
    noteEditing.current = true
    setEditingNote(n.key)
    setNoteDraft(n.text)
  }

  function cancelEditNote() {
    noteEditing.current = false
    setEditingNote('')
    setNoteDraft('')
  }

  /** Commit the in-place edit. An emptied row keeps its attachments; a row with
   *  neither text nor files is refused by the server, so treat that as "nothing
   *  to save" and close the editor instead of raising an error the user cannot
   *  act on. */
  async function saveEditNote(n: SessionNote) {
    const sid = activeId
    if (!sid) return
    const text = noteDraft.trim()
    const attachments = n.attachments ?? []
    if (!text && attachments.length === 0) {
      cancelEditNote()
      return
    }
    try {
      await api.editSessionNote(sid, n.key, text, attachments)
      cancelEditNote()
      toast(t('sessions.noteEdited'))
      void refreshNotes()
    } catch (e) {
      toastError(e)
    }
  }

  async function send(e?: Event, queued?: string, intoSid?: string) {
    e?.preventDefault()
    // A continuation turn carries its own prompt: the words came from the
    // notes the last turn never read, not from the composer. The draft and the
    // tray on screen belong to whatever the user types next, so they are left
    // untouched — clearing them here would eat text typed while the answer was
    // still streaming.
    const fromComposer = queued === undefined
    const typed = (queued ?? input).trim()
    // An armed chip owns the whole draft; a continuation is never a command.
    const intent = fromComposer ? composerIntent(armed, typed) : { goalText: null, planText: null }
    let goalText = intent.goalText
    const budget = goalText ? goalBudget(goalText) : undefined
    if (budget?.invalid) { toast(t('sessions.goal.invalidBudget')); return }
    if (budget) goalText = budget.text
    const planText = intent.planText
    // A bare `/goal` is a pick, not a send: the token leaves the editor and
    // the chip carries the mode until the task is published. The same words
    // with the chip already armed mean the objective is still empty.
    if (goalText === '') { if (!armed) armMode('goal'); return }
    if (planText === '') {
      // The pick itself is the instruction, whether it came from the menu or
      // was typed out: planning already on means the same pick leaves the
      // mode; otherwise the chip arms and the next send says what to plan.
      // Typing the word and pressing Enter must not do something else than
      // picking it from the list did.
      if (armed) return
      if (controlState?.planning) { removeModeCommand(); await exitPlanning() }
      else armMode('plan')
      return
    }
    if (planText !== null && (busy || inflight.current)) {
      toast(t('sessions.plan.busy'))
      return
    }
    if (goalText) {
      // An armed chip means the draft *is* the objective: the legacy control
      // words (`pause`, `stop`, `edit`…) belong to the `/goal …` text form,
      // and an objective that happens to be one word must not act on the
      // current goal instead of becoming a new one.
      const command = armed === 'goal' ? { action: 'create' as const, objective: goalText } : goalCommand(goalText)
      if (command.action === 'create') {
        goalText = command.objective
        if (!goalText) { toast(t('sessions.goal.hint')); return }
      } else {
        if (!goal) { toast(t('sessions.goal.noCurrent')); return }
        if (command.action === 'edit' && !command.objective) { openGoalEditor(); removeModeCommand(true); return }
        const sid = activeIdRef.current
        const applied = await changeGoal(command.action, command.objective, budget?.tokens)
        if (applied && sid === activeIdRef.current && composer.current && editorTextOf(composer.current).trim() === typed) removeModeCommand(true)
        return
      }
    }
    const goalTarget = goalText
    if (goalTarget && (busy || inflight.current)) {
      toast(t('sessions.goal.busy'))
      return
    }
    const raw = planText ?? goalText ?? typed
    // The tray mirrors the pills still in the editor: anything whose pill is
    // gone (Backspace, a selection-delete) stays behind instead of uploading
    // with the send.
    const el0 = composer.current
    const tray =
      !fromComposer ? [] : el0 ? pending.filter((p) => livePillKeys(el0).has(p.key)) : pending
    // A tray-only send is a real send: dropping a screenshot and hitting Enter
    // is the whole point, and the prompt then carries nothing but the file.
    if (!raw && tray.length === 0) return
    // A slash line is a command, never a prompt — the REPL's rule. It is
    // checked before the busy guard because a command acts on the console, not
    // on the running turn: a job can hold this thread for minutes, and folding
    // `/new` or `/tasks` into a note for it is not "type an addition". A
    // continuation is exempt either way: the words are what the user already
    // queued, and running a command they typed mid-answer would execute it out
    // of order.
    if (fromComposer && goalText === null && planText === null && raw.startsWith('/') && dispatchSlash(raw)) {
      setInput('')
      editorClear(composer.current)
      setCompleteDismissed('')
      return
    }
    // A continuation is never a mid-turn note: it starts the turn the previous
    // one ended without, so the busy guard (and its "type an addition" path)
    // does not apply — `busy` is still true in this closure's finally.
    if (fromComposer && (busy || inflight.current)) {
      void sendIntoRunningTurn(raw, tray)
      return
    }
    // Claim the slot before the async file expansion — a second Enter during
    // the /api/fs/read round-trips must not start a parallel ask.
    const ctrl = new AbortController()
    inflight.current = ctrl
    inflightOperation.current = null
    stopRequested.current = false
    let id = intoSid ?? activeId
    const origin = activeIdRef.current
    const keepAlive = acquireKeepAlive()
    let patch: (fn: (m: ChatMsg) => ChatMsg) => void = () => {}
    let prompt = raw
    let attached: string[] = []
    /** Set once the user's bubble is on screen, so a later failure has
     *  somewhere to render instead of needing a toast. */
    let announced = false
    // Deltas arrive per chunk — dozens a second — and each one re-rendered
    // the whole transcript, re-parsing the growing reply every time. They
    // are coalesced into one update per frame; nothing is lost, because the
    // turn's exit flushes whatever is still buffered.
    let pendingText = ''
    let pendingThought = ''
    let flushFrame = 0
    const flushStream = () => {
      flushFrame = 0
      const text = pendingText
      const thought = pendingThought
      pendingText = ''
      pendingThought = ''
      if (text) patch((m) => ({ ...m, text: m.text + text, lastActivity: Date.now() }))
      if (thought)
        patch((m) => ({ ...m, thought: (m.thought ?? '') + thought, lastActivity: Date.now() }))
    }
    const scheduleFlush = () => {
      if (flushFrame) return
      flushFrame = requestAnimationFrame(flushStream)
    }

    try {
      // Where this thread runs. A directory picked in the composer's footer is
      // resolved before anything else: the panel reads a session's work dir
      // from its project row, so a directory no project points at would be
      // silently ignored.
      let threadProject = project || selectedProject || undefined
      if (dirTouched && pickedDir) {
        try {
          threadProject = await projectForDir(pickedDir)
          setSelectedProject(threadProject)
          setDirProject(threadProject)
        } catch (err) {
          // The send matters more than the pick: report the failure and fall
          // back to the scope's directory rather than dropping the turn.
          toastError(err)
        }
      }
      // The thread landed outside the project this pane was opened in. The
      // URL still says otherwise, and the pane's boundary guard compares the
      // two — so the scope has to be dropped, or the thread it just opened is
      // unselected on the next render and lost on reload.
      const dropScope = !!project && !!threadProject && threadProject !== project
      /** Open the thread that was just created: through the route when it
       *  stays inside this pane's scope, and by a project-less hash when it
       *  does not. */
      const openCreatedThread = (created: Session) => {
        setSessions((ls) => [created, ...ls])
        if (activeIdRef.current !== origin || inflight.current !== ctrl) return
        setSession(created)
        inflightSid.current = created.id
        activeIdRef.current = created.id
        if (dropScope) navigate({ view: 'sessions', id: created.id })
        else onOpenSession(created.id)
      }
      // Uploads land in the session's own uploads directory, so a brand-new
      // chat with attachments needs its thread up front — the same create the
      // send below would have made anyway, one step earlier.
      // Only byte attachments need a thread before the send: a workspace
      // reference resolves from the prompt after the thread exists anyway.
      const uploads = tray.filter((p) => p.file)
      if (uploads.length > 0 && !id) {
        const s = await api.createSession((raw || uploads[0]?.name || '').slice(0, 48), threadProject, team)
        id = s.id
        openCreatedThread(s)
      }
      // One multipart round-trip for the tray's real uploads, then the
      // mentions ride the prompt as @paths: text files arrive as fenced
      // blocks, images stay paths for the agent to open (see expandFileRefs).
      let mentions: string[] = []
      if (uploads.length > 0) {
        const up = await api.uploadSessionFiles(id!, uploads.flatMap((p) => (p.file ? [p.file] : [])))
        mentions = up.files.map((f) => f.mention)
      }
      // The files dock's 插入 inserts a pill for a workspace path rather than
      // uploading bytes. Hand that path to the same @file expansion, so the
      // model sees the file's contents instead of just a filename.
      const refs = tray.flatMap((p) => (p.ref ? [p.ref] : []))
      // @file references become inline fenced blocks before the prompt
      // leaves the composer — "explain @main.go" works without pasting the
      // file. Inside try so a rejected expansion still clears inflight.
      const expanded = await expandFileRefs(
        withAttachments(raw, [...mentions, ...refs]),
        (p) => api.fsRead(p),
      )
      prompt = expanded.prompt
      // The bubble lists what rode along, under paths the panel can resolve:
      // an upload's mention (its stamped name keeps the author's name at the
      // tail — see msg-files.tsx) and a reference pill's workspace path. The
      // tray's display names are not used: a bare name is not a path, so the
      // bubble could not draw a pasted picture with it. Both groups are
      // dropped from the expanded list, or every file would show twice.
      const trayMentions = new Set([...mentions, ...refs])
      attached = [
        ...new Set([
          ...expanded.attached.filter((a) => !trayMentions.has(a)),
          ...mentions,
          ...refs,
        ]),
      ]
      // No thread yet: create one titled after the first prompt.
      if (!id) {
        const s = await api.createSession(prompt.slice(0, 48), threadProject, team)
        id = s.id
        // A mode picked before the first send has no row to live on until
        // now; persist it so reopening the thread does not silently widen it.
        if (permission !== 'auto') {
          api.patchSession(s.id, { permission_mode: permission }).catch(() => {
            /* the ask below still carries the mode for this turn */
          })
        }
        openCreatedThread(s)
      }
      // Race guard: snapshot the thread this reply belongs to. Every write
      // below checks it — if the user switched threads mid-turn the writes
      // are silently dropped. That is safe because the panel persists every
      // turn: the refetch in finally (and the new thread's own loader) is
      // the eventual truth for whichever thread ends up on screen. The
      // stream's rendering stops; the server-side turn is not cancelled.
      const sid = id
      if (inflight.current === ctrl) inflightSid.current = sid
      const live = () => inflight.current === ctrl && isLiveSession(sid, activeIdRef.current)
      if (goalTarget) {
        await api.controlConversation(sid, { action: 'create', objective: prompt, token_budget: budget?.tokens })
        if (fromComposer && live()) {
          setArmed((mode) => mode === armed ? null : mode)
          if (composer.current && editorTextOf(composer.current).trim() === typed) {
            setInput('')
            clearPending()
          }
        } else {
          const draft = drafts.current.get(sid)
          if (draft && draft.mode === armed && draft.text.trim() === typed && draft.attachments === pending) drafts.current.delete(sid)
        }
        void refreshControl(sid).catch(() => {})
        return
      }
      const replyKey = localMsgId()
      if (live()) {
        setBusy(true)
        // A continuation has no composer draft behind it: the text came from
        // the parked notes, and whatever is in the editor now was typed after
        // this turn started — it must survive.
        if (fromComposer) {
          setInput('')
          setArmed(null)
          clearPending()
        }
        // Sending is an explicit "show me the answer", so re-pin the view.
        pinned.current = true
        setIsPinned(true)
        // Clock starts when the bubble appears: the wait the user is about to
        // sit through is the one they will read back above the reply, ticking
        // once a second until it lands.
        turnStartRef.current = Date.now()
        setLiveFrom(turnStartRef.current)
        setMsgs((ms) => [
          ...ms,
          {
            role: 'user',
            text: raw,
            files: attached.length ? attached : undefined,
            // Stamped here as well as server-side: the bubble is on screen
            // before the turn is filed, and its clock should not pop in on the
            // refetch that follows.
            ts: Math.floor(Date.now() / 1000),
            k: localMsgId(),
          },
          { role: 'assistant', text: '', streaming: true, k: replyKey },
        ])
        announced = true
      }

      // Aim every delta at the bubble actually streaming. A refetch racing
      // the stream can leave a stale message at the tail; patching the tail
      // blindly used to write replies into the user's own message. The
      // session guard rides along: a backgrounded reply patches nothing.
      patch = (fn: (m: ChatMsg) => ChatMsg) => {
        if (!live()) return
        setMsgs((ms) => patchStreaming(ms, fn, replyKey))
      }

      await askSessionStream(
        id,
        prompt,
        // No standing grant from this surface: a tier-2 operation parks on the
        // approval card, and "run everything" is the bypass permission mode.
        false,
        {
          onReasoning: (text) => {
            pendingThought += text
            scheduleFlush()
          },
          onDelta: (text) => {
            pendingText += text
            scheduleFlush()
          },
          // Usage is its own event: a counter, not a status note. It lands at
          // most a few times a second (the engine throttles it), so it can
          // patch state directly.
          onUsage: (input, output, estimated) =>
            patch((m) => ({
              ...m,
              tokens: input + output,
              tokensEstimated: estimated,
              lastActivity: Date.now(),
            })),
          onActivity: record => patch(m => ({ ...m, activities: upsertActivity(m.activities ?? [], record), lastActivity: Date.now() })),
          onStatus: (text, ev) =>
            patch((m) => ({ ...m, status: statusNote(text, ev), lastActivity: Date.now() })),
          // The note typed mid-turn reached a round. This is the upgrade from
          // "queued" to "delivered" — without it the user cannot tell a steer
          // the model read from one the turn ended before reaching.
          onSteer: (_text, round) =>
            patch((m) => ({
              ...m,
              status: t('sessions.steerDelivered', { n: String(round) }),
              lastActivity: Date.now(),
            })),
          // The entry model asked before it will create the task (or the
          // runtime's plan-confirm gate fired). The ask goroutine is parked on
          // this answer; the card below the transcript collects it.
          onQuestion: (spec, operationID) => {
            if (live()) setClarify({ sessionID: sid, operationID, spec })
          },
          // The turn ended before a round could read what was queued behind it.
          // The server parked those notes; remember them so the finally below
          // opens the continuation turn instead of letting them evaporate.
          onQueuedTurn: (text) => {
            queuedTurn.current = text
          },
          onOperation: (operationID) => {
            if (inflight.current === ctrl && inflightSid.current === sid) {
              inflightOperation.current = operationID
              patch((m) => ({ ...m, operation_id: operationID }))
              if (stopRequested.current) {
                void api.cancelSession(sid, operationID).then(() => ctrl.abort()).catch(toastError)
              }
            }
          },
          // A task row exists for this turn. Adopt its id as a side channel —
          // `kind` stays untouched, because a plan turn must not flip into a
          // task card mid-stream — so the thread can render the employee's
          // live progress row for the whole execution instead of a spinner.
          onTask: (taskID) => patch((m) => ({ ...m, liveTask: taskID })),
          onResult: (r) => {
            // Land what is buffered first: the reconcile below appends only
            // the tail the stream withheld, and a flush afterwards would
            // duplicate it.
            flushStream()
            patch((m) => {
              // Reconcile the accumulated deltas against the parsed reply:
              // whatever the stream withheld still lands — "something
              // streamed" is not "everything did".
              let text = m.text
              const answer = r.answer
              if (answer) {
                if (answer.startsWith(text)) {
                  const tail = answer.slice(text.length)
                  if (tail.trim()) text += tail
                } else if (!text.includes(answer)) {
                  text = text ? text + '\n' + answer : answer
                }
              }
              // A dispatched task's frame carries no prose, only its id: the
              // turn is a task turn, and everything that renders it — the orbit,
              // the task card, the jump into the session — keys off kind+ref.
              // Without adopting them here the bubble stays empty until some
              // unrelated refetch replaces the transcript.
              const task =
                r.kind === 'task' && r.task_id
                  ? ({ kind: 'task', ref: r.task_id } as const)
                  : {}
              // A plan turn carries no prose either: its id is what the plan
              // card and the cast link key off. Adopting it here keeps the
              // bubble from staying blank until a reload replaces the turn.
              const plan =
                r.kind === 'plan' && r.plan_id
                  ? ({ kind: 'plan', ref: r.plan_id } as const)
                  : {}
              return {
                ...m,
                ...task,
                ...plan,
                result: r,
                thought: m.thought || r.thought,
                status: undefined,
                text,
                liveTask: undefined,
                // The billed total wins; a stream that ended before any usage
                // event keeps whatever the live counter last showed.
                tokens: r.tokens || m.tokens,
                tokensEstimated: r.tokens ? false : m.tokensEstimated,
              }
            })
          },
          onError: (message, code, info) => {
            flushStream()
            patch((m) => ({
              ...m,
              status: undefined,
              kind: 'error',
              text: m.text || message,
              code,
              // The classified model-error kind + provider detail drive the
              // troubleshooting <details> under the bubble — same fields the
              // panel persists on the turn, so a reload renders identically.
              err_kind: info?.kind,
              err_detail: info?.detail,
            }))
          },
        },
        ctrl.signal,
        nodePref || undefined,
        // The roster as the composer shows it, every turn: the pick can be
        // edited mid-thread, and the ask carries the answer rather than
        // trusting whatever the session stored before the edit landed.
        team,
        // The permission policy as the picker shows it. The session PATCH
        // keeps it across reloads; sending it here keeps this turn honest
        // even if the patch has not landed yet.
        permission,
        // A continuation: the server drains its parked notes as this turn's
        // input, so the prompt above is display only and cannot double-post.
        queued !== undefined,
        planText !== null ? 'plan' : undefined,
      )
      flushStream()
      patch((m) => ({ ...m, streaming: false, status: undefined }))
      finishTurn()
    } catch (err) {
      // Whatever streamed before the failure is the user's only copy of it.
      flushStream()
      if (!announced) {
        // Nothing left the composer yet — the failure was in the upload or the
        // @ref expansion, and there is no bubble to carry the reason. Say it
        // out loud instead of looking like the send button did nothing; the
        // tray is still there, so the send can be retried.
        toastError(err)
        return
      }
      if (isLiveSession(id, activeIdRef.current)) {
        if (isAbort(err)) {
          // Stopped on purpose: mark the turn done and note why, rather than
          // painting the user's own action as a failure. Aimed at the bubble
          // that was streaming — same target rule as the deltas above.
          patch((m) => ({
              ...m,
              streaming: false,
              status: undefined,
              text: m.text || `· ${t('sessions.stopped')}`,
            }))
          // A stop still took the time it took — say so rather than leaving
          // the foot of the bubble blank.
          finishTurn()
        } else {
          // The operation snapshot is durable in the session. Follow that
          // exact generation until it terminalizes instead of guessing from a
          // fixed number of transcript polls.
          //
          // Two things bound this watch. A stream that died before the
          // `operation` frame arrived leaves no id to follow, so the session's
          // own operation row is the fallback — and the deadline is the floor
          // under both: the loop used to run forever when the id was missing,
          // which is a spinner nobody could dismiss without reloading.
          let resolved = false
          if (id) {
            patch((m) => ({ ...m, status: t('sessions.syncing') }))
            let delay = 500
            const deadline = Date.now() + RECOVER_WATCH_MS
            while (
              !ctrl.signal.aborted &&
              isLiveSession(id, activeIdRef.current) &&
              Date.now() < deadline
            ) {
              try {
                const s = await api.session(id)
                const operationID = inflightOperation.current
                const op = s.operation
                const settled = operationID
                  ? op?.id === operationID && op.status !== 'running'
                  : !op || op.status !== 'running'
                if (settled) {
                  const turns = s.turns ?? []
                  setSession(s)
                  setMsgs(hydrateTurns(turns, (i) => `srv-${i}`))
                  // The turn did land server-side, just not over the stream —
                  // its wall-clock is as real as any other reply's.
                  finishTurn()
                  // Settled without a reply means the turn never wrote one
                  // (cancelled, failed before the model answered). That is
                  // still an outcome the pane must report, so it falls through
                  // to the error bubble below rather than looking finished.
                  resolved = turns[turns.length - 1]?.role === 'assistant'
                  break
                }
              } catch {}
              await new Promise((r) => setTimeout(r, delay))
              delay = Math.min(delay * 2, 5000)
            }
          }
          if (!resolved && isLiveSession(id, activeIdRef.current) && !ctrl.signal.aborted) {
            const message =
              err instanceof ApiError && err.status === 0
                ? t('sessions.streamLost')
                : err instanceof Error
                  ? err.message
                  : String(err)
            setMsgs((ms) => [
              ...ms.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
              { role: 'assistant', kind: 'error', text: message, k: localMsgId() },
            ])
          }
        }
      }
      // Else the user switched threads mid-turn: the pane already belongs to
      // the transcript loader of the new thread, so this reply's failure
      // (or stop) has nowhere to render. The persisted transcript stands.
    } finally {
      keepAlive.release()
      const ownsFlight = inflight.current === ctrl
      if (ownsFlight) {
        setClarify(null)
        inflight.current = null
        inflightSid.current = null
        inflightOperation.current = null
        stopRequested.current = false
        setBusy(false)
      }
      // Persisted transcript is server-side truth; refresh title + turns.
      if (id) {
        api.session(id).then((s) => {
          // The rail entry's title is worth refreshing either way; the pane
          // belongs to whichever thread is active now.
          setSessions((ls) => ls.map((x) => (x.id === s.id ? s : x)))
          if (isLiveSession(id, activeIdRef.current) && !inflight.current) {
            setSession(s)
            // …and the pane follows it. The change feed skipped this turn
            // while the ask was in flight (it must not paint over a live
            // stream) and its fingerprint is already spent, so this refetch
            // is the only chance to pick up a turn the wire under-reported.
            // A fanned-out round is under-reported by design — one result
            // frame, one turn per task — so the surplus rows are adopted here
            // rather than waiting for a reload.
            setMsgs((ms) =>
              reconcileTranscript(ms, hydrateTurns(s.turns ?? [], (i) => `srv-${i}`), s.operation?.id),
            )
          }
        }).catch(() => {})
      }
      // What the turn owed: notes it ended without any round reading. The
      // server parked them, and this is the turn that opens with them — the
      // composer used to print "queued" and then let them vanish at the end of
      // the answer. A stopped turn is the one exception: the user asked for
      // quiet, so the notes stay parked for the next thing they send.
      const owed = ownsFlight ? queuedTurn.current : ''
      if (ownsFlight) queuedTurn.current = ''
      // The continuation goes back into the thread this turn belonged to —
      // `id`, not the `activeId` this render closed over. On the
      // create-then-send path that prop is still null (the navigation has
      // not re-rendered this closure), so reading it here minted a second
      // thread and posted the queued words there, leaving the original
      // strip waiting forever.
      if (
        ownsFlight &&
        !ctrl.signal.aborted &&
        id &&
        !inflight.current &&
        isLiveSession(id, activeIdRef.current)
      ) {
        if (owed) void send(undefined, owed, id)
        // `queued_turn` is a single shot, and the window between a turn's
        // last round and the handler's exit can accept a note after the
        // leftovers were already parked. When the frame does not arrive, the
        // notes endpoint is the truth: ask it once and resume what it still
        // holds, instead of leaving the strip to wait for a round that will
        // never come.
        else void resumeLeftoverNotes(id)
      }
    }
  }

  /** Pick a mode: the token leaves the editor and a chip beside the actions
   *  carries it until the task is published. Whatever followed the token stays
   *  as the draft, so `/goal fix the login` picked from the menu keeps
   *  "fix the login" as the objective instead of throwing it away. */
  function armMode(mode: ComposerMode) {
    removeModeCommand()
    setArmed(mode)
    setGoalEditor(null)
    composer.current?.focus()
  }

  function openGoalEditor() {
    if (goal && activeId) setGoalEditor({ sid: activeId, id: goal.id, text: goal.text })
  }

  async function exitPlanning() {
    const sid = activeIdRef.current
    if (!sid) return
    try {
      await api.controlConversation(sid, { action: 'mode', planning: false })
      await refreshControl(sid)
    } catch (error) { toastError(error) }
  }

  // Delete only the slash token. DOM Range adjustment preserves attachment
  // pills and the selection in the body, including split text nodes.
  function removeModeCommand(entire = false) {
    const el = composer.current
    if (!el) return
    const prefix = /^\s*\/\S*\s?/.exec(editorTextOf(el))
    if (!prefix) return
    const selection = document.getSelection()
    const saved = selection?.rangeCount ? selection.getRangeAt(0).cloneRange() : null
    const walker = editorTextWalker(el)
    let left = entire ? editorTextOf(el).length : prefix[0].length
    let node: Node | null
    while (left > 0 && (node = walker.nextNode())) {
      if (node.nodeType === Node.TEXT_NODE) { const n = Math.min(left, (node as Text).length); (node as Text).deleteData(0, n); left -= n }
      else left--
    }
    el.focus()
    if (saved && el.contains(saved.startContainer)) { selection?.removeAllRanges(); selection?.addRange(saved) }
    setInput(editorTextOf(el)); setCaret(editorCaretOffset(el)); setCompleteDismissed('')
  }

  async function changeGoal(action: 'pause' | 'resume' | 'clear' | 'stop' | 'edit', objective?: string, tokenBudget?: number) {
    const sid = activeIdRef.current
    if (!sid || !goal || goalAction.current) return false
    goalAction.current = goal.id; setPendingGoalAction(goal.id)
    try {
      if (action === 'clear') {
        const ok = await confirmDialog({
          title: t('sessions.goal.clear'),
          message: t('sessions.goal.clearConfirm'),
          confirmLabel: t('sessions.goal.clear'),
        })
        if (!ok || activeIdRef.current !== sid) return false
      }
      await api.controlConversation(sid, { action, goal_id: goal.id, objective, token_budget: tokenBudget })
      void refreshControl(sid).catch(() => {})
      if (action === 'pause' || action === 'stop') {
        // Both interrupt the round server-side. Re-read the session so its
        // operation settles here and the thinking bubble leaves with it,
        // rather than waiting for the next change frame.
        const s = await api.session(sid).catch(() => null)
        if (s && activeIdRef.current === sid) setSession(s)
      }
      return true
    } catch (error) { toastError(error); return false }
    finally { goalAction.current = null; setPendingGoalAction(null) }
  }

  async function confirmProposal(review: NonNullable<typeof planReview>) {
    const { sid, version } = review
    if (sid !== activeIdRef.current || proposal?.version !== version || busy || inflight.current) return
    try {
      await api.controlConversation(sid, { action: 'confirm', version })
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 409) {
        toastError(error)
        return
      }
      toast(t('sessions.plan.changed'), 'info')
    }
    setPlanReview((current) => current === review ? null : current)
    void refreshControl(sid).catch(() => {})
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
      { id: 'cmd:new', group: grp, label: '/new', hint: t('sessions.cmdNew'), run: () => newChat() },
      { id: 'cmd:export', group: grp, label: '/export', hint: t('sessions.cmdExport'), run: exportSession },
      { id: 'cmd:clear', group: grp, label: '/clear', hint: t('sessions.cmdClear'), run: () => { clearPending(); setInput(''); setArmed(null) } },
      { id: 'cmd:cost', group: grp, label: '/cost', hint: t('sessions.cmdCost'), run: go('settings:system') },
      { id: 'cmd:context', group: grp, label: '/context', hint: t('sessions.cmdContext'), run: go('settings:system') },
      { id: 'cmd:doctor', group: grp, label: '/doctor', hint: t('sessions.cmdDoctor'), run: go('settings:system') },
      { id: 'cmd:model', group: grp, label: '/model', hint: t('sessions.cmdModel'), run: go('settings:models') },
      { id: 'cmd:tasks', group: grp, label: '/tasks', hint: t('sessions.cmdTasks'), run: go('control') },
      { id: 'cmd:goal', group: grp, label: '/goal', hint: t('sessions.goal.hint'), run: () => armMode('goal') },
      // While planning is on the pick keeps its old meaning and leaves the
      // mode; otherwise it arms the chip and the send decides what to plan.
      { id: 'cmd:plan', group: grp, label: '/plan', hint: t('sessions.plan.hint'), run: () => {
        if (controlState?.planning) { void exitPlanning(); setArmed(null); removeModeCommand(); return }
        armMode('plan')
      } },
      { id: 'cmd:plans', group: grp, label: '/plans', hint: t('sessions.cmdPlans'), run: go('control:plans') },
      {
        id: 'cmd:read',
        group: grp,
        label: '/read',
        hint: t('sessions.cmdRead'),
        // The DOM is the composer's truth: an argument rides insertMentions
        // like a picked path; a bare '@' lands without its trailing space so
        // atQuery still sees the open token and the file menu comes up.
        run: (arg) => {
          if (arg) {
            insertMentions([`@${arg}`])
            return
          }
          const el = composer.current
          el?.focus()
          editorInsertText('@')
          requestAnimationFrame(() => {
            if (!el) return
            setInput(editorTextOf(el))
            setCaret(editorCaretOffset(el))
            autoGrow(el)
          })
        },
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
  // The token is typed relative to the thread's own workspace, so the listing
  // is too — without the session it would enumerate the node's data tree.
  const atTok = atQuery(input, caret)
  const atSession = session?.id
  useEffect(() => {
    if (!atTok) {
      setAtFiles([])
      return
    }
    let alive = true
    const timer = setTimeout(() => {
      api
        .fsFiles(atTok.token, atSession)
        .then((r) => alive && setAtFiles(r.entries.slice(0, 12)))
        .catch(() => alive && setAtFiles([]))
    }, 120)
    return () => {
      alive = false
      clearTimeout(timer)
    }
  }, [atTok?.token, atSession])

  // Not memoized for the same reason as chatCommands — insertAtRef splices
  // the token against the live input/caret, and a memoized item captures
  // whichever render produced it.
  const fileItems: CompletionItem[] = atFiles.map((f) => ({
    id: `file:${f.path}`,
    label: f.name + (f.dir ? '/' : ''),
    hint: f.dir ? 'dir' : fmtSize(f.size),
    run: () => insertAtRef(f),
  }))

  // The `/` menu names a command, so it closes once whitespace follows the
  // token: what comes next is that command's argument, not another verb. A
  // bare `/plan` or `/goal` keeps it open — suppressing the menu on the
  // keystroke that completes the word was what made both commands unpickable
  // exactly when they were fully typed. Matching is word-start (`rankWords`),
  // not the palette's subsequence: `/pla` must offer the two plan verbs, not
  // every destination whose English keywords happen to spell p…l…a.
  const slashArg = slashArgStarted(input)
  const completeShown: CompletionItem[] = (() => {
    if (input === completeDismissed || slashArg) return []
    if (slashToken !== null) return rankWords(commands, slashToken, slashWords)
    if (atTok) return fileItems
    return []
  })()
  // Ranking reorders on every keystroke; clamp instead of trusting the old
  // index still points at a row.
  const completeIdx = Math.min(completeCursor, Math.max(completeShown.length - 1, 0))

  /** Replace the @token under the caret with the picked path; directories
   *  keep the token open so the menu refetches one level deeper.
   *
   *  The DOM is the truth here: writing `next` into state left the editor
   *  showing the old text while `input` already held the expanded path, so the
   *  @menu never reopened and the completion looked dead. Select the token
   *  span instead and let the browser's own insertion replace it. */
  function insertAtRef(f: FsFileEntry) {
    const el = composer.current
    if (!el || !atTok) return
    el.focus()
    // Replace the whole @token under the caret, not just insert at the caret:
    //  the token's span is [atTok.start, caret).
    const end = caret
    const walker = editorTextWalker(el)
    let at = 0
    let startNode: Text | null = null
    let startOff = 0
    let endNode: Text | null = null
    let endOff = 0
    let node: Node | null
    while ((node = walker.nextNode())) {
      // Non-text nodes the walker accepts are the breaks editorText renders
      // as one '\n' — they count a character but can never anchor an
      // endpoint, since the token is a run of text.
      const len = node.nodeType === Node.TEXT_NODE ? (node.textContent ?? '').length : 1
      if (!startNode && node.nodeType === Node.TEXT_NODE && at + len > atTok.start) {
        startNode = node as Text
        startOff = atTok.start - at
      }
      if (at + len >= end) {
        if (node.nodeType === Node.TEXT_NODE) {
          endNode = node as Text
          endOff = end - at
        }
        break
      }
      at += len
    }
    const range = document.createRange()
    if (!startNode || !endNode) {
      // No text node to span the token (an empty editor): insert at the caret.
      const sel = document.getSelection()
      if (sel && sel.rangeCount > 0) range.setStart(sel.getRangeAt(0).startContainer, sel.getRangeAt(0).startOffset)
      else range.selectNodeContents(el)
    } else {
      range.setStart(startNode, Math.min(startOff, (startNode.textContent ?? '').length))
      range.setEnd(endNode, Math.min(endOff, (endNode.textContent ?? '').length))
    }
    const sel = document.getSelection()
    if (sel) {
      sel.removeAllRanges()
      sel.addRange(range)
    }
    // insertText replaces the selection, so the token goes and the path lands.
    // A directory appends its separator to keep the menu open on the level.
    editorInsertText(`@${f.path}${f.dir ? '/' : ' '}`)
    // The caret after `field` is a space one shorter than the inserted text.
    setCaret(atTok.start + f.path.length + (f.dir ? 2 : 2))
    setCompleteDismissed('')
    requestAnimationFrame(() => {
      const now = editorTextOf(el)
      setInput(now)
      autoGrow(el)
    })
  }

  function acceptCompletion(item: CompletionItem) {
    if (item.id === 'cmd:plan' || item.id === 'cmd:goal') { item.run(); return }
    if (item.id.startsWith('file:')) {
      item.run()
      return
    }
    // Text already typed after the command name is its argument: `/read
    // foo.go` + Enter runs /read with "foo.go", not a bare menu pick that
    // would silently drop the path.
    const arg = /^\/\S+\s+(.+)$/.exec(input)?.[1]?.trim() || undefined
    setInput('')
    // The state is not the surface: clearing `input` alone left the typed
    // slash line sitting in the editable under whatever the command inserts.
    editorClear(composer.current)
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

  /** Drop mentions at the caret and leave the caret just after them. Each
   *  token carries a trailing space, which is load-bearing: `atQuery` only
   *  matches an `@…` token that ends the text, so without it the file
   *  completion would open on top of what we just wrote. */
  function insertMentions(tokens: string[]): void {
    if (tokens.length === 0) return
    const el = composer.current
    const chunk = tokens.map((t) => `${t} `).join('')
    el?.focus()
    editorInsertText(chunk)
    // The DOM is the source of truth now; the state follows it one render later.
    requestAnimationFrame(() => {
      if (el) autoGrow(el)
      if (el) setInput(editorTextOf(el))
    })
  }

  /** A dictated phrase lands where the caret was, like typing does. */
  function insertDictation(text: string): void {
    const el = composer.current
    if (!el) return
    insertTextAtCaret(el, text)
    requestAnimationFrame(() => {
      setInput(editorTextOf(el))
      setCaret(editorCaretOffset(el))
      autoGrow(el)
    })
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

  // The new-chat page: nothing open, nothing to read. The footer's directory
  // pick and the keyboard legend are that page's furniture only — a thread
  // that exists already knows where it runs, and the legend has been read.
  const newChatPage = !activeId && msgs.length === 0 && !loading
  // 新对话页没有右侧栏。这一页还没有线程：栏里的每一页（文件、页面、侧聊）都无从
  // 谈起，而唯一与这一页有关的那个决定 —— 在哪台机器上跑 —— 原本也锁在栏的工具
  // 页后面。所以这一页把节点按钮直接摆到头部（见下），栏本身不出现，连折叠按钮
  // 也不留（没有栏可折）。线程一存在就恢复原样：栏、栏的按钮、它记住的那一页。
  const dockShown = dockOpen && !newChatPage

  // The overview is furniture, not a preference: a thread that is open always
  // has one, the way it always has a composer. It is a single line in the pane
  // head now — no panel to open — so it costs the transcript nothing and stays
  // up even when the side dock takes the right edge. The new-chat page has no
  // thread to summarize, so it gets none.
  //
  // The task read that feeds this line (and the live employee rows, and the
  // composer's Stop button) is taken at the top of the pane, where `busy`
  // needs it; it is not gated on the rail being open.
  const showRail = !newChatPage

  // "Context used" is the last request's billed total: the prompt the thread
  // actually sent. The newest assistant turn carries it — live, while the
  // reply streams, and again after a reload, because the panel files the
  // number with the turn. One read answers both the footprint and the model
  // that produced it.
  const newestUsage = (() => {
    for (let i = msgs.length - 1; i >= 0; i--) {
      const m = msgs[i]!
      if (m.role === 'assistant' && ((m.tokens ?? 0) > 0 || m.model)) return m
    }
    return null
  })()
  // Where this thread is pinned to run, in the same words the node picker
  // uses: the scheduler's own answer when nothing is pinned.
  const railMachine = nodePref
    ? nodes.find((n) => n.id === nodePref)?.name || nodePref
    : t('sessions.nodeAuto')

  // The model registry is read once for the two surfaces that print the
  // context share — the pane head's status line and the dock's start pane.
  // Reading it in each of them put two identical requests on every page load.
  const modelRegistry = useAsync(() => api.models().catch(() => null), [])
  const activeModel = modelRegistry.data?.active
  const contextWindow = activeModel?.context_window ?? 0
  const modelName = activeModel?.alias || newestUsage?.model || ''

  // Whose context the rail and the dock print. The entry model only routes
  // and judges — its window is not the one the reader is watching fill up.
  // Every agent working this thread reports a request-scoped sample per model
  // call, so those win: all live agents first (a team or a fan-out has one
  // window each), the last one that ran second, and the entry model's own
  // turn only for a thread that never dispatched anything.
  const agentCtxs = agentContextSamples(sessionTasks)
  const primaryCtx = agentCtxs[0]
  const contextTokens = primaryCtx?.tokens ?? newestUsage?.tokens ?? 0
  const contextWin = primaryCtx ? primaryCtx.window : contextWindow
  const contextAgent = primaryCtx ? prettyAgent(primaryCtx.agent) : ''
  const contextAgents: ContextAgentView[] = agentCtxs.map((sample) => ({
    name: prettyAgent(sample.agent),
    tokens: sample.tokens,
    window: sample.window,
  }))

  // The open thread's directory, read-only: the foot row under the box keeps
  // its place on every surface — 新对话 picks a directory there, 工作空间
  // picks one, and a thread shows where it runs — so moving between them does
  // not bounce the composer up and down. The pill is display, not a control:
  // a thread's directory is fixed by its project row and cannot be re-picked
  // mid-thread. A project with no dir yet falls back to its name, which is
  // what the rail calls it anyway.
  const threadDir = (() => {
    const name = session?.project || activeProject || ''
    if (!name) return ''
    const hit = threadProjects.find((d) => d.name === name)
    return hit?.work_dir || name
  })()

  // What the dock's start pane says before anything is picked: where this
  // thread runs, how much window the last request carried, and how many files
  // it changed. The worktree is the real path the transcript's file links
  // resolve against; a thread without one runs in its project's directory.
  // The context caption names the agent when the sample is its own, so the
  // number is never mistaken for the entry model's.
  const dockEnv: DockEnv = {
    machine: railMachine,
    path: session?.worktree || threadDir,
    branch: session?.branch || '',
    contextTokens,
    contextWindow: contextWin,
    model: contextAgent || modelName,
    contextAgent,
    contextAgents,
    changes: diff
      ? {
          files: diff.changes.length,
          insertions: diff.stat?.insertions ?? 0,
          deletions: diff.stat?.deletions ?? 0,
        }
      : undefined,
  }

  // The panda closes a round once it is over: it dispatched, the employees
  // reported, and nothing has been said since. Derived from the turns already
  // on screen (see `trailingRunSummary`) — nothing new is written to the
  // transcript, so the model's own history is untouched.
  //
  // "Over" is the whole batch, not the first report: the finalizer files one
  // turn per task as each settles, so closing on the last turn alone would
  // print "本轮工作已全部完成" while a colleague was still running — and then
  // rewrite the roll-up every time another one landed. The task board is the
  // only place that knows the batch (see `useSessionTasks`).
  const legacySummary = busy || roundOpen ? null : trailingRunSummary(msgs)
  const runSummary = legacySummary?.refKind === 'plan' ? legacySummary : null
  // The same rows keyed for the transcript. A task bubble renders from here
  // instead of fetching its own copy on every change frame: a thread with
  // several dispatch turns was issuing that many extra requests per frame,
  // all competing with the event stream for the browser's six connections.
  const tasksById = new Map((sessionTasks ?? []).map((t) => [t.id, t]))
  const openOperation = session?.operation?.status === 'running' ? session.operation.id : msgs.find(m => m.streaming)?.operation_id
  const projection = projectTaskRounds(msgs, sessionTasks, openOperation)
  const selectedReport = reportSelection && projection.rounds.find(round => round.key === reportSelection.key && round.settled)
  const reportShown = dockShown && dockTab === 'report' && !!selectedReport
  // A round can settle from a task-list update before its report turn arrives.
  // Follow that layout change only if the reader was already at the bottom.
  const roundLayout = projection.rounds.map(round => `${round.key}:${round.settled}:${round.jobs.length}`).join('|')
  // The proposal bar takes height from the transcript when it appears, so a
  // reader parked at the bottom has to be moved down with it — otherwise the
  // last reply slides under the bar while they are looking at it.
  const proposalVersion = proposal?.version ?? 0
  useEffect(() => {
    if (!pinned.current) return
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: 'instant' })
  }, [msgs, roundLayout, dockShown, proposalVersion])
  const roundRows = (anchor: string | undefined) => projection.rounds.filter(round => round.anchor === anchor).map(round =>
    <SessionTaskRound key={`${activeId}:${round.key}`} round={round} reportSelection={reportShown ? reportSelection : undefined}
      onReport={openRoundReport} onOpenTask={openTaskInPlace} onPreview={openTaskPreview} />)
  const reportRefs = reportFallbackRefs(msgs)
  // The export page shows exactly what the download will contain. Built only
  // while that page is up, so a long thread does not serialize on every render.
  const exportText =
    dockTab === 'export'
      ? exportMarkdown(msgs, {
          project: session?.project ?? activeProject,
          title: session?.title,
        })
      : ''

  return (
    <FileViewContext.Provider value={fileOpener}>
    <ComposerContext.Provider value={composerInsert}>
    <section
      class={`chat${dockShown ? ' dock-open' : ''}`}
      style={dockShown ? `--dock-w:${dockW}px` : undefined}
    >
      <div class="chat-pane session-chat-pane">
        {/* The header is unconditional so the drawer toggle is reachable even
            on a brand-new, empty thread — that is exactly when a phone user
            needs to get back to the thread list. */}
        <header class="chat-head">
          <div class="chat-title-group">
            {/* The thread's directory mark. The header reads as one line —
                mark, title, tools — rather than a title floating in a bar. */}
            <Icon name="folder-closed" size={16} class="chat-head-icon" />
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
                <span class="chat-title-text">{headerTitle}</span>
                {session && <Icon name="pencil" class="chat-title-edit-icon" />}
              </h1>
            )}
          </div>
          {/* 分支名不挂在标题旁：同一份事实由右栏起始页的环境卡承担（机器 →
              路径 → 分支），标题这一行只留标题本身。
              头部右侧两件东西：恒展示的会话状态条（任务与设备、上下文占用、
              滚动的改动说明），以及最右的侧栏门。门放在最右，因为它开的就
              是右边缘那一栏，按钮贴着它要打开的东西。状态条整条是一个按钮，
              点开的是它自己的概览浮层（三段事实各带去处）；浮层只在点开时
              盖住右上角，点外面就收起，所以 log、输入框和跳转箭头不会长期
              被挡。超长的改动文字仍先在条内的小框里滚动。导出、节点和编排
              卡都搬进了栏的头部，所以这条 bar 不再重复它们。新对话页反过来：
              那一页没有栏，也没有线程可概览，唯一要答的问题是"在哪台机器上
              跑"，所以这里站的是节点按钮本身 —— 设备不再锁在栏的工具页后面，
              也没有一个点了会打开空栏的折叠按钮。 */}
          <div class="chat-head-tools">
            {showRail && (
              <SessionRail
                diff={diff}
                tasks={sessionTasks ?? []}
                nodes={nodes}
                contextTokens={contextTokens}
                contextWindow={contextWin}
                modelName={modelName}
                contextAgent={contextAgent}
                contextAgents={contextAgents}
                working={busy}
                machine={railMachine}
                onOpenDiff={openChanges}
                onOpenTask={openTaskInPlace}
                onOpenQueue={() => navigateView('control')}
              />
            )}
            {newChatPage ? (
              <NodePicker nodes={nodes} value={nodePref} onPick={setNodePref} />
            ) : (
              /* Open, the column carries the fold control in its own head, so
                 this one steps aside: two `panel-right` glyphs on the same row
                 were the same button twice. */
              !dockShown && (
                <button
                  type="button"
                  class="icon-btn tip-down"
                  data-tip={t('dock.expand')}
                  aria-label={t('dock.expand')}
                  aria-expanded={false}
                  onClick={() => setDockOpen(true)}
                >
                  <Icon name="panel-right" size={19} />
                </button>
              )
            )}
          </div>
        </header>

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
            if (!railFrame.current) {
              railFrame.current = requestAnimationFrame(() => {
                railFrame.current = 0
                syncPromptAt()
              })
            }
          }}
        >
          {msgs.length === 0 && loading && <ChatSkeleton />}
          {msgs.length === 0 && !loading && !busy && (
            <ChatEmpty
              onPick={useStarter}
              hasDraft={input.trim() !== '' || pending.length > 0}
            />
          )}
          {msgs.map((m, i) => (
            <div class="session-chat-turn" key={m.k}>
            {!projection.hidden.has(m.k) && (
              <ChatTurn
                msg={m}
                tasksById={tasksById}
                fallbackRef={reportRefs.get(m.k)}
                /* The local measurement wins while it is there (it is the only
                   one that exists between the reply landing and the transcript
                   being re-fetched); the panel's `duration_ms` is what survives
                   a reload. */
                elapsedMs={turnMs[i] ?? m.duration_ms}
                liveFrom={liveFrom}
                onOpenTask={openTaskInPlace}
                onPreviewTask={openTaskPreview}
              />
            )}
            {roundRows(m.k)}
            </div>
          ))}
          {roundRows(undefined)}
          {liveIds.filter(id => !projection.assigned.has(id)).map((id) => {
            const task = tasksById.get(id)
            return task ? <TaskProgress key={id} task={task} /> : null
          })}
          {runSummary && <RunSummaryRow key={`${session?.id}:${msgs.at(-1)?.k}`} run={runSummary} onOpenTask={openTaskInPlace} />}
        </div>

        {/* The prompt rail. A long thread scrolls past dozens of replies and
            the only way back to a question was to scroll and read; these ticks
            are that map. Absolute, not in the log's flow — the log is the
            scroller, and a marker that scrolls with the content would only
            ever be visible when the content it marks is.

            Pointing at the rail opens the map as a list: every question, the
            one you are reading in bold, each with the same tick the rail
            draws. The wrapper owns the hover so moving into the list does not
            close it — the panel is a sibling of the ticks, not a tooltip. */}
        {prompts.length > 1 && (
          <div
            class="chat-rail-wrap"
            onMouseEnter={() => setRailPreview(true)}
            onMouseLeave={() => setRailPreview(false)}
          >
            {railPreview && (
              <div class="chat-rail-preview">
                {prompts.map((p, i) => (
                  <button
                    key={p.k}
                    type="button"
                    class={`chat-rail-item${i === promptAt ? ' on' : ''}`}
                    aria-current={i === promptAt ? 'true' : undefined}
                    onClick={() => {
                      setRailPreview(false)
                      jumpToPrompt(i)
                    }}
                  >
                    <span class="chat-rail-item-tick" aria-hidden="true" />
                    <span class="chat-rail-item-text">{p.label}</span>
                  </button>
                ))}
              </div>
            )}
            <nav class="chat-rail" aria-label={t('sessions.promptRail')}>
              {prompts.map((p, i) => (
                <button
                  key={p.k}
                  type="button"
                  class={`chat-rail-tick${i === promptAt ? ' on' : ''}`}
                  aria-label={p.label}
                  aria-current={i === promptAt ? 'true' : undefined}
                  onClick={() => jumpToPrompt(i)}
                />
              ))}
            </nav>
          </div>
        )}

        {/* Jump to the newest message. The slot is zero-height: the control
            floats over the last line of the transcript instead of taking a row
            between the log and the composer, which used to shorten the log and
            clip the line it cut off. The label lives in the tooltip and the
            accessible name — a pill that spells out "back to the bottom" is a
            sentence where an arrow is enough.

            The inner column mirrors the reading column's box (same lane on the
            right, same 900px cap) so the arrow rides the middle of the text
            even when the node card has pushed that column left. */}
        {!isPinned && (
          <div class="scroll-bottom-slot">
            <div class="scroll-bottom-inner">
              <button
                type="button"
                class="scroll-bottom-btn"
                aria-label={t('sessions.jumpBottom')}
                data-tip={t('sessions.jumpBottom')}
                onClick={() => {
                  pinned.current = true
                  setIsPinned(true)
                  scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: 'smooth' })
                }}
              >
                <Icon name="arrow-down" size={16} />
              </button>
            </div>
          </div>
        )}

        {/* Notes typed into the running turn, worn by the pet instead of
            listed above it. They live here rather than in the transcript
            because they have not been delivered yet: the agent has not read
            them, so the user can still send one ahead of the round, edit it,
            or drop it.

            The strip is a zero-height row: what the eye gets is a trail of
            small round bubbles above the panda's head — the pet thinking — and
            the queue itself is behind a hover, so a waiting note no longer
            costs a shelf between the transcript and the composer. One bubble
            per note, four at most, and none of them numbered: a bubble is a
            thought, not a badge, and the list behind the hover is where the
            order and the count live. */}
        <QueuedNotes
          notes={notes}
          notesOpen={notesOpen}
          setNotesOpen={setNotesOpen}
          noteBubbles={noteBubbles}
          noteList={noteList}
          noteRoom={noteRoom}
          noteEdges={noteEdges}
          editingNote={editingNote}
          noteDraft={noteDraft}
          setNoteDraft={setNoteDraft}
          saveEditNote={saveEditNote}
          cancelEditNote={cancelEditNote}
          beginEditNote={beginEditNote}
          promoteNote={promoteNote}
          dropNote={dropNote}
        />

        {/* A finished plan waits on the lane above the box, not inside it:
            the proposal is the reply's product, so it belongs between the
            transcript and the composer — the same shelf the clarify card
            takes — and the box stays one control with nothing bolted under
            its send row. The full text and the confirmation stay in the
            dialog the bar opens. */}
        {activeId && proposal && (
          <PlanProposalDock
            version={proposal.version}
            text={proposal.text}
            onReview={() => setPlanReview({ sid: activeId, version: proposal.version })}
          />
        )}

        {clarify && clarify.sessionID === activeId && (
          <div class="clarify-dock">
            <ClarifyCard
              spec={clarify.spec}
              onSubmit={(answers) => void submitClarify(answers)}
              onCancel={cancelClarify}
            />
          </div>
        )}

        {/* Textarea and actions share one bordered box so the composer reads
            as a single control rather than a field with a toolbar loose
            underneath it — the focus ring belongs to the whole thing. */}
        <form class="composer" onSubmit={send}>
          <div
            class={`composer-box${busy ? ' composer-running' : ''}${dropping ? ' composer-dropping' : ''}`}
            onDragOver={(e) => {
              // Only a file drag is ours; a text selection dragged over the box
              // must keep its own default (the browser inserts it on drop).
              if (!dragHasFiles(e)) return
              e.preventDefault()
              setDropping(true)
            }}
            onDragLeave={(e) => {
              // Leaving for a child fires dragleave on the parent too, so only
              // clear the state when the pointer really left the box.
              if (e.currentTarget.contains(e.relatedTarget as Node | null)) return
              setDropping(false)
            }}
            onDrop={(e) => {
              if (!dragHasFiles(e)) return
              e.preventDefault()
              setDropping(false)
              addFiles([...(e.dataTransfer?.files ?? [])])
            }}
          >
            {dropping && <div class="attach-drop-hint">{t('sessions.attachDrop')}</div>}
            <ComposerInput
              composer={composer}
              input={input}
              pendingCount={pending.length}
              placeholder={
                busy && goalActive(shownGoal)
                  ? ''
                  : t(armed === 'goal' ? 'sessions.goal.hint'
                    : armed === 'plan' ? 'sessions.plan.hint'
                    : busy ? 'sessions.placeholderRunning'
                    : 'sessions.placeholder')
              }
              completeShown={completeShown}
              completeIdx={completeIdx}
              setInput={setInput}
              setCaret={setCaret}
              setCompleteCursor={setCompleteCursor}
              setCompleteDismissed={setCompleteDismissed}
              acceptCompletion={acceptCompletion}
              syncPending={syncPending}
              removePending={removePending}
              zoomPill={zoomPill}
              peekPill={peekPill}
              unpeek={unpeek}
              addFiles={addFiles}
              send={send}
            />
            <div class="composer-actions">
              {/* Context picks share the row with Send: the box reads as one
                  control, and a toolbar floating above it read as a second
                  one competing with the input. */}
              {/* The working directory used to ride this row as a pill and the
                  node picker beside it. The pick moved to the header; what is
                  left is one + that adds context rather than a row of
                  controls. */}
              <AttachMenu
                onInsert={insertMentions}
                onAttach={addFiles}
                project={session?.project || activeProject || ''}
              />
              <PermissionPicker value={permission} onChange={pickPermission} />
              {(armed === 'goal' || goalEditor?.sid === activeId) && <button type="button" class="composer-goal-trigger" title={t('sessions.goal.hint')} onClick={() => { setGoalEditor(null); setArmed(null); if (goalDraft !== null) removeModeCommand() }}><Icon name="target" size={16} />{t('sessions.goal.label')}</button>}
              {(armed === 'plan' || controlState?.planning) && <button type="button" class="composer-goal-trigger composer-plan-trigger" title={t(armed === 'plan' && !controlState?.planning ? 'sessions.plan.hint' : 'sessions.plan.exit')} onClick={() => { if (controlState?.planning) void exitPlanning(); setArmed(null); if (planArgument(input) !== null) removeModeCommand() }}><Icon name="lightbulb" size={16} />{t('sessions.plan.label')}</button>}
              {/* The model switch, immediately left of Send — the row's right
                  edge is where you reach for before sending, and "which model"
                  is the one more thing that decision wants asked. WorkBuddy's
                  composer puts its model selector in the same slot. */}
              <ModelSelect disabled={busy} />
              {/* Dictation sits between the two: it feeds the same box Send is
                  about to take, so it belongs to the sending half of the row,
                  and putting it on the far side of the model name would break
                  the pair that reads as "how this turn will be answered". */}
              <VoiceInput onText={insertDictation} />
              {busy ? (
                // While a reply streams, the primary action is stopping it —
                // a disabled "Thinking…" button leaves no way out of a bad ask.
                <button class="btn stop-btn" type="button" onClick={stop}>
                  <span class="stop-icon" aria-hidden="true" />
                  {t('sessions.stop')}
                </button>
              ) : (
                <button class="btn primary" type="submit" disabled={!input.trim() && pending.length === 0}>
                  {t('sessions.send')}
                </button>
              )}
            </div>
            {goalEditor?.sid === activeId && <section class="composer-proposal">
              <textarea aria-label={t('sessions.goal.edit')} value={goalEditor.text} onInput={e => setGoalEditor({ ...goalEditor, text: e.currentTarget.value })} />
              <button type="button" disabled={!goalEditor.text.trim()} onClick={async () => {
                try {
                  await api.controlConversation(goalEditor.sid, { action: 'edit', goal_id: goalEditor.id, objective: goalEditor.text })
                  setGoalEditor((current) => current === goalEditor ? null : current)
                  void refreshControl(goalEditor.sid).catch(() => {})
                } catch (error) { toastError(error) }
              }}>{t('common.save')}</button>
              <button type="button" onClick={() => setGoalEditor(null)}>{t('common.cancel')}</button>
            </section>}
            {legacyGoal && <section class="composer-proposal"><span>{t('sessions.goal.legacy')}</span><button type="button" onClick={async () => {
              if (!activeId) return
              try { await api.controlConversation(activeId, { action: 'create', objective: legacyGoal.text }); const cached = JSON.parse(sessionStorage.getItem('openpanda.composerGoals') || '{}'); delete cached[activeId]; sessionStorage.setItem('openpanda.composerGoals', JSON.stringify(cached)); await refreshControl(activeId) } catch (error) { toastError(error) }
            }}>{t('sessions.goal.restore')}</button></section>}
            <div class="composer-head">
              <ComposerPet
                working={busy}
                team={team}
                onTeam={pickTeam}
                onManage={() => navigateView('settings:agents')}
              />
              {shownGoal && (
                <GoalBar goal={shownGoal} pending={pendingGoalAction === shownGoal.id}
                   onStart={() => {
                     if (activeId && goalNeedsMoreBudget(shownGoal)) setBudgetGoal({ sid: activeId, id: shownGoal.id })
                     else void changeGoal('resume')
                   }}
                  onPause={() => void changeGoal('pause')}
                  onCancel={() => void changeGoal('clear')}
                  onStop={() => void changeGoal('stop')}
                  onEdit={openGoalEditor}
                />
              )}
            </div>
          </div>
          {/* Below the box, on one line: where the thread will run, then the
              keyboard legend. The roster used to sit here too; it now hangs
              off the pet (composer-pet.tsx), so this row is footer furniture
              on every surface — a thread that exists shows its directory
              read-only, which keeps the box above it on the same line as
              新对话 and 工作空间 instead of letting it drop a row height when
              the reader opens history. A thread with a directory but no legend
              keeps the row; a thread with neither has no footer at all. */}
          {newChatPage ? (
            <div class="composer-foot">
              <DirButton
                value={pickedDir}
                onChange={(path) => {
                  setPickedDir(path)
                  setDirTouched(true)
                }}
                label={t('sessions.chooseDir')}
                disabled={busy}
              />
              <p class="composer-shortcut-hint">{t('sessions.sendShortcut')}</p>
            </div>
          ) : (
            <div class="composer-foot">
              {/* Display, not a control: a thread's directory is fixed by its
                  project row, so the pick is disabled and shows where this
                  conversation runs — the same slot 新对话 uses to choose. */}
              <DirButton
                value={threadDir}
                onChange={() => {}}
                label={t('sessions.threadDir')}
                disabled
              />
              <p class="composer-shortcut-hint">{t('sessions.sendShortcut')}</p>
            </div>
          )}
        </form>
        {proposal && planReview && planReview.sid === activeId && planReview.version === proposal.version && (
          <PlanProposalDialog
            {...proposal}
            busy={busy}
            onClose={() => setPlanReview(null)}
            onConfirm={() => confirmProposal(planReview)}
          />
        )}
        {budgetGoal && budgetGoal.sid === activeId && shownGoal?.id === budgetGoal.id && goalNeedsMoreBudget(shownGoal) && (
          <GoalBudgetDialog
            used={shownGoal.tokens ?? 0}
            limit={shownGoal.tokenBudget ?? 0}
            pending={pendingGoalAction === shownGoal.id}
            onClose={() => setBudgetGoal((current) => current === budgetGoal ? null : current)}
            onResume={(value) => changeGoal('resume', undefined, value)}
          />
        )}
        {/* The pill's floating copy and its lightbox. Fixed-position and
            outside the box, so neither the composer's own edge nor the
            transcript's scroller can clip them. */}
        {peek && <HoverPreview src={peek.src} name={peek.name} anchor={peek.anchor} />}
        {shot && (
          <Lightbox
            src={shot.src}
            path={shot.path}
            label={shot.name}
            onClose={() => setShot(null)}
          />
        )}
        {loadError && <p class="gate-error composer-error">{loadError}</p>}
      </div>


      {/* The side dock: a sibling of the pane, not a layer over it. It is the
          second column of `.chat`'s grid, so opening it narrows the reading
          pane the way a flex split does — the transcript, the composer and the
          jump arrow reflow to the width they actually have, instead of being
          padded away from a panel that floats on top of them.

          The panels stay mounted once visited, so switching tabs returns to
          the same file, the same page, the same side conversation. */}
      <Dock
        open={dockShown}
        tab={dockTab}
        width={dockW}
        env={dockEnv}
        badges={{ changes: diff?.changes.length ?? 0 }}
        tools={
          <>
            {selectedReport && <button type="button" class={`icon-btn tip-down${reportShown ? ' open' : ''}`}
              aria-label={t('sessions.round.report')} data-tip={t('sessions.round.report')} aria-pressed={reportShown}
              onClick={() => reportShown ? closeRoundReport() : (setDockTab('report'), setDockOpen(true))}>
              <Icon name="file-text" size={17} />
            </button>}
            {!newChatPage && session && msgs.length > 0 && (
              <button
                type="button"
                class={`icon-btn tip-down${dockTab === 'export' ? ' open' : ''}`}
                onClick={() => pickTool('export')}
                /* The page is already saying what the tool is; a tooltip over
                   its first line would be the floating layer we removed. */
                data-tip={dockTab === 'export' ? undefined : t('sessions.export')}
                aria-label={t('sessions.export')}
                aria-pressed={dockTab === 'export'}
              >
                <Icon name="download" size={18} />
              </button>
            )}
            <button
              type="button"
              class={`icon-btn tip-down${dockTab === 'node' ? ' open' : ''}`}
              onClick={() => pickTool('node')}
              data-tip={dockTab === 'node' ? undefined : t('sessions.nodeBtn')}
              aria-label={t('sessions.nodePick')}
              aria-pressed={dockTab === 'node'}
            >
              <Icon name="git-fork" size={18} />
            </button>
            {!newChatPage && (
              <button
                type="button"
                class={`icon-btn tip-down${dockTab === 'orch' ? ' open' : ''}`}
                onClick={() => pickTool('orch')}
                data-tip={dockTab === 'orch' ? undefined : t('sessions.orchestrate')}
                aria-label={t('sessions.orchestrate')}
                aria-pressed={dockTab === 'orch'}
              >
                <Icon name="route" size={18} />
              </button>
            )}
          </>
        }
        onWidth={setDockW}
        /* Back to the overview. Every pane keeps its own state while the start
           pane is showing, so this is a step aside, not a reset: returning to
           files lands on the same file, and the side conversation keeps its
           place. */
        onHome={() => setDockTab('')}
        onTab={(next) => {
          setDockTab(next)
          setDockOpen(true)
          // Coming back to 对话 re-reads this thread's history. Clicking the
          // tab is never a new conversation — only `+` is.
          if (next === 'side') setSideReload((n) => n + 1)
          // The change list is a snapshot of the worktree, and the worktree
          // moves while the reader is elsewhere. Every visit re-reads it.
          if (next === 'changes') void refreshDiff(activeId)
        }}
        onToggle={() => reportShown ? closeRoundReport() : setDockOpen(v => !v)}
        onReselect={(key) => {
          // Only the side conversation has a second-level surface to open;
          // every other tab keeps the fold-it-away behaviour.
          if (key === 'side') setSideHistory((n) => n + 1)
          else setDockOpen(false)
        }}
        onNew={() => {
          // `+` is 新对话 in every pane now — including files and the browser.
          // It lands on the side conversation; the ones already there stay in
          // that pane's history list, one click away, so `+` never throws the
          // last one away. The two panels keep their own start-over controls
          // (workspace root / close page) in their own bars. Switching the tab
          // here is also the visible answer to the click: from files or a page
          // the reader sees the conversation they just started, not a panel
          // that quietly reset itself.
          const sideMounted = dockSeen.includes('side')
          setSideFresh(!sideMounted)
          setDockTab('side')
          setDockOpen(true)
          // An already-mounted pane sees the epoch change; a first-time pane
          // takes the mount flag (it did not exist when the epoch moved).
          if (sideMounted) setSideEpoch((n) => n + 1)
        }}
      >
        {reportShown && selectedReport && reportSelection && (
          <div class="dock-pane">
            <RoundReportPane key={`${activeId}:${selectedReport.key}`} round={selectedReport} selection={reportSelection}
              onOpenTask={openTaskInPlace} onPreview={openTaskPreview} onClose={closeRoundReport} />
          </div>
        )}
        {/* The three tool pages. They are panes like files/browser/side, not
            layers: a tool button swaps the body, so nothing floats over the
            "start from here" rows underneath. */}
        {dockSeen.includes('export') && (
          <div class="dock-pane dock-tool-pane" hidden={dockTab !== 'export'}>
            <div class="dock-export">
              <div class="dock-export-head">
                <p class="dock-export-title">{t('sessions.export')}</p>
                <span class="dock-export-name mono">{exportFilename(session?.title)}</span>
              </div>
              <pre class="dock-export-preview">{exportText}</pre>
              <div class="dock-export-foot">
                <button type="button" class="btn" onClick={exportSession}>
                  <Icon name="download" size={14} />
                  {t('sessions.export')}
                </button>
              </div>
            </div>
          </div>
        )}
        {dockSeen.includes('node') && (
          <div class="dock-pane dock-tool-pane" hidden={dockTab !== 'node'}>
            <NodePicker
              key={`node-${session?.id ?? 'none'}`}
              inline
              nodes={nodes}
              value={nodePref}
              onPick={setNodePref}
              open
              onOpenChange={(open) => {
                if (!open) setDockTab('')
              }}
            />
          </div>
        )}
        {dockSeen.includes('orch') && (
          <div class="dock-pane dock-tool-pane" hidden={dockTab !== 'orch'}>
            <OrchestrateCard
              key={`orch-${session?.id ?? 'none'}`}
              inline
              sessionId={session?.id ?? null}
              open
              /* The picked task's card is portalled out of this pane, so it
                 cannot inherit the `hidden` on a pane that is not showing: the
                 column being folded and the tab being switched away both have
                 to put the card away with it. */
              shown={dockShown && dockTab === 'orch'}
              onOpenChange={(open) => {
                if (!open) setDockTab('')
              }}
              onlineNodeCount={onlineNodeCount}
              selfNodeId={selfNodeId}
              focusTask={focusTask}
            />
          </div>
        )}
        {dockSeen.includes('changes') && (
          <div class="dock-pane" hidden={dockTab !== 'changes'}>
            <DockChanges
              session={session?.id ?? null}
              diff={diff}
              error={diffError}
              merging={merging}
              reverting={reverting}
              onRefresh={() => refreshDiff(activeId)}
              onMerge={mergeChanges}
              onRevert={revertMerge}
              /* The file itself, not another diff: the panel hands the path to
                 the files pane and lands on the first changed line. */
              onOpenFile={(path, line) => {
                setDockTab('files')
                setFileRef({ path, line })
              }}
            />
          </div>
        )}
        {dockSeen.includes('files') && (
          <div class="dock-pane" hidden={dockTab !== 'files'}>
            <DockFiles
              session={session?.id ?? null}
              request={fileRef}
              resetSignal={dockEpoch}
              onInsert={addFileRef}
            />
          </div>
        )}
        {dockSeen.includes('browser') && (
          <div class="dock-pane" hidden={dockTab !== 'browser'}>
            <DockBrowser
              resetSignal={dockEpoch}
              artifact={pageAsk}
              direct={directAsk}
              project={session?.project ?? ''}
              onInsert={composerInsert}
            />
          </div>
        )}
        {dockSeen.includes('side') && (
          <div class="dock-pane" hidden={dockTab !== 'side'}>
            <SideChat
              sessionId={session?.id ?? null}
              resetSignal={sideEpoch}
              reloadSignal={sideReload}
              historySignal={sideHistory}
              freshStart={sideFresh}
              onInsert={composerInsert}
              onOpenTask={openTaskInPlace}
            />
          </div>
        )}
      </Dock>
    </section>
    </ComposerContext.Provider>
    </FileViewContext.Provider>
  )
}

