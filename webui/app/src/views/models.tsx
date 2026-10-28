// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import {
  api,
  type AddModelRequest,
  type ModelEntry,
  type ModelRef,
  type ModelsResponse,
  type ProviderInfo,
} from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ModelPicker } from '../components/model-picker'
import { useModalFocus } from '../components/modal-focus'
import { Icon } from '../components/icons'
import { toast, toastError } from '../components/toast'

/** Models, grouped the way they are actually configured.
 *
 *  A *connection* is one endpoint + one key (one account); the models under it
 *  are the ids that key can ask for. The registry stores that flat — every row
 *  carries its own copy of the key — so the page derives the grouping from
 *  (provider, endpoint, key) and paints one card per connection. That is what
 *  makes "one key, five models" a thing you can see, add to, and trim without
 *  re-typing the key five times.
 *
 *  Two flows live behind two dialogs:
 *   - add: provider (the plan, for a vendor that sells several) → connection →
 *     fetch the roster → tick the models to register, all in one pass.
 *   - edit: one row, with the endpoint and protocol behind a disclosure —
 *     usually they came from the catalogue and should not be retyped.
 */

interface Member {
  entry: ModelEntry
  /** False for the live model when it was configured outside the registry —
   *  it can be edited and tested, but there is no row to remove. */
  registry: boolean
}

interface Conn {
  id: string
  provider: string
  providerLabel: string
  baseURL: string
  apiType: string
  keyHint: string
  keySet: boolean
  noAuth: boolean
  members: Member[]
}

/** One row's address: the pair a legacy config can duplicate under one alias. */
function refOf(e: ModelEntry): ModelRef {
  return { model: e.model, base_url: e.base_url }
}

function keyOf(e: ModelEntry): string {
  return `${e.alias}\u0000${e.model}\u0000${e.base_url}`
}

function connIdOf(e: ModelEntry): string {
  return [e.provider ?? '', e.base_url, e.api_type, e.key_hint ?? ''].join('\u0000')
}

function labelOf(providers: ProviderInfo[], id?: string): string {
  if (!id) return ''
  return providers.find((p) => p.id === id)?.label ?? id
}

/** The limit readout every row shares. The "(auto)" tag is the point: a number
 *  the resolver picked and a number the user typed used to print identically,
 *  so "I never set this" and "I set it to 128k" looked the same on screen. */
function limitsText(e: ModelEntry): string {
  const parts: string[] = []
  if (e.context_window) {
    parts.push(
      `${fmtTokens(e.context_window)} ctx${e.context_auto ? ` (${t('models.auto')})` : ''}`,
    )
  }
  if (e.max_tokens) {
    parts.push(
      `${fmtTokens(e.max_tokens)} ${t('models.out')}${e.max_tokens_auto ? ` (${t('models.auto')})` : ''}`,
    )
  }
  if (e.thinking) parts.push(t(`models.thinking.${e.thinking}`))
  return parts.join(' · ')
}

function groupConnections(data: ModelsResponse): Conn[] {
  const members: Member[] = data.models.map((m) => ({ entry: m, registry: true }))
  // A live model configured outside the registry (the old single-model form
  // wrote one) still belongs on the page — it is the row in use. When the
  // registry already holds it, the registry row is the one to paint, because
  // that is the row a delete addresses.
  const activeKey = keyOf(data.active)
  const registered = data.models.some((m) => keyOf(m) === activeKey)
  if (!registered && (data.active.base_url || data.active.model)) {
    members.push({ entry: data.active, registry: false })
  }

  const byId = new Map<string, Conn>()
  for (const m of members) {
    const id = connIdOf(m.entry)
    let conn = byId.get(id)
    if (!conn) {
      conn = {
        id,
        provider: m.entry.provider ?? '',
        providerLabel: m.entry.provider_label ?? m.entry.provider ?? '',
        baseURL: m.entry.base_url,
        apiType: m.entry.api_type,
        keyHint: m.entry.key_hint ?? '',
        keySet: m.entry.key_set,
        noAuth: m.entry.no_auth ?? false,
        members: [],
      }
      byId.set(id, conn)
    }
    if (!conn.members.some((x) => keyOf(x.entry) === keyOf(m.entry))) {
      conn.members.push(m)
    }
  }
  return [...byId.values()].sort((a, b) => {
    const aActive = a.members.some((m) => m.entry.active)
    const bActive = b.members.some((m) => m.entry.active)
    if (aActive !== bActive) return aActive ? -1 : 1
    return (a.providerLabel || a.baseURL).localeCompare(b.providerLabel || b.baseURL)
  })
}

