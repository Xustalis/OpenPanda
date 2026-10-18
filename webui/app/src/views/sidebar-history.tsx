// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, type Session } from '../api/client'
import { busSubscribeChange } from '../hooks'
import { locale, t } from '../i18n'
import { relativeTime } from '../format/time'
import { groupSessions, pruneSelection, sortSessions, threadMark, threadMarkKey } from '../components/session-list'
import { Icon } from '../components/icons'
import { toastError, toast } from '../components/toast'
import { confirmDialog } from '../components/confirm'

/** Which run of each thread the reader has already looked at — session id to
 *  threadMarkKey. Persisted, because "read" has to survive a reload: a mark
 *  that returns every time the console opens is noise, not a reminder. */
const SEEN_KEY = 'openpanda.thread-marks-seen'

function readSeen(): Record<string, string> | null {
  try {
    const raw = localStorage.getItem(SEEN_KEY)
    // Never recorded: the caller seeds the first listing as read, so a fresh
    // browser does not greet you with a dot on every thread you finished last
    // week.
    if (raw === null) return null
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return parsed as Record<string, string>
  } catch {
    // Private mode, quota, corrupted JSON: the marks still work for this tab,
    // only the persistence is lost.
    return {}
  }
}

function writeSeen(map: Record<string, string>) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(map))
  } catch {
    /* see readSeen: in-memory marks are better than none */
  }
}

/** The conversation history, living in the sidebar under the 对话 nav item.
 *
 *  It used to be a separate rail column beside the transcript — a whole
 *  244px of chrome spent naming what this component now names from inside
 *  the navigation the user was already reading. Groups by working directory
 *  (the headers say where a thread lives), newest first, with pin and delete
 *  on the row. A directory header is itself the fold control — no caret, just
 *  click the label: the list is the tallest block in the sidebar, so folding
 *  one directory is how you get the nav below it back without folding the
 *  whole history.
 *
 *  Each header also carries a trailing "…" that opens what you can do to the
 *  *directory* rather than to one thread: reveal it in the OS file manager,
 *  or switch the list into batch mode. Batch mode is where the per-row pin and
 *  delete step aside for checkboxes, a select-all and one bulk delete — the
 *  rail had that chrome before it moved into the sidebar, and a history you
 *  can only prune one row at a time is a history that never gets pruned. */
