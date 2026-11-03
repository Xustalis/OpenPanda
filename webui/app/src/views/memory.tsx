// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  api,
  type MemoryFiles,
  type MemoryGraphNode,
  type ProjectDetail,
  type ProjectMemory,
  type TopicFile,
} from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { navigate } from '../nav'
import { ErrorState, LoadingState } from '../components/page'
import { Icon, type IconName } from '../components/icons'
import { MemoryGraphView } from '../components/memory-graph'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

type Tab = 'graph' | 'user' | 'memory' | 'dreams' | 'topics' | 'daily' | 'projects'

const TABS: Tab[] = ['graph', 'user', 'memory', 'dreams', 'topics', 'daily', 'projects']

/** Real file names, so a card always says which file it is editing. */
const FILE_NAME: Record<'user' | 'memory', string> = { user: 'USER.md', memory: 'MEMORY.md' }

/** The memory view (C2): every Hermes file is user-managed here. USER.md /
 *  MEMORY.md edit as § entries (entries promoted from the daily logs carry a
 *  [from:…] badge and can be fixed or dropped individually), DREAMS.md is
 *  the Dreamer's diary — open for editing behind a confirm — topics/*.md are
 *  the selective-load memory files (create / edit / delete), daily/*.md is a
 *  read-only browse of the warm-layer diary, and project memory is picked
 *  per project and edited in place. Caps come from the live config values.
 *
 *  Layout follows the console's one-line page header: the mark, the name, a
 *  one-line count summary and the node clock + refresh on the right — the two
 *  stat cards that used to hold the clock and a bare refresh button are gone.
 *  The surfaces are a single segmented strip with their entry counts, and
 *  every editable file is one card whose head carries the file name, the
 *  budget meter, the filter and the edit verb. */
export function MemoryView(props: { initialTab?: string }) {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const { data, error } = useAsync(() => api.memory(), [], tick)
  const [tab, setTab] = useState<Tab>(
    TABS.includes(props.initialTab as Tab) ? (props.initialTab as Tab) : 'graph',
  )
  useEffect(() => {
    if (TABS.includes(props.initialTab as Tab)) setTab(props.initialTab as Tab)
  }, [props.initialTab])

  const reload = () => setTick((v) => v + 1)

  // Entries seen so far this session, per file. A refresh then marks only the
  // entries the agent actually added — the first paint marks none, because
  // "everything is new" says nothing. The result is memoised per content
  // snapshot: re-renders of the same data must not consume the highlight.
  const seenRef = useRef<Map<string, Map<string, number>>>(new Map())
  const freshRef = useRef<Map<string, { content: string; set: Set<string> }>>(new Map())
  const freshEntries = (file: string, content: string): Set<string> => {
    const cached = freshRef.current.get(file)
    if (cached && cached.content === content) return cached.set
    const entries = parseEntries(content)
    const now = Date.now()
    const fresh = new Set<string>()
    let seen = seenRef.current.get(file)
    if (!seen) {
      seen = new Map(entries.map((e) => [e, now]))
      seenRef.current.set(file, seen)
    } else {
      for (const e of entries) {
        if (!seen.has(e)) {
          seen.set(e, now)
          fresh.add(e)
        }
      }
    }
    freshRef.current.set(file, { content, set: fresh })
    return fresh
  }

  if (error) {
    return (
      <section class="memory-page">
        <MemoryHead />
        <ErrorState head={false} title={t('memory.title')} sub={t('memory.subtitle')} error={error} onRetry={reload} />
      </section>
    )
  }
  if (!data) {
    return (
      <section class="memory-page">
        <MemoryHead />
        <LoadingState head={false} title={t('memory.title')} sub={t('memory.subtitle')} />
      </section>
    )
  }

  // Offset between the browser clock and the node clock, so the ticking
  // display shows node time, not local time.
  const nodeOffset = data.time ? new Date(data.time).getTime() - Date.now() : 0
  const counts = {
    user: parseEntries(data.user).length,
    memory: parseEntries(data.memory).length,
    topics: data.topics.length,
    daily: data.daily.length,
  }
  const summary = t('memory.headSummary', {
    user: counts.user,
    memory: counts.memory,
    topics: counts.topics,
    daily: counts.daily,
  })

  return (
    <section class="memory-page">
      <MemoryHead summary={summary} nodeOffset={nodeOffset} onRefresh={reload} />

      <div class="segmented memory-tabs" role="tablist">
        {TABS.map((k) => {
          const n = tabCount(k, counts)
          return (
            <button
              key={k}
              role="tab"
              aria-selected={tab === k}
              class={`seg${tab === k ? ' on' : ''}`}
              onClick={() => setTab(k)}
            >
              {t(`memory.${k}`)}
              {n != null && <span class="memory-tab-n mono">{n}</span>}
            </button>
          )
        })}
      </div>

      {tab === 'graph' && <GraphTab onOpenTab={setTab} />}
      {tab === 'user' && (
        <MemoryFileView
          file="user"
          content={data.user}
          fresh={freshEntries('user', data.user)}
          limit={data.user_limit}
          reload={reload}
        />
      )}
      {tab === 'memory' && (
        <MemoryFileView
          file="memory"
          content={data.memory}
          fresh={freshEntries('memory', data.memory)}
          limit={data.mem_limit}
          reload={reload}
        />
      )}
      {tab === 'dreams' && <DreamsView content={data.dreams} reload={reload} />}
      {tab === 'topics' && <TopicsView topics={data.topics} limit={data.mem_limit} reload={reload} />}
      {tab === 'daily' && <DailyView daily={data.daily} reload={reload} />}
      {tab === 'projects' && <ProjectsView defaultLimit={data.project_limit} reload={reload} />}
    </section>
  )
}