export function ModelsSection() {
  useLocaleRerender()
  const [data, setData] = useState<ModelsResponse | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [results, setResults] = useState<Record<string, string>>({})
  const [adding, setAdding] = useState<{ conn?: Conn } | null>(null)
  const [editing, setEditing] = useState<Member | null>(null)

  function reload() {
    return api
      .models()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }
  useEffect(() => {
    void reload()
  }, [])

  async function test(entry: ModelEntry) {
    const k = keyOf(entry)
    setBusy(`test:${k}`)
    try {
      const r = await api.testModelRef({ alias: entry.alias, ...refOf(entry) })
      setResults((rs) => ({
        ...rs,
        [k]: r.ok ? `ok:${(r.reply ?? '').slice(0, 60)}` : `err:${r.error ?? ''}`,
      }))
    } catch (e: unknown) {
      setResults((rs) => ({ ...rs, [k]: `err:${e instanceof Error ? e.message : String(e)}` }))
    } finally {
      setBusy('')
    }
  }

  if (error && !data) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('models.title')}</h2>
        <p class="gate-error">{error}</p>
      </div>
    )
  }
  if (!data) return null

  const conns = groupConnections(data)
  const active = data.active

  function afterWrite() {
    void reload()
  }

  return (
    <div class="models-section">
      <div class="card settings-card model-active-card">
        <div class="model-active-head">
          <div>
            <div class="section-title">{t('models.active')}</div>
            <div class="model-active-name">
              <span class="mono">{active.model || '—'}</span>
              <span class={`badge ${active.key_set || active.no_auth ? 'green' : 'yellow'}`}>
                {active.no_auth
                  ? t('models.noAuth')
                  : active.key_set
                    ? t('models.keySet')
                    : t('models.keyMissing')}
              </span>
            </div>
            <div class="dim model-active-sub">
              {active.provider_label || active.provider || '—'} · <span class="mono">{active.alias}</span> ·{' '}
              <span class="mono">{active.base_url || '—'}</span>
              {limitsText(active) && ` · ${limitsText(active)}`}
            </div>
          </div>
          <div class="model-active-actions">
            <button class="btn small" disabled={busy !== ''} onClick={() => test(active)}>
              {busy === `test:${keyOf(active)}` ? t('models.testing') : t('models.test')}
            </button>
            <button class="btn small" onClick={() => setEditing({ entry: active, registry: false })}>
              {t('models.edit')}
            </button>
          </div>
        </div>
        {active.warning_code && (
          <WarningStrip entry={active} providers={data.providers} onFix={() => setEditing({ entry: active, registry: false })} />
        )}
        {results[keyOf(active)] && <ModelResult line={results[keyOf(active)]!} />}
      </div>

      <div class="models-head">
        <div>
          <h2 class="block-title">{t('models.connections')}</h2>
          <p class="hint">{t('models.connectionsHelp')}</p>
        </div>
        <button class="btn primary small" onClick={() => setAdding({})}>
          <Icon name="plus" size={14} />
          {t('models.addConnection')}
        </button>
      </div>

      {conns.length === 0 && <p class="dim">{t('models.empty')}</p>}

      {conns.map((conn) => (
        <ConnectionCard
          key={conn.id}
          conn={conn}
          providers={data.providers}
          busy={busy}
          results={results}
          onTest={(e) => void test(e)}
          onSwitch={async (e) => {
            setBusy(`use:${keyOf(e)}`)
            try {
              await api.useModel(e.alias, refOf(e))
              toast(t('models.switched', { alias: e.alias }), 'success')
              afterWrite()
            } catch (err) {
              toastError(err)
            } finally {
              setBusy('')
            }
          }}
          onEdit={(m) => setEditing(m)}
          onRemove={async (m) => {
            setBusy(`rm:${keyOf(m.entry)}`)
            try {
              const r = await api.removeModel(m.entry.alias, refOf(m.entry))
              toast(
                r.switched_to
                  ? t('models.removedSwitched', { alias: m.entry.alias, next: r.switched_to })
                  : t('models.removed', { alias: m.entry.alias }),
                'success',
              )
              afterWrite()
            } catch (err) {
              toastError(err)
            } finally {
              setBusy('')
            }
          }}
          onAddModels={() => setAdding({ conn })}
          onRemoveConn={async () => {
            const rows = conn.members.filter((m) => m.registry)
            setBusy(`rmconn:${conn.id}`)
            let removed = 0
            let switched: string | undefined
            try {
              for (const m of rows) {
                const r = await api.removeModel(m.entry.alias, refOf(m.entry))
                removed++
                switched = r.switched_to
              }
              toast(
                switched
                  ? t('models.removedConnSwitched', { n: removed, next: switched })
                  : t('models.removedConn', { n: removed }),
                'success',
              )
            } catch (err) {
              // Rows already deleted stay deleted, so a bare error would leave
              // the card half-gone with no explanation of where it stopped.
              if (removed > 0) {
                const msg = err instanceof Error ? err.message : String(err)
                toast(t('models.removeConnPartial', { n: removed, error: msg }), 'error')
              } else {
                toastError(err)
              }
            } finally {
              setBusy('')
              afterWrite()
            }
          }}
        />
      ))}

      {error && <p class="gate-error">{error}</p>}

      {adding && (
        <AddModelDialog
          providers={data.providers}
          connection={adding.conn}
          known={data.models}
          onClose={() => setAdding(null)}
          onDone={(n, switched) => {
            setAdding(null)
            toast(
              switched
                ? t('models.addedSwitched', { n, alias: switched })
                : t('models.addedN', { n }),
              'success',
            )
            afterWrite()
          }}
        />
      )}

      {editing && (
        <EditModelDialog
          entry={editing.entry}
          providers={data.providers}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null)
            toast(t('models.saved'), 'success')
            afterWrite()
          }}
        />
      )}
    </div>
  )
}

function WarningStrip(props: { entry: ModelEntry; providers: ProviderInfo[]; onFix: () => void }) {
  if (props.entry.warning_code !== 'endpoint-owner') return null
  return (
    <div class="model-warn">
      <span>
        {t('models.warn.endpointOwner', {
          owner: labelOf(props.providers, props.entry.warning_arg),
          current: props.entry.provider_label || props.entry.provider || '',
        })}
      </span>
      <button class="btn small" onClick={props.onFix}>
        {t('models.warn.fix')}
      </button>
    </div>
  )
}

