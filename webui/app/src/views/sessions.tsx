// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  ApiError,
  askSessionStream,
  isAbort,
  isTaskStalled,
  type ApprovalScope,
  type AskResult,
  type AskStatusEvent,
  type FsFileEntry,
  type MCPSettings,
  type NodeInfo,
  type PlanDetail,
  type ProjectDetail,
  type Session,
  type SessionDiff,
  type SessionNote,
  type SessionTurn,
  type SkillEntry,
  type Task,
} from '../api/client'
import { Icon } from '../components/icons'
import { DiffViewer } from '../components/diff-viewer'
import {
  useAsync,
  useChangeSignal,
  useLocaleRerender,
  useTaskEvents,
  useVisibleInterval,
  busSubscribeChange,
} from '../hooks'
import { t, locale } from '../i18n'
import { isNodeConnected } from '../format/node-status'
import { clockStamp, clockTime } from '../format/time'
import { navigate, navigateView } from '../nav'
import { Markdown } from '../md/render'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'
import { MessageCopyButton } from '../components/copy'
import { buildCommands, type Command } from '../components/palette'
import { rank } from '../components/fuzzy'
import { mergeTailTurn, patchStreaming, slashQuery } from '../components/chatstate'
import { atQuery, expandFileRefs, exportMarkdown, exportFilename, isImageFile, withAttachments } from '../components/attach'
import { isLiveSession } from '../components/session-guard'
import { sortSessions, turnsChanged } from '../components/session-list'
import { splitReportHead } from '../components/report-head'
import { liveTaskIds } from '../components/task-activity'
import { groupDispatches } from './chat-batch.ts'
import {
  prettyAgent,
  roundStillOpen,
  roundTally,
  sharedTitlePrefix,
  speakerOf,
  stateTone,
  stripRefId,
  trailingRunSummary,
  type RunSummary,
} from '../components/chat-speaker'
import {
  ChatAvatar,
  PlanCast,
  TaskCast,
  TaskProgress,
  usePlanBrief,
  useTaskBrief,
} from '../components/chat-cast'
import { ScopeSelect } from '../components/scope-select'
import {
  PermissionPicker,
  authorizeRelevant,
  normalizePermission,
  type PermissionMode,
} from '../components/permission-picker'
import { NodePicker } from '../components/node-picker'
import { ComposerPet } from '../components/composer-pet'
import { OrchestrateCard } from '../components/orchestrate'
import { DirButton } from '../components/dir-picker'
import { ModelSelect } from '../components/model-select'
import { VoiceInput } from '../components/voice-input'
import { suggestProjectName } from '../components/dir-utils'
import { EventTimeline } from '../components/event-timeline'
import { acquireKeepAlive } from '../utils/keepalive'

/** Grow the composer with its content up to a ceiling, then scroll inside.
 *  A fixed two-row box makes pasting a paragraph feel like typing into a
 *  keyhole; unbounded growth would eat the transcript instead. */
const COMPOSER_MAX_PX = 240

/** How long the pane keeps watching a turn it lost the live stream for.
 *  Comfortably past a slow classify plus a long agent run, and finite: a
 *  watch with no end is a spinner the user cannot dismiss. */
const RECOVER_WATCH_MS = 10 * 60 * 1000

/** The composer's editable surface is a contenteditable, not a textarea: an
 *  attachment has to render as a pill *inside* the line the user is typing,
 *  and only a rich surface can host one. Everything the textarea used to give
 *  us for free — the placeholder, the newline character, the caret offset —
 *  is re-derived here. */

/** Chromium's default paragraph separator is a div, which would turn every
 *  Enter into a nested block and double the newlines when read back. Ask for
 *  <br> once per document instead. */
let paragraphSeparatorSet = false
export function useBrParagraphSeparator(): void {
  if (paragraphSeparatorSet || typeof document === 'undefined') return
  try {
    document.execCommand('defaultParagraphSeparator', false, 'br')
    paragraphSeparatorSet = true
  } catch {
    // A browser that refuses it still works; newlines just come back as blocks.
  }
}

export function autoGrow(el: HTMLElement): void {
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, COMPOSER_MAX_PX)}px`
}

/** The editor's text as the model should see it: text nodes verbatim, <br> as
 *  a newline, blocks as a newline before their contents. Attachment pills are
 *  skipped — they travel as the attachment block appended to the prompt, so
 *  listing their names here would put them in twice. */
function editorText(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return (node.textContent ?? '').replace(/\u200b/g, '')
  if (node.nodeType !== Node.ELEMENT_NODE) return ''
  const el = node as HTMLElement
  if (el.classList.contains('attach-pill')) return ''
  if (el.tagName === 'BR') return '\n'
  let inner = ''
  for (const child of el.childNodes) inner += editorText(child)
  if (el.tagName === 'DIV' || el.tagName === 'P') return `\n${inner}`
  return inner
}

/** The text as the user sees it typed, with nothing but a newline counting as
 *  empty — deleting everything leaves a <br> behind, and the placeholder has to
 *  show through that. */
export function editorPlain(text: string): string {
  return text.trim()
}

/** The caret's offset in the plain text, so @file completion can slice the
 *  editor's value the same way it sliced the textarea's. The offset has to
 *  count exactly what editorText counts — Range.toString sees only text
 *  nodes, so it under-reads by the <br> and block newlines editorText
 *  synthesizes and over-reads by the pill names it skips. Both adjustments
 *  land here so caret and `input` stay in the same space. */
export function editorCaretOffset(el: HTMLElement): number {
  const sel = el.ownerDocument.getSelection()
  if (!sel || sel.rangeCount === 0) return editorTextOf(el).length
  const range = sel.getRangeAt(0)
  const head = el.ownerDocument.createRange()
  head.selectNodeContents(el)
  head.setEnd(range.startContainer, range.startOffset)
  let len = head.toString().length
  for (const pill of el.querySelectorAll('.attach-pill')) {
    if (head.intersectsNode(pill)) len -= (pill.textContent ?? '').length
  }
  for (const n of el.querySelectorAll('br, div, p')) {
    if (head.intersectsNode(n)) len += 1
  }
  return len
}

export function editorTextOf(el: HTMLElement): string {
  let out = ''
  for (const child of el.childNodes) out += editorText(child)
  return out
}

/** Character stream as `input` counts it: text nodes at their length, <br>
 *  and block elements as the one '\n' editorText gives them, nothing for an
 *  attachment pill (REJECT keeps its name out too). Positions along this
 *  walk and offsets into editorTextOf are the same space, which is what
 *  atQuery's [start, caret) addresses. */
function editorTextWalker(el: HTMLElement): TreeWalker {
  return el.ownerDocument.createTreeWalker(el, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
    acceptNode: (n) => {
      if (n.nodeType === Node.ELEMENT_NODE) {
        const e = n as HTMLElement
        if (e.classList.contains('attach-pill')) return NodeFilter.FILTER_REJECT
        return e.tagName === 'BR' || e.tagName === 'DIV' || e.tagName === 'P'
          ? NodeFilter.FILTER_ACCEPT
          : NodeFilter.FILTER_SKIP
      }
      return NodeFilter.FILTER_ACCEPT
    },
  })
}

/** Plain text into the editor at the caret, without the HTML a paste would
 *  bring along. execCommand keeps the browser's own undo stack working, which
 *  a hand-built Range insertion does not. */
function editorInsertText(text: string): void {
  document.execCommand('insertText', false, text)
}

/** Is the document's selection inside `el`? Every caret read assumes it is:
 *  `editorCaretOffset` builds a Range anchored in the editor, and a selection
 *  parked elsewhere (a button that took the focus, a panel that opened) makes
 *  that Range throw instead of answering. */
function caretInside(el: HTMLElement): boolean {
  const sel = el.ownerDocument.getSelection()
  if (!sel || sel.rangeCount === 0) return false
  return el.contains(sel.getRangeAt(0).startContainer)
}

/** Park the caret at the end of the editor — where a re-focused box lands when
 *  there is no remembered selection to restore. */
function caretToEnd(el: HTMLElement): void {
  const rg = el.ownerDocument.createRange()
  rg.selectNodeContents(el)
  rg.collapse(false)
  const sel = el.ownerDocument.getSelection()
  if (!sel) return
  sel.removeAllRanges()
  sel.addRange(rg)
}

/** Drop plain text in at the caret of a contenteditable composer. Shared by
 *  both composers (the thread's and the map's), because a dictated phrase has
 *  to land the same way in either: the box may have lost its selection to the
 *  button that started the dictation, so the caret is re-established first,
 *  and a space is added when it sits at the end of a word — two phrases in a
 *  row must not arrive welded together. */
export function insertTextAtCaret(el: HTMLElement, text: string): void {
  el.focus()
  if (!caretInside(el)) caretToEnd(el)
  const plain = editorTextOf(el)
  const at = editorCaretOffset(el)
  const needsSpace = at > 0 && !/\s/.test(plain.charAt(at - 1))
  editorInsertText(needsSpace ? ` ${text}` : text)
}

/** A pill's markup. It has to be a raw string rather than the <Icon>
 *  component because the pill is inserted as a DOM node the framework never
 *  sees — React reconciling children inside a contenteditable would fight the
 *  text nodes the user's typing creates there. The two glyphs mirror
 *  components/icons.tsx on purpose.
 *
 *  The image glyph keeps the outline frame of the Lucide original but swaps
 *  its diagonal mountain stroke for a solid silhouette. At 13px the two
 *  strokes of the original sat a pixel apart and fused into a box with a slash
 *  through it — the universal "missing image" symbol, the opposite of what an
 *  attachment pill says. A filled landscape survives the downscale, and the
 *  frame is what separates it from a person glyph (a dot above a triangle on
 *  its own reads as a head over shoulders). 16px, not 13: the silhouette needs
 *  the extra pixels to show two peaks rather than one lump. */
function chipMarkup(image: boolean): string {
  if (image) {
    return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="none">'
      + '<rect x="3" y="3" width="18" height="18" rx="4.2" stroke="currentColor" stroke-width="1.7"/>'
      + '<path fill="currentColor" d="M4.2 20 8.4 12.2 11.6 16.4 15.2 12.8 19.8 20z"/>'
      + '<circle cx="7.9" cy="7.9" r="1.75" fill="currentColor"/></svg>'
  }
  return '<svg width="13" height="13" viewBox="0 0 24 24" fill="none"'
    + ' stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">'
    + '<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/>'
    + '</svg>'
}

/** A pill for a pending attachment, inserted at the caret. A pill is atomic:
 *  the caret steps over it as one unit, and clicking it removes the file from
 *  the turn — the same contract WorkBuddy's chip has. */
/** One file waiting in the composer's attachment pills.
 *
 *  The File is kept rather than uploaded on arrival — the pill needs no
 *  server round-trip to show its name, and a brand-new chat has no session to
 *  upload into yet. The bytes go up at send. */
interface PendingAttachment {
  key: string
  name: string
  size: number
  image: boolean
  file: File
}

function makeChipNode(p: PendingAttachment): HTMLElement {
  const chip = document.createElement('span')
  chip.className = 'attach-pill'
  chip.setAttribute('data-pk', p.key)
  chip.setAttribute('contenteditable', 'false')
  chip.setAttribute('title', p.name)
  chip.textContent = p.name
  const ico = document.createElement('span')
  ico.className = p.image ? 'attach-pill-ico img' : 'attach-pill-ico'
  ico.setAttribute('aria-hidden', 'true')
  ico.innerHTML = chipMarkup(p.image)
  chip.insertBefore(ico, chip.firstChild)
  return chip
}

/** Keep the pill from welding to the text after it. */
export function range_or_append(after: Node, node: Node): void {
  if (after.parentNode) after.parentNode.insertBefore(node, after.nextSibling)
}

export function editorClear(el: HTMLElement | null): void {
  if (!el) return
  el.textContent = ''
  // 'auto' under a min-height floor resolves to that floor, but the inline
  // height from the last autoGrow call wins over it, so put the number back
  // explicitly or the box stays the height of the turn just sent.
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
  /** The task row this turn dispatched, adopted the moment the ask stream
   *  names it. It drives the live progress row while the turn is still
   *  waiting for the task to settle; `kind`/`ref` are only set once the
   *  result lands, which is far too late to show progress. */
  liveTask?: string
}

/** One file waiting in the composer's attachment tray.
 *
 *  The File is kept rather than uploaded on arrival: the preview then needs no
 *  server round-trip, and a brand-new chat has no session to upload into yet.
 *  The upload happens at send, when the thread it belongs to exists. */
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

// statusNote phrases one ask progress event for the composer status line.
// A retry milestone carries the numbers needed to say it in the browser's
// locale ("retrying in 3s · 2/6") instead of the server's English fallback;
// every other kind keeps the wire text.
function statusNote(text: string, ev?: AskStatusEvent): string {
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
    // An outage is not a provider failure: say the link is being watched and
    // that the turn picks itself back up, then report what the wait cost.
    const waited = Math.max(0, Math.round((ev.waited_ms ?? 0) / 1000))
    return ev.detail === 'resumed'
      ? t('sessions.offline.resumed', { delay: waited })
      : t('sessions.offline.waiting')
  }
  if (ev?.kind === 'spawn' && ev.name) {
    // The fan-out milestone in the browser's own language: the mode
    // descriptor says how the child relates to the shared tree — a private
    // copy that merges back, or the lanes it claims.
    if (ev.detail === 'isolated') return t('sessions.spawn.isolated', { name: ev.name })
    if (ev.detail) return t('sessions.spawn.scoped', { name: ev.name, paths: ev.detail })
    return t('sessions.spawn', { name: ev.name })
  }
  return text
}

// ErrHints is the troubleshooting fold under a classified model error: the
// kind names its bullet list in i18n; the provider's own detail heads it when
// one arrived. A browser-native <details> needs no state and reopens across
// rerenders for free.
function ErrHints({ kind, detail }: { kind: string; detail?: string }) {
  const raw = t(`err.hints.${kind}`)
  const known = raw !== `err.hints.${kind}`
  const lines = (known ? raw : t('err.hints.generic')).split('\n').filter(Boolean)
  return (
    <details class="err-hints">
      <summary class="dim">{t('err.hints.title')}</summary>
      {detail && <p class="dim err-hint-detail">{t('err.hints.detail', { detail })}</p>}
      <ul class="err-hint-list">
        {lines.map((line, i) => (
          <li key={i}>{line.replace(/^-\s*/, '')}</li>
        ))}
      </ul>
    </details>
  )
}

function fmtSize(n?: number): string {
  if (n === undefined) return ''
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)}M`
  if (n >= 1024) return `${(n / 1024).toFixed(1)}k`
  return `${n}B`
}

