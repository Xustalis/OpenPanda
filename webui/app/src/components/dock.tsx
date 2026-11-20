// SPDX-License-Identifier: AGPL-3.0-or-later

// The console's side dock: one column on the right of a thread that holds the
// surfaces you reach for while reading it — the files it names, a page it is
// talking about, and a conversation beside the one you are in. The thread's
// unfinished work is not a dock panel: the orchestration card in the pane head
// owns that list, so the same tasks do not appear twice in one view.
//
// It replaces two surfaces that used to have two different shapes: the file
// viewer (a horizontal drawer across the top) and the artifact preview (a modal
// dialog over the whole console). Each was the right answer to its own question
// and the wrong answer to "show me that beside what I am reading": a drawer that
// eats the top of the transcript moves nothing else out of the way, and a modal
// asks the reader to stop reading.
//
// The dock is a column, not a popover. Its width is the lane the chat pane
// reserves (`.chat-pane.dock-open`), so opening it slides the transcript, the
// composer and the jump arrow left together — the same promise the header cards
// keep, with one number instead of three.
//
// One head line, not two: `+`, the four places, the thread's tools and the
// fold control sit in the same row. A second rail would cost a column of width
// in a panel whose whole job is showing content beside the reading column.
//
// The order is by how often the hand reaches for the button: start something,
// go somewhere, then the tools that only this thread needs; the fold stays at
// the outer edge. Previously the tools opened the row and the tabs sat at its
// end, which put the six glyphs at one weight and left `+` stranded between
// two groups it belonged to neither. The tabs now wear a segmented well, so
// the row reads as one action, one place-group and one tool-group.
//
// Tabs keep their own state. Switching from files to a side conversation and
// back must land on the same file, scrolled where it was; that is the whole
// reason the panels stay mounted instead of being swapped out.
//
// The way back to the start pane is a crumb above the panel, not a fifth tab:
// `''` is not a place, and the head is already full at the column's floor. It
// is a step aside rather than a reset — the panels stay mounted, so nothing is
// thrown away on the way home.
//
// The empty column used to be three rows and a lot of white space. It now
// answers the three questions a thread's panel is opened to ask: where this
// runs, how full the window was on the last turn, and where to go next. The
// first two are one card of facts — the pane's only material step — and the
// four places sit under it, spelled out because a bare icon row does not say
// what a first-time reader should do. Nothing in the card is a control except
// the path's copy button: the panel is not a settings form.

import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { compactNumber } from '../format/number'
import { t } from '../i18n'
import { Icon, type IconName } from './icons'
import { contextShare, contextUsage, type ContextAgentView } from './session-rail-stats'

/** What the column can show. The first three are the thread's tools, reached
 *  from the left group in the head; the last four are the places a tab opens.
 *  All seven own a pane in the body — none of them is a floating layer over it.
 *  `''` means it is open but nothing is picked, the "start from here" state. */
export type DockTab = 'export' | 'node' | 'orch' | 'files' | 'changes' | 'browser' | 'side' | 'report' | ''

/** Narrower than this and a line of code wraps on every scroll; wider and the
 *  transcript stops being the page. Exported so the state that owns the width
 *  can restore a stored value inside the same bounds. */
export const DOCK_MIN_WIDTH = 320
export const DOCK_MAX_WIDTH = 760

/** The facts the start pane prints about the thread it belongs to. Every field
 *  is optional: a thread with no worktree, no branch and no turn yet still
 *  gets the "start from here" rows, just without the sections above them. */
export interface DockEnv {
  /** Where the turn will run, in the node picker's words. */
  machine: string
  /** The thread's worktree, or its project's directory. */
  path: string
  branch?: string
  /** Billed tokens of the newest turn, and the model's window when known. */
  contextTokens?: number
  contextWindow?: number
  model?: string
  /** The thread's working agent, when the context sample is its own rather
   *  than the entry model's. The caption already prints `model` (the agent's
   *  display name in that case); this flag only switches the explanatory
   *  sentence, which would otherwise call an agent a model. */
  contextAgent?: string
  /** Every agent working this thread, primary first. One entry keeps the
   *  card's single-number shape; several turn it into a row per agent — a
   *  team fills one window each and a lone percentage would hide the rest. */
  contextAgents?: ContextAgentView[]
  /** The worktree's change roll-up, when it has one. */
  changes?: { files: number; insertions: number; deletions: number }
}

interface TabSpec {
  key: Exclude<DockTab, ''>
  icon: IconName
  label: string
}