export function SidebarHistory({
  activeId,
  onOpenSession,
}: {
  activeId: string | null
  onOpenSession(id: string): void
}) {
  const [sessions, setSessions] = useState<Session[]>([])
  const [loadError, setLoadError] = useState('')
  // Folded directories, by group key ('' is the unassigned bucket). Absent
  // means open — a directory nobody has touched reads as it always did.
  const [folded, setFolded] = useState<Record<string, boolean>>({})
  // The open directory menu, pinned to the viewport: the nav column scrolls
  // and clips, so a popover positioned inside the row would be cut off the
  // moment the list is taller than the rail.
  const [menu, setMenu] = useState<{ gid: string; name: string; top: number; right: number } | null>(
    null,
  )
  // Batch ops: which directory is in edit mode, the checked ids, and a
  // running bulk delete. The mode belongs to ONE directory — "manage" is a
  // gesture on a group header, so the checkboxes it turns on are that
  // directory's rows only. A global mode made every other project look
  // editable too, which is both confusing and one mis-click away from
  // deleting threads the user never looked at.
  const [editGid, setEditGid] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  // Null until the first listing seeds it (see readSeen): history from before
  // this browser starts read. After that, only runs the reader has not opened
  // carry a mark, and opening one clears it.
  const [seen, setSeen] = useState<Record<string, string> | null>(() => readSeen())

  useEffect(() => {
    api
      .sessions()
      .then((ls) => {
        setSessions(ls)
        setSeen((cur) => {
          if (cur !== null) return cur
          const seeded: Record<string, string> = {}
          for (const s of ls) seeded[s.id] = threadMarkKey(s)
          writeSeen(seeded)
          return seeded
        })
      })
      .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
  }, [])

  // Live rail: the panel pushes a 'sessions' fingerprint slot on the change
  // feed, so a thread created/deleted anywhere lands here without a refresh.
  useEffect(() => {
    let lastFP: string | undefined
    return busSubscribeChange((ev) => {
      const reconnect = ev.kinds.includes('reconnect')
      if (!reconnect && ev.sessionFP === lastFP) return
      lastFP = ev.sessionFP
      api
        .sessions()
        .then((ls) => setSessions(ls))
        .catch(() => {})
    })
  }, [])

  const ordered = useMemo(() => sortSessions(sessions), [sessions])
  const grouped = useMemo(() => groupSessions(ordered), [ordered])

  // A session deleted elsewhere must not linger as a ghost checkbox.
  useEffect(() => {
    setSelected((sel) => pruneSelection(sel, sessions))
  }, [sessions])

  // Outside click, Escape, scroll and resize all close the menu — the same
  // ways every other popover in the console closes, plus the two things that
  // would leave a fixed-position card pointing at nothing.
  useEffect(() => {
    if (!menu) return
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null
      if (!target) return
      // The trigger handles its own toggle; closing here too would make a
      // second click on the same "…" reopen instead of close.
      if (target.closest('.thread-menu') || target.closest('.thread-group-menu')) return
      setMenu(null)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      setMenu(null)
    }
    const onMove = () => setMenu(null)
    // Only the rail's own scroll moves the trigger out from under the card —
    // a transcript scrolling elsewhere on the page must not dismiss a menu the
    // user just opened.
    const onScroll = (e: Event) => {
      const target = e.target
      if (target === document) setMenu(null)
      else if ((target as HTMLElement | null)?.closest?.('.primary-nav')) setMenu(null)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('resize', onMove)
    window.addEventListener('scroll', onScroll, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('resize', onMove)
      window.removeEventListener('scroll', onScroll, true)
    }
  }, [menu])

  // Half-checked while a subset is selected — the DOM has no JSX attribute
  // for it, so it lands on the element after paint (see GroupCheck).

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
    toast(t('sessions.deletedChats', { n: 1 }))
  }

  async function togglePin(s: Session) {
    try {
      const updated = await api.patchSession(s.id, { pinned: !s.pinned })
      setSessions((ls) => ls.map((x) => (x.id === s.id ? updated : x)))
    } catch (e) {
      toastError(e)
    }
  }

  /** Opening a thread is reading its latest run: stamp it, so the red alert or
   *  green dot goes away and only the next run brings one back. */
  function openThread(s: Session) {
    setSeen((cur) => {
      if (!cur) return cur
      const key = threadMarkKey(s)
      if (cur[s.id] === key) return cur
      const next = { ...cur, [s.id]: key }
      writeSeen(next)
      return next
    })
    onOpenSession(s.id)
  }

  function toggleGroupMenu(e: MouseEvent, gid: string, name: string) {
    e.stopPropagation()
    if (menu?.gid === gid) {
      setMenu(null)
      return
    }
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    // Two rows of 7px padding + 2px gaps ≈ 84px. A directory at the bottom of
    // the rail would otherwise open a card off the bottom of the screen, so
    // it flips above the trigger when there is no room below.
    const height = 84
    const below = rect.bottom + 4
    const top = below + height > window.innerHeight - 8 ? Math.max(8, rect.top - height - 4) : below
    setMenu({ gid, name, top, right: window.innerWidth - rect.right })
  }

  /** Reveal the group's working directory in the desktop's file manager. The
   *  header only knows the project's name, so the directory is resolved on
   *  the node — which also means the node decides whether it may be opened. */
  async function openFolder(name: string) {
    setMenu(null)
    if (!name) return
    try {
      const project = await api.project(name)
      const dir = (project.work_dir ?? '').trim()
      if (!dir) {
        toast(t('sessions.openFolderMissing'), 'error')
        return
      }
      await api.revealPath(dir)
    } catch (e) {
      toastError(e)
    }
  }

  function toggleSelect(id: string, on: boolean) {
    setSelected((sel) => {
      const next = new Set(sel)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  /** Select-all is a per-directory gesture: it picks the threads under the
   *  header it sits beneath, never the whole history. Bulk-deleting "all of
   *  my chats across every project" is a different, scarier action than the
   *  one this button promises. */
  function toggleGroupSelect(ids: string[], on: boolean) {
    setSelected((sel) => {
      const next = new Set(sel)
      for (const id of ids) {
        if (on) next.add(id)
        else next.delete(id)
      }
      return next
    })
  }

  function exitSelectMode() {
    setEditGid(null)
    setSelected(new Set())
  }

  /** Enter edit mode for one directory. Switching directories mid-mode starts
   *  a fresh selection: the checks belonged to the group that was being
   *  edited, and carrying them across would silently re-scope the count. */
  function enterSelectMode(gid: string) {
    setMenu(null)
    setSelected(new Set())
    setEditGid(gid)
  }

  // A directory that leaves the listing (its last thread was deleted) must not
  // leave the mode pointing at a group that no longer renders.
  useEffect(() => {
    if (!editGid) return
    if (grouped.some(([name]) => (name || '__none__') === editGid)) return
    setEditGid(null)
    setSelected(new Set())
  }, [grouped, editGid])

  /** Delete just the ids the caller owns — the group whose button was pressed.
   *  Other groups' checks survive, so pruning one directory never silently
   *  empties the selection somewhere the reader is not looking. */
  async function removeSelected(ids: string[]) {
    if (ids.length === 0 || bulkBusy) return
    const ok = await confirmDialog({
      title: t('sessions.bulkDeleteTitle', { n: ids.length }),
      message: t('sessions.bulkDeleteMsg'),
      confirmLabel: t('sessions.deleteConfirm'),
    })
    if (!ok) return
    setBulkBusy(true)
    try {
      const res = await api.deleteSessions(ids)
      const gone = new Set(res.deleted)
      setSessions((ls) => ls.filter((s) => !gone.has(s.id)))
      // Whatever the server refused stays checked — the user can retry or
      // uncheck it, but nothing silently "deletes" a row that survived.
      const remaining = [...selected].filter((id) => !gone.has(id))
      setSelected(new Set(remaining))
      if (activeId && gone.has(activeId)) onOpenSession('')
      if (res.failed.length > 0) {
        toast(t('sessions.deletePartial', { n: res.failed.length }), 'error')
      } else {
        toast(t('sessions.deletedChats', { n: res.deleted.length }), 'success')
        // Leaving edit mode is only right once nothing anywhere is checked.
        if (remaining.length === 0) setEditGid(null)
      }
    } catch (e) {
      toastError(e)
    } finally {
      setBulkBusy(false)
    }
  }

  return (
    <div class="sidebar-history">
      {loadError && <p class="thread-empty">{loadError}</p>}
      <div class="thread-list">
        {ordered.length === 0 && !loadError && <p class="thread-empty">{t('sessions.railEmpty')}</p>}
        {grouped.map(([projectName, items]) => {
          const gid = projectName || '__none__'
          const open = !folded[gid]
          const editing = editGid === gid
          const groupSel = items.filter((s) => selected.has(s.id)).length
          return (
            <div class={`thread-group${editing ? ' editing' : ''}`} key={gid}>
              <div class="thread-group-row">
                <button
                  type="button"
                  class="thread-group-head"
                  aria-expanded={open}
                  onClick={() => setFolded((f) => ({ ...f, [gid]: open }))}
                >
                  {/* 13 to the header's 13.5px: the open glyph's silhouette stops
                      at y=18.5 instead of 20, so it needs a px more than the
                      closed folder did to sit level with the text. */}
                  <Icon name="folder-open" size={13} />
                  <span class="thread-group-name">{projectName || t('sessions.noProject')}</span>
                </button>
                {/* The trigger is the mode's own switch: "…" opens the menu,
                    the same spot turns into a cross while this directory is
                    being edited, and the cross is the way out. No floating
                    "done" bar above the list — the exit sits where the mode
                    was entered, so there is nothing to hunt for. */}
                {editing ? (
                  <button
                    type="button"
                    class="thread-group-menu thread-group-exit"
                    aria-label={t('sessions.exitManage')}
                    title={t('sessions.exitManage')}
                    onClick={exitSelectMode}
                  >
                    <Icon name="x" size={14} />
                  </button>
                ) : (
                  <button
                    type="button"
                    class={`thread-group-menu${menu?.gid === gid ? ' open' : ''}`}
                    aria-label={t('sessions.groupMenu')}
                    aria-haspopup="menu"
                    aria-expanded={menu?.gid === gid}
                    onClick={(e) => toggleGroupMenu(e as unknown as MouseEvent, gid, projectName)}
                  >
                    <Icon name="more-horizontal" size={14} />
                  </button>
                )}
              </div>
              {/* Batch controls live under the directory they act on: the
                  checkbox picks this group's threads only, and the delete
                  button says how many of them it would remove. A bar floating
                  above the whole list made "全选" look like it meant every
                  project at once, which it never did. */}
              {editing && open && (
                <div class="thread-group-actions">
                  <label class="thread-select-all">
                    <GroupCheck
                      checked={items.length > 0 && groupSel === items.length}
                      indeterminate={groupSel > 0 && groupSel < items.length}
                      onChange={(on) => toggleGroupSelect(items.map((s) => s.id), on)}
                    />
                    <span>
                      {groupSel > 0
                        ? t('sessions.selected', { n: groupSel })
                        : t('sessions.selectAll')}
                    </span>
                  </label>
                  <button
                    type="button"
                    class="thread-group-delete"
                    disabled={groupSel === 0 || bulkBusy}
                    onClick={() =>
                      void removeSelected(items.filter((s) => selected.has(s.id)).map((s) => s.id))
                    }
                  >
                    <Icon name="trash" size={12} />
                    {t('sessions.deleteSelected', { n: groupSel })}
                  </button>
                </div>
              )}
              {open &&
                items.map((s) => {
                  const mark = seen ? threadMark(s, seen) : ''
                  return (
                  <div
                    key={s.id}
                    class={`thread-item${s.id === activeId ? ' active' : ''}${
                      editing && selected.has(s.id) ? ' selected' : ''
                    }`}
                    role="button"
                    tabIndex={0}
                    aria-current={s.id === activeId}
                    aria-pressed={editing ? selected.has(s.id) : undefined}
                    onClick={() =>
                      editing ? toggleSelect(s.id, !selected.has(s.id)) : openThread(s)
                    }
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        if (editing) toggleSelect(s.id, !selected.has(s.id))
                        else openThread(s)
                      }
                    }}
                  >
                    {/* The leading column — the slot the directory's folder
                        glyph occupies above — carries what the run did: red
                        alert for a failure, green dot for a clean finish.
                        Absolutely placed in the row's left padding, so the
                        titles below stay aligned whether or not a mark is up. */}
                    {mark && (
                      <span
                        class={`thread-mark ${mark}`}
                        title={t(mark === 'failed' ? 'sessions.failed' : 'sessions.done')}
                        data-testid={`thread-mark-${mark}`}
                      >
                        {mark === 'failed' ? (
                          <Icon name="alert" size={12} />
                        ) : (
                          <span class="thread-done-dot" />
                        )}
                      </span>
                    )}
                    {editing && (
                      <input
                        type="checkbox"
                        class="thread-check"
                        checked={selected.has(s.id)}
                        tabIndex={-1}
                        aria-label={t('sessions.selectItem')}
                        onClick={(e) => e.stopPropagation()}
                        onChange={(e) =>
                          toggleSelect(s.id, (e.target as HTMLInputElement).checked)
                        }
                      />
                    )}
                    <div class="thread-item-main">
                      <span class="thread-title">
                        {s.pinned && (
                          <span class="thread-pinned-mark" title={t('sessions.pinned')}>
                            <Icon name="pin" size={11} />
                          </span>
                        )}
                        {s.title || t('sessions.untitled')}
                      </span>
                      <span class="thread-meta">
                        <span class="thread-time">{relativeTime(s.updated_at, locale())}</span>
                      </span>
                    </div>
                    {/* In batch mode the row's own pin/delete would compete with
                        the checkbox for the same click — the bulk bar owns
                        deletion while the list is being picked from. */}
                    {!editing && (
                      <>
                        <button
                          class={`thread-pin${s.pinned ? ' on' : ''}`}
                          type="button"
                          title={s.pinned ? t('sessions.unpin') : t('sessions.pin')}
                          aria-pressed={s.pinned}
                          onClick={(e) => {
                            e.stopPropagation()
                            void togglePin(s)
                          }}
                        >
                          <Icon name={s.pinned ? 'pin-off' : 'pin'} size={13} />
                        </button>
                        <button
                          class="thread-del"
                          type="button"
                          title={t('sessions.delete')}
                          onClick={(e) => {
                            e.stopPropagation()
                            void removeChat(s.id)
                          }}
                        >
                          <Icon name="trash" size={13} />
                        </button>
                      </>
                    )}
                  </div>
                  )
                })}
            </div>
          )
        })}
      </div>
      {menu && (
        <div
          class="thread-menu"
          role="menu"
          aria-label={t('sessions.groupMenu')}
          style={{ top: `${menu.top}px`, right: `${menu.right}px` }}
        >
          <button
            type="button"
            role="menuitem"
            class="thread-menu-item"
            disabled={!menu.name}
            title={menu.name ? undefined : t('sessions.openFolderMissing')}
            onClick={() => void openFolder(menu.name)}
          >
            <Icon name="folder" size={14} />
            <span class="thread-menu-label">{t('sessions.openFolder')}</span>
          </button>
          <button
            type="button"
            role="menuitem"
            class="thread-menu-item"
            onClick={() => enterSelectMode(menu.gid)}
          >
            <Icon name="check" size={14} />
            <span class="thread-menu-label">{t('sessions.manageChats')}</span>
          </button>
        </div>
      )}
    </div>
  )
}

/** A checkbox that can also render the "some of this group" dash. The DOM has
 *  no attribute for indeterminate — it is a property that only exists after
 *  the element does — so it has to be written on every change. */
function GroupCheck({
  checked,
  indeterminate,
  onChange,
}: {
  checked: boolean
  indeterminate: boolean
  onChange(on: boolean): void
}) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate
  }, [indeterminate])
  return (
    <input
      type="checkbox"
      ref={ref}
      checked={checked}
      onChange={(e) => onChange((e.target as HTMLInputElement).checked)}
    />
  )
}
