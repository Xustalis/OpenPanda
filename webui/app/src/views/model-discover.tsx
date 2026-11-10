// SPDX-License-Identifier: AGPL-3.0-or-later

// The "discover" sheet: what this machine can already serve, grouped by the
// tool that reported it.
//
// The page it belongs to is a registry of hand-written rows, and this is the
// one place that answers the obvious next question — "why am I typing an
// endpoint and a key I already configured somewhere else?". Four collectors
// answer it (Codex, Claude Code, OpenCode, loopback inference); this file only
// paints their report and turns the ticked rows into one import call.
//
// Two deliberate choices:
//
//   - Nothing here holds a credential. A row shows *where* its key comes from
//     ("env:DASHSCOPE_API_KEY" → the variable's name, "codex:custom" → "from the
//     Codex config") because the panel never receives the secret.
//   - Suggested rows are ticked, catalogue rows are not. A vendor's built-in
//     list can be eighty entries long; importing all of them to use one turns a
//     registry into a phone book.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type DiscoverSource, type DiscoveredModel, type ModelImportItem } from '../api/client'
import { useModalFocus } from '../components/modal-focus'
import { Icon } from '../components/icons'
import { t } from '../i18n'
import { hostOf } from './model-connections'

/** Rows shown before a source collapses into "show all". A catalogue source can
 *  carry eighty; the sheet is for choosing, not for scrolling. */
const COLLAPSED_ROWS = 8

