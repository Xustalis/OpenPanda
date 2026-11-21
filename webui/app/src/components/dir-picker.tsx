// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, type DirectoryListing } from '../api/client'
import { t } from '../i18n'
import { layerAboveOwnsEscape } from './card-dismiss'
import { Icon } from './icons'
import { lastPathSegment, suggestProjectName } from './dir-utils'
import { useModalFocus } from './modal-focus'

export interface DirPickerProps {
  value?: string
  onChange: (path: string) => void
  onSuggestName?: (name: string) => void
  placeholder?: string
  disabled?: boolean
}

/** The pick itself: the native chooser first, the in-page browser as the
 *  fallback for a node whose panel cannot raise one. Shared by the boxed
 *  picker on the projects page and the compact button the composer's
 *  new-chat footer uses — the two look nothing alike but must fail alike. */
function useDirPick(onPicked: (path: string) => void, disabled: boolean) {
  const [modalOpen, setModalOpen] = useState(false)
  const [loading, setLoading] = useState(false)

  async function open(current: string) {
    if (disabled || loading) return
    setLoading(true)
    try {
      // The caption is ours to write: the panel serves five languages and
      // cannot tell which one the browser is in.
      const res = await api.chooseDirectory(current, t('sessions.chooseDir'))
      const canceled = !!(res && (res.canceled ?? res.cancelled))
      if (res && res.path && !canceled) {
        onPicked(res.path)
        return
      }
      // A cancel is an answer — the user looked and changed their mind. Do not
      // answer it by opening a second picker over the one they just closed.
      if (canceled) return
      // Otherwise no window can be raised for this console (a browser on
      // another device, a host with no desktop shell) or it failed to open:
      // fall back to the in-page browser, which always works.
      setModalOpen(true)
    } catch {
      setModalOpen(true)
    } finally {
      setLoading(false)
    }
  }

  return { modalOpen, setModalOpen, loading, open }
}

export function DirPicker({
  value = '',
  onChange,
  onSuggestName,
  placeholder,
  disabled = false,
}: DirPickerProps) {
  const pick = useDirPick((path) => {
    onChange(path)
    if (onSuggestName) {
      onSuggestName(suggestProjectName(path))
    }
  }, disabled)
  const { modalOpen, setModalOpen, loading } = pick

  function handlePick() {
    void pick.open(value)
  }

  return (
    <div class="dir-picker-wrap">
      <div class={`dir-picker-box ${value ? 'has-value' : ''} ${disabled ? 'disabled' : ''}`}>
        <Icon name="folder" class="dir-picker-icon" />
        <span class="dir-picker-path" title={value || placeholder || t('projects.dirPlaceholder')}>
          {value || <span class="dim">{placeholder || t('projects.dirPlaceholder')}</span>}
        </span>
        {value && !disabled && (
          <button
            type="button"
            class="dir-picker-clear"
            onClick={(e) => {
              e.stopPropagation()
              onChange('')
            }}
            title={t('common.clear')}
          >
            <Icon name="x" size={12} />
          </button>
        )}
        <button
          type="button"
          class="btn dir-picker-browse-btn"
          onClick={handlePick}
          disabled={disabled || loading}
        >
          {loading ? t('common.loading') : t('projects.chooseDir')}
        </button>
      </div>

      {modalOpen && (
        <DirBrowserModal
          initialPath={value}
          onSelect={(selectedPath) => {
            onChange(selectedPath)
            if (onSuggestName) {
              onSuggestName(suggestProjectName(selectedPath))
            }
            setModalOpen(false)
          }}
          onClose={() => setModalOpen(false)}
        />
      )}
    </div>
  )
}

/** One directory the console has already used. A directory only means
 *  something to the panel once a project points at it (`projectForDir` makes
 *  that row on send), so "already opened" and "has a project" are the same
 *  set — and the project's name is the label the rail already shows it under. */
interface WorkdirRow {
  name: string
  dir: string
}

/** Windows and POSIX both: a trailing separator is not a different directory,
 *  and the compare is the console's own (see `projectForDir`). */
const normDir = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase()