/** Which surfaces get a count in the tab strip. DREAMS.md is free-form prose
 *  and project memory is loaded lazily, so neither carries one here. */
function tabCount(k: Tab, counts: { user: number; memory: number; topics: number; daily: number }): number | null {
  if (k === 'user') return counts.user
  if (k === 'memory') return counts.memory
  if (k === 'topics') return counts.topics
  if (k === 'daily') return counts.daily
  return null
}

/** One bar: the mark, the page name, the counts, then the node clock and the
 *  reload verb. Same shape as 工作空间 and 队列·任务·项目. */
function MemoryHead({
  summary,
  nodeOffset,
  onRefresh,
}: {
  summary?: string
  nodeOffset?: number
  onRefresh?(): void
}) {
  return (
    <header class="memory-head">
      <Icon name="orbit" size={16} class="memory-head-icon" />
      <h1 class="memory-head-title">{t('memory.title')}</h1>
      {summary && (
        <p class="memory-head-meta" title={t('memory.subtitle')}>
          {summary}
        </p>
      )}
      <div class="memory-head-tools">
        {nodeOffset != null && <NodeClock offset={nodeOffset} />}
        {onRefresh && (
          <button class="icon-btn" title={t('memory.refresh')} aria-label={t('memory.refresh')} onClick={onRefresh}>
            <Icon name="refresh" />
          </button>
        )}
      </div>
    </header>
  )
}

/** The node's wall clock, ticking on its own. It used to live in the page
 *  state, which re-rendered — and re-laid-out — the whole graph once a
 *  second. */
function NodeClock({ offset }: { offset: number }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])
  return (
    <span class="memory-clock mono" title={t('memory.nodeTime')} data-testid="node-clock">
      {new Date(now + offset).toLocaleString()}
    </span>
  )
}

/** The char budget as a thin bar: the counter says how much, the bar says how
 *  close to the cap — the one thing a bare number hides. */
function UsageMeter({ chars, limit }: { chars: number; limit: number }) {
  if (!limit || limit <= 0) return null
  const pct = Math.min(100, Math.round((chars / limit) * 100))
  return (
    <span
      class={`memory-usage${pct >= 90 ? ' hot' : ''}`}
      role="img"
      aria-label={t('memory.usageTip', { chars, limit })}
      title={t('memory.usageTip', { chars, limit })}
    >
      <i style={{ width: `${pct}%` }} />
    </span>
  )
}

/** USER.md / MEMORY.md / a topic file / a project file as editable § entries.
 *  Entries the Dreamer promoted out of the daily logs carry a `[from:…]`
 *  marker: the card strips it from the text, shows it as a badge, and gives
 *  the entry per-row fix / delete shortcuts so a weak signal that slipped in
 *  can be corrected without rewriting the file. */
