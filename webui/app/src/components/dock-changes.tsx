// SPDX-License-Identifier: AGPL-3.0-or-later

// The dock's change panel: what this thread did to its worktree, one file at a
// time.
//
// The list is `git status --porcelain` order with the per-file `+n −m` read
// back off the same unified patch the panel already holds (see
// session-rail-stats). A click opens that file's own block — not the whole
// patch scrolled to a heading — so "where is the change" is answered by the
// viewport rather than by a search.
//
// Merge and undo live here because this is the only surface that shows what
// they would do. The old worktree drawer put the same list, the same patch and
// the same two buttons across the top of the transcript; a drawer that pushes
// the conversation down to describe a change is the wrong shape for reading
// the change beside the conversation.
//
// The panel is a reader, not an editor: `打开文件` hands the path to the files
// pane, which is read-only on purpose (see dock-files.tsx).

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { SessionDiff } from '../api/client'
import { t } from '../i18n'
import { DiffViewer } from './diff-viewer'
import { Icon, type IconName } from './icons'
import { firstChangedLine, patchFileStats, patchForFile, porcelainParts, type FilePatch } from './session-rail-stats'

export interface DockChangesProps {
  /** The thread whose worktree the paths resolve against. */
  session: string | null
  /** The worktree diff, or null before the thread has a worktree. */
  diff: SessionDiff | null
  /** Re-read `git status` + the patch; the head's refresh button. */
  onRefresh(): void | Promise<void>
  /** Show the file itself in the files pane, on the first changed line. */
  onOpenFile(path: string, line: number): void
  onMerge?(): void
  onRevert?(): void
  merging?: boolean
  reverting?: boolean
  error?: string
}

type Tone = 'add' | 'del' | 'mod'

interface ChangeRow {
  path: string
  /** The old side of a rename, when git printed one. A rename whose content
   *  changed too much is two blocks in the patch (a delete and an add), so the
   *  row owns both sides rather than showing only the half git named last. */
  from?: string
  /** The porcelain code, `??` for untracked. */
  status: string
  stat?: { insertions: number; deletions: number }
}

function statusLetter(status: string): string {
  if (status === '??') return 'U'
  return status.slice(0, 1) || '?'
}

/** The row leads with the file itself, not with a status chip: Claude's and
 *  Codex's change lists both put a file glyph first and let the `+n −m` pair
 *  carry added / modified / deleted. Images get the only other mark — a
 *  document glyph on a PNG is a lie. */
function fileIcon(path: string): IconName {
  return /\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i.test(path) ? 'image' : 'file-text'
}

function toneOf(status: string): Tone {
  if (status === '??' || status.startsWith('A')) return 'add'
  if (status.startsWith('D')) return 'del'
  return 'mod'
}

/** `src/a/b.ts` → `{ dir: 'src/a', name: 'b.ts' }`; a root file has no dir. */
function splitPath(path: string): { dir: string; name: string } {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return i < 0 ? { dir: '', name: path } : { dir: path.slice(0, i), name: path.slice(i + 1) }
}

/** The block's leading meta lines name the file the bar already names, so the
 *  body starts at the first hunk. Everything from that hunk on is untouched —
 *  a removed line can itself start with `--`, and only the header lines
 *  *before* the first `@@` are safe to drop. A second block in the same body
 *  (a rename git split into a delete and an add) keeps its own header, which
 *  is what tells the reader the next hunk is another file. */
function diffBody(patch: string): string {
  const lines = patch.split('\n')
  const first = lines.findIndex((line) => line.startsWith('@@'))
  if (first < 0) return patch
  return lines
    .filter((line, i) => i >= first || !/^(diff --git |index |--- |\+\+\+ )/.test(line))
    .join('\n')
}