/** Wall-clock for the foot of a finished reply. Sub-minute waits keep one
 *  decimal — 2.4s and 2.9s are different answers to "how long did that take" —
 *  and past that it walks the same ladder the rest of the console uses. */
/** A turn's wall clock, in the two-unit form the reply header shows it:
 *  "41.2秒", "2分钟 41秒", "1小时 4分钟". Seconds stay next to minutes because
 *  a turn is a short thing — "2分钟" alone hides the half of it you actually
 *  sat through. Rounds once, up front, so 119.7s cannot print "1分钟 60秒". */
function fmtDuration(ms: number): string {
  const total = Math.max(0, ms) / 1000
  if (total < 60) return `${total.toFixed(1)}${t('time.unit.s')}`
  const whole = Math.round(total)
  if (whole < 3600) {
    return `${Math.floor(whole / 60)}${t('time.unit.m')} ${whole % 60}${t('time.unit.s')}`
  }
  return `${Math.floor(whole / 3600)}${t('time.unit.h')} ${Math.floor((whole % 3600) / 60)}${t('time.unit.m')}`
}

/** The reply's timer while the turn is still running: one tick a second, in the
 *  row the finished duration will occupy.
 *
 *  Its own component rather than a number computed by the parent, so the
 *  re-render each second touches this bubble only — a long thread holds
 *  hundreds of messages, and re-rendering all of them to advance one number is
 *  felt while scrolling.
 *
 *  It formats through `fmtDuration`, the same function the finished line uses,
 *  so the number does not change shape the moment the turn lands. */
function LiveElapsed({ from }: { from: number }) {
  const [ms, setMs] = useState(() => Date.now() - from)
  useEffect(() => {
    setMs(Date.now() - from)
    const id = setInterval(() => setMs(Date.now() - from), 1000)
    return () => clearInterval(id)
  }, [from])
  return <span class="msg-dur">{t('sessions.elapsed', { d: fmtDuration(ms) })}</span>
}

/** One line for a prompt-rail tick's tooltip: a question can be a paragraph,
 *  and a native tooltip that runs off the screen is worse than a truncated
 *  one. Whitespace is collapsed first — a pasted block would otherwise open
 *  the tooltip on an empty first line. */
function promptLabel(text: string): string {
  const line = text.replace(/\s+/g, ' ').trim()
  return line.length > 60 ? `${line.slice(0, 60)}…` : line
}

/** Bind a working directory to a project name.
 *
 *  A session cannot carry a directory of its own: the panel derives the work
 *  dir from the thread's project row and falls back to the node's work path
 *  (see the ask handler), so a directory picked for a new chat has to become a
 *  project before it can mean anything. An existing project pointing there is
 *  reused — that is the directory's own group in the rail — and a new
 *  directory gets one named after the folder, so the thread files under it
 *  exactly like every other thread does. */