function MemoryFileView({
  file,
  content,
  fresh,
  limit,
  reload,
}: {
  file: 'user' | 'memory'
  content: string
  /** Entries the agent added since the previous load (see MemoryView). */
  fresh: Set<string>
  limit: number
  reload(): void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  // A reload that lands while an edit is open brings the agent's new entries
  // into `content` but not into `draft`; saving the draft would then write
  // the file back without them. Close the edit on a content change instead —
  // the card the reader returns to shows what is actually on disk.
  const lastContent = useRef(content)
  useEffect(() => {
    if (lastContent.current === content) return
    lastContent.current = content
    setEditing(false)
    setDraft('')
  }, [content])

  const entries = parseEntries(content)

  /** Rebuild the whole file from entries and persist (whole-file PUT). */
  async function persistEntries(next: string[]) {
    try {
      await api.saveMemory(file, next.join('\n§\n'))
      toast(t('memory.saved'), 'success')
      reload()
      return true
    } catch (e) {
      toastError(e)
      return false
    }
  }

  async function save() {
    if (saving) return
    setSaving(true)
    try {
      await api.saveMemory(file, draft)
      toast(t('memory.saved'), 'success')
      setEditing(false)
      reload()
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  if (editing) {
    return (
      <MemoryEditor
        name={FILE_NAME[file]}
        value={draft}
        onInput={setDraft}
        chars={countChars(draft)}
        limit={limit}
        saving={saving}
        onCancel={() => setEditing(false)}
        onSave={save}
      />
    )
  }

  return (
    <MemoryFileCard
      name={FILE_NAME[file]}
      entries={entries}
      fresh={fresh}
      persist={persistEntries}
      chars={countChars(content)}
      limit={limit}
      onEdit={() => {
        setDraft(content)
        setEditing(true)
      }}
    />
  )
}

/** The editor for a whole file: the same head as the read card, a monospace
 *  well, and the save/cancel pair where the eye expects it. */
function MemoryEditor({
  name,
  value,
  onInput,
  chars,
  limit,
  hint,
  saving,
  onCancel,
  onSave,
}: {
  name: string
  value: string
  onInput(v: string): void
  chars: number
  limit: number
  hint?: string
  saving: boolean
  onCancel(): void
  onSave(): void
}) {
  const over = limit > 0 && chars > limit
  return (
    <div class="card memory-card memory-editor">
      <div class="memory-card-head">
        <span class="memory-card-name mono">{name}</span>
        <UsageMeter chars={chars} limit={limit} />
        <span class={`memory-counter mono${over ? ' over' : ''}`}>
          {limit > 0 ? `${chars} / ${limit}` : t('memory.charsN', { n: chars })}
        </span>
        <div class="memory-card-tools">
          <button class="btn small ghost" disabled={saving} onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button class="btn small primary" disabled={saving || over} onClick={onSave}>
            {saving ? t('memory.saving') : t('common.save')}
          </button>
        </div>
      </div>
      <textarea
        class="input memory-textarea mono"
        value={value}
        onInput={(e) => onInput((e.target as HTMLTextAreaElement).value)}
        rows={Math.min(22, Math.max(10, value.split('\n').length + 2))}
        spellcheck={false}
      />
      {hint && <p class="hint memory-editor-hint">{hint}</p>}
    </div>
  )
}

/** One §-entry file as a single card: file name, entry count, budget meter,
 *  filter and the edit verb share the head; the rows below are the entries
 *  themselves; the add field closes the card. Shared by USER.md, MEMORY.md,
 *  topic files and project memory so every list offers the same powers. */
function MemoryFileCard({
  name,
  entries,
  fresh,
  persist,
  chars,
  limit,
  onEdit,
  actions,
}: {
  name: string
  entries: string[]
  fresh?: Set<string>
  persist(next: string[]): Promise<boolean>
  chars: number
  limit: number
  onEdit(): void
  /** Extra head verbs (delete this topic, …). */
  actions?: ComponentChildren
}) {
  const [query, setQuery] = useState('')
  const [fixIdx, setFixIdx] = useState<number | null>(null)
  const [fixDraft, setFixDraft] = useState('')
  const [newEntry, setNewEntry] = useState('')
  const [busy, setBusy] = useState(false)

  const q = query.trim().toLowerCase()
  const visible = entries.map((e, i) => ({ e, i })).filter(({ e }) => q === '' || e.toLowerCase().includes(q))

  async function remove(i: number) {
    const ok = await confirmDialog({
      title: t('memory.entryDeleteTitle'),
      message: t('memory.entryDeleteMsg'),
    })
    if (!ok) return
    await persist(entries.filter((_, idx) => idx !== i))
  }

  async function saveFix(i: number) {
    const text = fixDraft.trim()
    const next = entries.slice()
    if (text === '') next.splice(i, 1)
    else next[i] = text
    if (await persist(next)) setFixIdx(null)
  }

  async function add() {
    const text = newEntry.trim()
    if (!text || busy) return
    setBusy(true)
    try {
      if (await persist([...entries, text])) setNewEntry('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card memory-card">
      <div class="memory-card-head">
        <span class="memory-card-name mono" title={name}>
          {name}
        </span>
        <span class="dim memory-card-count">{t('memory.entriesN', { n: entries.length })}</span>
        <UsageMeter chars={chars} limit={limit} />
        {limit > 0 && (
          <span class="memory-counter mono">
            {chars} / {limit}
          </span>
        )}
        <div class="memory-card-tools">
          {entries.length > 0 && (
            <label class="memory-filter">
              <Icon name="search" size={13} />
              <input
                type="search"
                placeholder={t('memory.search')}
                value={query}
                onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
              />
            </label>
          )}
          {actions}
          <button class="btn small" onClick={onEdit}>
            {t('memory.edit')}
          </button>
        </div>
      </div>

      {entries.length === 0 ? (
        <p class="dim memory-empty">{t('memory.empty')}</p>
      ) : visible.length === 0 ? (
        <p class="dim memory-empty">{t('memory.noMatches')}</p>
      ) : (
        <ul class="memory-entries">
          {visible.map(({ e, i }) => {
            const fromLog = fromLogSource(e)
            return (
              <li key={i} class={`memory-entry${fresh?.has(e) ? ' fresh' : ''}`}>
                <span class="memory-entry-idx mono" aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <div class="memory-entry-main">
                  {fixIdx === i ? (
                    <>
                      <textarea
                        class="input memory-entry-editbox mono"
                        rows={3}
                        value={fixDraft}
                        onInput={(ev) => setFixDraft((ev.target as HTMLTextAreaElement).value)}
                        spellcheck={false}
                      />
                      <div class="memory-entry-fix">
                        <button class="btn small ghost" onClick={() => setFixIdx(null)}>
                          {t('common.cancel')}
                        </button>
                        <button class="btn small primary" onClick={() => saveFix(i)}>
                          {t('common.save')}
                        </button>
                      </div>
                    </>
                  ) : (
                    <span class="memory-entry-text" title={e}>
                      {stripFromMarker(e)}
                    </span>
                  )}
                  {fromLog && fixIdx !== i && (
                    <span class="badge from-log" title={t('memory.fromLogTip')}>
                      {t('memory.fromLog')}
                    </span>
                  )}
                </div>
                {fixIdx !== i && (
                  <span class="memory-entry-actions">
                    <button
                      class="icon-btn"
                      title={t('memory.edit')}
                      aria-label={t('memory.edit')}
                      onClick={() => {
                        setFixIdx(i)
                        setFixDraft(e)
                      }}
                    >
                      <Icon name="pencil" size={13} />
                    </button>
                    <button
                      class="icon-btn memory-entry-del"
                      title={t('memory.entryDelete')}
                      aria-label={t('memory.entryDelete')}
                      onClick={() => remove(i)}
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  </span>
                )}
              </li>
            )
          })}
        </ul>
      )}

      <div class="memory-add-row">
        <input
          class="input"
          type="text"
          placeholder={t('memory.addPlaceholder')}
          value={newEntry}
          onInput={(e) => setNewEntry((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) add()
          }}
        />
        <button class="btn small" disabled={!newEntry.trim() || busy} onClick={add}>
          {t('memory.add')}
        </button>
      </div>
    </div>
  )
}

/** DREAMS.md — the Dreamer's diary. Read-only at a glance; opening the
 *  editor asks for confirmation (it is free-form prose, not § entries). */
function DreamsView({ content, reload }: { content: string; reload(): void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  async function startEdit() {
    const ok = await confirmDialog({
      title: t('memory.dreamsEditTitle'),
      message: t('memory.dreamsEditMsg'),
      danger: false,
      confirmLabel: t('memory.edit'),
    })
    if (!ok) return
    setDraft(content)
    setEditing(true)
  }

  async function save() {
    if (saving) return
    setSaving(true)
    try {
      await api.saveMemory('dreams', draft)
      toast(t('memory.saved'), 'success')
      setEditing(false)
      reload()
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  if (editing) {
    return (
      <MemoryEditor
        name="DREAMS.md"
        value={draft}
        onInput={setDraft}
        chars={[...draft].length}
        limit={0}
        hint={t('memory.dreamsEditHint')}
        saving={saving}
        onCancel={() => setEditing(false)}
        onSave={save}
      />
    )
  }

  return (
    <div class="card memory-card">
      <div class="memory-card-head">
        <span class="memory-card-name mono">DREAMS.md</span>
        <span class="dim memory-card-count">{t('memory.charsN', { n: [...content].length })}</span>
        <div class="memory-card-tools">
          <button class="btn small" onClick={startEdit}>
            {t('memory.edit')}
          </button>
        </div>
      </div>
      {content ? (
        <pre class="memory-content mono">{content}</pre>
      ) : (
        <p class="dim memory-empty">{t('memory.empty')}</p>
      )}
    </div>
  )
}

/** An empty pane: the icon names the surface, the line says what to do next.
 *  It is not a bare sentence floating in a card. */
function MemoryPlaceholder({
  icon,
  text,
  children,
}: {
  icon: IconName
  text: string
  children?: ComponentChildren
}) {
  return (
    <div class="card memory-card memory-placeholder">
      <Icon name={icon} size={20} />
      <p class="dim">{text}</p>
      {children}
    </div>
  )
}

/** The left rail shared by topics / daily / projects: a title with a count, a
 *  scrollable list, and an optional foot (create a topic, prune the diary). */
function MemoryRail({
  title,
  count,
  foot,
  children,
}: {
  title: string
  count?: number
  foot?: ComponentChildren
  children: ComponentChildren
}) {
  return (
    <aside class="card memory-rail">
      <div class="memory-rail-head">
        <span class="memory-rail-title">{title}</span>
        {count != null && <span class="mono dim memory-rail-count">{count}</span>}
      </div>
      <div class="memory-rail-list">{children}</div>
      {foot && <div class="memory-rail-foot">{foot}</div>}
    </aside>
  )
}

/** topics/*.md — the selective-load memory files. The rail picks one (the
 *  first is open by default, like the diary), typing a name creates a new
 *  one, and the card edits it in place. */
function TopicsView({ topics, limit, reload }: { topics: TopicFile[]; limit: number; reload(): void }) {
  const [selected, setSelected] = useState<string>('')
  const [newName, setNewName] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  const current = topics.find((tp) => tp.name === selected) ?? topics[0] ?? null

  function open(name: string) {
    setSelected(name)
    setEditing(false)
  }

  async function create() {
    const name = newName.trim()
    if (!name || saving) return
    setSaving(true)
    try {
      await api.saveMemoryTopic(name, '')
      toast(t('memory.topicSaved'), 'success')
      setNewName('')
      reload()
      setSelected(name)
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  async function remove(name: string) {
    const ok = await confirmDialog({
      title: t('memory.topicDeleteTitle'),
      message: t('memory.topicDeleteMsg', { name }),
    })
    if (!ok) return
    try {
      await api.deleteMemoryTopic(name)
      toast(t('memory.topicDeleted'), 'success')
      if (selected === name) setSelected('')
      reload()
    } catch (e) {
      toastError(e)
    }
  }

  async function save() {
    if (saving || !current) return
    setSaving(true)
    try {
      await api.saveMemoryTopic(current.name, draft)
      toast(t('memory.saved'), 'success')
      setEditing(false)
      reload()
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  /** Per-entry ops write the whole topic file back (same route as the editor). */
  async function persistEntries(next: string[]) {
    if (!current) return false
    try {
      await api.saveMemoryTopic(current.name, next.join('\n§\n'))
      toast(t('memory.saved'), 'success')
      reload()
      return true
    } catch (e) {
      toastError(e)
      return false
    }
  }

  const createRow = (
    <div class="memory-empty-form">
      <input
        class="input mono"
        type="text"
        placeholder={t('memory.topicNewPlaceholder')}
        value={newName}
        onInput={(e) => setNewName((e.target as HTMLInputElement).value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) create()
        }}
      />
      <button class="btn small" disabled={!newName.trim() || saving} onClick={create}>
        {t('memory.topicNew')}
      </button>
    </div>
  )

  // No topics yet: one card that both explains the surface and owns the
  // create field, instead of an empty rail beside an empty pane.
  if (topics.length === 0) {
    return (
      <MemoryPlaceholder icon="folder" text={t('memory.topicsEmpty')}>
        {createRow}
      </MemoryPlaceholder>
    )
  }

  return (
    <div class="memory-split">
      <MemoryRail
        title={t('memory.topics')}
        count={topics.length}
        foot={
          <>
            <input
              class="input mono"
              type="text"
              placeholder={t('memory.topicNewPlaceholder')}
              value={newName}
              onInput={(e) => setNewName((e.target as HTMLInputElement).value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) create()
              }}
            />
            <button class="btn small" disabled={!newName.trim() || saving} onClick={create}>
              {t('memory.topicNew')}
            </button>
          </>
        }
      >
        <ul class="memory-file-list">
          {topics.map((tp) => (
            <li key={tp.name}>
              <button
                class={`memory-file-item${current?.name === tp.name ? ' active' : ''}`}
                onClick={() => open(tp.name)}
              >
                <span class="memory-file-label">{tp.name}</span>
                <span class="memory-file-meta mono">{parseEntries(tp.content).length}</span>
              </button>
            </li>
          ))}
        </ul>
      </MemoryRail>
      <div class="memory-pane">
        {!current ? (
          <MemoryPlaceholder icon="folder" text={t('memory.topicPick')} />
        ) : editing ? (
          <MemoryEditor
            name={`topics/${current.name}.md`}
            value={draft}
            onInput={setDraft}
            chars={countChars(draft)}
            limit={limit}
            hint={t('memory.editHint')}
            saving={saving}
            onCancel={() => setEditing(false)}
            onSave={save}
          />
        ) : (
          <MemoryFileCard
            name={`topics/${current.name}.md`}
            entries={parseEntries(current.content)}
            persist={persistEntries}
            chars={countChars(current.content)}
            limit={limit}
            onEdit={() => {
              setDraft(current.content)
              setEditing(true)
            }}
            actions={
              <button class="btn small ghost memory-card-del" onClick={() => remove(current.name)}>
                {t('memory.topicDelete')}
              </button>
            }
          />
        )}
      </div>
    </div>
  )
}

/** daily/*.md — the warm-layer diary. Content stays read-only (the Dreamer
 *  owns the writes), but whole files can be deleted and the 30d-archive /
 *  90d-delete retention windows can be applied on demand. Newest first. */
function DailyView({ daily, reload }: { daily: TopicFile[]; reload(): void }) {
  const [selected, setSelected] = useState<string>('')
  const [pruning, setPruning] = useState(false)
  const current = daily.find((d) => d.name === selected) ?? daily[0] ?? null

  async function remove(name: string) {
    const ok = await confirmDialog({
      title: t('memory.dailyDeleteTitle'),
      message: t('memory.dailyDeleteMsg', { name }),
    })
    if (!ok) return
    try {
      await api.deleteMemoryDaily(name)
      toast(t('memory.dailyDeleted'), 'success')
      if (current?.name === name) setSelected('')
      reload()
    } catch (e) {
      toastError(e)
    }
  }

  async function prune() {
    if (pruning) return
    const ok = await confirmDialog({
      title: t('memory.dailyPruneTitle'),
      message: t('memory.dailyPruneMsg'),
      danger: false,
      confirmLabel: t('memory.dailyPrune'),
    })
    if (!ok) return
    setPruning(true)
    try {
      await api.pruneMemoryDaily()
      toast(t('memory.dailyPruned'), 'success')
      reload()
    } catch (e) {
      toastError(e)
    } finally {
      setPruning(false)
    }
  }

  if (daily.length === 0) {
    return (
      <MemoryPlaceholder icon="list-checks" text={t('memory.dailyEmpty')}>
        <p class="hint">{t('memory.dailyRetention')}</p>
      </MemoryPlaceholder>
    )
  }

  return (
    <div class="memory-split">
      <MemoryRail
        title={t('memory.daily')}
        count={daily.length}
        foot={
          <>
            <button class="btn small" disabled={pruning} onClick={prune}>
              {pruning ? t('memory.dailyPruning') : t('memory.dailyPrune')}
            </button>
            <p class="hint memory-rail-hint">{t('memory.dailyRetention')}</p>
          </>
        }
      >
        <ul class="memory-file-list">
          {daily.map((d) => (
            <li key={d.name} class="memory-file-row">
              <button
                class={`memory-file-item${current?.name === d.name ? ' active' : ''}`}
                onClick={() => setSelected(d.name)}
              >
                <span class="memory-file-label">{d.name}</span>
                <span class="memory-file-meta mono">{dayLines(d.content)}</span>
              </button>
              <button
                class="btn small ghost memory-file-del"
                title={t('memory.dailyDelete')}
                aria-label={`${t('memory.dailyDelete')} ${d.name}`}
                onClick={() => remove(d.name)}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      </MemoryRail>
      <div class="memory-pane">
        <div class="card memory-card">
          <div class="memory-card-head">
            <span class="memory-card-name mono">{current ? `daily/${current.name}.md` : 'daily'}</span>
            {current && <span class="dim memory-card-count">{t('memory.entriesN', { n: dayLines(current.content) })}</span>}
            <span class="dim memory-card-hint memory-card-tools">{t('memory.dailyHint')}</span>
          </div>
          {current ? (
            current.content ? (
              <pre class="memory-content mono">{current.content}</pre>
            ) : (
              <p class="dim memory-empty">{t('memory.empty')}</p>
            )
          ) : (
            <p class="dim memory-empty">{t('memory.dailyEmpty')}</p>
          )}
        </div>
      </div>
    </div>
  )
}

/** Per-project MEMORY.md: pick a project, read or rewrite its memory with
 *  the configured project cap. */
function ProjectsView({ defaultLimit, reload }: { defaultLimit: number; reload(): void }) {
  const [list, setList] = useState<ProjectDetail[] | null>(null)
  const [selected, setSelected] = useState('')
  const [mem, setMem] = useState<ProjectMemory | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .projects()
      .then((p) => setList(p.detail?.length ? p.detail : p.projects.map((name) => ({ name }) as ProjectDetail)))
      .catch((e: unknown) => setLoadErr(e instanceof Error ? e.message : String(e)))
  }, [])

  const current = useMemo(
    () => list?.find((p) => p.name === selected) ?? list?.[0] ?? null,
    [list, selected],
  )

  useEffect(() => {
    const name = current?.name
    if (!name) {
      setMem(null)
      return
    }
    setMem(null)
    setEditing(false)
    api
      .projectMemory(name)
      .then(setMem)
      .catch((e: unknown) => setLoadErr(e instanceof Error ? e.message : String(e)))
  }, [current?.name])

  async function save() {
    if (saving || !current) return
    setSaving(true)
    try {
      await api.saveProjectMemory(current.name, draft)
      toast(t('memory.saved'), 'success')
      setEditing(false)
      reload()
      const re = await api.projectMemory(current.name)
      setMem(re)
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  /** Per-entry ops write the whole project file back (same route as the editor). */
  async function persistEntries(next: string[]) {
    if (!current) return false
    try {
      await api.saveProjectMemory(current.name, next.join('\n§\n'))
      toast(t('memory.saved'), 'success')
      const re = await api.projectMemory(current.name)
      setMem(re)
      return true
    } catch (e) {
      toastError(e)
      return false
    }
  }

  const limit = mem?.limit ?? defaultLimit

  if (loadErr && !list) {
    return <MemoryPlaceholder icon="layout" text={loadErr} />
  }
  if (!list) return <MemoryPlaceholder icon="layout" text={t('common.loading')} />
  if (list.length === 0) return <MemoryPlaceholder icon="layout" text={t('memory.projectsEmpty')} />

  return (
    <div class="memory-split">
      <MemoryRail title={t('memory.projects')} count={list.length}>
        <ul class="memory-file-list">
          {list.map((p) => (
            <li key={p.name}>
              <button
                class={`memory-file-item${current?.name === p.name ? ' active' : ''}`}
                onClick={() => setSelected(p.name)}
              >
                <span class="memory-file-label">{p.name}</span>
                {p.memory_entries != null && <span class="memory-file-meta mono">{p.memory_entries}</span>}
              </button>
            </li>
          ))}
        </ul>
      </MemoryRail>

      <div class="memory-pane">
        {loadErr && <p class="gate-error">{loadErr}</p>}
        {!current ? (
          <MemoryPlaceholder icon="layout" text={t('memory.projectPick')} />
        ) : !mem ? (
          <MemoryPlaceholder icon="layout" text={t('common.loading')} />
        ) : editing ? (
          <MemoryEditor
            name={current.name}
            value={draft}
            onInput={setDraft}
            chars={countChars(draft)}
            limit={limit}
            hint={t('memory.editHint')}
            saving={saving}
            onCancel={() => setEditing(false)}
            onSave={save}
          />
        ) : (
          <MemoryFileCard
            name={current.name}
            entries={parseEntries(mem.content)}
            persist={persistEntries}
            chars={countChars(mem.content)}
            limit={limit}
            onEdit={() => {
              setDraft(mem.content)
              setEditing(true)
            }}
          />
        )}
      </div>
    </div>
  )
}

/** The knowledge-graph tab: an at-a-glance answer to "what does the agent
 *  know, and how did it learn it". Edges are honest — a promotion marker the
 *  Dreamer wrote, or a literal name mention — so the picture never invents
 *  structure. Clicking a node lists its connections and jumps to the owning
 *  surface. */
function GraphTab({ onOpenTab }: { onOpenTab(tab: Tab): void }) {
  useLocaleRerender()
  const change = useChangeSignal()
  const { data: graph, error } = useAsync(() => api.memoryGraph(), [], change)

  function openNode(n: MemoryGraphNode) {
    // Topics / projects / daily are tabs of this very page, so they switch
    // locally. A skill node is a different section, so it routes there.
    if (n.kind === 'topic') onOpenTab('topics')
    else if (n.kind === 'project') onOpenTab('projects')
    else if (n.kind === 'daily') onOpenTab('daily')
    else if (n.kind === 'skill') navigate({ view: 'settings', tab: 'skills' })
  }

  if (error) return <ErrorState head={false} title={t('memory.graphTitle')} sub="" error={error} onRetry={() => {}} />
  if (!graph) return <LoadingState head={false} title={t('memory.graphTitle')} sub="" />
  if (graph.nodes.length === 0) return <p class="dim">{t('memory.graphEmpty')}</p>
  return <MemoryGraphView graph={graph} onOpen={openNode} />
}

/** Entries promoted from the daily logs carry a `[from:…]` marker (e.g.
 *  `[from:daily]`) — returns the source label when present. */
function fromLogSource(entry: string): string | null {
  const m = entry.match(/\[from:([^\]]+)\]/)
  return m && m[1] ? m[1] : null
}

/** The marker is provenance, not prose: the row shows it as a badge, so the
 *  text itself is rendered without it. */
function stripFromMarker(entry: string): string {
  return entry.replace(/^\[from:[^\]]+\]\s*/, '')
}

/** Diary lines: the warm-layer files are bullet lists, so the useful count is
 *  the number of entries, not the character total. */
function dayLines(content: string): number {
  return content.split('\n').filter((l) => l.trim() !== '').length
}

/** Split a memory file into its §-separated entries — same parse the backend
 *  (memory.ParseMem) applies, mirrored here for rendering. */
function parseEntries(content: string): string[] {
  return content
    .split('§')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

/** Char count with the same measure as the backend cap (serialized length
 *  including separators). */
function countChars(content: string): number {
  const entries = parseEntries(content)
  return [...entries.join('\n§\n')].length
}

export type { MemoryFiles }
