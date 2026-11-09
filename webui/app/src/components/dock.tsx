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
// One head line, not two: `+`, the three places, the thread's tools and the
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

import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { Icon, type IconName } from './icons'

/** What the column can show. The first three are the thread's tools, reached
 *  from the left group in the head; the last three are the places a tab opens.
 *  All six own a pane in the body — none of them is a floating layer over it.
 *  `''` means it is open but nothing is picked, the "start from here" state. */
export type DockTab = 'export' | 'node' | 'orch' | 'files' | 'browser' | 'side' | ''

/** Narrower than this and a line of code wraps on every scroll; wider and the
 *  transcript stops being the page. Exported so the state that owns the width
 *  can restore a stored value inside the same bounds. */
export const DOCK_MIN_WIDTH = 320
export const DOCK_MAX_WIDTH = 760

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
  onToggle(): void
  onWidth(width: number): void
  /** The `+` in the head: a fresh object in the current panel (a new page, a
   *  new side conversation, the workspace root). The button is always shown
   *  while the column is open; without a handler it opens a fresh side chat. */
  onNew?(): void
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
    { key: 'browser', icon: 'globe', label: t('dock.browser') },
    { key: 'side', icon: 'message', label: t('dock.side') },
  ]

  const pick = (key: Exclude<DockTab, ''>) => {
    // A second click on the tab already showing folds the column: the rail is
    // the map, and clicking the place you already are is how you leave it.
    if (open && tab === key) {
      onToggle()
      return
    }
    onTab(key)
  }

  // The body stays mounted while the column is folded or another tab is
  // picked: switching from a file to a page and back must land on the same
  // file at the same scroll offset, and a fold is not a reason to throw the
  // reader's place away. Visibility is the only thing that changes.
  const showBody = tab !== ''
  const startPane = open && !showBody ? (
    <div class="dock-start">
      <p class="dock-start-title">{t('dock.start')}</p>
      <button type="button" class="dock-start-row" onClick={() => onTab('files')}>
        <Icon name="folder" size={18} />
        <span class="dock-start-name">{t('dock.files')}</span>
        <span class="dock-start-hint">{t('dock.filesHint')}</span>
      </button>
      <button type="button" class="dock-start-row" onClick={() => onTab('browser')}>
        <Icon name="globe" size={18} />
        <span class="dock-start-name">{t('dock.browser')}</span>
        <span class="dock-start-hint">{t('dock.browserHint')}</span>
      </button>
      <button type="button" class="dock-start-row" onClick={() => onTab('side')}>
        <Icon name="message" size={18} />
        <span class="dock-start-name">{t('dock.side')}</span>
        <span class="dock-start-hint">{t('dock.sideHint')}</span>
      </button>
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

        {/* The three places share one segmented well: files, the page being
            read, and the conversation beside this one. Keeping them together
            makes the lit tab read as "where you are" rather than as one of six
            equally weighted glyphs. */}
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