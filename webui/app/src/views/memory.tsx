import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type MemoryFiles } from '../api/client'
import { useAsync, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, LoadingState, PageHeader } from '../components/page'
import { toast, toastError } from '../components/toast'

type Tab = 'user' | 'memory' | 'dreams'

/** The memory view (P1: 产品化). What used to be a debug-style `<pre>` dump is
 *  now a product page: USER.md / MEMORY.md render as individual memory
 *  entries, entries the agent added since your last look are highlighted, and
 *  both files are editable in place with a live character counter against the
 *  Hermes caps. DREAMS.md stays read-only — it is the Dreamer's diary. Plus
 *  the node's system clock: the agent has no senses of its own, so this page
 *  shows exactly the "now" its time.now tool reports. */
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
  const files: Record<Tab, string> = {
    user: data.user,
    memory: data.memory,
    dreams: data.dreams,
  }
  const limits: Record<Tab, number> = {
    user: data.user_limit,
    memory: data.mem_limit,
    dreams: 0,
  }

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
          <button class="btn" onClick={() => setTick((v) => v + 1)}>
            {t('memory.refresh')}
          </button>
        </div>
      </div>

      <div class="segmented memory-tabs" role="tablist">
        {(['user', 'memory', 'dreams'] as const).map((k) => (
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

      {tab === 'dreams' ? (
        <div class="card memory-card">
          {files.dreams ? (
            <pre class="memory-content mono">{files.dreams}</pre>
          ) : (
            <p class="dim">{t('memory.empty')}</p>
          )}
        </div>
      ) : (
        <MemoryFileView
          key={tab}
          tab={tab}
          content={files[tab]}
          limit={limits[tab]}
          reload={() => setTick((v) => v + 1)}
        />
      )}
    </section>
  )
}

/** One editable memory file: rendered entries by default, raw textarea in
 *  edit mode. `key={tab}` on the caller remounts on tab switch so edit state
 *  never leaks across files. */
function MemoryFileView({
  tab,
  content,
  limit,
  reload,
}: {
  tab: 'user' | 'memory'
  content: string
  limit: number
  reload(): void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

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

  async function save() {
    if (saving) return
    setSaving(true)
    try {
      await api.saveMemory(tab, draft)
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
      {entries.length === 0 ? (
        <p class="dim">{t('memory.empty')}</p>
      ) : (
        <ul class="memory-entries">
          {entries.map((e, i) => (
            <li key={i} class={`memory-entry${fresh.has(e) ? ' fresh' : ''}`} title={e}>
              {e}
            </li>
          ))}
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