function ConnectionCard(props: {
  conn: Conn
  providers: ProviderInfo[]
  busy: string
  results: Record<string, string>
  onTest(e: ModelEntry): void
  onSwitch(e: ModelEntry): void
  onEdit(m: Member): void
  onRemove(m: Member): void
  onRemoveConn(): void
  onAddModels(): void
}) {
  const { conn } = props
  // Removing a whole connection is N single-row deletes, so it needs its own
  // confirmation — a stray click here drops an account, not one model.
  const [confirming, setConfirming] = useState(false)
  const removable = conn.members.filter((m) => m.registry).length
  const busy = props.busy === `rmconn:${conn.id}`
  return (
    <div class="card settings-card conn-card">
      <div class="conn-head">
        <div class="conn-identity">
          <span class="conn-provider">{conn.providerLabel || t('models.customProvider')}</span>
          {conn.noAuth ? (
            <span class="badge">{t('models.noAuth')}</span>
          ) : (
            <span class={`badge ${conn.keySet ? 'green' : 'yellow'}`}>
              {conn.keySet ? `key ${conn.keyHint}` : t('models.keyMissing')}
            </span>
          )}
          <span class="badge quiet">{conn.apiType}</span>
          {conn.members.length > 1 && (
            <span class="badge quiet">{t('models.modelCount', { n: conn.members.length })}</span>
          )}
        </div>
        <div class="conn-actions">
          {removable > 0 &&
            (confirming ? (
              <>
                <button class="btn small danger" disabled={props.busy !== ''} onClick={props.onRemoveConn}>
                  {busy ? '…' : t('models.removeConnConfirm', { n: removable })}
                </button>
                <button class="btn small" onClick={() => setConfirming(false)}>
                  {t('common.cancel')}
                </button>
              </>
            ) : (
              <button
                class="btn small ghost"
                disabled={props.busy !== ''}
                onClick={() => setConfirming(true)}
              >
                <Icon name="trash" size={14} />
                {t('models.removeConnection')}
              </button>
            ))}
          <button class="btn small" disabled={props.busy !== ''} onClick={props.onAddModels}>
            <Icon name="plus" size={14} />
            {t('models.addModel')}
          </button>
        </div>
      </div>
      <div class="conn-endpoint dim mono">{conn.baseURL || '—'}</div>
      <div class="model-rows">
        {conn.members.map((m) => (
          <ModelRow
            key={keyOf(m.entry)}
            m={m}
            busy={props.busy}
            result={props.results[keyOf(m.entry)]}
            providers={props.providers}
            onTest={() => props.onTest(m.entry)}
            onSwitch={() => props.onSwitch(m.entry)}
            onEdit={() => props.onEdit(m)}
            onRemove={() => props.onRemove(m)}
          />
        ))}
      </div>
    </div>
  )
}

function ModelRow(props: {
  m: Member
  busy: string
  result?: string
  providers: ProviderInfo[]
  onTest(): void
  onSwitch(): void
  onEdit(): void
  onRemove(): void
}) {
  const { entry } = props.m
  const [confirming, setConfirming] = useState(false)
  const k = keyOf(entry)
  return (
    <div class={`model-row${entry.active ? ' active' : ''}`}>
      <div class="model-row-main">
        <div class="model-row-top">
          <span class="model-alias mono">{entry.alias}</span>
          {entry.active && <span class="badge green">{t('models.inUse')}</span>}
          {!props.m.registry && <span class="badge quiet">{t('models.notRegistered')}</span>}
          {/* The key belongs to the connection, not the row — repeating its
              hint on every model of one account was the same string five
              times over. The connection head states it once. */}
        </div>
        <div class="model-row-sub dim">
          <span class="mono">{entry.model || '—'}</span>
          {limitsText(entry) && ` · ${limitsText(entry)}`}
        </div>
        {/* The live row sits under the active card, whose banner already
            spells this out with a fix action — repeating it here would make
            one misconfiguration look like two. */}
        {entry.warning_code && !entry.active && (
          <p class="model-warn-text">
            {t('models.warn.endpointOwnerShort', { owner: labelOf(props.providers, entry.warning_arg) })}
          </p>
        )}
        {props.result && <ModelResult line={props.result} />}
      </div>
      <div class="model-row-actions">
        {!confirming && (
          <>
            {!entry.active && (
              <button class="btn small" disabled={props.busy !== ''} onClick={props.onSwitch}>
                {props.busy === `use:${k}` ? '…' : t('models.use')}
              </button>
            )}
            <button class="btn small" disabled={props.busy !== ''} onClick={props.onTest}>
              {props.busy === `test:${k}` ? '…' : t('models.test')}
            </button>
            <button class="btn small ghost" onClick={props.onEdit}>
              {t('models.edit')}
            </button>
          </>
        )}
        {props.m.registry &&
          (confirming ? (
            <>
              <button class="btn small danger" disabled={props.busy !== ''} onClick={props.onRemove}>
                {t('models.confirmRemove')}
              </button>
              <button class="btn small" onClick={() => setConfirming(false)}>
                {t('common.cancel')}
              </button>
            </>
          ) : (
            <button class="btn small ghost" onClick={() => setConfirming(true)}>
              {t('models.remove')}
            </button>
          ))}
      </div>
      {confirming && (
        <p class="model-row-confirm dim">
          {entry.active ? t('models.removeActiveHint') : t('models.removeHint')}
        </p>
      )}
    </div>
  )
}

function ModelResult(props: { line: string }) {
  const ok = props.line.startsWith('ok:')
  return (
    <p class={`test-result ${ok ? 'ok' : 'bad'}`}>
      {ok ? t('models.testOk', { reply: props.line.slice(3) }) : `${t('models.testFail')} ${props.line.slice(4)}`}
    </p>
  )
}

// ---- dialogs ----