export function DockChanges(props: DockChangesProps) {
  const [picked, setPicked] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [copied, setCopied] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const copyTimer = useRef<number | undefined>(undefined)

  const rows = useMemo<ChangeRow[]>(() => {
    const stats = patchFileStats(props.diff?.patch ?? '')
    return (props.diff?.changes ?? []).map((c) => {
      const { from, to } = porcelainParts(c.path)
      const toStat = stats.get(to)
      const fromStat = from !== to ? stats.get(from) : undefined
      // Both sides of a split rename: the row's `+n −m` is the whole move, not
      // the new file's additions alone.
      const stat =
        toStat && fromStat
          ? {
              insertions: toStat.insertions + fromStat.insertions,
              deletions: toStat.deletions + fromStat.deletions,
            }
          : (toStat ?? fromStat)
      return { path: to, from: from !== to ? from : undefined, status: c.status, stat }
    })
  }, [props.diff])

  const totals = useMemo(() => {
    if (props.diff?.stat) {
      return { ins: props.diff.stat.insertions, del: props.diff.stat.deletions }
    }
    let ins = 0
    let del = 0
    for (const row of rows) {
      ins += row.stat?.insertions ?? 0
      del += row.stat?.deletions ?? 0
    }
    return { ins, del }
  }, [props.diff, rows])

  // A refresh can delete the file the reader was looking at. The pane is still
  // mounted, so without this it would keep showing a block the patch no longer
  // contains — or, worse, a different file's block under the same path.
  useEffect(() => {
    if (picked && !rows.some((r) => r.path === picked)) setPicked(null)
  }, [rows, picked])

  useEffect(
    () => () => {
      if (copyTimer.current !== undefined) window.clearTimeout(copyTimer.current)
    },
    [],
  )

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return q ? rows.filter((r) => r.path.toLowerCase().includes(q)) : rows
  }, [rows, query])

  async function refresh() {
    if (refreshing) return
    setRefreshing(true)
    try {
      await props.onRefresh()
    } finally {
      setRefreshing(false)
    }
  }

  async function copyPath(path: string) {
    try {
      await navigator.clipboard.writeText(path)
      setCopied(true)
      if (copyTimer.current !== undefined) window.clearTimeout(copyTimer.current)
      copyTimer.current = window.setTimeout(() => setCopied(false), 1400)
    } catch {
      // Clipboard denied: the path is selectable in the bar either way.
    }
  }

  // A rename can be one block in the patch (git recognised the move) or two
  // (delete + add, when the content changed too much). Reading both sides by
  // path makes the detail view whole either way.
  const pickedBlocks: FilePatch[] = []
  if (picked) {
    for (const path of [rows.find((r) => r.path === picked)?.from, picked]) {
      if (!path) continue
      const block = patchForFile(props.diff?.patch ?? '', path)
      if (block) pickedBlocks.push(block)
    }
  }
  const pickedPatch = pickedBlocks
    .map((b) => b.patch)
    .filter(Boolean)
    .join('\n')
  const pickedTruncated = pickedBlocks.some((b) => b.truncated)

  const head = (
    <header class="df-bar dc-bar">
      {picked ? (
        <button
          type="button"
          class="icon-btn df-up"
          title={t('dock.back')}
          aria-label={t('dock.back')}
          onClick={() => setPicked(null)}
        >
          <Icon name="arrow-left" size={15} />
        </button>
      ) : (
        <Icon name="file-diff" size={15} class="dc-bar-icon" />
      )}
      <span class="dc-bar-title mono" title={picked ?? props.diff?.branch ?? ''}>
        {picked ? splitPath(picked).name : props.diff?.branch ? `⎇ ${props.diff.branch}` : t('dock.changes')}
      </span>
      {picked ? (
        <div class="fv-actions">
          <button
            type="button"
            class="fv-btn"
            title={t('fileView.copy')}
            onClick={() => void copyPath(picked)}
          >
            <Icon name={copied ? 'check' : 'copy'} size={14} />
            <span class="fv-btn-label">{copied ? t('dock.copied') : t('fileView.copy')}</span>
          </button>
          {rows.find((r) => r.path === picked)?.status !== 'D' && (
            <button
              type="button"
              class="fv-btn"
              title={t('dock.openFile')}
              onClick={() =>
                props.onOpenFile(
                  picked,
                  // The picked file is the last block (a split rename lists the
                  // old side first); its first hunk is where the reader should
                  // land, not the deleted half's line 0.
                  firstChangedLine(pickedBlocks[pickedBlocks.length - 1]?.patch ?? ''),
                )
              }
            >
              <Icon name="file-text" size={14} />
              <span class="fv-btn-label">{t('dock.openFile')}</span>
            </button>
          )}
        </div>
      ) : (
        <>
          <span class="dc-total mono">
            <i class="dc-add">+{totals.ins}</i>
            <i class="dc-del">−{totals.del}</i>
          </span>
          <button
            type="button"
            class="icon-btn dc-refresh"
            title={t('dock.refresh')}
            aria-label={t('dock.refresh')}
            disabled={refreshing || !props.session}
            onClick={() => void refresh()}
          >
            <Icon name="refresh" size={14} class={refreshing ? 'is-spinning' : undefined} />
          </button>
        </>
      )}
    </header>
  )

  if (picked) {
    const row = rows.find((r) => r.path === picked)
    const hasHunk = /^@@/m.test(pickedPatch)
    const { dir } = splitPath(picked)
    return (
      <div class="dock-changes">
        {head}
        <div class="dc-file-meta">
          <span class="dc-file-dir mono" title={picked}>
            {dir}
          </span>
          {row && <span class={`dc-status is-${toneOf(row.status)}`}>{statusLetter(row.status)}</span>}
          {row?.stat && (
            <span class="dc-stat mono">
              <i class="dc-add">+{row.stat.insertions}</i>
              <i class="dc-del">−{row.stat.deletions}</i>
            </span>
          )}
        </div>
        {props.error && <p class="df-note is-error">{props.error}</p>}
        {pickedTruncated ? (
          <p class="df-note">{t('dock.patchTruncated')}</p>
        ) : hasHunk ? (
          <div class="dc-diff">
            <DiffViewer patch={diffBody(pickedPatch)} numbered />
          </div>
        ) : (
          <p class="df-note">{t('dock.noPatch')}</p>
        )}
      </div>
    )
  }

  const stat = props.diff?.stat
  const sensitive = stat?.sensitive ?? []
  const binary = stat?.binary ?? []
  return (
    <div class="dock-changes">
      {head}
      {props.error && <p class="df-note is-error">{props.error}</p>}
      {!props.diff ? (
        <div class="dc-empty">
          <Icon name="file-diff" size={22} />
          <p>{t('dock.noWorktree')}</p>
        </div>
      ) : rows.length === 0 ? (
        <div class="dc-empty">
          <Icon name="check" size={22} />
          <p>{t('dock.noChanges')}</p>
        </div>
      ) : (
        <>
          {rows.length > 8 && (
            <input
              type="search"
              class="dc-filter"
              value={query}
              placeholder={t('dock.filter')}
              aria-label={t('dock.filter')}
              onInput={(e) => setQuery((e.currentTarget as HTMLInputElement).value)}
            />
          )}
          {sensitive.length > 0 && (
            <p class="dc-warn">
              {t('sessions.sensitiveFiles')}: <span class="mono">{sensitive.join(', ')}</span>
            </p>
          )}
          {binary.length > 0 && (
            <p class="dc-note">
              {t('sessions.binaryFiles')}: <span class="mono">{binary.join(', ')}</span>
            </p>
          )}
          <div class="dc-list">
            {filtered.map((row) => {
              const { dir, name } = splitPath(row.path)
              return (
                <button
                  key={row.path}
                  type="button"
                  class={`dc-row is-${toneOf(row.status)}`}
                  title={row.path}
                  onClick={() => setPicked(row.path)}
                >
                  <Icon name={fileIcon(row.path)} size={15} class="dc-icon" />
                  <span class="dc-main">
                    <span class="dc-name">{name}</span>
                    {dir && <span class="dc-dir">{dir}</span>}
                  </span>
                  {row.stat && (
                    <span class="dc-stat mono">
                      <i class="dc-add">+{row.stat.insertions}</i>
                      <i class="dc-del">−{row.stat.deletions}</i>
                    </span>
                  )}
                </button>
              )
            })}
            {filtered.length === 0 && <p class="df-note dim">{t('dock.noMatch')}</p>}
          </div>
          <footer class="dc-foot">
            {props.onRevert && (
              <button
                type="button"
                class="btn small danger"
                disabled={props.merging || props.reverting}
                onClick={props.onRevert}
              >
                {props.reverting ? t('sessions.reverting') : t('sessions.revert')}
              </button>
            )}
            <span class="dc-foot-gap" />
            {props.onMerge && (
              <button
                type="button"
                class="btn small primary"
                disabled={props.merging || props.reverting}
                onClick={props.onMerge}
              >
                {props.merging ? t('sessions.merging') : t('sessions.merge')}
              </button>
            )}
          </footer>
        </>
      )}
    </div>
  )
}
