// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type FsFileEntry, type MCPSettings, type SkillEntry } from '../../api/client'
import { Icon } from '../../components/icons'
import { t } from '../../i18n'
import { fmtSize } from './attachment-pills'

/** The composer's "+" menu: attach context to the turn you are about to send.
 *
 *  A skill and an MCP server both go in as a mention in the prompt text —
 *  `@skill:name` / `@mcp:name` — which is exactly what the server receives.
 *  Nothing is expanded behind the user's back, so the box keeps showing the
 *  text the model will read. Files are the third entry and are not wired yet. */
export function AttachMenu({
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