export interface DockProps {
  /** Whether the column is on screen at all. */
  open: boolean
  /** Which panel it holds. Kept across a collapse, so reopening returns to it. */
  tab: DockTab
  width: number
  onTab(tab: Exclude<DockTab, ''>): void
  /** Back to the start pane — the thread's overview. A pane replaces those
   *  rows, and `''` is not a place, so this is a step back rather than a
   *  fifth tab. The button lives in the head, beside `+`. */
  onHome(): void
  onToggle(): void
  onWidth(width: number): void
  /** What the start pane says about this thread. */
  env?: DockEnv
  /** The `+` in the head: a fresh object in the current panel (a new page, a
   *  new side conversation, the workspace root). The button is always shown
   *  while the column is open; without a handler it opens a fresh side chat. */
  onNew?(): void
  /** A second click on the tab already showing. The side conversation uses it
   *  to open its history; without a handler the click folds the column, which
   *  is what every other tab still wants. */
  onReselect?(tab: Exclude<DockTab, ''>): void
  /** Counts shown as a badge on a tab. */
  badges?: Partial<Record<Exclude<DockTab, ''>, number>>
  /** The thread's actions, parked at the far end of the head: export, the node
   *  pick and the orchestration card. They live here rather than in the chat
   *  head so the two cards of the workbench do not carry the same controls
   *  twice. Only mounted while the column is open. */
  tools?: ComponentChildren
  /** The panel body. Rendered only once a tab is picked. */
  children?: ComponentChildren
}