/** The compact pick: a folder glyph and the chosen path set in the same small
 *  print as the composer's keyboard legend. The projects page needs a form
 *  field; a new thread only needs to say where it will run, so this one is a
 *  footnote that happens to be clickable.
 *
 *  Clicking it opens a card, not the OS dialog. The dialog was the wrong first
 *  answer for the common case: a directory you have used before is one you are
 *  about to use again, and making the user walk the tree to find `panda` when
 *  the console already knows where `panda` lives is the slow path through a
 *  control that has all the answers. So the card lists the directories this
 *  node has filed work under — newest first, the current one ticked, a search
 *  field on top for the day the list outgrows the card — and the native
 *  chooser moves to the bottom row, where "a directory I have never used" now
 *  lives. The card is the same shape as the thread pick beside it on 工作空间:
 *  search, rows, one footer of actions.
 *
 *  A chosen directory is still undoable in place: under the pointer the glyph
 *  turns into a cross, and the cross drops the pick back to "choose a
 *  directory". The glyph is the cross's slot rather than a second control
 *  beside the name — the chip is one line of small print, and an × parked next
 *  to a long folder name would either crowd it or be pushed out of the clamp. */
export function DirButton({
  value = '',
  onChange,
  label,
  allowClear = true,
  clearLabel,
  hint,
  disabled = false,
}: {
  value?: string
  onChange: (path: string) => void
  /** The picker's own name, shown while nothing is chosen. */
  label: string
  hint?: string
  clearLabel?: string
  allowClear?: boolean
  disabled?: boolean
}) {
  const pick = useDirPick(onChange, disabled)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  /** null while the list is in flight: an empty menu and a loading menu must
   *  not look alike, or "no workspaces yet" flashes on every open. */
  const [spaces, setSpaces] = useState<WorkdirRow[] | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const clearable = allowClear && !!value && !disabled

  // Read on every open rather than cached at mount: filing work under a
  // project touches its row, which is what orders this list, and the endpoint
  // is local — a stale menu that hides the directory you just used is worse
  // than one extra round-trip.
  useEffect(() => {
    if (!open) return
    let alive = true
    setSpaces(null)
    api
      .projects()
      .then((pl) => {
        if (!alive) return
        setSpaces(
          (pl.detail ?? [])
            .filter((d) => !!d.work_dir)
            .map((d) => ({ name: d.name, dir: d.work_dir as string })),
        )
      })
      .catch(() => {
        if (alive) setSpaces([])
      })
    return () => {
      alive = false
    }
  }, [open])

  // Escape and a click elsewhere close it. The header cards refuse outside
  // clicks so that reading the transcript cannot dismiss them; this card hangs
  // over the composer, where a click in the text box means the user has moved
  // on, not that they are reading behind it.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) {
        setOpen(false)
        setQuery('')
      }
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape('.wd-panel')) return
      e.stopPropagation()
      setOpen(false)
      setQuery('')
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  // A pick no project owns yet (a folder chosen but not sent into) still
  // deserves a row: otherwise the card opens with nothing ticked and the pill's
  // answer nowhere in its own list.
  const all = useMemo(() => {
    const rows = spaces ?? []
    if (!value || rows.some((r) => normDir(r.dir) === normDir(value))) return rows
    return [{ name: lastPathSegment(value), dir: value }, ...rows]
  }, [spaces, value])

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return all
    return all.filter(
      (r) => r.name.toLowerCase().includes(q) || r.dir.toLowerCase().includes(q),
    )
  }, [all, query])

  /** Closing always forgets the search: a card reopened a minute later must not
   *  open onto the filter typed before, with half the list missing. */
  function close() {
    setOpen(false)
    setQuery('')
  }

  function choose(dir: string) {
    onChange(dir)
    close()
  }

  /** The pill used to be this action outright; it is now the card's last row,
   *  and the native chooser still falls back to the in-page browser when no
   *  window can be raised (see `useDirPick`). */
  function browse() {
    close()
    void pick.open(value)
  }

  return (
    <div class="workdir-pick" ref={box}>
      <div
        class={`workdir-btn${value ? ' has-value' : ''}${clearable ? ' can-clear' : ''}${disabled ? ' is-disabled' : ''}`}
        /* The bubble is where the whole path lives: the label shows the folder
           on its own, so hovering is how you check *which* folder. Nothing to
           reveal while the pick is still empty — the label already says it,
           and nothing while the card is up: the card lists every path. */
        data-tip={!open && value ? value : undefined}
        /* The bubble is also the only place the cross can be announced: it is
           invisible until the pointer is on the chip, so hovering is the one
           moment the affordance needs a name. */
        data-tip-hint={clearable ? clearLabel || t('sessions.clearDirHint') : undefined}
      >
        <button
          type="button"
          class="workdir-open"
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={value ? `${label}: ${value}` : label}
          disabled={disabled}
          onClick={() => (open ? close() : setOpen(true))}
        >
          {/* The rail's working-directory mark, at the rail's own size: the
              header's closed folder would read as a second title down here. */}
          <Icon name="folder-open" size={13} class="workdir-glyph" />
          <span class="workdir-path">{value ? lastPathSegment(value) : label}</span>
          {/* The caret is the whole difference between "opens a dialog" and
              "opens a card", and it costs 12px of a chip that already ends in
              a hairline. */}
          <Icon name="chevron-down" size={12} class="workdir-caret" />
        </button>
        {clearable && (
          <button
            type="button"
            class="workdir-clear"
            aria-label={clearLabel || t('sessions.clearDir')}
            onClick={() => onChange('')}
          >
            <Icon name="x" size={13} />
          </button>
        )}
      </div>
      {open && (
        <div class="wd-panel" role="dialog" aria-label={label}>
          <div class="wd-search-wrap">
            <Icon name="search" size={14} class="wd-search-icon" />
            <input
              class="input wd-search"
              type="search"
              value={query}
              placeholder={t('sessions.workdirSearch')}
              aria-label={t('sessions.workdirSearch')}
              autoFocus
              onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
              onKeyDown={(e) => {
                if (e.key !== 'Enter') return
                // Enter belongs to the card, never to the form around it: the
                // composer is a `<form>` whose submit is "send", so an Enter
                // that fell through would post the draft because the search
                // found nothing.
                e.preventDefault()
                // With a match it takes the first one — the list is already the
                // answer to what was typed, and reaching for the mouse would be
                // the slow path through a control built to be fast.
                const first = rows[0]
                if (first) choose(first.dir)
              }}
            />
          </div>
          <div class="wd-list" role="listbox" aria-label={label}>
            {spaces === null ? (
              <p class="wd-empty">{t('common.loading')}</p>
            ) : rows.length === 0 ? (
              <p class="wd-empty">
                {all.length === 0 ? t('sessions.workdirEmpty') : t('sessions.workdirNoMatch')}
              </p>
            ) : (
              rows.map((r) => {
                const on = !!value && normDir(r.dir) === normDir(value)
                return (
                  <button
                    key={r.dir}
                    type="button"
                    class={`wd-row${on ? ' sel' : ''}`}
                    role="option"
                    aria-selected={on}
                    title={r.dir}
                    onClick={() => choose(r.dir)}
                  >
                    <Icon name="folder" size={15} class="wd-row-icon" />
                    <span class="wd-row-text">
                      <span class="wd-row-name">{r.name}</span>
                      {/* The name is usually the folder's own name, which is
                          exactly why the path earns the second line: two
                          `panda` folders on two drives are one name. */}
                      <span class="wd-row-path">{r.dir}</span>
                    </span>
                    {on && <Icon name="check" size={14} class="wd-row-check" />}
                  </button>
                )
              })
            )}
          </div>
          {hint && <p class="wd-hint">{hint}</p>}
          <div class="wd-actions">
            <button type="button" class="wd-action" onClick={browse}>
              <Icon name="folder-plus" size={15} />
              {t('sessions.workdirNew')}
            </button>
            {clearable && (
              <button
                type="button"
                class="wd-action"
                onClick={() => {
                  onChange('')
                  close()
                }}
              >
                <Icon name="x" size={15} />
                {clearLabel || t('sessions.workdirNone')}
              </button>
            )}
          </div>
        </div>
      )}
      {pick.modalOpen && (
        <DirBrowserModal
          initialPath={value}
          onSelect={(selectedPath) => {
            onChange(selectedPath)
            pick.setModalOpen(false)
          }}
          onClose={() => pick.setModalOpen(false)}
        />
      )}
    </div>
  )
}

