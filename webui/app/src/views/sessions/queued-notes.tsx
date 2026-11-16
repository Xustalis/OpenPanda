// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RefObject } from 'preact'
import type { Dispatch, StateUpdater } from 'preact/hooks'
import type { SessionNote } from '../../api/client'
import { Icon } from '../../components/icons'
import { t } from '../../i18n'

interface QueuedNotesProps {
  notes: SessionNote[]
  notesOpen: boolean
  setNotesOpen: Dispatch<StateUpdater<boolean>>
  noteBubbles: RefObject<HTMLDivElement>
  noteList: RefObject<HTMLDivElement>
  noteRoom: number
  noteEdges: { above: boolean; below: boolean }
  editingNote: string
  noteDraft: string
  setNoteDraft(value: string): void
  saveEditNote(note: SessionNote): Promise<void>
  cancelEditNote(): void
  beginEditNote(note: SessionNote): void
  promoteNote(note: SessionNote): Promise<void>
  dropNote(note: SessionNote): Promise<void>
}

/** Queued notes stay editable until the running turn consumes them. */
export function QueuedNotes({
  notes, notesOpen, setNotesOpen, noteBubbles, noteList, noteRoom, noteEdges,
  editingNote, noteDraft, setNoteDraft, saveEditNote, cancelEditNote,
  beginEditNote, promoteNote, dropNote,
}: QueuedNotesProps) {
  if (notes.length === 0) return null
  return (
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
                            if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) {
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
  )
}