function Dialog(props: {
  title: string
  wide?: boolean
  onClose(): void
  children: ComponentChildren
}) {
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)
  return (
    <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
      <div
        ref={box}
        class={`modal wizard-modal${props.wide ? ' wizard-wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={props.title}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            props.onClose()
          }
        }}
      >
        <h3 class="modal-title">{props.title}</h3>
        {props.children}
      </div>
    </div>
  )
}

/** A limit field where an empty box means "the catalogue's answer" — the
 *  placeholder names the number that will be used, so leaving it blank is an
 *  informed choice rather than a blank stare. */
function LimitInput(props: {
  id?: string
  value: number
  autoValue?: number
  onChange(v: number): void
}) {
  return (
    <input
      id={props.id}
      class="input"
      type="number"
      min={0}
      value={props.value > 0 ? String(props.value) : ''}
      placeholder={
        props.autoValue ? t('models.autoPlaceholder', { value: fmtTokens(props.autoValue) }) : t('models.auto')
      }
      onInput={(e) => props.onChange(Number((e.target as HTMLInputElement).value) || 0)}
    />
  )
}

interface Draft {
  id: number
  model: string
  alias: string
  context: number
  maxTokens: number
  thinking: 'auto' | 'on' | 'off'
  budget: number
  retries: number
  params: string
  headers: string
  noAuth: boolean
  picked: boolean
  open: boolean
}

let draftSeq = 1
function newDraft(model = '', picked = true): Draft {
  return {
    id: draftSeq++,
    model,
    alias: model,
    context: 0,
    maxTokens: 0,
    thinking: 'auto',
    budget: 0,
    retries: 0,
    params: '',
    headers: '',
    noAuth: false,
    // A row exists because someone added it — the catalogue default on open,
    // or a blank line on "add row". Both are intents, so both start ticked;
    // the checkbox is there to *un*tick the noise a fetched roster brings in.
    picked,
    open: false,
  }
}

/** Add models: pick the provider (which is also the plan, for a vendor that
 *  sells several rosters on one host), state the connection once, then tick
 *  the ids to register — one key, several models, one save. */
function AddModelDialog(props: {
  providers: ProviderInfo[]
  connection?: Conn
  /** Every registered row, so a draft can say "this one is already here"
   *  before it is added a second time under a suffixed name. */
  known: ModelEntry[]
  onClose(): void
  onDone(count: number, switched?: string): void
}) {
  const pre = props.connection
  const [provider, setProvider] = useState<ProviderInfo | null>(() =>
    pre ? (props.providers.find((p) => p.id === pre.provider) ?? null) : null,
  )
  const [apiKey, setApiKey] = useState('')
  const [baseURL, setBaseURL] = useState(pre?.baseURL ?? '')
  const [apiType, setApiType] = useState(pre?.apiType ?? 'openai')
  const [protoTouched, setProtoTouched] = useState(false)
  const [remote, setRemote] = useState<string[] | null>(null)
  // Extending a connection starts with one blank row: the table would
  // otherwise open empty and read as a broken dialog.
  const [drafts, setDrafts] = useState<Draft[]>(() => (pre ? [newDraft()] : []))
  const [useAfter, setUseAfter] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const isCustom = provider?.id === 'custom'

  // Ids already registered on this exact endpoint. The same model under a
  // second account is a legitimate duplicate; the same model on the same
  // endpoint is the row the user is looking at, and re-adding it only mints a
  // "-2" alias nobody asked for.
  const knownModels = new Set(
    props.known
      .filter((m) => (m.provider ?? '') === (provider?.id ?? '') && m.base_url === currentBase())
      .map((m) => m.model),
  )

  function pick(p: ProviderInfo) {
    setProvider(p)
    setBaseURL(p.base_url)
    setApiType(p.api_type)
    setProtoTouched(false)
    setRemote(null)
    setDrafts([newDraft(p.default_model)])
    setError('')
  }

  function currentBase(): string {
    return baseURL.trim() || provider?.base_url || ''
  }

  function requestFor(d: Draft, key?: string): AddModelRequest {
    const req: AddModelRequest = {
      provider: provider!.id,
      model: d.model.trim() || undefined,
      alias: d.alias.trim() || undefined,
      api_key: (key ?? apiKey).trim() || undefined,
    }
    // Only restate the endpoint when it is not the catalogue's own — a
    // restated one is harmless, but sending it keeps a relay under a known
    // vendor from being silently rebased.
    const base = currentBase()
    if (base && base !== provider!.base_url) {
      req.base_url = base
      req.api_type = apiType
    } else if (isCustom || protoTouched) {
      req.api_type = apiType
    }
    if (d.context > 0) req.context_window = d.context
    if (d.maxTokens > 0) req.max_tokens = d.maxTokens
    if (d.thinking !== 'auto') req.thinking = d.thinking
    if (d.budget > 0) req.thinking_budget = d.budget
    if (d.retries !== 0) req.max_retries = d.retries
    if (d.noAuth) req.no_auth = true
    // Extending an existing connection borrows that row's key specifically —
    // not "whatever key this provider has", which is the wrong account when
    // the same vendor is registered twice.
    if (!(key ?? apiKey).trim() && pre?.keySet) {
      req.reuse_from = pre.members[0]?.entry.alias
    }
    const params = parseJSON(d.params)
    if (params) req.params = params
    const headers = parseJSON(d.headers)
    if (headers) req.headers = headers as Record<string, string>
    return req
  }

  async function fetchRemote() {
    if (!provider) return
    setBusy('fetch')
    setError('')
    try {
      const r = await api.fetchModels({
        provider: provider.id,
        model: drafts[0]?.model || provider.default_model,
        api_key: apiKey.trim() || undefined,
        ...(currentBase() !== provider.base_url
          ? { base_url: currentBase(), api_type: apiType }
          : isCustom
            ? { base_url: currentBase(), api_type: apiType }
            : {}),
      })
      if (!r.ok || !r.models) {
        setError(r.error ?? t('models.fetchFail'))
        return
      }
      setRemote(r.models)
      // Keep whatever the user already typed, append the catalogue under it —
      // with the ids this connection already has left unticked, so the roster
      // comes back as "what is new" plus an explicit opt-in to the rest.
      const known = new Set(drafts.map((d) => d.model.trim()).filter(Boolean))
      const extra = r.models
        .filter((id) => !known.has(id))
        .map((id) => newDraft(id, !knownModels.has(id)))
      setDrafts((cur) => [...cur.filter((d) => d.model.trim() !== '' || d === cur[0]), ...extra])
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  function patch(id: number, p: Partial<Draft>) {
    setDrafts((cur) => cur.map((d) => (d.id === id ? { ...d, ...p } : d)))
  }

  async function submit(e: Event) {
    e.preventDefault()
    if (!provider || busy) return
    const picked = drafts.filter((d) => d.picked && d.model.trim() !== '')
    if (picked.length === 0) {
      setError(t('models.noneSelected'))
      return
    }
    // Validate the JSON escape hatches before the first POST: a typo in row
    // five must not leave rows one to four registered and no way to tell.
    const requests: AddModelRequest[] = []
    try {
      for (const d of picked) requests.push(requestFor(d))
    } catch {
      setError(t('models.badJSON'))
      return
    }
    setError('')
    setBusy('save')
    let created = 0
    let firstAlias: string | undefined
    let firstRef: ModelRef | undefined
    try {
      for (const req of requests) {
        const entry = await api.addModel(req)
        created++
        if (!firstAlias) {
          firstAlias = entry.alias
          firstRef = refOf(entry)
        }
      }
      if (useAfter && firstAlias) {
        await api.useModel(firstAlias, firstRef)
      }
      props.onDone(created, useAfter ? firstAlias : undefined)
    } catch (err) {
      // Rows already written stay — saying how many landed is more useful than
      // a bare error next to a half-applied form.
      const msg = err instanceof Error ? err.message : String(err)
      setError(created > 0 ? t('models.addPartial', { n: created, error: msg }) : msg)
      setBusy('')
    }
  }

  const canSubmit = provider !== null && (isCustom ? currentBase() !== '' : true)

  if (!provider) {
    return (
      <Dialog title={t('models.addConnection')} onClose={props.onClose}>
        <p class="hint">{t('models.pickProvider')}</p>
        <div class="wizard-scroll">
          {(['global', 'cn'] as const).map((r) => {
            const group = props.providers.filter((p) => p.region === r)
            if (group.length === 0) return null
            return (
              <div key={r}>
                <div class="section-title wizard-region">{t(`models.region.${r}`)}</div>
                <div class="provider-grid">
                  {group.map((p) => (
                    <button key={p.id} class="provider-card" onClick={() => pick(p)}>
                      <span class="provider-name">{p.label}</span>
                      <span class="provider-sub dim mono">{p.base_url || t('models.customEndpoint')}</span>
                      <span class="provider-badges">
                        {p.no_auth && <span class="badge">{t('models.noAuth')}</span>}
                        {p.key_saved && <span class="badge green">{t('models.keySaved')}</span>}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
        <div class="modal-actions">
          <button class="btn" onClick={props.onClose}>
            {t('common.cancel')}
          </button>
        </div>
      </Dialog>
    )
  }

  const pickedCount = drafts.filter((d) => d.picked && d.model.trim() !== '').length
  const skipped = remote ? remote.filter((id) => knownModels.has(id)).length : 0

  return (
    <Dialog title={pre ? t('models.addModelTitle') : t('models.addConnection')} wide onClose={props.onClose}>
      <form onSubmit={submit}>
        <div class="wizard-picked">
          {pre ? (
            <span class="badge">{provider.label}</span>
          ) : (
            <button class="btn small ghost" type="button" onClick={() => setProvider(null)}>
              ← {provider.label}
            </button>
          )}
        </div>

        <div class="conn-form">
          {!provider.no_auth && (
            <div class="field-group">
              <label for="add-key">{t('models.apiKey')}</label>
              <input
                id="add-key"
                class="input mono"
                type="password"
                autocomplete="off"
                value={apiKey}
                onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
                placeholder={
                  pre?.keySet
                    ? t('models.keyReuseConn')
                    : provider.key_saved
                      ? t('models.keyReuseHint')
                      : 'sk-…'
                }
              />
              {pre?.keySet ? (
                <p class="hint">{t('models.keyReuseConnHelp')}</p>
              ) : (
                provider.key_saved && <p class="hint">{t('models.keyReuseHelp')}</p>
              )}
            </div>
          )}
          <div class="field-row">
            <div class="field-group u-flex-1">
              <label for="add-base">{t('models.baseURL')}</label>
              <div class="field-row">
                <input
                  id="add-base"
                  class="input mono"
                  value={baseURL}
                  onInput={(e) => setBaseURL((e.target as HTMLInputElement).value)}
                  placeholder={provider.base_url || 'https://…/v1'}
                />
                {baseURL !== provider.base_url && provider.base_url !== '' && (
                  <button
                    type="button"
                    class="btn small"
                    onClick={() => {
                      setBaseURL(provider.base_url)
                      setProtoTouched(false)
                    }}
                  >
                    {t('models.baseURLReset')}
                  </button>
                )}
              </div>
            </div>
            <div class="field-group proto-field">
              <label for="add-proto">{t('models.protocol')}</label>
              <select
                id="add-proto"
                class="input"
                value={apiType}
                onChange={(e) => {
                  setApiType((e.target as HTMLSelectElement).value)
                  setProtoTouched(true)
                }}
              >
                {(['openai', 'anthropic', 'responses', 'google'] as const).map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </select>
            </div>
          </div>
        </div>

        <div class="models-table-head">
          <div class="section-title">{t('models.list')}</div>
          <div class="models-table-actions">
            <button type="button" class="btn small" disabled={busy !== ''} onClick={fetchRemote}>
              <Icon name="download" size={14} />
              {busy === 'fetch' ? '…' : t('models.fetch')}
            </button>
            <button
              type="button"
              class="btn small"
              onClick={() => setDrafts((cur) => [...cur, newDraft()])}
            >
              <Icon name="plus" size={14} />
              {t('models.manualAdd')}
            </button>
          </div>
        </div>
        {remote && (
          <p class="hint">
            {t('models.fetched', { n: remote.length })}
            {skipped > 0 ? ` ${t('models.fetchedKnown', { n: skipped })}` : ''}
          </p>
        )}

        <div class="models-table">
          {drafts.length > 1 && (
            <div class="draft-head">
              <span class="draft-head-check" />
              {/* Same skeleton as a row — same flex weights, same gaps — so
                  the labels land over the fields they name. */}
              <div class="draft-fields">
                <div class="draft-fields-row">
                  <div class="field-group">
                    <label>{t('models.col.model')}</label>
                  </div>
                  <div class="field-group">
                    <label>{t('models.col.alias')}</label>
                  </div>
                  <div class="field-group narrow">
                    <label>{t('models.col.context')}</label>
                  </div>
                  <div class="field-group narrow">
                    <label>{t('models.col.thinking')}</label>
                  </div>
                  <span class="draft-head-end" />
                </div>
              </div>
            </div>
          )}
          {drafts.map((d) => (
            <DraftRow
              key={d.id}
              d={d}
              remote={remote}
              // One header above the table beats a header per row: five
              // fetched models were five copies of the same four labels.
              bare={drafts.length > 1}
              onPatch={(p) => patch(d.id, p)}
              onDrop={() => setDrafts((cur) => cur.filter((x) => x.id !== d.id))}
            />
          ))}
        </div>

        {error && <p class="gate-error">{error}</p>}

        <div class="modal-actions spread">
          <label class="checkbox-label">
            <input
              type="checkbox"
              checked={useAfter}
              onChange={(e) => setUseAfter((e.target as HTMLInputElement).checked)}
            />
            {t('models.useAfterAdd')}
          </label>
          <div class="field-row">
            <button type="button" class="btn" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              class="btn primary"
              disabled={!canSubmit || busy !== '' || pickedCount === 0}
            >
              {/* "Add 0" reads like a broken counter; with nothing ticked the
                  button should name the action, not count nothing. */}
              {busy === 'save'
                ? '…'
                : pickedCount > 0
                  ? t('models.addConfirmN', { n: pickedCount })
                  : t('models.addModel')}
            </button>
          </div>
        </div>
      </form>
    </Dialog>
  )
}

function DraftRow(props: {
  d: Draft
  remote: string[] | null
  /** The table above carries the column names; keep the labels for screen
   *  readers but out of the visual flow. */
  bare?: boolean
  onPatch(p: Partial<Draft>): void
  onDrop(): void
}) {
  const { d } = props
  return (
    <div class={`draft-row${d.picked ? ' picked' : ''}${props.bare ? ' bare' : ''}`}>
      <div class="draft-main">
        <input
          type="checkbox"
          class="draft-check"
          checked={d.picked}
          aria-label={t('models.col.pick')}
          onChange={(e) => props.onPatch({ picked: (e.target as HTMLInputElement).checked })}
        />
        <div class="draft-fields">
          <div class="draft-fields-row">
            <div class="field-group">
              <label>{t('models.col.model')}</label>
              <ModelPicker
                value={d.model}
                onValue={(v) => props.onPatch({ model: v, alias: d.alias === d.model ? v : d.alias })}
                options={props.remote ?? []}
                placeholder="model-id"
              />
            </div>
            <div class="field-group">
              <label>{t('models.col.alias')}</label>
              <input
                class="input mono"
                value={d.alias}
                placeholder={d.model || t('models.aliasAuto')}
                onInput={(e) => props.onPatch({ alias: (e.target as HTMLInputElement).value })}
              />
            </div>
            <div class="field-group narrow">
              <label>{t('models.col.context')}</label>
              <LimitInput value={d.context} onChange={(v) => props.onPatch({ context: v })} />
            </div>
            <div class="field-group narrow">
              <label>{t('models.col.thinking')}</label>
              <select
                class="input"
                value={d.thinking}
                onChange={(e) =>
                  props.onPatch({ thinking: (e.target as HTMLSelectElement).value as Draft['thinking'] })
                }
              >
                {(['auto', 'on', 'off'] as const).map((v) => (
                  <option key={v} value={v}>
                    {t(`models.thinking.${v}`)}
                  </option>
                ))}
              </select>
            </div>
            <button
              type="button"
              class="btn small ghost icon-btn"
              aria-label={t('models.remove')}
              title={t('models.remove')}
              onClick={props.onDrop}
            >
              <Icon name="trash" size={14} />
            </button>
          </div>
          <button
            type="button"
            class="draft-advanced-toggle"
            onClick={() => props.onPatch({ open: !d.open })}
          >
            {d.open ? '▾' : '▸'} {t('models.advancedTuning')}
          </button>
          {d.open && (
            <div class="draft-advanced">
              <div class="draft-grid cols-3">
                <div class="field-group">
                  <label>{t('models.maxTokens')}</label>
                  <LimitInput value={d.maxTokens} onChange={(v) => props.onPatch({ maxTokens: v })} />
                  <p class="hint">{t('models.maxTokensHelp')}</p>
                </div>
                <div class="field-group">
                  <label>{t('models.thinkingBudget')}</label>
                  <LimitInput value={d.budget} onChange={(v) => props.onPatch({ budget: v })} />
                </div>
                <div class="field-group">
                  <label>{t('models.maxRetries')}</label>
                  <input
                    class="input"
                    type="number"
                    min={-1}
                    value={String(d.retries)}
                    onInput={(e) => props.onPatch({ retries: Number((e.target as HTMLInputElement).value) || 0 })}
                  />
                  <p class="hint">{t('models.maxRetriesHelp')}</p>
                </div>
              </div>
              <div class="draft-grid cols-2">
                <div class="field-group">
                  <label>{t('models.params')}</label>
                  <input
                    class="input mono"
                    value={d.params}
                    placeholder='{"temperature":0.7}'
                    onInput={(e) => props.onPatch({ params: (e.target as HTMLInputElement).value })}
                  />
                </div>
                <div class="field-group">
                  <label>{t('models.headers')}</label>
                  <input
                    class="input mono"
                    value={d.headers}
                    placeholder='{"X-Tenant":"blue"}'
                    onInput={(e) => props.onPatch({ headers: (e.target as HTMLInputElement).value })}
                  />
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

/** Edit one row. The endpoint and protocol sit behind a disclosure because
 *  they came from the catalogue; the limits show the resolver's number as a
 *  placeholder, and clearing the box puts them back on it. */
function EditModelDialog(props: {
  entry: ModelEntry
  providers: ProviderInfo[]
  onClose(): void
  onDone(): void
}) {
  const e = props.entry
  const [alias, setAlias] = useState(e.alias)
  const [model, setModel] = useState(e.model)
  const [apiKey, setApiKey] = useState('')
  const [context, setContext] = useState(e.context_auto ? 0 : (e.context_window ?? 0))
  const [maxTokens, setMaxTokens] = useState(e.max_tokens_auto ? 0 : (e.max_tokens ?? 0))
  const [thinking, setThinking] = useState<'auto' | 'on' | 'off'>(
    e.thinking === 'on' || e.thinking === 'off' ? e.thinking : 'auto',
  )
  const [budget, setBudget] = useState(e.thinking_budget ?? 0)
  const [retries, setRetries] = useState(e.max_retries ?? 0)
  const [noAuth, setNoAuth] = useState(!!e.no_auth)
  const [params, setParams] = useState('')
  const [headers, setHeaders] = useState('')
  // Open on a mismatch: that is the one thing this dialog was opened to fix.
  const [endpointOpen, setEndpointOpen] = useState(!!e.warning_code)
  // The plan is part of the identity, not just the URL — two rows from one
  // vendor are two products. Editing the endpoint without being able to
  // correct the plan left the warning unfixable from here.
  const [providerId, setProviderId] = useState(e.provider || 'custom')
  const [baseURL, setBaseURL] = useState(e.base_url)
  const [apiType, setApiType] = useState(e.api_type)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [remote, setRemote] = useState<string[] | null>(null)

  const ownerOffer = props.providers.find((p) => p.id === e.warning_arg && p.id !== providerId)

  function adoptProvider(id: string) {
    setProviderId(id)
    const p = props.providers.find((x) => x.id === id)
    if (p && p.base_url) {
      setBaseURL(p.base_url)
      setApiType(p.api_type)
    }
  }

  // Follows the plan picked above, so the reset button and the model
  // placeholder describe the catalogue row actually selected.
  const provider = props.providers.find((p) => p.id === providerId)

  async function fetchRemote() {
    setBusy(true)
    setError('')
    try {
      const r = await api.fetchModels({
        provider: providerId,
        model,
        base_url: baseURL,
        api_type: apiType,
        api_key: apiKey.trim() || undefined,
      })
      if (r.ok && r.models) setRemote(r.models)
      else setError(r.error ?? t('models.fetchFail'))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  async function submit(ev: Event) {
    ev.preventDefault()
    if (busy) return
    setError('')
    let p: Record<string, unknown> | undefined
    let h: Record<string, string> | undefined
    try {
      p = parseJSON(params)
      h = parseJSON(headers) as Record<string, string> | undefined
    } catch {
      setError(t('models.badJSON'))
      return
    }
    setBusy(true)
    try {
      const req: AddModelRequest = {
        provider: providerId,
        model: model.trim(),
        alias: alias.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        thinking,
        no_auth: noAuth,
      }
      if (baseURL !== e.base_url) {
        req.base_url = baseURL
        req.api_type = apiType
      } else if (apiType !== e.api_type) {
        req.api_type = apiType
      }
      // 0 means "put it back on the catalogue's answer" — sending nothing
      // would mean "leave what is stored", which is a different instruction.
      if (context > 0) req.context_window = context
      else if (!e.context_auto) req.context_window_reset = true
      if (maxTokens > 0) req.max_tokens = maxTokens
      else if (!e.max_tokens_auto) req.max_tokens_reset = true
      if (budget > 0) req.thinking_budget = budget
      else if ((e.thinking_budget ?? 0) > 0) req.thinking_budget_reset = true
      if (retries !== 0) req.max_retries = retries
      else if ((e.max_retries ?? 0) !== 0) req.max_retries_reset = true
      if (p) req.params = p
      if (h) req.headers = h
      await api.updateModel(e.alias, req, refOf(e))
      props.onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <Dialog title={t('models.editTitle')} onClose={props.onClose}>
      <form onSubmit={submit}>
        {/* Full width each: split in two, the id lost half its characters to
            the fetch button and the thing being edited was unreadable. */}
        <div class="field-group">
          <label for="edit-model">{t('models.col.model')}</label>
          <div class="field-row">
            <ModelPicker
              id="edit-model"
              value={model}
              onValue={(v) => {
                setModel(v)
                if (alias === e.alias) setAlias(v)
              }}
              options={remote ?? []}
              placeholder={provider?.default_model || 'model-id'}
              required
            />
            <button type="button" class="btn small" disabled={busy} onClick={fetchRemote}>
              {t('models.fetch')}
            </button>
          </div>
        </div>
        <div class="field-group">
          <label for="edit-alias">{t('models.col.alias')}</label>
          <input
            id="edit-alias"
            class="input mono"
            value={alias}
            onInput={(ev) => setAlias((ev.target as HTMLInputElement).value)}
          />
          <p class="hint">{t('models.aliasHelp')}</p>
        </div>

        {!noAuth && (
          <div class="field-group">
            <label for="edit-key">{t('models.apiKey')}</label>
            <input
              id="edit-key"
              class="input mono"
              type="password"
              autocomplete="off"
              value={apiKey}
              placeholder={e.key_set ? `${t('models.apiKeySet')} ${e.key_hint ?? ''}` : 'sk-…'}
              onInput={(ev) => setApiKey((ev.target as HTMLInputElement).value)}
            />
            <p class="hint">{t('models.keyEmptyKeep')}</p>
          </div>
        )}

        <div class="field-row">
          <div class="field-group">
            <label for="edit-ctx">{t('models.contextWindow')}</label>
            <LimitInput
              id="edit-ctx"
              value={context}
              autoValue={e.auto_context_window}
              onChange={setContext}
            />
            <p class="hint">{t('models.limitHint')}</p>
          </div>
          <div class="field-group">
            <label for="edit-max">{t('models.maxTokens')}</label>
            <LimitInput
              id="edit-max"
              value={maxTokens}
              autoValue={e.auto_max_tokens}
              onChange={setMaxTokens}
            />
            <p class="hint">{t('models.limitHint')}</p>
          </div>
        </div>

        <div class="field-row">
          <div class="field-group thinking-field">
            <label>{t('models.thinking')}</label>
            <div class="segmented">
              {(['auto', 'on', 'off'] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  class={`seg${thinking === v ? ' on' : ''}`}
                  onClick={() => setThinking(v)}
                >
                  {t(`models.thinking.${v}`)}
                </button>
              ))}
            </div>
          </div>
          <div class="field-group">
            <label for="edit-budget">{t('models.thinkingBudget')}</label>
            <LimitInput id="edit-budget" value={budget} onChange={setBudget} />
          </div>
          <div class="field-group">
            <label for="edit-retries">{t('models.maxRetries')}</label>
            <input
              id="edit-retries"
              class="input"
              type="number"
              min={-1}
              value={String(retries)}
              onInput={(ev) => setRetries(Number((ev.target as HTMLInputElement).value) || 0)}
            />
            <p class="hint">{t('models.maxRetriesHelp')}</p>
          </div>
        </div>

        <button type="button" class="draft-advanced-toggle" onClick={() => setEndpointOpen((v) => !v)}>
          {endpointOpen ? '▾' : '▸'} {t('models.endpointSection')}
        </button>
        {endpointOpen && (
          <div class="draft-advanced">
            {ownerOffer && (
              <div class="fix-suggest">
                <span>{t('models.warn.endpointOwnerShort', { owner: ownerOffer.label })}</span>
                <button type="button" class="btn small" onClick={() => adoptProvider(ownerOffer.id)}>
                  {t('models.warn.useOwner', { owner: ownerOffer.label })}
                </button>
              </div>
            )}
            <div class="field-group">
              <label for="edit-provider">{t('models.provider')}</label>
              <select
                id="edit-provider"
                class="input"
                value={providerId}
                onChange={(ev) => adoptProvider((ev.target as HTMLSelectElement).value)}
              >
                {providerId === 'custom' && <option value="custom">{t('models.customProvider')}</option>}
                {props.providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                    {p.region === 'cn' ? ` — ${t('models.region.cn')}` : ''}
                  </option>
                ))}
              </select>
            </div>
            <div class="field-row">
              <div class="field-group u-flex-1">
                <label for="edit-base">{t('models.baseURL')}</label>
                <div class="field-row">
                  <input
                    id="edit-base"
                    class="input mono"
                    value={baseURL}
                    onInput={(ev) => setBaseURL((ev.target as HTMLInputElement).value)}
                  />
                  {provider && provider.base_url !== '' && baseURL !== provider.base_url && (
                    <button
                      type="button"
                      class="btn small"
                      onClick={() => setBaseURL(provider.base_url)}
                    >
                      {t('models.baseURLReset')}
                    </button>
                  )}
                </div>
              </div>
              <div class="field-group proto-field">
                <label for="edit-proto">{t('models.protocol')}</label>
                <select
                  id="edit-proto"
                  class="input"
                  value={apiType}
                  onChange={(ev) => setApiType((ev.target as HTMLSelectElement).value)}
                >
                  {(['openai', 'anthropic', 'responses', 'google'] as const).map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>
              </div>
            </div>
            <div class="draft-grid cols-2">
              <div class="field-group">
                <label>{t('models.params')}</label>
                <input
                  class="input mono"
                  value={params}
                  placeholder='{"temperature":0.7}'
                  onInput={(ev) => setParams((ev.target as HTMLInputElement).value)}
                />
              </div>
              <div class="field-group">
                <label>{t('models.headers')}</label>
                <input
                  class="input mono"
                  value={headers}
                  placeholder='{"X-Tenant":"blue"}'
                  onInput={(ev) => setHeaders((ev.target as HTMLInputElement).value)}
                />
              </div>
            </div>
            <label class="checkbox-label">
              <input
                type="checkbox"
                checked={noAuth}
                onChange={(ev) => setNoAuth((ev.target as HTMLInputElement).checked)}
              />
              {t('models.noAuth')}
            </label>
            <p class="hint">{t('models.noAuthHelp')}</p>
          </div>
        )}

        {error && <p class="gate-error">{error}</p>}
        <div class="modal-actions">
          <button type="button" class="btn" onClick={props.onClose}>
            {t('common.cancel')}
          </button>
          <button type="submit" class="btn primary" disabled={busy || model.trim() === ''}>
            {busy ? '…' : t('common.save')}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

function parseJSON(v: string): Record<string, unknown> | undefined {
  if (!v.trim()) return undefined
  const o = JSON.parse(v)
  if (typeof o !== 'object' || o === null || Array.isArray(o)) throw new Error('not an object')
  return o as Record<string, unknown>
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`
  return String(n)
}
