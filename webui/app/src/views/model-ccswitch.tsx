// SPDX-License-Identifier: AGPL-3.0-or-later

// CC Switch's own import sheet: one tab per app roster (Claude Code, Claude
// Desktop, Codex, Gemini), one roster on screen at a time.
//
// It is a separate door from the discover sheet on purpose. The store holds
// four independent lists that answer different questions — "which Claude Code
// relays do I have", "which Codex providers" — and folding them into one list
// of thirty rows made the two a person actually wanted impossible to find.
// Choosing a tab is choosing which list to look at; nothing is shown for the
// rosters nobody opened.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type DiscoverSource, type DiscoveredModel, type ModelImportItem } from '../api/client'
import { useModalFocus } from '../components/modal-focus'
import { Icon } from '../components/icons'
import { t } from '../i18n'
import { hostOf } from './model-connections'
import { fmtTokens, keyLabel, noteHint, noteLabel } from './model-discover'

export function CCSwitchSheet(props: {
  onClose(): void
  onDone(added: number, skipped?: string[]): void
}) {
  const [groups, setGroups] = useState<DiscoverSource[] | null>(null)
  const [error, setError] = useState('')
  const [tab, setTab] = useState('')
  // Nothing is ticked on open. A CC Switch roster is somebody's list of saved
  // relays, not a shopping list: the sheet starts blank so the footer's "N
  // selected" is always a number the person chose. Ticking a whole roster is
  // one click away, which is the right cost for "import all of these".
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  function load() {
    setError('')
    setGroups(null)
    api
      .ccswitchGroups()
      .then((r) => {
        setGroups(r.groups)
        // Open the first roster that has something to offer. A group that is
        // configured but has no importable row stays one click away instead of
        // taking the opening screen.
        const first = r.groups.find((g) => g.candidates.some(importable)) ?? r.groups[0]
        setTab(first?.id ?? '')
        setPicked(new Set())
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }

  useEffect(load, [])

  function importable(c: DiscoveredModel): boolean {
    return c.importable && !c.existing
  }

  function toggle(id: string) {
    setPicked((cur) => {
      const next = new Set(cur)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  async function submit() {
    if (!groups) return
    const items: ModelImportItem[] = []
    for (const g of groups) {
      for (const c of g.candidates) {
        if (!picked.has(c.id) || !importable(c)) continue
        items.push({
          provider: c.provider,
          model: c.model,
          base_url: c.base_url || undefined,
          api_type: c.api_type || undefined,
          key_ref: c.key_ref || undefined,
          no_auth: c.no_auth,
          auth: c.auth || undefined,
          context_window: c.context_window,
        })
      }
    }
    if (!items.length) return
    setBusy(true)
    try {
      const r = await api.importModels(items)
      props.onDone(r.added, r.skipped)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  const current = groups?.find((g) => g.id === tab) ?? null
  const count = picked.size
  // How many of the ticked rows the open roster owns. A selection made on
  // another tab survives (the import covers every group), so the footer has to
  // say where the rest of it lives — otherwise "5 selected" next to one ticked
  // row reads as a bug.
  const here = current ? current.candidates.filter((c) => importable(c) && picked.has(c.id)).length : 0
  const hereIds = current ? current.candidates.filter(importable).map((c) => c.id) : []
  const allHere = hereIds.length > 0 && hereIds.every((id) => picked.has(id))

  // Roster-wide tick. Scoped to the open tab on purpose: the rows the person
  // cannot see must not change under a click they aimed at the ones they can.
  function toggleRoster() {
    setPicked((cur) => {
      const next = new Set(cur)
      for (const id of hereIds) {
        if (allHere) next.delete(id)
        else next.add(id)
      }
      return next
    })
  }

  return (
    <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
      <div
        ref={box}
        class="modal wizard-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="ccswitch-title"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            props.onClose()
          }
        }}
      >
        <header class="wizard-head">
          <div class="wizard-head-text">
            <h3 id="ccswitch-title" class="wizard-title">
              {t('models.ccswitch.title')}
            </h3>
            <p class="wizard-sub">{t('models.ccswitch.sub')}</p>
          </div>
          <button
            type="button"
            class="btn-icon wizard-close"
            aria-label={t('common.close')}
            title={t('common.close')}
            onClick={props.onClose}
          >
            <Icon name="x" size={16} />
          </button>
        </header>

        <div class="wizard-form">
          <div class="wizard-pane">
            {!groups && !error && <p class="wizard-empty">{t('models.ccswitch.scanning')}</p>}

            {error && (
              <div class="gate-error discover-error">
                <span>{error}</span>
                <button type="button" class="btn small" onClick={load}>
                  {t('common.retry')}
                </button>
              </div>
            )}

            {groups && (
              <div class="ccswitch-tabs" role="tablist" aria-label={t('models.ccswitch.title')}>
                {groups.map((g) => {
                  const usable = g.candidates.filter(importable).length
                  const on = g.id === tab
                  return (
                    <button
                      key={g.id}
                      type="button"
                      role="tab"
                      aria-selected={on}
                      class={`ccswitch-tab${on ? ' on' : ''}${usable === 0 ? ' bare' : ''}`}
                      onClick={() => setTab(g.id)}
                    >
                      <span class="ccswitch-tab-name">{g.label}</span>
                      <span class="ccswitch-tab-count">{usable}</span>
                    </button>
                  )
                })}
              </div>
            )}

            {current && (
              <>
                {(current.detail || hereIds.length > 0) && (
                  <div class="ccswitch-bar">
                    {current.detail && (
                      <p class="discover-hint ccswitch-detail" title={current.detail}>
                        {current.detail}
                      </p>
                    )}
                    {hereIds.length > 0 && (
                      <button type="button" class="discover-all" onClick={toggleRoster}>
                        {allHere ? t('models.discover.clearAll') : t('models.discover.selectAll')}
                      </button>
                    )}
                  </div>
                )}
                {current.candidates.filter(importable).length === 0 && (
                  <p class="discover-none">
                    {current.status === 'missing'
                      ? t('models.ccswitch.missing')
                      : t('models.ccswitch.empty')}
                  </p>
                )}
                {current.candidates
                  .filter((c) => importable(c) || c.existing)
                  .map((c) => {
                    const usable = importable(c)
                    return (
                      <label
                        key={c.id}
                        class={`discover-row${usable ? '' : ' off'}${c.existing ? ' existing' : ''}`}
                        title={usable ? undefined : noteHint(c) || undefined}
                      >
                        <input
                          type="checkbox"
                          disabled={!usable}
                          checked={usable && picked.has(c.id)}
                          onChange={() => toggle(c.id)}
                        />
                        <span class="discover-model mono">{c.model}</span>
                        <span class="discover-meta">
                          <span class="mono">{c.base_url ? hostOf(c.base_url) : c.provider_label || c.provider}</span>
                          {c.note && <span class="discover-note">{noteLabel(c)}</span>}
                          {c.context_window ? <span>{fmtTokens(c.context_window)}</span> : null}
                          {c.existing && <span class="discover-tag">{t('models.discover.existing')}</span>}
                          {!c.existing && <span class="discover-key">{keyLabel(c)}</span>}
                        </span>
                      </label>
                    )
                  })}
              </>
            )}
          </div>

          <footer class="wizard-foot">
            <span class="wizard-foot-note">
              {here === count
                ? t('models.discover.picked', { n: count })
                : t('models.ccswitch.pickedSplit', { n: count, here })}
            </span>
            <button type="button" class="btn" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              class="btn primary"
              disabled={count === 0 || busy}
              onClick={() => void submit()}
            >
              {busy
                ? t('models.discover.importing')
                : count === 0
                  ? t('models.discover.importNone')
                  : t('models.discover.import', { n: count })}
            </button>
          </footer>
        </div>
      </div>
    </div>
  )
}