async function projectForDir(dir: string): Promise<string> {
  const pl = await api.projects()
  const norm = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase()
  const want = norm(dir)
  const hit = (pl.detail ?? []).find((d) => d.work_dir && norm(d.work_dir) === want)
  if (hit) return hit.name
  const taken = new Set(pl.projects ?? [])
  const base = suggestProjectName(dir) || 'project'
  let name = base
  for (let i = 2; taken.has(name); i++) name = `${base}-${i}`
  const created = await api.createProject({ name, work_dir: dir })
  return created.name
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
  // Files pasted, dropped or uploaded into the composer, waiting for send.
  const [pending, setPending] = useState<PendingAttachment[]>([])
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
  // The conversation's permission policy. It decides what the thread may do
  // turn after turn; the authorize switch below grants THIS turn the right to
  // run irreversible operations, which is a finer question the mode does not
  // answer (auto and edits still stop at the tier-2 gate).
  const [permission, setPermission] = useState<PermissionMode>('auto')
  const [authorize, setAuthorize] = useState(false)
  // The composer's environment pick: empty = let the scheduler route by
  // capability; a node id pins this turn's spawned task to that executor.
  const [nodePref, setNodePref] = useState('')
  // Who works on this thread — the agent roster, picked in the composer's
  // footer when the conversation starts and remembered by the session after
  // that. Empty means no restriction (the scheduler picks the best hand),
  // which is what every thread had before rosters existed.
  const [team, setTeam] = useState<string[]>([])
  // Which header card is up: '' | 'node' | 'orch'. One slot, two cards — they
  // hang off the same corner of the bar, so the pane keeps the state for both
  // instead of letting each card decide (two `open` cards would stack).
  // While a card is up the pane reserves one lane for it on the right
  // (`.chat-pane.panel-open`), which is what moves the transcript, the composer
  // and the jump arrow left as one block instead of letting the card cover the
  // last reply's line ends. The lane is the same for both cards on purpose:
  // switching from one to the other is a swap in place, not a re-layout.
  const [panel, setPanel] = useState<'' | 'node' | 'orch'>('')
  // The task a transcript link asked for. Held here, not in the card, because
  // the click that asks for it is the transcript's: `{ id }` is rebuilt on
  // every request so a second click on the same 查看任务 still re-opens a pane
  // the reader had closed.
  const [focusTask, setFocusTask] = useState<{ id: string } | null>(null)
  /** Open one task where the reader already is — the orchestration card with
   *  that task picked, the thread's list beside it. Leaving for the task page
   *  used to cost the conversation: its scroll, its draft, its place. */
  function openTaskInPlace(id: string) {
    setPanel('orch')
    setFocusTask({ id })
  }
  // A card belongs to the thread it was opened in. Switching threads closes
  // it: the cards are remounted per session, so a lane kept reserved by the
  // parent would outlive the card that asked for it.
  useEffect(() => {
    setPanel('')
    // The queue belongs to the thread it was typed in: a counter carried over
    // from another thread would make the next note look like a removal.
    noteCount.current = 0
  }, [activeId])
  const [busy, setBusy] = useState(false)
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
  const [diffOpen, setDiffOpen] = useState(false)
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
  // Notes the finished turn never read, as the server reported them. They are
  // parked server-side, so this is only the trigger to open the continuation
  // turn — the text itself is drained by the server, which is what keeps a
  // reload or a second tab from delivering the same words twice.
  const queuedTurn = useRef('')
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

  useEffect(() => {
    onOpenSessionRef.current = onOpenSession
  }, [onOpenSession])

  useEffect(() => {
    activeProjectRef.current = activeProject
  }, [activeProject])

  // Session list (refresh when the active thread changes — its title may, or when project changes).
  useEffect(() => {
    api
      .sessions(activeProject || undefined)
      .then((ls) => {
        setSessions(ls)
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
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
      api
        .sessions(activeProjectRef.current || undefined)
        .then((ls) => {
          setSessions(ls)
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
            setMsgs((s.turns ?? []).map((turn, i) => ({ ...turn, k: `srv-${i}` })))
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
      setInput('')
      setPending([])
      // A brand-new thread starts with no roster: the pick in the footer is
      // for the conversation about to be created, not the last one.
      setTeam([])
      setPermission('auto')
      editorClear(composer.current)
      // Timings are filed by transcript index, so they belong to the thread
      // that produced them and must not follow the pane to another one.
      setTurnMs({})
      setLiveFrom(0)
      setLoading(false)
      return
    }
    if (inflight.current && inflightSid.current === activeId) return
    inflight.current?.abort()
    inflight.current = null
    inflightSid.current = null
    inflightOperation.current = null
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
    api
      .session(activeId)
      .then((s) => {
        setSession(s)
        setLoadError('')
        setTeam(s.team ?? [])
        setPermission(normalizePermission(s.permission_mode))
        setTurnMs({})
        setLiveFrom(0)
        setMsgs((s.turns ?? []).map((turn, i) => ({ ...turn, k: `srv-${i}` })))
      })
      .catch((e: unknown) => {
        setSession(null)
        setMsgs([])
        setLoadError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => setLoading(false))
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
    if (!op || op.status !== 'running') return
    // A local ask already owns the tail: its own placeholder is there and the
    // poll below would fight the stream for the transcript.
    if (inflight.current && inflightSid.current === activeId) return
    const key = `op-${op.id}`
    setMsgs((ms) =>
      ms.some((m) => m.k === key)
        ? ms
        : [...ms, { role: 'assistant', text: '', streaming: true, k: key }],
    )
    // The clock belongs to the turn, not to when this pane opened it.
    const startedAt = Date.parse(op.updated_at)
    if (Number.isFinite(startedAt) && startedAt > 0) setLiveFrom(startedAt)
    let cancelled = false
    // A killed process leaves its operation at "running" forever (the row is
    // only cleared when the ask returns). Following such a ghost would pin a
    // thinking bubble to the transcript indefinitely, so stop after a budget
    // that comfortably covers a slow classify/answer and fall back to the
    // persisted transcript.
    const deadline = startedAt > 0 ? startedAt + RECOVER_WATCH_MS : Date.now() + RECOVER_WATCH_MS
    const poll = async () => {
      while (!cancelled) {
        await new Promise((r) => setTimeout(r, 2000))
        if (cancelled) return
        if (Date.now() > deadline) {
          setMsgs((ms) => ms.filter((m) => m.k !== key))
          return
        }
        try {
          const s = await api.session(activeId)
          if (cancelled) return
          if (s.operation?.id === op.id && s.operation.status === 'running') continue
          setSession(s)
          setMsgs((s.turns ?? []).map((turn, i) => ({ ...turn, k: `srv-${i}` })))
          return
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
          (m.files ?? []).map((f) => f.split('/').pop()).join(' ') ||
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
      if (tray.length > 0) {
        const up = await api.uploadSessionFiles(sid, tray.map((p) => p.file))
        mentions = up.files.map((f) => f.mention)
      }
      const r = await api.sendSessionMessage(sid, text, false, mentions)
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
    const sid = activeId
    if (!sid) {
      setNotes([])
      return
    }
    try {
      const r = await api.sessionNotes(sid)
      if (sid === activeId) setNotes(r.notes)
    } catch {
      // A note list is advisory: a failed read must not paint an error over
      // the transcript. The next tick retries.
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
    setEditingNote(n.key)
    setNoteDraft(n.text)
  }

  function cancelEditNote() {
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

  async function send(e?: Event, queued?: string) {
    e?.preventDefault()
    // A continuation turn carries its own prompt: the words came from the
    // notes the last turn never read, not from the composer. The draft and the
    // tray on screen belong to whatever the user types next, so they are left
    // untouched — clearing them here would eat text typed while the answer was
    // still streaming.
    const raw = (queued ?? input).trim()
    // The tray mirrors the pills still in the editor: anything whose pill is
    // gone (Backspace, a selection-delete) stays behind instead of uploading
    // with the send.
    const el0 = composer.current
    const tray =
      queued !== undefined ? [] : el0 ? pending.filter((p) => livePillKeys(el0).has(p.key)) : pending
    // A tray-only send is a real send: dropping a screenshot and hitting Enter
    // is the whole point, and the prompt then carries nothing but the file.
    if (!raw && tray.length === 0) return
    // A continuation is never a mid-turn note: it starts the turn the previous
    // one ended without, so the busy guard (and its "type an addition" path)
    // does not apply — `busy` is still true in this closure's finally.
    if (queued === undefined && (busy || inflight.current)) {
      void sendIntoRunningTurn(raw, tray)
      return
    }
    // A slash line is a command, never a prompt — the REPL's rule. A
    // continuation is exempt: the words are what the user already queued, and
    // running a command they typed mid-answer would execute it out of order.
    if (queued === undefined && raw.startsWith('/') && dispatchSlash(raw)) {
      setInput('')
      editorClear(composer.current)
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
      if (text) patch((m) => ({ ...m, text: m.text + text }))
      if (thought) patch((m) => ({ ...m, thought: (m.thought ?? '') + thought }))
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
      const openThread = (sid: string) => {
        if (dropScope) navigate({ view: 'sessions', id: sid })
        else onOpenSession(sid)
      }
      // Uploads land in the session's own uploads directory, so a brand-new
      // chat with attachments needs its thread up front — the same create the
      // send below would have made anyway, one step earlier.
      if (tray.length > 0 && !id) {
        const s = await api.createSession((raw || tray[0]?.name || '').slice(0, 48), threadProject, team)
        id = s.id
        setSession(s)
        setSessions((ls) => [s, ...ls])
        openThread(s.id)
        activeIdRef.current = s.id
        // Name the thread this ask belongs to *before* the awaits below hand
        // control back. The transcript loader's guard is `inflightSid ===
        // activeId`, and without this the navigation lands mid-upload with the
        // sid still null, so the loader aborts the ask that has not been sent
        // yet. In the send-only path the same assignment happens with no await
        // in between, which is why it never had to be written twice.
        inflightSid.current = s.id
      }
      // One multipart round-trip for the whole tray, then the mentions ride
      // the prompt as @paths: text files arrive as fenced blocks, images stay
      // paths for the agent to open (see expandFileRefs).
      let mentions: string[] = []
      if (tray.length > 0) {
        const up = await api.uploadSessionFiles(id!, tray.map((p) => p.file))
        mentions = up.files.map((f) => f.mention)
      }
      // @file references become inline fenced blocks before the prompt
      // leaves the composer — "explain @main.go" works without pasting the
      // file. Inside try so a rejected expansion still clears inflight.
      const expanded = await expandFileRefs(withAttachments(raw, mentions), (p) => api.fsRead(p))
      prompt = expanded.prompt
      // The bubble lists what rode along: the refs the prompt expanded, plus
      // the tray's own files under the name the user knows them by. The tray's
      // mentions are dropped from the first group — they resolve to the same
      // file under its stamped upload name, and listing both showed every
      // pasted file twice.
      const trayMentions = new Set(mentions)
      attached = [
        ...new Set([
          ...expanded.attached.filter((a) => !trayMentions.has(a)),
          ...tray.map((p) => p.name),
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
        setSession(s)
        setSessions((ls) => [s, ...ls])
        openThread(s.id)
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
        // A continuation has no composer draft behind it: the text came from
        // the parked notes, and whatever is in the editor now was typed after
        // this turn started — it must survive.
        if (queued === undefined) {
          setInput('')
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
          { role: 'assistant', text: '', streaming: true, k: localMsgId() },
        ])
        announced = true
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
          onReasoning: (text) => {
            pendingThought += text
            scheduleFlush()
          },
          onDelta: (text) => {
            pendingText += text
            scheduleFlush()
          },
          onStatus: (text, ev) => patch((m) => ({ ...m, status: statusNote(text, ev) })),
          // The note typed mid-turn reached a round. This is the upgrade from
          // "queued" to "delivered" — without it the user cannot tell a steer
          // the model read from one the turn ended before reaching.
          onSteer: (_text, round) =>
            patch((m) => ({ ...m, status: t('sessions.steerDelivered', { n: String(round) }) })),
          // The turn ended before a round could read what was queued behind it.
          // The server parked those notes; remember them so the finally below
          // opens the continuation turn instead of letting them evaporate.
          onQueuedTurn: (text) => {
            queuedTurn.current = text
          },
          onOperation: (operationID) => {
            if (inflight.current === ctrl && inflightSid.current === sid) {
              inflightOperation.current = operationID
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
              return { ...m, ...task, ...plan, result: r, status: undefined, text, liveTask: undefined }
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
          setMsgs((ms) =>
            patchStreaming(ms, (m) => ({
              ...m,
              streaming: false,
              status: undefined,
              text: m.text || `· ${t('sessions.stopped')}`,
            })),
          )
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
                  setMsgs(turns.map((turn, i) => ({ ...turn, k: `srv-${i}` })))
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
          if (stillActive) {
            setSession(s)
            // …and the pane follows it. The change feed skipped this turn
            // while the ask was in flight (it must not paint over a live
            // stream) and its fingerprint is already spent, so this refetch
            // is the only chance to pick up a turn the wire under-reported.
            // A fanned-out round is under-reported by design — one result
            // frame, one turn per task — so the surplus rows are adopted here
            // rather than waiting for a reload.
            setMsgs((ms) =>
              mergeTailTurn(ms, s.turns ?? [], (turn) => ({ ...turn, k: localMsgId() }) as ChatMsg),
            )
          }
        }).catch(() => {})
      }
      // What the turn owed: notes it ended without any round reading. The
      // server parked them, and this is the turn that opens with them — the
      // composer used to print "queued" and then let them vanish at the end of
      // the answer. A stopped turn is the one exception: the user asked for
      // quiet, so the notes stay parked for the next thing they send.
      const owed = queuedTurn.current
      queuedTurn.current = ''
      if (
        owed &&
        !ctrl.signal.aborted &&
        !inflight.current &&
        id &&
        isLiveSession(id, activeIdRef.current)
      ) {
        void send(undefined, owed)
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
      { id: 'cmd:new', group: grp, label: '/new', hint: t('sessions.cmdNew'), run: () => newChat() },
      { id: 'cmd:export', group: grp, label: '/export', hint: t('sessions.cmdExport'), run: exportSession },
      { id: 'cmd:clear', group: grp, label: '/clear', hint: t('sessions.cmdClear'), run: () => { clearPending(); setInput('') } },
      { id: 'cmd:cost', group: grp, label: '/cost', hint: t('sessions.cmdCost'), run: go('settings:system') },
      { id: 'cmd:context', group: grp, label: '/context', hint: t('sessions.cmdContext'), run: go('settings:system') },
      { id: 'cmd:doctor', group: grp, label: '/doctor', hint: t('sessions.cmdDoctor'), run: go('settings:system') },
      { id: 'cmd:model', group: grp, label: '/model', hint: t('sessions.cmdModel'), run: go('settings:models') },
      { id: 'cmd:tasks', group: grp, label: '/tasks', hint: t('sessions.cmdTasks'), run: go('control') },
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

  /** Take pasted, dropped or picked files into the tray. The tile appears at
   *  once — the bytes themselves go up at send, when the thread they belong to
   *  is known and a brand-new chat has one to upload into. */
  function addFiles(files: File[]): void {
    if (files.length === 0) return
    const added: PendingAttachment[] = files.map((file) => ({
      key: localMsgId(),
      name: file.name || t('sessions.attachPasted'),
      size: file.size,
      image: isImageFile(file),
      file,
    }))
    setPending((ps) => [...ps, ...added])
    // The pill lands in the editable itself, at the caret — that is the whole
    // point of the contenteditable. Inserted by hand, never by React: the
    // framework reconciling children inside an editable would fight the text
    // nodes the user's own typing puts there.
    const el = composer.current
    if (!el) return
    el.focus()
    for (const p of added) {
      const chip = makeChipNode(p)
      const sel = el.ownerDocument.getSelection()
      if (sel && sel.rangeCount > 0) {
        const range = sel.getRangeAt(0)
        range.deleteContents()
        range.insertNode(chip)
        range.setStartAfter(chip)
        range.collapse(true)
        sel.removeAllRanges()
        sel.addRange(range)
      } else {
        el.appendChild(chip)
      }
      // A trailing space keeps the pill from welding to whatever follows.
      const gap = el.ownerDocument.createTextNode(' ')
      range_or_append(chip, gap)
    }
    setInput(editorTextOf(el))
    setCaret(editorCaretOffset(el))
    autoGrow(el)
  }

  function removePending(key: string): void {
    setPending((ps) => ps.filter((p) => p.key !== key))
    composer.current?.querySelector(`.attach-pill[data-pk="${key}"]`)?.remove()
    const el = composer.current
    if (el) {
      setInput(editorTextOf(el))
      autoGrow(el)
    }
  }

  /** The pill keys actually in the editor right now. The DOM is authoritative:
   *  Backspace or a selection-delete takes the node out without asking, so
   *  the tray drops whatever lost its pill rather than upload a file the user
   *  took back out of the turn. */
  function livePillKeys(el: HTMLElement): Set<string> {
    const keys = new Set<string>()
    el.querySelectorAll('.attach-pill').forEach((n) => {
      const k = (n as HTMLElement).dataset.pk
      if (k) keys.add(k)
    })
    return keys
  }

  /** Drop pending entries whose pill is no longer in the editor. */
  function syncPending(el: HTMLElement): void {
    const keys = livePillKeys(el)
    setPending((ps) => (ps.some((p) => !keys.has(p.key)) ? ps.filter((p) => keys.has(p.key)) : ps))
  }

  function clearPending(): void {
    setPending([])
    editorClear(composer.current)
  }

  /** `dragover` fires continuously while a file is over the element, and the
   *  drop only happens if this one calls preventDefault — the default action
   *  is the browser navigating away to open the file. */
  function dragHasFiles(e: DragEvent): boolean {
    return [...(e.dataTransfer?.types ?? [])].includes('Files')
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
  // A running operation counts too: a turn started elsewhere (the workspace
  // dispatcher, another tab, the TUI) has no bubble here yet, but the thread
  // must still show the employee's live row instead of a bare spinner.
  const dispatched =
    msgs.some((m) => m.kind === 'task' || m.kind === 'plan' || Boolean(m.liveTask)) ||
    session?.operation?.status === 'running'
  const sessionTasks = useSessionTasks(activeId, dispatched)
  const roundOpen = sessionTasks ? roundStillOpen(sessionTasks) : false
  const runSummary = busy || roundOpen ? null : trailingRunSummary(msgs)
  // The employees still working, drawn after the last turn: a running job is
  // the newest thing in the thread, whatever the position of the bubble that
  // dispatched it (see `liveTaskIds`).
  const liveIds = sessionTasks ? liveTaskIds(sessionTasks) : []
  // The same rows keyed for the transcript. A task bubble renders from here
  // instead of fetching its own copy on every change frame: a thread with
  // several dispatch turns was issuing that many extra requests per frame,
  // all competing with the event stream for the browser's six connections.
  const tasksById = new Map((sessionTasks ?? []).map((t) => [t.id, t]))

  // — one round, one bubble. The turns a fan-out files are consecutive and
  // carry the round's clock, so the first one wears the panda's face and holds
  // the rest: a five-agent test reads as one reply with five cards, not five
  // replies with one each. Nothing is lost — every card keeps its own line,
  // cast row and link, and the transcript on disk is untouched (the model's
  // history still lists every dispatch).
  const { batchOf, absorbed } = groupDispatches(msgs)

  return (
    <section class="chat">


      <div class={`chat-pane${panel ? ' panel-open' : ''}`}>
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
          {/* Last, and pushed to the far edge: the header reads title → tools →
              execution target. All three are ghost icon buttons at the top of
              the size scale — what you reach for before sending, in the order
              you reach for it: take the reply out, choose *where* the turn
              runs, choose *how* it is split into steps. The node choice lost
              its spelled-out `<select>`: the glyph plus the card behind it says
              more (which machine is local, which is offline, which one the
              scheduler would pick) in a quarter of the width, and the card is
              what the pane makes room for. */}
          <div class="chat-head-tools">
            {session && msgs.length > 0 && (
              <button
                class="icon-btn tip-down"
                onClick={exportSession}
                data-tip={t('sessions.export')}
                aria-label={t('sessions.export')}
              >
                <Icon name="download" size={19} />
              </button>
            )}
            <NodePicker
              /* Remounted per thread: the card belongs to the pane you opened
                 it in, and a stale `open` would keep the transcript's lane
                 reserved after switching threads with nothing in it. The
                 prefix keeps the key distinct from the orchestration card's —
                 two siblings keyed the same id made Preact warn on every
                 render of this bar. */
              key={`node-${session?.id ?? 'none'}`}
              nodes={nodes}
              value={nodePref}
              onPick={setNodePref}
              open={panel === 'node'}
              onOpenChange={(open) => setPanel(open ? 'node' : '')}
            />
            {/* Orchestration, to the right of the node pick: the node answers
                *where* a turn runs, this one answers *what the thread has
                become* — its tasks, moving or waiting on you. Both cards hang
                off the same corner, so `panel` keeps exactly one of them up. */}
            <OrchestrateCard
              key={`orch-${session?.id ?? 'none'}`}
              sessionId={session?.id ?? null}
              open={panel === 'orch'}
              onOpenChange={(open) => setPanel(open ? 'orch' : '')}
              onlineNodeCount={onlineNodeCount}
              selfNodeId={selfNodeId}
              focusTask={focusTask}
            />
          </div>
        </header>

        {diffOpen && diff && (
          <div class="diff-drawer">
            <div class="diff-drawer-head">
              <strong>
                {t('sessions.worktreeChanges')} — {diff.branch}
              </strong>
              <span>
                <button class="btn danger" disabled={reverting || merging} onClick={revertMerge}>
                  {reverting ? t('sessions.reverting') : t('sessions.revert')}
                </button>
                <button class="btn primary" disabled={merging || reverting} onClick={mergeChanges}>
                  {merging ? t('sessions.merging') : t('sessions.merge')}
                </button>
                <button class="btn" onClick={() => setDiffOpen(false)}>
                  ×
                </button>
              </span>
            </div>
            {diff.stat && (diff.stat.files_changed > 0 || diff.stat.untracked > 0) && (
              <p class="dim diff-stat">
                {t('sessions.diffStat', {
                  files: String(diff.stat.files_changed),
                  ins: String(diff.stat.insertions),
                  del: String(diff.stat.deletions),
                })}
                {diff.stat.untracked > 0 &&
                  ` · ${t('sessions.diffUntracked', { n: String(diff.stat.untracked) })}`}
              </p>
            )}
            {diff.stat && diff.stat.sensitive && diff.stat.sensitive.length > 0 && (
              <p class="gate-error">
                {t('sessions.sensitiveFiles')}: <span class="mono">{diff.stat.sensitive.join(', ')}</span>
              </p>
            )}
            {diff.stat && diff.stat.binary && diff.stat.binary.length > 0 && (
              <p class="dim">
                {t('sessions.binaryFiles')}: <span class="mono">{diff.stat.binary.join(', ')}</span>
              </p>
            )}
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
          {msgs.map((m, i) =>
            absorbed.has(m.k) ? null : (
              <ChatTurn
                key={m.k}
                msg={m}
                batch={batchOf.get(m.k)}
                tasksById={tasksById}
                /* The local measurement wins while it is there (it is the only
                   one that exists between the reply landing and the transcript
                   being re-fetched); the panel's `duration_ms` is what survives
                   a reload. */
                elapsedMs={turnMs[i] ?? m.duration_ms}
                liveFrom={liveFrom}
                onOpenTask={openTaskInPlace}
              />
            ),
          )}
          {liveIds.map((id) => {
            const task = tasksById.get(id)
            return task ? <TaskProgress key={id} task={task} /> : null
          })}
          {runSummary && <RunSummaryRow run={runSummary} onOpenTask={openTaskInPlace} />}
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
                    <span class="chat-rail-item-text">{p.label}</span>
                    <span class="chat-rail-item-tick" aria-hidden="true" />
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
        {notes.length > 0 && (
          <div class="note-strip" data-testid="note-strip">
            <div class="note-rail">
              <div
                class="note-bubbles"
                ref={noteBubbles}
                onMouseEnter={() => setNotesOpen(true)}
                onMouseLeave={() => setNotesOpen(false)}
                onFocusOut={(e) => {
                  // Focus moving into the popover's verbs is still inside;
                  // leaving the whole cluster is what closes it.
                  if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
                    setNotesOpen(false)
                  }
                }}
                onKeyDown={(e) => {
                  if (e.key !== 'Escape' || !notesOpen) return
                  e.stopPropagation()
                  setNotesOpen(false)
                  ;(e.currentTarget.querySelector('.note-dots') as HTMLElement | null)?.focus()
                }}
              >
                <button
                  type="button"
                  class="note-dots"
                  aria-haspopup="true"
                  aria-expanded={notesOpen ? 'true' : 'false'}
                  aria-label={`${t('sessions.noteModeQueue')} · ${notes.length}`}
                  title={`${t('sessions.noteModeQueue')} · ${notes.length}`}
                  onClick={() => setNotesOpen((v) => !v)}
                >
                  {/* Three circles, always: the trail is the idiom, not a
                      tally — one queued note used to draw one dot, which read
                      as a speck rather than as the panda thinking. The count
                      lives in the label and in the list behind the hover. */}
                  {[0, 1, 2].map((i) => (
                    <span class="note-dot" key={i} aria-hidden="true" />
                  ))}
                </button>
                {notesOpen && (
                  <div class="note-pop">
                    <div class="note-pop-card">
                      <div
                        class="note-list"
                        ref={noteList}
                        role="list"
                        style={noteRoom ? { maxHeight: `${noteRoom}px` } : undefined}
                        data-above={noteEdges.above ? 'true' : 'false'}
                        data-below={noteEdges.below ? 'true' : 'false'}
                      >
                        {notes.map((n, i) => (
                          <div class="note-row" key={n.key} role="listitem" data-mode={n.mode}>
                            <span class="note-index" aria-hidden="true">
                              {i + 1}
                            </span>
                            <span class={`note-mode ${n.mode === 'inject' ? 'inject' : ''}`}>
                              {n.mode === 'inject'
                                ? t('sessions.noteModeInject')
                                : t('sessions.noteModeQueue')}
                            </span>
                            {editingNote === n.key ? (
                              // Editing in place: the row stays where it is in
                              // the queue, so a note already queued ahead of an
                              // inject keeps both its position and its mode.
                              <input
                                class="note-edit"
                                value={noteDraft}
                                autoFocus
                                aria-label={t('sessions.noteEdit')}
                                onInput={(e) => setNoteDraft(e.currentTarget.value)}
                                onKeyDown={(e) => {
                                  if (e.key === 'Enter') {
                                    e.preventDefault()
                                    void saveEditNote(n)
                                  } else if (e.key === 'Escape') {
                                    e.preventDefault()
                                    cancelEditNote()
                                  }
                                }}
                              />
                            ) : (
                              <span
                                class="note-text"
                                title={n.text}
                                onDblClick={() => beginEditNote(n)}
                              >
                                {n.text}
                              </span>
                            )}
                            {n.attachments && n.attachments.length > 0 && (
                              <span class="note-files" title={n.attachments.join('\n')}>
                                <Icon name="paperclip" size={11} />
                                {n.attachments.length}
                              </span>
                            )}
                            <span class="note-actions">
                              {editingNote === n.key ? (
                                <>
                                  <button
                                    type="button"
                                    class="note-act"
                                    title={t('sessions.noteSave')}
                                    aria-label={t('sessions.noteSave')}
                                    onClick={() => void saveEditNote(n)}
                                  >
                                    <Icon name="check" size={13} />
                                  </button>
                                  <button
                                    type="button"
                                    class="note-act"
                                    title={t('sessions.noteCancel')}
                                    aria-label={t('sessions.noteCancel')}
                                    onClick={cancelEditNote}
                                  >
                                    <Icon name="x" size={13} />
                                  </button>
                                </>
                              ) : (
                                <>
                                  <button
                                    type="button"
                                    class="note-act"
                                    title={t('sessions.notePromote')}
                                    aria-label={t('sessions.notePromote')}
                                    onClick={() => void promoteNote(n)}
                                  >
                                    <Icon name="send" size={13} />
                                  </button>
                                  <button
                                    type="button"
                                    class="note-act"
                                    title={t('sessions.noteEdit')}
                                    aria-label={t('sessions.noteEdit')}
                                    onClick={() => beginEditNote(n)}
                                  >
                                    <Icon name="pencil" size={13} />
                                  </button>
                                  <button
                                    type="button"
                                    class="note-act danger"
                                    title={t('sessions.noteDrop')}
                                    aria-label={t('sessions.noteDrop')}
                                    onClick={() => void dropNote(n)}
                                  >
                                    <Icon name="trash" size={13} />
                                  </button>
                                </>
                              )}
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  </div>
                )}
              </div>
            </div>
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
            {/* The editable surface. Contenteditable rather than a textarea so
                an attachment can render as a pill inside the line being typed,
                with the caret carrying on after it — the behaviour the
                reference shows and a textarea cannot host. Everything the
                textarea gave for free is re-derived in the helpers above. */}
            <div
              ref={(el) => {
                composer.current = el
              }}
              class={`composer-input${editorPlain(input) === '' && pending.length === 0 ? ' is-empty' : ''}`}
              contenteditable="true"
              role="textbox"
              aria-multiline="true"
              aria-label={t('sessions.placeholder')}
              data-placeholder={busy ? t('sessions.placeholderRunning') : t('sessions.placeholder')}
              onInput={(e) => {
                useBrParagraphSeparator()
                const el = e.currentTarget as HTMLDivElement
                setInput(editorTextOf(el))
                setCaret(editorCaretOffset(el))
                // Deleting a pill with Backspace or a selection only removes
                // the node — drop the file behind it too, or a send would
                // still upload what the user took out.
                syncPending(el)
                autoGrow(el)
              }}
              onClick={(e) => {
                // One listener for the whole surface: a click on a pill takes
                // that file back out of the turn, a click anywhere else just
                // moves the caret.
                const chip = (e.target as HTMLElement).closest('.attach-pill') as HTMLElement | null
                if (chip) {
                  removePending(chip.dataset.pk ?? '')
                  return
                }
                setCaret(editorCaretOffset(e.currentTarget as HTMLDivElement))
              }}
              onKeyUp={(e) => setCaret(editorCaretOffset(e.currentTarget as HTMLDivElement))}
              onPaste={(e) => {
                // A screenshot on the clipboard arrives as a file with no
                // path, which is exactly what the pill is for.
                const files = [...(e.clipboardData?.files ?? [])]
                if (files.length > 0) {
                  e.preventDefault()
                  addFiles(files)
                  return
                }
                // Pasted text goes in as <br>-separated lines: the browser's
                // own paste plants literal newline text nodes, which Chromium
                // does not lay out as line breaks in here. A single line goes
                // through insertText — the default paste would bring the
                // source page's HTML styling into a plain-text surface.
                const text = e.clipboardData?.getData('text/plain')
                if (!text) return
                e.preventDefault()
                const el3 = e.currentTarget as HTMLDivElement
                if (!text.includes('\n')) {
                  editorInsertText(text)
                  requestAnimationFrame(() => {
                    setInput(editorTextOf(el3))
                    setCaret(editorCaretOffset(el3))
                    autoGrow(el3)
                  })
                  return
                }
                const parts = text.split('\n')
                const frag = el3.ownerDocument.createDocumentFragment()
                parts.forEach((part, i) => {
                  if (i > 0) frag.appendChild(el3.ownerDocument.createElement('br'))
                  if (part) frag.appendChild(el3.ownerDocument.createTextNode(part))
                })
                const sel3 = document.getSelection()
                if (sel3 && sel3.rangeCount > 0) {
                  const rg = sel3.getRangeAt(0)
                  rg.deleteContents()
                  rg.insertNode(frag)
                  rg.collapse(false)
                  sel3.removeAllRanges()
                  sel3.addRange(rg)
                } else {
                  el3.appendChild(frag)
                }
                setInput(editorTextOf(el3))
                setCaret(editorCaretOffset(el3))
                autoGrow(el3)
              }}
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
                // Shift+Enter breaks the line. The browser's own answer is a
                // literal newline text node, which Chromium does not lay out as
                // a line break inside a contenteditable — the row stays one
                // line tall and the caret sits mid-string. Insert the <br>
                // ourselves so the editor actually grows.
                if (e.key === 'Enter' && e.shiftKey && !e.isComposing) {
                  e.preventDefault()
                  const el2 = e.currentTarget as HTMLDivElement
                  const br = el2.ownerDocument.createElement('br')
                  const sel2 = document.getSelection()
                  if (sel2 && sel2.rangeCount > 0) {
                    const rg = sel2.getRangeAt(0)
                    rg.deleteContents()
                    rg.insertNode(br)
                    rg.setStartAfter(br)
                    rg.collapse(true)
                    sel2.removeAllRanges()
                    sel2.addRange(rg)
                  } else {
                    el2.appendChild(br)
                  }
                  // A zero-width space after the <br> keeps the caret on the
                  // new line; a bare <br> at the end of the block leaves the
                  // caret before it in some engines.
                  const nbsp = el2.ownerDocument.createTextNode('\u200b')
                  range_or_append(br, nbsp)
                  setInput(editorTextOf(el2))
                  setCaret(editorCaretOffset(el2))
                  autoGrow(el2)
                  return
                }
                // Enter sends — and while a Chinese or Japanese IME is
                // composing, Enter belongs to the composition, not to us.
                // Checking isComposing is the whole reason a contenteditable can
                // replace the textarea here.
                if (e.key === 'Enter' && !e.shiftKey && !e.isComposing
                    && !(e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  send()
                }
                if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                  e.preventDefault()
                  send()
                }
              }}
            >
            </div>
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
              {authorizeRelevant(permission) && (
                <label class="ask-authorize" title={t('approval.stateHint')}>
                  {/* A switch, not a tick box: this is a mode you leave on, not a
                      one-off box you tick. The knob rides a <span> rather than a
                      pseudo-element on the <input> — ::after on an input is not
                      reliably rendered. */}
                  <span class="switch">
                    <input
                      type="checkbox"
                      checked={authorize}
                      onChange={(e) => setAuthorize((e.target as HTMLInputElement).checked)}
                    />
                    <span class="switch-track" aria-hidden="true" />
                  </span>
                  {t('sessions.authorize')}
                </label>
              )}
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
            {/* The pet stands beside the box, not above it: parked over the
                box it cost a whole row of vertical space on the one page where
                the empty canvas is the point, and it read as a status line for
                a thread that has not started. Anchored to the box's right edge
                it stays with the control it belongs to — the box moves when a
                header card opens, and the pet moves with it. */}
            <ComposerPet
              working={busy}
              team={team}
              onTeam={pickTeam}
              onManage={() => navigateView('settings:agents')}
            />
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

/** Empty state: the greeting, one line of orientation, and three prompts that
 *  fill the composer. The brand mark and fleet strip stay out — the pet is
 *  already standing on the composer — but a blank page gives a first-time
 *  reader no verb, and a chip is cheaper to click than a sentence to invent. */
function ChatEmpty({
  onPick,
  hasDraft,
}: {
  onPick(text: string): void
  hasDraft: boolean
}) {
  return (
    <div class="chat-empty">
      <div class="chat-empty-cta">
        <h1>{t('sessions.hello')}</h1>
        <p class="chat-empty-hint">{t('sessions.hint')}</p>
        {/* Once there is a draft or an attachment, a suggestion would replace
            work the reader already did; the chips step aside instead. */}
        {!hasDraft && (
          <div class="chat-starters">
            {(['sessions.s1', 'sessions.s2', 'sessions.s3'] as const).map((key) => (
              <button
                type="button"
                class="chat-starter"
                key={key}
                onClick={() => onPick(t(key))}
              >
                {t(key)}
                <Icon name="chevron-right" size={14} />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** The composer's "+" menu: attach context to the turn you are about to send.
 *
 *  A skill and an MCP server both go in as a mention in the prompt text —
 *  `@skill:name` / `@mcp:name` — which is exactly what the server receives.
 *  Nothing is expanded behind the user's back, so the box keeps showing the
 *  text the model will read. Files are the third entry and are not wired yet. */
function AttachMenu({
  onInsert,
  onAttach,
  project,
}: {
  onInsert(tokens: string[]): void
  /** Files picked from the machine go to the composer's tray rather than into
   *  the text as `@path` tokens: they are not workspace files the user is
   *  naming mid-sentence, they are things being handed over. */
  onAttach(files: File[]): void
  /** The project this thread belongs to. Its work dir is the browser's root,
   *  so what is offered is what this thread's agent can actually act on — the
   *  node's own data tree holds no user files at all. */
  project?: string
}) {
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<'root' | 'skill' | 'mcp' | 'file'>('root')
  const [skills, setSkills] = useState<SkillEntry[] | null>(null)
  const [mcp, setMcp] = useState<MCPSettings | null>(null)
  const wrap = useRef<HTMLDivElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  // File browser state: the path being listed, its rows, and what is picked.
  const [cwd, setCwd] = useState<string | null>(null)
  const [rows, setRows] = useState<FsFileEntry[]>([])
  const [browseErr, setBrowseErr] = useState('')
  const [browsing, setBrowsing] = useState(false)
  const [picked, setPicked] = useState<FsFileEntry[]>([])
  /** The project's work dir, resolved on first entry to the file view: the
   *  session payload carries the project's *name*, not its directory. */
  const [root, setRoot] = useState<string | null>(null)
  /** The path we have already tried to open. The browser auto-opens the root
   *  exactly once: on a refusal `cwd` stays null, and without this the effect
   *  would fire again the moment `browsing` dropped back to false and hammer
   *  the endpoint forever, flashing "…" over the error the whole time. */
  const [openedPath, setOpenedPath] = useState<string | null>(null)

  function browse(path: string): void {
    setBrowsing(true)
    setBrowseErr('')
    setOpenedPath(path)
    api
      .fsFiles(path || undefined)
      .then((r) => {
        setCwd(r.current)
        setRows(r.entries)
      })
      .catch((e: unknown) => setBrowseErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBrowsing(false))
  }

  // Fetched on first entry to a view, not on mount: most turns never touch
  // this menu.
  useEffect(() => {
    if (!open) return
    if (view === 'skill' && skills === null) {
      api.skills().then(setSkills).catch(() => setSkills([]))
    }
    if (view === 'mcp' && mcp === null) {
      api.getMCPSettings().then(setMcp).catch(() => setMcp({ command: '' }))
    }
  }, [open, view, skills, mcp])

  // Resolve the root once per open of the file view, then open it. Both steps
  // are guarded so a refusal cannot restart them.
  useEffect(() => {
    if (!open || view !== 'file' || root !== null || openedPath !== null) return
    let alive = true
    api
      .projects()
      .then((pl) => {
        const name = project || pl.active || ''
        const hit = (pl.detail ?? []).find((p) => p.name === name)
        if (alive) setRoot(hit?.work_dir ?? '')
      })
      .catch(() => {
        if (alive) setRoot('')
      })
    return () => {
      alive = false
    }
  }, [open, view, root, openedPath, project])

  useEffect(() => {
    if (!open || view !== 'file' || root === null || openedPath !== null) return
    browse(root)
  }, [open, view, root, openedPath])

  // A menu that only closes by clicking itself again is a trap.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    addEventListener('mousedown', onDown)
    addEventListener('keydown', onKey)
    return () => {
      removeEventListener('mousedown', onDown)
      removeEventListener('keydown', onKey)
    }
  }, [open])

  function toggle(entry: FsFileEntry): void {
    setPicked((ps) => {
      const i = ps.findIndex((p) => p.path === entry.path)
      if (i >= 0) return ps.filter((p) => p.path !== entry.path)
      return [...ps, entry]
    })
  }

  function pick(token: string): void {
    onInsert([token])
    setOpen(false)
    setView('root')
  }

  function insertPicked(): void {
    if (picked.length === 0) return
    onInsert(picked.map((p) => `@${p.path}`))
    setPicked([])
    setOpen(false)
    setView('root')
  }

  /** A file picked off the machine goes straight to the composer's tray, which
   *  previews it locally and uploads it on send. The upload used to happen
   *  here and land in the text as a long `@…/1792172704123-name.png` token —
   *  the ugliest thing the composer could show, and it needed a session id
   *  that a brand-new chat does not have yet. */
  function uploadFiles(files: FileList | null): void {
    if (!files || files.length === 0) return
    onAttach([...files])
    setPicked([])
    setOpen(false)
    setView('root')
  }

  return (
    <div class="attach-wrap" ref={wrap}>
      <button
        type="button"
        class="icon-btn attach-btn"
        aria-label={t('sessions.attachTitle')}
        aria-expanded={open}
        data-tip={t('sessions.attachTitle')}
        onClick={() => {
          setOpen((v) => !v)
          setView('root')
        }}
      >
        +
      </button>
      {open && (
        <div class="attach-menu" role="menu">
          {view === 'root' ? (
            <>
              <button
                type="button"
                role="menuitem"
                class="attach-item"
                onClick={() => setView('file')}
              >
                <Icon name="paperclip" size={12} />
                {t('sessions.attachFile')}
              </button>
              <button
                type="button"
                role="menuitem"
                class="attach-item"
                onClick={() => setView('skill')}
              >
                <Icon name="lightbulb" size={12} />
                {t('nav.skills')}
              </button>
              <button
                type="button"
                role="menuitem"
                class="attach-item"
                onClick={() => setView('mcp')}
              >
                <Icon name="plug" size={12} />
                {t('settings.mcp')}
              </button>
            </>
          ) : view === 'file' ? (
            <div class="attach-browser">
              <div class="attach-browser-bar">
                <button
                  type="button"
                  class="attach-up"
                  disabled={!cwd || (!!root && cwd === root)}
                  title={t('sessions.attachUp')}
                  onClick={() => browse(parentPath(cwd ?? ''))}
                >
                  ‹
                </button>
                <span class="attach-cwd mono">{cwd}</span>
              </div>
              <div class="attach-rows">
                {browseErr && <p class="attach-empty">{browseErr}</p>}
                {!browseErr && rows.length === 0 && !browsing && (
                  <p class="attach-empty">{t('sessions.attachEmpty')}</p>
                )}
                {browsing && <p class="attach-empty">…</p>}
                {rows.map((e) =>
                  e.dir ? (
                    <button
                      key={e.path}
                      type="button"
                      role="menuitem"
                      class="attach-item"
                      onClick={() => browse(e.path)}
                    >
                      <Icon name="folder" size={12} />
                      <span class="attach-name">{e.name}/</span>
                    </button>
                  ) : (
                    <button
                      key={e.path}
                      type="button"
                      role="menuitem"
                      class={`attach-item${picked.some((p) => p.path === e.path) ? ' picked' : ''}`}
                      title={e.path}
                      onClick={() => toggle(e)}
                    >
                      <span class="attach-check" aria-hidden="true">
                        {picked.some((p) => p.path === e.path) ? '✓' : ''}
                      </span>
                      <span class="attach-name">{e.name}</span>
                      <span class="attach-size">{fmtSize(e.size)}</span>
                    </button>
                  ),
                )}
              </div>
              <div class="attach-foot">
                <button
                  type="button"
                  class="attach-upload"
                  onClick={() => fileInput.current?.click()}
                >
                  {t('sessions.attachUpload')}
                </button>
                <button
                  type="button"
                  class="btn primary attach-insert"
                  disabled={picked.length === 0}
                  onClick={insertPicked}
                >
                  {t('sessions.attachInsert', { n: picked.length })}
                </button>
              </div>
              <input
                ref={fileInput}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  uploadFiles((e.target as HTMLInputElement).files)
                  ;(e.target as HTMLInputElement).value = ''
                }}
              />
            </div>
          ) : (
            <>
              <button type="button" class="attach-back" onClick={() => setView('root')}>
                ‹ {view === 'skill' ? t('nav.skills') : t('settings.mcp')}
              </button>
              {view === 'skill' &&
                (skills ?? []).map((s) => (
                  <button
                    key={s.name}
                    type="button"
                    role="menuitem"
                    class="attach-item"
                    title={s.description}
                    onClick={() => pick(`@skill:${s.name}`)}
                  >
                    {s.name}
                  </button>
                ))}
              {view === 'mcp' &&
                (mcp?.command ? (
                  <button
                    type="button"
                    role="menuitem"
                    class="attach-item"
                    title={mcp.command}
                    onClick={() => pick(`@mcp:${mcp.command.split(/\s+/)[0]}`)}
                  >
                    {mcp.command}
                  </button>
                ) : (
                  <p class="attach-empty">{t('sessions.attachEmpty')}</p>
                ))}
              {view === 'skill' && skills?.length === 0 && (
                <p class="attach-empty">{t('sessions.attachEmpty')}</p>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}

/** Parent directory of a path from the fs listing, in either separator
 *  convention. Empty string when there is no parent to go to. */
function parentPath(p: string): string {
  const trimmed = p.replace(/[\\/]+$/, '')
  const i = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  if (i <= 0) return ''
  return trimmed.slice(0, i)
}

/** A transcript's 查看任务 link opens the orchestration card where the reader
 *  already is. The href stays real so ⌘/ctrl/shift-click and "open in new tab"
 *  still reach the full task page — a plain click is the only one that stays
 *  in the conversation. */
function inPlaceTaskClick(e: MouseEvent, id: string, open: (id: string) => void) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  e.preventDefault()
  open(id)
}

/** The receipt a turn came back with, when it is not the task's own card.
 *
 *  A parked tier-2 task must still render its decision in the same breath it
 *  parked in, and a remembered answer must say so where the user can see it.
 *  The turn that carries this is not always the head of a fanned-out round —
 *  the live result lands on whichever bubble was streaming — so the bubble
 *  asks every member of its group instead of only itself.
 *
 *  `needs_approval` is skipped for a turn with a task ref: that task's card
 *  already draws the approval gate (see `DispatchBlock`), and drawing it twice
 *  would ask the same question twice. */
function MetaRow({ msg }: { msg: ChatMsg }) {
  const res = msg.result
  if (!res?.needs_approval && !res?.denied && !res?.consent_scope && !res?.warning) return null
  return (
    <div class="bubble-slot-row slot-meta">
      {res.needs_approval && res.approval && !msg.ref && (
        <TaskApproval taskId={res.approval.task_id} preselect={res.approval.scope} />
      )}
      {res.denied && <span class="badge red">{t('approval.deniedRemembered')}</span>}
      {res.consent_scope && (
        <span class="dim approval-consent">{t(`approval.consent.${res.consent_scope}`)}</span>
      )}
      {/* Engine advisory — e.g. the dispatch went to a queue nothing drains.
          The string arrives server-localized. */}
      {res.warning && <span class="badge yellow">{res.warning}</span>}
    </div>
  )
}

/** One row of the transcript, drawn the way a group chat draws it.
 *
 *  A thread has more than one voice in it: you ask, the panda works out how to
 *  split the job and who gets it, an employee does it and reports back, and the
 *  panda closes the round. Drawing all of that as one anonymous column — a
 *  duration caption, a task card, and a `[done] @agent` line with nobody
 *  attached — is what made a finished task read like a log file. So a row now
 *  carries what a chat message carries: a face, a name, what that speaker is
 *  doing in the conversation, and a bubble of their own.
 *
 *  The speaker is derived, never stored (see components/chat-speaker.ts): a
 *  dispatch turn is the panda orchestrating, a report head that names an
 *  employee is that employee reporting, and everything else is the panda. */
function ChatBubble(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  /** How long the turn behind this reply took, once it is done. The local
   *  measurement comes from `turnMs` (it is the only one that exists between
   *  the reply landing and the transcript being re-fetched); `duration_ms`
   *  from the stored turn is what survives a reload. */
  elapsedMs?: number
  /** Wall-clock start of the turn still streaming, or 0. When set and this
   *  bubble is the streaming one, the timer runs here instead of printing a
   *  fixed number. */
  liveFrom?: number
  /** The plan a plan turn started. */
  plan?: PlanDetail | null
  /** The rest of a fanned-out round: the sibling dispatch turns this reply
   *  absorbed (see `groupDispatches`). Drawn under this one's card, in the same
   *  bubble, so the round reads as one statement. */
  batch?: ChatMsg[]
  /** The thread's task rows, keyed by id — each `DispatchBlock` reads its own
   *  from here instead of fetching a copy on every change frame. */
  tasksById?: Map<string, Task>
}) {
  const { msg, onOpenTask, elapsedMs, liveFrom = 0, plan = null, batch = [], tasksById } = props

  const report = msg.kind === 'error' ? null : splitReportHead(msg.text ?? '')
  const speaker = speakerOf(msg, report)
  const isTaskKind = msg.kind === 'task' && Boolean(msg.ref)
  const isPlanKind = msg.kind === 'plan' && Boolean(msg.ref)
  // The dispatch template appends the task id so the model's history keeps it
  // (sessions.go). In a chat row the card is the link and a uuid is not a
  // thing a person reads, so it comes back off.
  const text = isTaskKind || isPlanKind ? stripRefId(msg.text ?? '', msg.ref) : msg.text ?? ''
  const hasTaskStatus = Boolean(msg.streaming && msg.status)
  const hasThinking = Boolean(msg.streaming && !msg.text && !msg.status)
  // Which model wrote this reply. The live stream carries it on the result;
  // the stored turn carries it after a reload (see `Turn.Model`).
  const replyModel = msg.result?.entry_model || msg.model || ''
  // The round's own display-only state lands on whichever bubble was streaming
  // when it finished — for a fan-out that is the last absorbed sibling, not
  // the head (see `isBareDispatch`). Read it from the whole group so the fold
  // and the meta row do not vanish with the bubble that used to own them.
  const group = [msg, ...batch]
  const thought = group.find((m) => m.thought)?.thought ?? ''

  const name =
    speaker.who === 'agent'
      ? prettyAgent(speaker.agent)
      : speaker.who === 'user'
        ? t('sessions.you')
        : t('sessions.speakerPanda')
  // An employee's chip is the state of their job — that is what the row is
  // about. Everyone else's chip names the part they are playing.
  const chip =
    report && speaker.role === 'report'
      ? t(`state.${report.state}`)
      : t(`sessions.role.${speaker.role}`)
  const chipState = report && speaker.role === 'report' ? report.state : speaker.role

  return (
    <div class={`msg ${speaker.who} is-${speaker.role}`}>
      {speaker.who !== 'user' && <ChatAvatar speaker={speaker} />}
      <div class="msg-body">
        {/* — the head: who is talking, what they are doing here, and when.
              The duration used to sit above the reply as a caption with a
              hairline under it; in a chat it belongs beside the clock.
              Your own row keeps only the clock: the right side of the column
              already says who it is, and a face of your own beside it would be
              saying it a second time. */}
        <div class="msg-head">
          {speaker.who !== 'user' && (
            <>
              <span class="msg-name">{name}</span>
              <span class={`msg-role is-${chipState}`}>{chip}</span>
            </>
          )}
          <span class="msg-stamp">
            {msg.ts ? (
              <time
                class="msg-time"
                dateTime={new Date(msg.ts * 1000).toISOString()}
                title={clockStamp(msg.ts, locale())}
              >
                {clockTime(msg.ts, locale())}
              </time>
            ) : null}
            {msg.streaming && liveFrom > 0 ? (
              <LiveElapsed from={liveFrom} />
            ) : elapsedMs !== undefined ? (
              <span class="msg-dur">{t('sessions.elapsed', { d: fmtDuration(elapsedMs) })}</span>
            ) : null}
          </span>
        </div>

        <div
          class={`msg-bubble${speaker.who === 'user' ? ' is-you' : ''}${
            isTaskKind || isPlanKind ? ' is-wide' : ''
          }`}
        >
          {/* — what the panda is thinking on its way to a plan. It streamed
                into state and never reached the page before this; a reader who
                wants to know why the job was cut up the way it was gets to
                look. Open while it is still arriving, folded once the reply
                lands, because by then the answer is what matters. */}
          {thought && (
            <details class="msg-think" open={Boolean(msg.streaming)}>
              <summary>{t('sessions.thought')}</summary>
              <p class="msg-think-text">{thought}</p>
            </details>
          )}

          {msg.role === 'user' ? (
            <p class="msg-text">{msg.text}</p>
          ) : msg.kind === 'error' ? (
            <p class="msg-text">{msg.text}</p>
          ) : report ? (
            // The employee's report: the job as its subject, their own closing
            // paragraph under it. The state is on the head's chip.
            <>
              {report.title && <p class="msg-subject">{report.title}</p>}
              {report.body && <Markdown text={report.body} class="msg-text" />}
            </>
          ) : isTaskKind ? (
            // The orchestration: who got the job, which stage, how it stands —
            // and, for a fanned-out round, the same for every sibling in the
            // bubble below it.
            <>
              <DispatchBlock msg={msg} onOpenTask={onOpenTask} tasksById={tasksById} />
              {batch.map((b) => (
                <DispatchBlock key={b.k} msg={b} onOpenTask={onOpenTask} tasksById={tasksById} />
              ))}
            </>
          ) : (
            <>
              {text && <Markdown text={text} class="msg-text" streaming={msg.streaming} />}
              {isPlanKind && plan && <PlanCast plan={plan} />}
            </>
          )}

          {/* The task card lives inside `DispatchBlock` — it is part of the
                per-task block, so a round that handed out five jobs draws five
                of them in this one bubble. */}

          {/* The live ask path carries its approval state on the result, not
              through a persisted turn ref — a parked tier-2 task must still
              render its decision card in the same breath it parked in, and a
              remembered answer must say so where the user can see it. */}
          {/* One row per group member that has something to say: the round's
              receipt can sit on any of the absorbed turns (see
              `isBareDispatch`), and dropping the row because the head is empty
              would swallow an approval or an advisory. `MetaRow` renders
              nothing for a member with no meta, so the common case is one
              empty fragment. */}
          {group.map((m) => (
            <MetaRow key={`meta-${m.k}`} msg={m} />
          ))}

          {/* — slot-hints: troubleshooting fold under a classified model error.
              err_kind is also persisted on the turn, so a reloaded thread
              shows the same fold the live stream drew. */}
          {msg.kind === 'error' && msg.err_kind && (
            <ErrHints kind={msg.err_kind} detail={msg.err_detail} />
          )}
          {msg.kind === 'error' && msg.code === 'model_config' && (
            <a class="btn small" href="#/settings?tab=models">
              {t('sessions.fixModel')}
            </a>
          )}

          {/* — the live state, at the foot of the bubble: thinking, or what the
                turn is doing right now. */}
          {(hasThinking || hasTaskStatus) && (
            <div class="bubble-slot-row slot-status">
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
          )}
        </div>

        {/* The reply is copied as the Markdown it was written in, the way
            `/copy` does in the TUI — the rendered text would lose its
            structure the moment it is pasted anywhere. It stays away while
            the turn is still streaming: half an answer is not the message. */}
        {msg.text && !msg.streaming && (
          <div class="msg-foot">
            <MessageCopyButton text={msg.text} markdown={msg.kind !== 'error'} />
            {/* A task's own card already carries the employee in its cast
                line; this is the plain-answer case. */}
            {!isTaskKind && speaker.who === 'panda' && replyModel && (
              <span class="slot-model">{replyModel}</span>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/** One dispatched task inside a reply: its dispatch line, the cast row naming
 *  the employee and the stage, and the card that opens it.
 *
 *  It is a block rather than part of `ChatBubble` because a single reply can
 *  carry several: a round that fanned out is filed as one turn per task and
 *  drawn as one bubble, so the bubble holds one of these per job. The task row
 *  is read here, per block — the only place that knows who is actually running
 *  it. */
function DispatchBlock(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  tasksById?: Map<string, Task>
}) {
  const { msg, onOpenTask, tasksById } = props
  const [chainOpen, setChainOpen] = useState(false)
  // The thread's list is polled once per frame already; reading this block's
  // row from it keeps a five-card round from issuing five extra requests per
  // frame. The own read is only for a task outside the list.
  const listed = msg.ref ? tasksById?.get(msg.ref) : undefined
  const fetched = useTaskBrief(listed ? undefined : msg.kind === 'task' ? msg.ref : undefined)
  const task = listed ?? fetched
  const text = stripRefId(msg.text ?? '', msg.ref)
  return (
    <div class="msg-batch-item">
      {text && <Markdown text={text} class="msg-text" />}
      {task && <TaskCast task={task} />}
      {msg.ref && (
        <div class="task-card u-flex-1 u-min-w-0">
          {/* A task that needs consent must be decidable where the user is
              looking. Without this the conversation only said "review" and the
              decision lived on another page — so it waited, or worse, was never
              seen. Nothing else may answer it on the user's behalf: this is the
              web twin of the TUI's approval card. */}
          <TaskApproval taskId={msg.ref} hint={task} />
          {msg.result?.report && <Markdown text={msg.result.report} class="task-report" />}
          {msg.result?.stdout && <TaskOutput text={msg.result.stdout} />}
          {/* One quiet row of controls: open the chain, talk to the employee,
              jump into the task. As three stacked children of the card's column
              they read as a form — a chip-shaped toggle above a full-width
              centred button — instead of what they are, three links about the
              same job. */}
          <div class="task-card-actions">
            <button class="chain-toggle" onClick={() => setChainOpen((v) => !v)}>
              {chainOpen ? t('sessions.chainHide') : t('sessions.chainShow')}
            </button>
            <TaskQuickSend taskId={msg.ref} hint={task} />
            <a
              class="task-card-open"
              href={`#/task/${encodeURIComponent(msg.ref)}`}
              onClick={(e) => inPlaceTaskClick(e, msg.ref!, onOpenTask)}
            >
              {t('sessions.viewTask')} →
            </a>
          </div>
          {chainOpen && <TaskChain taskId={msg.ref} />}
        </div>
      )}
    </div>
  )
}

/** A chat row. The employee's own progress row does *not* hang off it any
 *  more: a still-running job belongs at the foot of the thread, not under the
 *  bubble that dispatched it (see `liveTaskIds` / `TaskProgress`). */
function ChatTurn(props: {
  msg: ChatMsg
  onOpenTask(id: string): void
  elapsedMs?: number
  liveFrom?: number
  batch?: ChatMsg[]
  /** The session's own rows for this thread's tasks, keyed by id. It is
   *  fresher than a fetch and costs no request; a block whose task is not in
   *  the list (one delegated elsewhere, or the first paint before the list
   *  lands) falls back to its own read. */
  tasksById?: Map<string, Task>
}) {
  const { msg } = props
  const plan = usePlanBrief(msg.kind === 'plan' ? msg.ref : undefined)
  return <ChatBubble {...props} plan={plan} />
}

/** Whether the thread's round still has work in flight.
 *
 *  The roll-up under the transcript says "本轮工作已全部完成", so it may only
 *  appear once every task this thread dispatched has settled. The turns alone
 *  cannot answer that — the finalizer files one report per task as each one
 *  lands, so the newest turn is a finished employee while its colleagues are
 *  still running (see `roundStillOpen`). Nor is every `review` row settled: a
 *  row parked before execution — 待审批, "尚未执行——批准后将继续执行" — has not
 *  done its work yet, so it holds the round open the same way a running row
 *  does. The task board knows both, so it is read here and re-read on every
 *  change frame. Only a thread that dispatched something pays for the read.
 *
 *  The same list answers the other half of the question — which employees are
 *  working right now, see `liveTaskIds`. It is one read, not two. */
function useSessionTasks(sessionId: string | null, enabled: boolean): Task[] | null {
  const change = useChangeSignal()
  const { data } = useAsync<Task[] | null>(
    () =>
      enabled && sessionId
        ? api.tasks({ session: sessionId, brief: true }).catch(() => null)
        : Promise.resolve(null),
    [sessionId, enabled],
    change,
  )
  return data
}

/** The panda's closing line for a round that just finished.
 *
 *  The employees have each said what they did; what a round still owes the
 *  reader is the roll-up — it is over, how the batch split, this many people
 *  worked on it, here is where the work lives. It is derived from the turns
 *  already in the thread (see `trailingRunSummary`), not a stored message:
 *  nothing new is written to the transcript, so the model's own history is
 *  untouched. */
function RunSummaryRow({ run, onOpenTask }: { run: RunSummary; onOpenTask(id: string): void }) {
  // A round is a batch. Reading its verdict off the last report alone said
  // "已失败" over two employees who finished, so the split is counted here and
  // the headline names it: all home is "done", some home is "finished — 2 done,
  // 1 failed", nothing home keeps the single-state line.
  const total = run.outcomes.length
  const ok = run.outcomes.filter((s) => s === 'done').length
  const mixed = ok > 0 && ok < total
  const line = mixed
    ? t('sessions.runSummary.partial', {
        parts: roundTally(run.outcomes)
          .map(({ state, n }) => t(`sessions.runSummary.tally.${state}`, { n: String(n) }))
          .join(t('sessions.runSummary.sep')),
      })
    : ok === total && total > 0
      ? t('sessions.runSummary.done')
      : t('sessions.runSummary.ended', { state: t(`state.${run.state}`) })
  // The dot carries the batch's tone, not the last employee's: one failure
  // among successes is amber, and only a round where nobody finished is red.
  const tone = mixed ? 'warn' : ok === total && total > 0 ? 'ok' : stateTone(run.state)
  // Three jobs off one dispatch template share a head ("连通性测试：调用
  // agent:") that the reader does not need three times.
  const prefix = sharedTitlePrefix(run.titles)
  const titles = prefix ? run.titles.map((title) => title.slice(prefix.length).trim()) : run.titles
  const ref = run.ref
  const isTaskRef = run.refKind === 'task'
  const isPlanRef = run.refKind === 'plan'
  return (
    <div class="msg panda is-summary">
      <ChatAvatar speaker={{ who: 'panda', agent: '', role: 'summary' }} />
      <div class="msg-body">
        <div class="msg-head">
          <span class="msg-name">{t('sessions.speakerPanda')}</span>
          <span class="msg-role is-summary">{t('sessions.role.summary')}</span>
        </div>
        <div class="msg-bubble is-summary">
          <p class="msg-summary-line">
            <span class={`msg-cast-state is-${tone}`}>
              <span class="msg-report-dot" aria-hidden="true" />
              {line}
            </span>
            {/* The mixed headline already counts everyone; repeating the
                headcount under it would be the same number twice. */}
            {!mixed && run.agents.length > 0 && (
              <span class="msg-summary-people">
                {t('sessions.runSummary.people', { n: String(run.agents.length) })}
              </span>
            )}
          </p>
          {/* A single job is already named by the row above; the recap only
              earns its line when the round covered more than one. */}
          {titles.length > 1 && <p class="msg-summary-what">{titles.join(' · ')}</p>}
          {ref && isTaskRef && (
            <a
              class="msg-summary-link"
              href={`#/task/${encodeURIComponent(ref)}`}
              onClick={(e) => inPlaceTaskClick(e, ref, onOpenTask)}
            >
              {t('sessions.viewTask')} →
            </a>
          )}
          {ref && isPlanRef && (
            <a class="msg-summary-link" href="#/control?tab=plans">
              {t('sessions.castPlan')} →
            </a>
          )}
        </div>
      </div>
    </div>
  )
}

/** Inline approval gate for a task referenced by a chat bubble.
 *
 *  It re-reads the task on every change signal, so a task that parks in review
 *  after the turn already finished (a queued run, a peer's delegation landing
 *  back here) still surfaces its decision in the conversation rather than only
 *  on the board. Approve/Reject call the same endpoints the queue and detail
 *  views use; needs_changed_input disables Approve, because consent alone
 *  cannot continue that task and a button that silently does nothing is worse
 *  than an honest disabled one. */
function TaskApproval({
  taskId,
  preselect,
  hint,
}: {
  taskId: string
  preselect?: ApprovalScope
  /** The bubble's own copy of the row, when it has one — the thread's task
   *  list is already polled once per frame, so a second fetch per bubble is
   *  load with no fresher answer. */
  hint?: Task | null
}) {
  const change = useChangeSignal()
  const { data: fetched } = useAsync<Task | null>(
    () => (hint ? Promise.resolve(null) : api.taskBrief(taskId).catch(() => null)),
    [taskId, Boolean(hint)],
    change,
  )
  const task = hint ?? fetched
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // The remember scope: where this answer lands for the gates that come after.
  // The server names its default (the project's configured scope) so the card
  // preselects it; the user can still pick differently per decision.
  const [scope, setScope] = useState<ApprovalScope>(preselect ?? 'once')
  useEffect(() => {
    if (preselect) setScope(preselect)
  }, [preselect])
  // A stalled task emits no SSE event — nothing changes — so the badge can
  // only appear on a local clock tick.
  const [, setStallTick] = useState(0)
  useVisibleInterval(() => setStallTick((v) => v + 1), 30_000)

  if (!task) return null
  if (task.state !== 'review') {
    // The other half of "no silent stall": a task the conversation spawned
    // that stopped moving without reaching a decision shows it right in the
    // thread, not only on the board.
    if (!isTaskStalled(task)) return null
    return (
      <div class="task-approval" data-testid="chat-task-stalled">
        <span class="badge yellow">{t('task.stalled')}</span>
        <span class="dim">{t('task.stalledHint')}</span>
      </div>
    )
  }

  async function act(fn: () => Promise<unknown>) {
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  // needs_changed_input is the one disposition where consent is not the
  // missing piece: the task failed in a way that only a different prompt can
  // fix (a deterministic crash, a spent token budget, an overflowed context),
  // so Approve is disabled — and a disabled button that says nothing is the
  // "it won't let me approve" report this card kept generating. The line below
  // the buttons says why, and what the two working exits are.
  const blocked = task.approval_disposition === 'needs_changed_input'

  return (
    <div class="task-approval" data-testid="chat-task-approval">
      <span class="badge yellow">{t('state.review')}</span>
      {task.approval_disposition && (
        <span class="dim review-kind">
          {t(`detail.reviewKind.${task.approval_disposition}`)}
        </span>
      )}
      <ScopeSelect value={scope} onChange={setScope} disabled={busy} />
      <button
        class="btn primary small"
        disabled={busy || blocked}
        onClick={() => void act(() => api.approve(task.id, scope))}
      >
        {t('detail.approve')}
      </button>
      <button
        class="btn danger small"
        disabled={busy}
        onClick={() => void act(() => api.reject(task.id, t('detail.rejectedViaWeb'), scope))}
      >
        {t('detail.reject')}
      </button>
      {blocked && <p class="gate-hint">{t('detail.approveBlocked')}</p>}
      {error && <p class="gate-error">{error}</p>}
    </div>
  )
}

/** TaskQuickSend delivers a message into the task straight from its card in
 *  the thread — the same two modes the detail view and TUI expose: queue
 *  (the agent reads it at its next round) or inject (a steer that leads the
 *  next round, aborting the in-flight one when this console runs the task).
 *  Renders nothing once the task is terminal: a finished task refuses
 *  messages, and a button that can only fail is worse than none. */
function TaskQuickSend({ taskId, hint }: { taskId: string; hint?: Task | null }) {
  const change = useChangeSignal()
  const { data: fetched } = useAsync<Task | null>(
    () => (hint ? Promise.resolve(null) : api.taskBrief(taskId).catch(() => null)),
    [taskId, Boolean(hint)],
    change,
  )
  const task = hint ?? fetched
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  if (!task || ['done', 'failed', 'cancelled', 'expired'].includes(task.state)) return null

  async function deliver(inject: boolean) {
    const body = text.trim()
    if (!body || busy) return
    setBusy(true)
    try {
      const r = await api.sendTaskMessage(taskId, body, inject)
      toast(
        r.warning ||
          (r.status === 'interrupted' ? t('detail.msgInjectedToast') : t('detail.msgQueuedToast')),
        r.warning ? 'info' : 'success',
      )
      setText('')
      setOpen(false)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  if (!open) {
    return (
      <button class="btn small" onClick={() => setOpen(true)}>
        {t('sessions.msgSend')}
      </button>
    )
  }
  return (
    <div class="task-quick-send">
      <input
        class="input"
        type="text"
        placeholder={t('detail.msgPlaceholder')}
        value={text}
        onInput={(e) => setText((e.target as HTMLInputElement).value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') void deliver(false)
          if (e.key === 'Escape') setOpen(false)
        }}
      />
      <button class="btn small" disabled={busy || !text.trim()} onClick={() => deliver(false)}>
        {t('detail.msgQueue')}
      </button>
      <button class="btn small primary" disabled={busy || !text.trim()} onClick={() => deliver(true)}>
        {t('detail.msgInject')}
      </button>
    </div>
  )
}

/** Agent result text under a task bubble — markdown like the reply lane,
 *  collapsed past a few lines so a long dump never pushes the chat down. */
function TaskOutput({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const long = text.length > 600
  const shown = open || !long ? text : text.slice(0, 600) + '…'
  return (
    <div class="task-out-wrap">
      <Markdown text={shown} class="task-out" />
      {long && (
        <button type="button" class="btn small" onClick={() => setOpen((v) => !v)}>
          {open ? t('sessions.outputCollapse') : t('sessions.outputExpand')}
        </button>
      )}
    </div>
  )
}

/** The task's thinking chain: the task_events log replayed live — history
 *  comes from GET /api/tasks/{id}/logs, and the panel's SSE change signal
 *  triggers a refetch while the chain is open (design §5). */
function TaskChain({ taskId }: { taskId: string }) {
  const change = useChangeSignal()
  const { events, error } = useTaskEvents(taskId, change)
  if (error) return <p class="dim chain-empty">{t('common.error')} ({error})</p>
  if (events.length === 0) return <p class="dim chain-empty">{t('sessions.chainEmpty')}</p>
  return <EventTimeline events={events} className="task-chain-timeline" />
}