export function DiscoverSheet(props: {
  onClose(): void
  onDone(added: number, skipped?: string[]): void
}) {
  const [sources, setSources] = useState<DiscoverSource[] | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  // The scan runs the OpenCode CLI and then a live check per loopback model, so
  // it can take twenty seconds. A frozen "scanning…" reads as a hang; a ticking
  // number reads as work.
  const [elapsed, setElapsed] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  function scan() {
    setError('')
    setSources(null)
    api
      .discoverModels()
      .then((r) => {
        setSources(r.sources)
        // The default tick: importable, not already registered, and the
        // collector considers it the real thing (a configured model, a live
        // server) rather than one row of a vendor catalogue.
        const next = new Set<string>()
        for (const s of r.sources) {
          for (const c of s.candidates) {
            if (c.importable && !c.existing && c.suggested) next.add(c.id)
          }
        }
        setPicked(next)
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }

  useEffect(scan, [])

  useEffect(() => {
    if (sources || error) return
    const started = Date.now()
    const timer = setInterval(() => setElapsed(Math.floor((Date.now() - started) / 1000)), 1000)
    return () => clearInterval(timer)
  }, [sources, error])

  function toggle(id: string) {
    setPicked((cur) => {
      const next = new Set(cur)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleSource(s: DiscoverSource) {
    const ids = s.candidates.filter(importable).map((c) => c.id)
    const all = ids.every((id) => picked.has(id))
    setPicked((cur) => {
      const next = new Set(cur)
      for (const id of ids) {
        if (all) next.delete(id)
        else next.add(id)
      }
      return next
    })
  }

  function importable(c: DiscoveredModel): boolean {
    return c.importable && !c.existing
  }

  async function submit() {
    if (!sources) return
    const items: ModelImportItem[] = []
    for (const s of sources) {
      for (const c of s.candidates) {
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

  const count = picked.size
  const nothing = sources !== null && !sources.some((s) => s.candidates.some(importable))

  return (
    <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
      <div
        ref={box}
        class="modal wizard-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="discover-title"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            props.onClose()
          }
        }}
      >
        <header class="wizard-head">
          <div class="wizard-head-text">
            <h3 id="discover-title" class="wizard-title">
              {t('models.discover.title')}
            </h3>
            <p class="wizard-sub">{t('models.discover.sub')}</p>
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
            {!sources && !error && (
              <p class="wizard-empty">
                {t('models.discover.scanning')}
                {elapsed > 0 ? ` ${elapsed}s` : ''}
              </p>
            )}

            {error && (
              <div class="gate-error discover-error">
                <span>{error}</span>
                <button type="button" class="btn small" onClick={scan}>
                  {t('common.retry')}
                </button>
              </div>
            )}

            {sources?.map((s) => {
              const rows = s.candidates.filter((c) => c.importable || c.existing || c.model)
              const open = expanded.has(s.id)
              const shown = open ? rows : rows.slice(0, COLLAPSED_ROWS)
              const ids = rows.filter(importable).map((c) => c.id)
              const allPicked = ids.length > 0 && ids.every((id) => picked.has(id))
              // Nothing tickable but rows on screen: the sheet owes an
              // explanation, not just a greyed checklist. Taken from the first
              // row that has one, so a source whose rows are unreachable for
              // different reasons says the one that applies.
              const hint =
                rows.length > 0 && ids.length === 0
                  ? (rows.map((c) => (importable(c) ? '' : noteHint(c))).find(Boolean) ?? '')
                  : ''
              return (
                <section class="discover-source" key={s.id}>
                  <header class="discover-head">
                    <span class="discover-name">{t(`models.discover.source.${s.id}`, s.label)}</span>
                    <span class={`discover-chip ${s.status}`}>
                      {t(`models.discover.status.${s.status}`)}
                    </span>
                    {s.detail && (
                      <span class="discover-detail mono" title={s.detail}>
                        {s.detail}
                      </span>
                    )}
                    {ids.length > 0 && (
                      <button type="button" class="discover-all" onClick={() => toggleSource(s)}>
                        {allPicked ? t('models.discover.clearAll') : t('models.discover.selectAll')}
                      </button>
                    )}
                  </header>

                  {hint && <p class="discover-hint">{hint}</p>}

                  {shown.map((c) => {
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
                          {c.loaded && <span class="discover-tag">{t('models.discover.loaded')}</span>}
                          {/* Two levels, not a footnote: a loopback row was
                              actually answered during the scan, a catalogue
                              row was not — and "imported" must not read as
                              "works". */}
                          {usable && c.verified && (
                            <span class="discover-ok" title={t('models.discover.verified')}>
                              <Icon name="check" size={11} />
                            </span>
                          )}
                          {usable && !c.verified && (
                            <span class="discover-tag unchecked" title={t('models.discover.unverifiedHint')}>
                              {t('models.discover.unverified')}
                            </span>
                          )}
                          {c.existing && <span class="discover-tag">{t('models.discover.existing')}</span>}
                          {!c.existing && <span class="discover-key">{keyLabel(c)}</span>}
                        </span>
                      </label>
                    )
                  })}

                  {rows.length > COLLAPSED_ROWS && (
                    <button
                      type="button"
                      class="discover-more"
                      onClick={() =>
                        setExpanded((cur) => {
                          const next = new Set(cur)
                          if (next.has(s.id)) next.delete(s.id)
                          else next.add(s.id)
                          return next
                        })
                      }
                    >
                      {open
                        ? t('models.discover.showLess')
                        : t('models.discover.showAll', { n: rows.length })}
                    </button>
                  )}
                </section>
              )
            })}

            {nothing && <p class="discover-none">{t('models.discover.none')}</p>}
          </div>

          <footer class="wizard-foot">
            <span class="wizard-foot-note">{t('models.discover.picked', { n: count })}</span>
            <button type="button" class="btn" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              class="btn primary"
              disabled={count === 0 || busy}
              onClick={() => void submit()}
            >
              {busy ? t('models.discover.importing') : t('models.discover.import', { n: count })}
            </button>
          </footer>
        </div>
      </div>
    </div>
  )
}

/** The collector's reason, in the person's words. A note that is a stable
 *  token (`cli-login`) is translated; anything else is the collector's own
 *  sentence — an endpoint's refusal reads better verbatim than paraphrased. */
export function noteLabel(c: DiscoveredModel): string {
  if (c.note === 'cli-login') return t('models.discover.note.cliLogin')
  if (c.note === 'no-endpoint') return t('models.discover.note.noEndpoint')
  // The free tier is a grant inside OpenCode's own client: the API answers
  // 429 for every outside call, so the row says that instead of "no key".
  if (c.note === 'opencode-free') return t('models.discover.note.freeClientOnly')
  return c.note ?? ''
}

/** The long form of a note, for a row that cannot be ticked: what is missing
 *  and what to do instead. Empty for a note that already says everything. */
export function noteHint(c: DiscoveredModel): string {
  if (c.note === 'cli-login') return t('models.discover.note.cliLoginHint')
  if (c.note === 'opencode-free') return t('models.discover.note.freeClientOnlyHint')
  return ''
}

/** Where a row's key comes from, in the person's words. The reference itself is
 *  a server-side name; only the env variable's name is worth showing, because
 *  that is the thing a person can go check. */
export function keyLabel(c: DiscoveredModel): string {
  const ref = c.key_ref ?? ''
  // A literal from a config file that is not shaped like a secret: the row
  // still sends it (such a proxy rejects a request without the header), but it
  // must not read as a key the person can look up.
  if (c.placeholder) return t('models.discover.key.placeholder')
  if (ref.startsWith('env:')) return t('models.discover.key.env', { name: ref.slice(4) })
  if (ref.startsWith('codex:')) return t('models.discover.key.codex')
  if (ref.startsWith('claude:')) return t('models.discover.key.claude')
  return c.no_auth ? t('models.discover.key.none') : ''
}

export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}