export function Dock(props: DockProps) {
  const { open, tab, width, onTab, onToggle, onWidth } = props
  const [dragging, setDragging] = useState(false)
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<number | undefined>(undefined)
  const start = useRef<{ x: number; w: number } | null>(null)

  // The drag is owned by the window: the handle is a hairline and the pointer
  // leaves it on the first pixel of travel.
  useEffect(() => {
    if (!dragging) return
    const move = (e: PointerEvent) => {
      const s = start.current
      if (!s) return
      if (e.buttons === 0) {
        setDragging(false)
        return
      }
      const next = Math.round(s.w - (e.clientX - s.x))
      onWidth(Math.min(DOCK_MAX_WIDTH, Math.max(DOCK_MIN_WIDTH, next)))
    }
    const end = () => setDragging(false)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', end)
    window.addEventListener('pointercancel', end)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', end)
      window.removeEventListener('pointercancel', end)
    }
  }, [dragging, onWidth])

  const tabs: TabSpec[] = [
    { key: 'files', icon: 'folder', label: t('dock.files') },
    { key: 'changes', icon: 'file-diff', label: t('dock.changes') },
    { key: 'browser', icon: 'globe', label: t('dock.browser') },
    { key: 'side', icon: 'message', label: t('dock.side') },
  ]

  const pick = (key: Exclude<DockTab, ''>) => {
    // A second click on the tab already showing is that surface's own
    // gesture: the side conversation opens its history popover, the rest fold
    // the column. Folding stays the default — the rail is the map, and
    // clicking the place you already are is how you leave it.
    if (open && tab === key) {
      if (props.onReselect) props.onReselect(key)
      else onToggle()
      return
    }
    onTab(key)
  }

  // The body stays mounted while the column is folded or another tab is
  // picked: switching from a file to a page and back must land on the same
  // file at the same scroll offset, and a fold is not a reason to throw the
  // reader's place away. Visibility is the only thing that changes.
  const showBody = tab !== ''
  const env = props.env
  const tokens = env?.contextTokens ?? 0
  const share = contextShare(tokens, env?.contextWindow ?? 0)
  const used = tokens > 0 ? compactNumber(tokens) : ''
  const windowLabel = (env?.contextWindow ?? 0) > 0 ? compactNumber(env!.contextWindow!) : ''
  // A team fills one window per agent. One sample keeps the single-number
  // card; several become a row each, because the number that matters is the
  // one closest to its ceiling and a lone bar would hide the others.
  const agents = env?.contextAgents ?? []
  const changeHint =
    env?.changes && env.changes.files > 0
      ? t('dock.changesHintCount', {
          n: String(env.changes.files),
          ins: String(env.changes.insertions),
          del: String(env.changes.deletions),
        })
      : t('dock.changesHint')
  // The agent owns the number while it works; the sentence under the gauge
  // has to say so, or "这个模型没有已知的上下文窗口" reads as a broken model
  // row instead of a CLI that does not report its window.
  const contextHint = env?.contextAgent
    ? (env.contextWindow ?? 0) > 0
      ? t('dock.contextAgentNote', { agent: env.contextAgent })
      : t('dock.contextAgentNoWindow', { agent: env.contextAgent })
    : share === null
      ? t('dock.contextNoWindow')
      : t('dock.contextNote')

  async function copyPath(path: string) {
    try {
      await navigator.clipboard.writeText(path)
      setCopied(true)
      if (copyTimer.current !== undefined) window.clearTimeout(copyTimer.current)
      copyTimer.current = window.setTimeout(() => setCopied(false), 1400)
    } catch {
      // Clipboard denied: the path stays selectable in the row.
    }
  }

  useEffect(
    () => () => {
      if (copyTimer.current !== undefined) window.clearTimeout(copyTimer.current)
    },
    [],
  )

  const startPane = open && !showBody ? (
    <div class="dock-start">
      {/* Where this thread runs. Claude's panel opens with the environment for
          the same reason: a conversation is pinned to a machine and a
          directory, and both are invisible everywhere else in the pane. */}
      {((env && (env.machine || env.path || env.branch)) || tokens > 0) && (
        <section class="dock-facts">
          {env?.machine && <p class="dock-facts-title">{env.machine}</p>}
          {env?.path && (
            <div class="dock-facts-row">
              <span class="mono" title={env.path}>
                {env.path}
              </span>
              <button
                type="button"
                class="icon-btn dock-facts-copy"
                title={copied ? t('dock.copied') : t('fileView.copy')}
                aria-label={t('dock.copyPath')}
                onClick={() => void copyPath(env.path)}
              >
                <Icon name={copied ? 'check' : 'copy'} size={13} />
              </button>
            </div>
          )}
          {env?.branch && (
            <div class="dock-facts-row" title={env.branch}>
              <span class="mono">{env.branch}</span>
            </div>
          )}

          {/* How full the window was on the last request: the one display
              number in the pane, a hairline gauge under it, and the raw counts
              with the model as captions. The share is omitted when the model
              row has no catalogue window — a made-up denominator would be
              worse than the count alone. */}
          {tokens > 0 && (
            <>
              {(env?.machine || env?.path || env?.branch) && <div class="dock-facts-rule" />}
              {agents.length > 1 ? (
                <div class="dock-ctx dock-ctx-multi">
                  <div class="dock-ctx-head">
                    <span class="dock-ctx-label">{t('dock.context')}</span>
                  </div>
                  <div class="dock-ctx-list">
                    {agents.slice(0, 4).map((agent) => {
                      const agentShare = contextShare(agent.tokens, agent.window)
                      const high = agentShare !== null && agentShare >= 80
                      return (
                        <div key={agent.name} class="dock-ctx-agent">
                          <div class="dock-ctx-agent-head">
                            <span class="dock-ctx-agent-name" title={agent.name}>
                              {agent.name}
                            </span>
                            <span class={`dock-ctx-agent-pct${high ? ' is-high' : ''}`}>
                              {agentShare === null ? '—' : `${agentShare}%`}
                            </span>
                          </div>
                          <span class="dock-ctx-meter" aria-hidden="true">
                            <i style={`width:${agentShare ?? 0}%`} />
                          </span>
                          <div class="dock-ctx-agent-foot">{contextUsage(agent.tokens, agent.window)}</div>
                        </div>
                      )
                    })}
                  </div>
                  {agents.length > 4 && (
                    <p class="dock-ctx-more">
                      {t('sessionRail.contextMoreAgents', { n: String(agents.length - 4) })}
                    </p>
                  )}
                </div>
              ) : (
                <div
                  class="dock-ctx"
                  title={contextHint}
                >
                  <div class="dock-ctx-head">
                    <span class="dock-ctx-label">{t('dock.context')}</span>
                    <span class={`dock-ctx-pct${share !== null && share >= 80 ? ' is-high' : ''}`}>
                      {share === null ? '—' : `${share}%`}
                    </span>
                  </div>
                  <span class="dock-ctx-meter" aria-hidden="true">
                    <i style={`width:${share ?? 0}%`} />
                  </span>
                  <div class="dock-ctx-foot">
                    <span class="dock-ctx-num mono">
                      {used}
                      {windowLabel ? ` / ${windowLabel}` : ''}
                    </span>
                    {env?.model && (
                      <span class="dock-ctx-model" title={env.model}>
                        {env.model}
                      </span>
                    )}
                  </div>
                </div>
              )}
            </>
          )}
        </section>
      )}

      <section class="dock-acts">
        <p class="dock-acts-label">{t('dock.start')}</p>
        <button type="button" class="dock-start-row" onClick={() => onTab('files')}>
          <Icon name="folder" size={16} />
          <span class="dock-start-text">
            <span class="dock-start-name">{t('dock.files')}</span>
            <span class="dock-start-hint">{t('dock.filesHint')}</span>
          </span>
        </button>
        <button type="button" class="dock-start-row" onClick={() => onTab('changes')}>
          <Icon name="file-diff" size={16} />
          <span class="dock-start-text">
            <span class="dock-start-name">{t('dock.changes')}</span>
            <span class="dock-start-hint">{changeHint}</span>
          </span>
        </button>
        <button type="button" class="dock-start-row" onClick={() => onTab('browser')}>
          <Icon name="globe" size={16} />
          <span class="dock-start-text">
            <span class="dock-start-name">{t('dock.browser')}</span>
            <span class="dock-start-hint">{t('dock.browserHint')}</span>
          </span>
        </button>
        <button type="button" class="dock-start-row" onClick={() => onTab('side')}>
          <Icon name="message" size={16} />
          <span class="dock-start-text">
            <span class="dock-start-name">{t('dock.side')}</span>
            <span class="dock-start-hint">{t('dock.sideHint')}</span>
          </span>
        </button>
      </section>
    </div>
  ) : null

  return (
    <aside
      class={`dock${open ? ' is-open' : ''}${dragging ? ' is-dragging' : ''}`}
      aria-label={t('dock.label')}
      style={open ? `--dock-w:${width}px` : undefined}
    >
      {open && (
        <div
          class="dock-grip"
          role="separator"
          aria-orientation="vertical"
          aria-label={t('dock.resize')}
          onPointerDown={(e) => {
            if (e.button !== 0) return
            start.current = { x: e.clientX, w: width }
            setDragging(true)
          }}
        />
      )}

      <header class="dock-head">
        {/* `+` opens the row and never leaves it. It used to be hidden on the
            three tool pages and on the start state: the one control that says
            "start fresh" disappeared exactly where there was nothing fresh to
            lose. Every pane has an answer for it now (the caller owns which
            one), and a Dock without an `onNew` opens a side conversation. */}
        {open && (
          <button
            type="button"
            class="icon-btn dock-new"
            title={t('dock.new')}
            aria-label={t('dock.new')}
            onClick={() => (props.onNew ? props.onNew() : onTab('side'))}
          >
            <Icon name="plus" size={16} />
          </button>
        )}

        {/* The way home sits beside `+`, not in a crumb line of its own: the
            overview is where `+` and the four places both start, and the crumb
            it used to be cost every pane a 30px row above its own header. It
            is a house, not an arrow — the destination is fixed, so it should
            not read as "one step back" in a history. Greyed while the start
            pane is showing — there is nothing behind it then — rather than
            coming and going, which would slide the four places sideways on
            every tab click. */}
        {open && (
          <button
            type="button"
            class="icon-btn dock-home"
            title={t('dock.overview')}
            aria-label={t('dock.overview')}
            disabled={!showBody}
            onClick={() => props.onHome()}
          >
            <Icon name="home" size={16} />
          </button>
        )}

        {/* The four places share one segmented well: files, the thread's
            changes, the page being read, and the conversation beside this
            one. Keeping them together makes the lit tab read as "where you
            are" rather than as one of seven equally weighted glyphs. */}
        {open && (
          <nav class="dock-tabs" aria-label={t('dock.label')}>
            {tabs.map(({ key, icon, label }) => {
              const n = props.badges?.[key] ?? 0
              return (
                <button
                  key={key}
                  type="button"
                  class={`dock-tab${tab === key ? ' on' : ''}`}
                  title={label}
                  aria-label={label}
                  aria-pressed={tab === key}
                  onClick={() => pick(key)}
                >
                  <Icon name={icon} size={18} />
                  {n > 0 && <span class="dock-badge mono">{n > 99 ? '99+' : n}</span>}
                </button>
              )
            })}
          </nav>
        )}

        {/* The thread-only tools sit at the far end, next to the fold: export,
            node pick and orchestration are reached for far less often than a
            place, and they vary with the thread (the export one only exists
            once there is a transcript). The hairline is the group boundary. */}
        {open && props.tools && <div class="dock-tools">{props.tools}</div>}

        <button
          type="button"
          class="icon-btn dock-fold"
          title={open ? t('dock.collapse') : t('dock.expand')}
          aria-label={open ? t('dock.collapse') : t('dock.expand')}
          aria-expanded={open}
          onClick={onToggle}
        >
          <Icon name="panel-right" size={17} />
        </button>
      </header>

      <div class="dock-body" data-tab={tab} hidden={!open || !showBody}>
        {props.children}
      </div>
      {startPane}
    </aside>
  )
}
