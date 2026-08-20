import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type MemoryFiles, type ProjectMemory, type TopicFile } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, LoadingState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'
import { confirmDialog } from '../components/confirm'

type Tab = 'user' | 'memory' | 'dreams' | 'topics' | 'daily' | 'projects'

/** The memory view (C2): every Hermes file is user-managed here. USER.md /
 *  MEMORY.md edit as § entries (entries promoted from the daily logs carry a
 *  [from:…] badge and can be fixed or dropped individually), DREAMS.md is
 *  the Dreamer's diary — open for editing behind a confirm — topics/*.md are
 *  the selective-load memory files (create / edit / delete), daily/*.md is a
 *  read-only browse of the warm-layer diary, and project memory is picked
 *  per project and edited in place. Caps come from the live config values. */
export function MemoryView() {
  useLocaleRerender()
  const [tick, setTick] = useState(0)
  const { data, error } = useAsync(() => api.memory(), [], tick)
  const [tab, setTab] = useState<Tab>('user')
  const [now, setNow] = useState(() => Date.now())

  // Live clock: anchor on the node's reported time, then tick locally so the
  // display stays useful between refreshes.
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  if (error) {
    return (
      <ErrorState
        title={t('memory.title')}
        sub={t('memory.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )
  }
  if (!data) return <LoadingState title={t('memory.title')} sub={t('memory.subtitle')} />

  // Offset between the browser clock and the node clock, so the ticking
  // display shows node time, not local time.
  const nodeOffset = data.time ? new Date(data.time).getTime() - Date.now() : 0
  const clock = new Date(now + nodeOffset)
  const reload = () => setTick((v) => v + 1)

  return (
    <section>
      <PageHeader title={t('memory.title')} sub={t('memory.subtitle')} />

      <div class="system-head">
        <div class="card version-card">
          <span class="dim">{t('memory.nodeTime')}</span>
          <span class="version-num mono" data-testid="node-clock">
            {clock.toLocaleString()}
          </span>
        </div>
        <div class="card version-card">
          <span class="dim">{t('memory.refreshedAt')}</span>
          <button class="btn" onClick={reload}>
            {t('memory.refresh')}
          </button>
        </div>
      </div>

      <div class="segmented memory-tabs" role="tablist">
        {(['user', 'memory', 'dreams', 'topics', 'daily', 'projects'] as const).map((k) => (
          <button
            key={k}
            role="tab"
            aria-selected={tab === k}
            class={`seg${tab === k ? ' on' : ''}`}
            onClick={() => setTab(k)}
          >
            {t(`memory.${k}`)}
          </button>
        ))}
      </div>

      {tab === 'user' && (
        <MemoryFileView
          key={`user-${tick}`}
          file="user"
          content={data.user}
          limit={data.user_limit}
          reload={reload}
        />
      )}
      {tab === 'memory' && (
        <MemoryFileView
          key={`memory-${tick}`}
          file="memory"
          content={data.memory}
          limit={data.mem_limit}
          reload={reload}
        />
      )}
      {tab === 'dreams' && <DreamsView content={data.dreams} reload={reload} />}
      {tab === 'topics' && <TopicsView topics={data.topics} limit={data.mem_limit} reload={reload} />}
      {tab === 'daily' && <DailyView daily={data.daily} />}
      {tab === 'projects' && <ProjectsView defaultLimit={data.project_limit} reload={reload} />}
    </section>
  )
}

/** USER.md / MEMORY.md / a topic file as editable § entries. Entries the
 *  Dreamer promoted out of the daily logs carry a `[from:…]` marker: they
 *  render with a badge and get per-entry fix / delete shortcuts so a weak
 *  signal that slipped in can be corrected without rewriting the file. */
function MemoryFileView({
  file,
  content,
  limit,
  reload,
  displayName,
}: {
  file: 'user' | 'memory'
  content: string
  limit: number
  reload(): void
  /** Topic tab reuses this component; the save route differs. */
  displayName?: string
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [entryFix, setEntryFix] = useState<number | null>(null)
  const [entryDraft, setEntryDraft] = useState('')

  // Highlight: entries that were not present at the previous load glow once
  // so "what did the agent just learn" is answerable at a glance. The ref
  // survives re-renders (refreshes produce new props, not a remount).
  const seenRef = useRef<Map<string, number>>(new Map())
  const entries = parseEntries(content)
  const seen = seenRef.current
  const fresh = new Set<string>()
  const now = Date.now()
  for (const e of entries) {
    if (!seen.has(e)) {
      seen.set(e, now)
      fresh.add(e)
    }
  }

  function startEdit() {
    setDraft(content)
    setEditing(true)
  }

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

  async function deleteEntry(i: number) {
    const ok = await confirmDialog({
      title: t('memory.entryDeleteTitle'),
      message: t('memory.entryDeleteMsg'),
    })
    if (!ok) return
    const next = entries.filter((_, idx) => idx !== i)
    await persistEntries(next)
  }

  async function saveEntryFix(i: number) {
    const text = entryDraft.trim()
    const next = entries.slice()
    if (text === '') next.splice(i, 1)
    else next[i] = text
    if (await persistEntries(next)) setEntryFix(null)
  }

  const chars = countChars(draft)
  const over = editing && limit > 0 && chars > limit

  if (editing) {
    return (
      <div class="card memory-editor">
        <textarea
          class="input memory-textarea mono"
          value={draft}
          onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
          rows={Math.min(20, Math.max(8, draft.split('\n').length + 2))}
          spellcheck={false}
        />
        <div class="memory-editor-foot">
          <span class={`memory-counter mono${over ? ' over' : ''}`}>
            {chars} / {limit}
          </span>
          <span class="hint">{t('memory.editHint')}</span>
          <span class="memory-editor-actions">
            <button class="btn" disabled={saving} onClick={() => setEditing(false)}>
              {t('common.cancel')}
            </button>
            <button class="btn primary" disabled={saving || over} onClick={save}>
              {saving ? t('memory.saving') : t('common.save')}
            </button>
          </span>
        </div>
      </div>
    )
  }

  return (
    <div class="card memory-card">
      {displayName && <h2 class="block-title file-title">{displayName}</h2>}
      {entries.length === 0 ? (
        <p class="dim">{t('memory.empty')}</p>
      ) : (
        <ul class="memory-entries">
          {entries.map((e, i) => {
            const fromLog = fromLogSource(e)
            return (
              <li key={i} class={`memory-entry${fresh.has(e) ? ' fresh' : ''}`} title={e}>
                {entryFix === i ? (
                  <>
                    <textarea
                      class="input memory-entry-editbox mono"
                      rows={3}
                      value={entryDraft}
                      onInput={(ev) => setEntryDraft((ev.target as HTMLTextAreaElement).value)}
                      spellcheck={false}
                    />
                    <span class="memory-entry-actions">
                      <button class="btn small" onClick={() => setEntryFix(null)}>
                        {t('common.cancel')}
                      </button>
                      <button class="btn small primary" onClick={() => saveEntryFix(i)}>
                        {t('common.save')}
                      </button>
                    </span>
                  </>
                ) : (
                  <>
                    {e}
                    {fromLog && (
                      <span class="badge from-log" title={t('memory.fromLogTip')}>
                        {t('memory.fromLog')}
                      </span>
                    )}
                    {fromLog && (
                      <span class="memory-entry-actions">
                        <button
                          class="btn small"
                          onClick={() => {
                            setEntryFix(i)
                            setEntryDraft(e)
                          }}
                        >
                          {t('memory.entryFix')}
                        </button>
                        <button class="btn small" onClick={() => deleteEntry(i)}>
                          {t('memory.entryDelete')}
                        </button>
                      </span>
                    )}
                  </>
                )}
              </li>
            )
          })}
        </ul>
      )}
      <div class="memory-edit-row">
        <span class="dim memory-counter mono">
          {countChars(content)} / {limit}
        </span>
        <button class="btn" onClick={startEdit}>
          {t('memory.edit')}
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
      <div class="card memory-editor">
        <textarea
          class="input memory-textarea mono"
          value={draft}
          onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
          rows={Math.min(24, Math.max(10, draft.split('\n').length + 2))}
          spellcheck={false}
        />
        <div class="memory-editor-foot">
          <span class="memory-counter mono">{[...draft].length}</span>
          <span class="hint">{t('memory.dreamsEditHint')}</span>
          <span class="memory-editor-actions">
            <button class="btn" disabled={saving} onClick={() => setEditing(false)}>
              {t('common.cancel')}
            </button>
            <button class="btn primary" disabled={saving} onClick={save}>
              {saving ? t('memory.saving') : t('common.save')}
            </button>
          </span>
        </div>
      </div>
    )
  }

  return (
    <div class="card memory-card">
      {content ? (
        <pre class="memory-content mono">{content}</pre>
      ) : (
        <p class="dim">{t('memory.empty')}</p>
      )}
      <div class="memory-edit-row">
        <span class="dim memory-counter mono">{[...content].length}</span>
        <button class="btn" onClick={startEdit}>
          {t('memory.edit')}
        </button>
      </div>
    </div>
  )
}

/** topics/*.md — the selective-load memory files. Pick one on the left to
 *  edit, type a name to create a new one, delete behind a confirm. */
function TopicsView({ topics, limit, reload }: { topics: TopicFile[]; limit: number; reload(): void }) {
  const [selected, setSelected] = useState<string>('')
  const [newName, setNewName] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  const current = topics.find((tp) => tp.name === selected) ?? null

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

  const chars = countChars(draft)
  const over = editing && limit > 0 && chars > limit

  return (
    <div class="memory-split">
      <div class="card">
        <h2 class="block-title">{t('memory.topics')}</h2>
        <ul class="memory-file-list">
          {topics.length === 0 && <li class="dim">{t('memory.topicsEmpty')}</li>}
          {topics.map((tp) => (
            <li key={tp.name}>
              <button
                class={`memory-file-item${selected === tp.name ? ' active' : ''}`}
                onClick={() => open(tp.name)}
              >
                {tp.name}
              </button>
            </li>
          ))}
        </ul>
        <div class="memory-topic-foot">
          <input
            class="input mono"
            type="text"
            placeholder={t('memory.topicNewPlaceholder')}
            value={newName}
            onInput={(e) => setNewName((e.target as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') create()
            }}
          />
          <button class="btn small" disabled={!newName.trim() || saving} onClick={create}>
            {t('memory.topicNew')}
          </button>
        </div>
      </div>

      <div>
        {!current ? (
          <div class="card memory-card">
            <p class="dim">{t('memory.topicPick')}</p>
          </div>
        ) : editing ? (
          <div class="card memory-editor">
            <textarea
              class="input memory-textarea mono"
              value={draft}
              onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
              rows={Math.min(20, Math.max(8, draft.split('\n').length + 2))}
              spellcheck={false}
            />
            <div class="memory-editor-foot">
              <span class={`memory-counter mono${over ? ' over' : ''}`}>
                {chars} / {limit}
              </span>
              <span class="hint">{t('memory.editHint')}</span>
              <span class="memory-editor-actions">
                <button class="btn" disabled={saving} onClick={() => setEditing(false)}>
                  {t('common.cancel')}
                </button>
                <button class="btn primary" disabled={saving || over} onClick={save}>
                  {saving ? t('memory.saving') : t('common.save')}
                </button>
              </span>
            </div>
          </div>
        ) : (
          <div class="card memory-card">
            <h2 class="block-title mono file-title">{current.name}</h2>
            {parseEntries(current.content).length === 0 ? (
              <p class="dim">{t('memory.empty')}</p>
            ) : (
              <ul class="memory-entries">
                {parseEntries(current.content).map((e, i) => (
                  <li key={i} class="memory-entry" title={e}>
                    {e}
                  </li>
                ))}
              </ul>
            )}
            <div class="memory-edit-row">
              <span class="dim memory-counter mono">
                {countChars(current.content)} / {limit}
              </span>
              <button
                class="btn"
                onClick={() => {
                  setDraft(current.content)
                  setEditing(true)
                }}
              >
                {t('memory.edit')}
              </button>
              <button class="btn" onClick={() => remove(current.name)}>
                {t('memory.topicDelete')}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

/** daily/*.md — the warm-layer diary. Read-only browse, newest first. */
function DailyView({ daily }: { daily: TopicFile[] }) {
  const [selected, setSelected] = useState<string>('')
  const current = daily.find((d) => d.name === selected) ?? daily[0] ?? null

  return (
    <div class="memory-split">
      <div class="card">
        <h2 class="block-title">{t('memory.daily')}</h2>
        <p class="hint">{t('memory.dailyHint')}</p>
        <ul class="memory-file-list">
          {daily.length === 0 && <li class="dim">{t('memory.dailyEmpty')}</li>}
          {daily.map((d) => (
            <li key={d.name}>
              <button
                class={`memory-file-item${current?.name === d.name ? ' active' : ''}`}
                onClick={() => setSelected(d.name)}
              >
                {d.name}
              </button>
            </li>
          ))}
        </ul>
      </div>
      <div class="card memory-card">
        {current ? (
          current.content ? (
            <pre class="memory-content mono">{current.content}</pre>
          ) : (
            <p class="dim">{t('memory.empty')}</p>
          )
        ) : (
          <p class="dim">{t('memory.dailyEmpty')}</p>
        )}
      </div>
    </div>
  )
}

/** Per-project MEMORY.md: pick a project, read or rewrite its memory with
 *  the configured project cap. */
function ProjectsView({ defaultLimit, reload }: { defaultLimit: number; reload(): void }) {
  const [projects, setProjects] = useState<string[] | null>(null)
  const [selected, setSelected] = useState('')
  const [mem, setMem] = useState<ProjectMemory | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .projects()
      .then((p) => setProjects(p.projects))
      .catch((e: unknown) => setLoadErr(e instanceof Error ? e.message : String(e)))
  }, [])

  useEffect(() => {
    if (!selected) {
      setMem(null)
      return
    }
    setMem(null)
    setEditing(false)
    api
      .projectMemory(selected)
      .then(setMem)
      .catch((e: unknown) => setLoadErr(e instanceof Error ? e.message : String(e)))
  }, [selected])

  async function save() {
    if (saving || !selected) return
    setSaving(true)
    try {
      await api.saveProjectMemory(selected, draft)
      toast(t('memory.saved'), 'success')
      setEditing(false)
      reload()
      const re = await api.projectMemory(selected)
      setMem(re)
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  const limit = mem?.limit ?? defaultLimit
  const chars = countChars(draft)
  const over = editing && limit > 0 && chars > limit

  return (
    <div class="memory-split">
      <div class="card">
        <h2 class="block-title">{t('memory.projects')}</h2>
        {loadErr && <p class="gate-error">{loadErr}</p>}
        {!projects ? (
          <p class="dim">{t('common.loading')}</p>
        ) : projects.length === 0 ? (
          <p class="dim">{t('memory.projectsEmpty')}</p>
        ) : (
          <ul class="memory-file-list">
            {projects.map((p) => (
              <li key={p}>
                <button
                  class={`memory-file-item${selected === p ? ' active' : ''}`}
                  onClick={() => setSelected(p)}
                >
                  {p}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        {!selected ? (
          <div class="card memory-card">
            <p class="dim">{t('memory.projectPick')}</p>
          </div>
        ) : editing ? (
          <div class="card memory-editor">
            <textarea
              class="input memory-textarea mono"
              value={draft}
              onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
              rows={Math.min(20, Math.max(8, draft.split('\n').length + 2))}
              spellcheck={false}
            />
            <div class="memory-editor-foot">
              <span class={`memory-counter mono${over ? ' over' : ''}`}>
                {chars} / {limit}
              </span>
              <span class="hint">{t('memory.editHint')}</span>
              <span class="memory-editor-actions">
                <button class="btn" disabled={saving} onClick={() => setEditing(false)}>
                  {t('common.cancel')}
                </button>
                <button class="btn primary" disabled={saving || over} onClick={save}>
                  {saving ? t('memory.saving') : t('common.save')}
                </button>
              </span>
            </div>
          </div>
        ) : (
          <div class="card memory-card">
            <h2 class="block-title mono file-title">{selected}</h2>
            {!mem ? (
              <p class="dim">{t('common.loading')}</p>
            ) : parseEntries(mem.content).length === 0 ? (
              <p class="dim">{t('memory.empty')}</p>
            ) : (
              <ul class="memory-entries">
                {parseEntries(mem.content).map((e, i) => (
                  <li key={i} class="memory-entry" title={e}>
                    {e}
                  </li>
                ))}
              </ul>
            )}
            {mem && (
              <div class="memory-edit-row">
                <span class="dim memory-counter mono">
                  {countChars(mem.content)} / {limit}
                </span>
                <button
                  class="btn"
                  onClick={() => {
                    setDraft(mem.content)
                    setEditing(true)
                  }}
                >
                  {t('memory.edit')}
                </button>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/** Entries promoted from the daily logs carry a `[from:…]` marker (e.g.
 *  `[from:daily]`) — returns the source label when present. */
function fromLogSource(entry: string): string | null {
  const m = entry.match(/\[from:([^\]]+)\]/)
  return m && m[1] ? m[1] : null
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