function DirBrowserModal({
  initialPath,
  onSelect,
  onClose,
}: {
  initialPath?: string
  onSelect: (path: string) => void
  onClose: () => void
}) {
  const [currentPath, setCurrentPath] = useState(initialPath || '')
  const [listing, setListing] = useState<DirectoryListing | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  async function loadPath(path: string) {
    setLoading(true)
    setError('')
    try {
      const res = await api.listDirectories(path)
      setListing(res)
      setCurrentPath(res.current)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadPath(currentPath)
  }, [])

  const parts = currentPath ? currentPath.split(/[/\\]/).filter(Boolean) : []
  const isWindows = listing?.separator === '\\'

  return (
    <div
      class="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={box}
        class="modal dir-browser-modal"
        role="dialog"
        aria-modal="true"
        aria-label={t('projects.selectDirTitle')}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
      >
        <div class="dir-browser-header">
          <h2 class="modal-title">{t('projects.selectDirTitle')}</h2>
          <button type="button" class="btn-icon" onClick={onClose} title={t('common.close')}>
            ✕
          </button>
        </div>

        <div class="dir-breadcrumbs">
          <button
            type="button"
            class="dir-crumb-btn"
            onClick={() => loadPath(isWindows ? 'C:\\' : '/')}
          >
            {isWindows ? 'C:\\' : '/'}
          </button>
          {parts.map((p, i) => {
            // On Windows parts[0] is the drive ("C:") — a bare "C:" resolves
            // to the process cwd on that drive, not the drive root, so it
            // needs its trailing separator.
            const pathUpTo = isWindows && i === 0
              ? parts[0] + '\\'
              : (isWindows ? '' : '/') + parts.slice(0, i + 1).join(isWindows ? '\\' : '/')
            return (
              <span key={i} class="dir-crumb-item">
                <span class="dir-crumb-sep">/</span>
                <button
                  type="button"
                  class={`dir-crumb-btn ${i === parts.length - 1 ? 'active' : ''}`}
                  onClick={() => loadPath(pathUpTo)}
                >
                  {p}
                </button>
              </span>
            )
          })}
        </div>

        {listing?.parent && listing.parent !== listing.current && (
          <div class="dir-up-row">
            <button
              type="button"
              class="dir-entry-btn up"
              onClick={() => loadPath(listing.parent)}
            >
              <Icon name="arrow-up" class="dir-icon" />
              <span>.. ({t('projects.parentDir')})</span>
            </button>
          </div>
        )}

        <div class="dir-entries-list">
          {loading ? (
            <div class="dim dir-loading">
              <span class="spinner spinner-inline" aria-hidden="true" />
              {t('common.loading')}
            </div>
          ) : error ? (
            <div class="dir-error">{error}</div>
          ) : listing?.directories && listing.directories.length > 0 ? (
            listing.directories.map((entry) => (
              <button
                key={entry.path}
                type="button"
                class="dir-entry-btn"
                onClick={() => loadPath(entry.path)}
              >
                <Icon name="folder" class="dir-icon" />
                <span class="dir-entry-name">{entry.name}</span>
              </button>
            ))
          ) : (
            <div class="dim dir-empty">{t('projects.noSubdirs')}</div>
          )}
        </div>

        <div class="dir-browser-footer">
          <div class="dir-current-selected">
            <span class="dim">{t('projects.currentPath')}: </span>
            <code>{currentPath || '/'}</code>
          </div>
          <div class="modal-actions">
            <button type="button" class="btn" onClick={onClose}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              class="btn primary"
              disabled={!currentPath}
              onClick={() => onSelect(currentPath)}
            >
              {t('projects.confirmSelect')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
