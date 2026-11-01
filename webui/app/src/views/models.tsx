// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
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
import { Select } from '../components/select'
import { useModalFocus } from '../components/modal-focus'
import { Icon } from '../components/icons'
import { toast, toastError } from '../components/toast'
import { ProviderLogo } from '../brand/providers'
import { needsPlaintextConsent } from './model-endpoint'
import {
  type Conn,
  type Member,
  copyAlias,
  groupConnections,
  hostOf,
  keyOf,
  labelOf,
  refOf,
} from './model-connections'

/** Models, grouped the way they are actually configured.
 *
 *  A *connection* is one endpoint + one key (one account); the models under it
 *  are the ids that key can ask for. The registry stores that flat — every row
 *  carries its own copy of the key — so the page derives the grouping from
 *  (provider, endpoint, key) and paints one card per connection, three tiles
 *  across. That is what makes "one key, five models" a thing you can see, add
 *  to, and trim without re-typing the key five times.
 *
 *  Every write goes through one sheet, and the sheet is a master–detail: the
 *  vendor catalogue on the left as a rail, the form on the right. The rail is
 *  not a step you can only walk forward through — switching plans mid-form is
 *  a click, and it is also how a row pointed at the wrong plan gets fixed.
 *
 *    - 添加: one key, fetch the roster, tick the ids to register, one save.
 *      Several models of one account are the normal case, not five passes
 *      through the same form.
 *    - 编辑: the same sheet on a single row, with the endpoint, the limits and
 *      the tuning the row was configured with.
 */

/** What one connectivity test said. `ms` is the panel round trip, measured in
 *  the page because the endpoint answers with a one-word completion and no
 *  timing of its own — "it works" and "it works in 4 seconds" are different
 *  answers to the question a test is asked. */
interface TestResult {
  ok: boolean
  /** The reply on success, the upstream's own words on failure. */
  detail: string
  ms: number
}

/** One row's address: the pair a legacy config can duplicate under one alias. */
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

export function ModelsSection() {
  useLocaleRerender()
  const [data, setData] = useState<ModelsResponse | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [results, setResults] = useState<Record<string, TestResult>>({})
  const [sheet, setSheet] = useState<SheetTarget | null>(null)

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
    const started = performance.now()
    const record = (ok: boolean, detail: string) =>
      setResults((rs) => ({
        ...rs,
        [k]: { ok, detail, ms: Math.round(performance.now() - started) },
      }))
    try {
      const r = await api.testModelRef({ alias: entry.alias, ...refOf(entry) })
      record(r.ok, (r.ok ? r.reply : r.error) ?? '')
    } catch (e: unknown) {
      record(false, e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  if (error && !data) {
    return (
      <div class="card settings-card">
        <h2 class="block-title">{t('models.title')}</h2>
        <p class="gate-error">{error}</p>
        {/* The Agents page offers a retry here; a console that lost its first
            read (a panel restarting under it) otherwise stays empty until the
            user reloads the whole page. */}
        <button
          class="btn small"
          type="button"
          onClick={() => {
            setError('')
            void reload()
          }}
        >
          {t('common.retry')}
        </button>
      </div>
    )
  }
  if (!data) return null

  const conns = groupConnections(data)
  const active = data.active
  // 这一页只剩卡片。连接（一个端点 + 一把密钥）是配置的存储方式，不是读的
  // 东西——哪张卡属于哪个账号，卡片自己的角标已经说了；分组只决定「添加」
  // 续在哪把密钥上，不再有标题、徽章和那一排按钮。
  const tiles = conns.flatMap((c) => c.members)
  // 只有一个连接时，「添加」直接续在那把密钥上；没有连接（或不止一个）时它
  // 退回选供应商的流程——这也是新建连接的唯一入口。
  const onlyConn = conns.length === 1 ? conns[0] : undefined
  const registry = data.models

  function afterWrite() {
    void reload()
  }

  async function switchTo(e: ModelEntry) {
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
  }

  /** 复制走后端：面板不回显密钥，也不返回 params/headers，只有源行自己能
   *  把这些带过去，前端填表只会得到一条没有凭据的副本。 */
  async function copy(m: Member) {
    const e = m.entry
    const alias = copyAlias(registry.map((x) => x.alias), e.alias)
    setBusy(`copy:${keyOf(e)}`)
    try {
      await api.addModel({
        provider: e.provider ?? '',
        model: e.model,
        base_url: e.base_url,
        api_type: e.api_type,
        alias,
        copy_from: e.alias,
      })
      // Report the row that was created, not the source: the toast is how the
      // user learns which alias to look for.
      toast(t('models.copied', { alias }), 'success')
      afterWrite()
    } catch (err) {
      toastError(err)
    } finally {
      setBusy('')
    }
  }

  async function remove(m: Member) {
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
  }

  return (
    <div class="models-section">
      <div class="model-grid">
        <button
          type="button"
          class="model-tile model-add"
          aria-label={t('models.addModel')}
          onClick={() => setSheet(onlyConn ? { kind: 'add', conn: onlyConn } : { kind: 'add' })}
        >
          <span class="model-add-plus" aria-hidden="true">
            <Icon name="plus" size={26} />
          </span>
          <span class="model-add-label">{t('models.addModel')}</span>
        </button>
        {tiles.map((m) => (
          <ModelTile
            key={keyOf(m.entry)}
            m={m}
            busy={busy}
            result={results[keyOf(m.entry)]}
            providers={data.providers}
            onUse={() => void switchTo(m.entry)}
            onTest={() => void test(m.entry)}
            onEdit={() => setSheet({ kind: 'edit', entry: m.entry })}
            onCopy={() => void copy(m)}
            onRemove={() => void remove(m)}
          />
        ))}
      </div>

      {error && <p class="gate-error">{error}</p>}

      {sheet && (
        <ModelSheet
          key={sheetKey(sheet)}
          target={sheet}
          providers={data.providers}
          active={active}
          onClose={() => setSheet(null)}
          onDone={(r) => {
            setSheet(null)
            if (r.edited) {
              toast(t('models.saved'), 'success')
            } else if (r.switched) {
              toast(t('models.addedSwitched', { n: r.added ?? 0, alias: r.switched }), 'success')
            } else {
              toast(t('models.addedN', { n: r.added ?? 0 }), 'success')
            }
            afterWrite()
          }}
        />
      )}
    </div>
  )
}

/** One model as a square tile: the vendor mark *is* the tile.
 *
 *  A grid can hold a dozen models of several accounts; as rows they all read
 *  alike, and the only thing telling them apart was a model id at the end of a
 *  line. The mark fills the square and the alias is the caption, so "which
 *  endpoint is this" is answered before reading. Clicking the tile switches to
 *  that model; test / edit / copy / remove surface on hover or keyboard focus,
 *  leaving the resting state a picture.
 *
 *  Nothing that can run long is printed here — the model id, the endpoint and
 *  the limits live in the tooltip and in the edit sheet. What the bottom strip
 *  carries is the one thing a picture cannot: whether this config actually
 *  answered, and why not when it did not.
 */
function ModelTile(props: {
  m: Member
  busy: string
  result?: TestResult
  providers: ProviderInfo[]
  onUse(): void
  onTest(): void
  onEdit(): void
  onCopy(): void
  onRemove(): void
}) {
  const { entry } = props.m
  const [confirming, setConfirming] = useState(false)
  const k = keyOf(entry)
  const keyOk = entry.no_auth || entry.key_set
  const testing = props.busy === `test:${k}`
  const verdict = props.result ? (props.result.ok ? 'ok' : 'bad') : ''
  // The corner dot is down to one job: a config that cannot dial at all. A test
  // result is said in the strip instead — two marks for one fact read as two
  // facts, and a 7px dot was never going to carry "and here is what broke".
  // An endpoint-owner warning counts even when the key is present: the row
  // would dial another vendor's roster, which is exactly what the dot is for.
  const dot = verdict || testing ? '' : entry.warning_code || !keyOk ? 'warn' : ''
  const dotTitle = entry.warning_code
    ? t('models.warn.endpointOwnerShort', {
        owner: labelOf(props.providers, entry.warning_arg),
      })
    : t('models.keyMissing')
  const busyHere = props.busy.endsWith(`:${k}`)
  const title = [entry.alias, entry.model, hostOf(entry.base_url), limitsText(entry)]
    .filter(Boolean)
    .join(' · ')

  return (
    <div
      class={`model-tile${entry.active ? ' active' : ''}${confirming ? ' confirming' : ''}${busyHere ? ' busy' : ''}${testing || props.result ? ' has-result' : ''}`}
    >
      <button
        type="button"
        class="model-tile-use"
        title={title}
        disabled={props.busy !== '' || entry.active}
        onClick={props.onUse}
      >
        <span class="model-tile-logo">
          <ProviderLogo id={entry.provider || entry.api_type} size={72} />
        </span>
        <span class="model-tile-name">{entry.alias}</span>
      </button>

      {/* The one tile that is lit up says so in words as well as in colour:
          a ring alone reads as "hovered" in a grid where hover is a verb. */}
      {entry.active && <span class="model-tile-active">{t('models.inUse')}</span>}
      {!props.m.registry && <span class="model-tile-flag">{t('models.notRegistered')}</span>}

      {dot && (
        <span class={`model-tile-dot ${dot}`} title={dotTitle} role="img" aria-label={dotTitle} />
      )}

      {/* The verdict, at the size of a sentence: a tinted band on the bottom
          edge with the icon, the word, and the one detail worth keeping —
          the round trip when it worked, the upstream's own complaint when it
          did not. It stays until the next test, because a result that fades
          after a few seconds is a result nobody can compare. */}
      {(testing || props.result) && (
        <div
          class={`model-tile-result${testing ? ' testing' : ` ${verdict}`}`}
          role="status"
          title={
            !testing && props.result && !props.result.ok
              ? `${t('models.testFailed')} · ${props.result.detail}`
              : undefined
          }
        >
          <Icon
            name={testing ? 'refresh' : verdict === 'ok' ? 'check' : 'alert'}
            size={12}
            class={testing ? 'model-tile-result-spin' : undefined}
          />
          <span class="model-tile-result-label">
            {testing
              ? t('models.testing')
              : t(verdict === 'ok' ? 'models.testPassed' : 'models.testFailed')}
          </span>
          {!testing && props.result && (
            <span class="model-tile-result-detail">
              {props.result.ok ? fmtMs(props.result.ms) : props.result.detail}
            </span>
          )}
        </div>
      )}

      <div class="model-tile-actions">
        {confirming ? (
          <>
            <button
              type="button"
              class="model-tile-act danger"
              title={t('models.confirmRemove')}
              aria-label={t('models.confirmRemove')}
              disabled={props.busy !== ''}
              onClick={props.onRemove}
            >
              <Icon name="check" size={15} />
            </button>
            <button
              type="button"
              class="model-tile-act"
              title={t('common.cancel')}
              aria-label={t('common.cancel')}
              onClick={() => setConfirming(false)}
            >
              <Icon name="x" size={15} />
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              class="model-tile-act"
              title={t('models.test')}
              aria-label={t('models.test')}
              disabled={props.busy !== ''}
              onClick={props.onTest}
            >
              <Icon name={props.busy === `test:${k}` ? 'refresh' : 'activity'} size={15} />
            </button>
            <button
              type="button"
              class="model-tile-act"
              title={t('models.edit')}
              aria-label={t('models.edit')}
              disabled={props.busy !== ''}
              onClick={props.onEdit}
            >
              <Icon name="pencil" size={15} />
            </button>
            {/* Copy exists because the same account often serves one more id
                than the config lists: duplicating the row carries the key and
                the overrides the form can never echo back. */}
            {props.m.registry && (
              <button
                type="button"
                class="model-tile-act"
                title={t('models.copy')}
                aria-label={t('models.copy')}
                disabled={props.busy !== ''}
                onClick={props.onCopy}
              >
                <Icon name="copy" size={15} />
              </button>
            )}
            {props.m.registry && (
              <button
                type="button"
                class="model-tile-act"
                title={t('models.remove')}
                aria-label={t('models.remove')}
                disabled={props.busy !== ''}
                onClick={() => setConfirming(true)}
              >
                <Icon name="trash" size={15} />
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// ---- the sheet ----

/** What the sheet is opened on: a new connection, an existing one (adding more
 *  models to it), or one registered row. */
type SheetTarget = { kind: 'add'; conn?: Conn } | { kind: 'edit'; entry: ModelEntry }

function sheetKey(s: SheetTarget): string {
  return s.kind === 'edit' ? `edit:${keyOf(s.entry)}` : `add:${s.conn?.id ?? ''}`
}

/** The catalogue entry to open on: the one behind the row being edited, else
 *  the connection being extended, else the live model's, else the first. */
function providerFor(providers: ProviderInfo[], id?: string): ProviderInfo | null {
  return (
    (id ? providers.find((p) => p.id === id) : undefined) ??
    providers.find((p) => p.id === 'custom') ??
    providers[0] ??
    null
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
    // A row exists because someone added it — the catalogue default on open,
    // or a blank line on "add row". Both are intents, so both start ticked;
    // the checkbox is there to *un*tick the noise a fetched roster brings in.
    picked,
    open: false,
  }
}

/** An existing row as a draft. A zero limit means "the catalogue's answer",
 *  which is exactly what the row's auto flags encode. */
function draftOfEntry(e: ModelEntry): Draft {
  return {
    id: draftSeq++,
    model: e.model,
    alias: e.alias,
    context: e.context_auto ? 0 : (e.context_window ?? 0),
    maxTokens: e.max_tokens_auto ? 0 : (e.max_tokens ?? 0),
    thinking: e.thinking === 'on' || e.thinking === 'off' ? e.thinking : 'auto',
    budget: e.thinking_budget ?? 0,
    retries: e.max_retries ?? 0,
    params: '',
    headers: '',
    picked: true,
    open: false,
  }
}

/** The one write surface: vendor rail on the left, form on the right.
 *
 *  Two shapes share it. Adding is the multi-model pass — one row per model the
 *  user means to register, and every ticked row is one POST. The provider's
 *  roster feeds those rows' model fields as a picker; it never creates rows of
 *  its own (see fetchRemote). Editing is the same form on one row, where the
 *  endpoint is a *choice* rather than a typed URL: picking a vendor in the rail
 *  re-points the row at that plan's endpoint, which is the fix for a base URL
 *  pasted from a sibling plan's docs.
 */
function ModelSheet(props: {
  target: SheetTarget
  providers: ProviderInfo[]
  active: ModelEntry
  onClose(): void
  onDone(r: { added?: number; switched?: string; edited?: boolean }): void
}) {
  const editing = props.target.kind === 'edit' ? props.target.entry : null
  const pre = props.target.kind === 'add' ? props.target.conn : null
  const initial = providerFor(
    props.providers,
    editing?.provider ?? pre?.provider ?? props.active.provider,
  )

  const [provider, setProvider] = useState<ProviderInfo | null>(initial)
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [baseURL, setBaseURL] = useState(editing?.base_url ?? pre?.baseURL ?? '')
  const [apiType, setApiType] = useState(editing?.api_type ?? pre?.apiType ?? initial?.api_type ?? 'openai')
  // Consent to reach a plain-http endpoint. Opens on what is stored: an edit
  // that never mentions the field must not silently revoke a waiver the row
  // was configured with.
  const [allowPlaintext, setAllowPlaintext] = useState(!!editing?.allow_plaintext)
  const [noAuth, setNoAuth] = useState(!!(editing?.no_auth ?? pre?.noAuth ?? initial?.no_auth))
  const [drafts, setDrafts] = useState<Draft[]>(() => {
    if (editing) return [draftOfEntry(editing)]
    // Extending a connection starts with one blank row: the table would
    // otherwise open empty and read as a broken form.
    if (pre) return [newDraft()]
    return [newDraft(initial?.default_model ?? '')]
  })
  const [remote, setRemote] = useState<string[] | null>(null)
  const [useAfter, setUseAfter] = useState(false)
  const [tuneOpen, setTuneOpen] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const box = useRef<HTMLDivElement>(null)
  useModalFocus(box, true)

  const d = drafts[0]

  /** The endpoint the row would be written with: what is typed, else the
   *  catalogue's own. */
  function currentBase(): string {
    return baseURL.trim() || provider?.base_url || ''
  }

  /** Always shown once a vendor is picked. Hiding it unless the typed value
   *  already differed made "a known vendor behind a company relay" reachable
   *  only by choosing custom, which loses the catalogue defaults and the key
   *  reuse that comes with the vendor row. */
  const showBase = provider !== null

  function pick(p: ProviderInfo) {
    // The rail keeps the current vendor selected; re-clicking it is not a
    // switch, and must not wipe a half-filled form.
    if (provider?.id === p.id) return
    setProvider(p)
    setBaseURL('')
    setApiType(p.api_type)
    setNoAuth(p.no_auth)
    setAllowPlaintext(false)
    setRemote(null)
    setError('')
    // A provider switch starts a new model. Carrying the previous one's key,
    // alias, or overrides across would submit them to a different endpoint.
    // While editing, the alias is the row's identity and must survive a
    // vendor change — otherwise "edit" silently renames the config.
    setApiKey('')
    setShowKey(false)
    setTuneOpen(false)
    if (editing) {
      const cur = drafts[0]!
      setDrafts([
        {
          ...cur,
          model: p.default_model,
          context: 0,
          maxTokens: 0,
          thinking: 'auto',
          budget: 0,
          retries: 0,
          params: '',
          headers: '',
        },
      ])
    } else {
      setDrafts([newDraft(p.default_model)])
    }
  }

  function patch(id: number, p: Partial<Draft>) {
    setDrafts((cur) => cur.map((x) => (x.id === id ? { ...x, ...p } : x)))
  }

  function requestFor(draft: Draft): AddModelRequest {
    const p = provider!
    const req: AddModelRequest = {
      provider: p.id,
      model: draft.model.trim() || undefined,
      alias: draft.alias.trim() || undefined,
      api_key: apiKey.trim() || undefined,
    }
    // Only restate the endpoint when it is not the catalogue's own — a
    // restated one is harmless, but sending it keeps a relay under a known
    // vendor from being silently rebased.
    const base = currentBase()
    if (base && base !== p.base_url) {
      req.base_url = base
      req.api_type = apiType
    } else if (p.id === 'custom' || apiType !== p.api_type) {
      req.api_type = apiType
    }
    if (draft.context > 0) req.context_window = draft.context
    if (draft.maxTokens > 0) req.max_tokens = draft.maxTokens
    if (draft.thinking !== 'auto') req.thinking = draft.thinking
    if (draft.budget > 0) req.thinking_budget = draft.budget
    if (draft.retries !== 0) req.max_retries = draft.retries
    if (noAuth) req.no_auth = true
    // The waiver only means something for a plain-http endpoint, and the
    // backend refuses one without it — so it rides on every row of this form.
    if (needsPlaintextConsent(base)) req.allow_plaintext = allowPlaintext
    // Extending an existing connection borrows that row's key specifically —
    // not "whatever key this provider has", which is the wrong account when
    // the same vendor is registered twice. A vendor picked from the rail is a
    // different account, so the borrow only stands while the vendor does.
    if (!apiKey.trim() && pre?.keySet && vendorSame) req.reuse_from = pre.members[0]?.entry.alias
    const params = parseJSON(draft.params)
    if (params) req.params = params
    const headers = parseJSON(draft.headers)
    if (headers) req.headers = headers as Record<string, string>
    return req
  }

  async function fetchRemote() {
    if (!provider) return
    setBusy('fetch')
    setError('')
    try {
      const base = currentBase()
      const r = await api.fetchModels({
        provider: provider.id,
        model: drafts[0]?.model || provider.default_model,
        api_key: apiKey.trim() || undefined,
        // Name the row being edited. Two accounts of one vendor share the
        // provider id, and without the alias the server borrows whichever key
        // it finds first — probing the wrong account and reporting its health.
        alias: editing?.alias,
        ...(base !== provider.base_url || provider.id === 'custom'
          ? { base_url: base, api_type: apiType }
          : {}),
        allow_plaintext: needsPlaintextConsent(base) ? allowPlaintext : undefined,
      })
      if (!r.ok || !r.models) {
        setError(r.error ?? t('models.fetchFail'))
        return
      }
      setRemote(r.models)
      // The roster feeds the picker, it does not mint rows. Fetching a
      // 21-model endpoint used to append 21 drafts (unknown ids pre-ticked),
      // so "look at what this key can reach" ended as 21 cards nobody asked
      // for. The list now only fills the model field's caret — the same
      // contract the setup wizard already had — and the draft list stays
      // exactly as the user built it.
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  async function submit(ev: Event) {
    ev.preventDefault()
    if (!provider || busy) return
    setError('')
    if (editing) {
      await saveEdit()
      return
    }
    await saveAdded()
  }

  async function saveAdded() {
    const picked = drafts.filter((x) => x.picked && x.model.trim() !== '')
    if (picked.length === 0) {
      setError(t('models.noneSelected'))
      return
    }
    // Validate the JSON escape hatches before the first POST: a typo in row
    // five must not leave rows one to four registered and no way to tell.
    const requests: AddModelRequest[] = []
    try {
      for (const draft of picked) requests.push(requestFor(draft))
    } catch {
      setError(t('models.badJSON'))
      return
    }
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
      props.onDone({ added: created, switched: useAfter ? firstAlias : undefined })
    } catch (err) {
      // Rows already written stay — saying how many landed is more useful than
      // a bare error next to a half-applied form.
      const msg = err instanceof Error ? err.message : String(err)
      setError(created > 0 ? t('models.addPartial', { n: created, error: msg }) : msg)
      setBusy('')
    }
  }

  async function saveEdit() {
    const row = drafts[0]
    const src = editing
    if (!row || !src || !row.model.trim()) return
    let params: Record<string, unknown> | undefined
    let headers: Record<string, string> | undefined
    try {
      params = parseJSON(row.params)
      headers = parseJSON(row.headers) as Record<string, string> | undefined
    } catch {
      setError(t('models.badJSON'))
      return
    }
    setBusy('save')
    try {
      const req: AddModelRequest = {
        provider: provider!.id,
        model: row.model.trim(),
        alias: row.alias.trim() || undefined,
        api_key: apiKey.trim() || undefined,
        thinking: row.thinking,
        no_auth: noAuth,
      }
      const base = currentBase()
      if (base !== src.base_url) {
        req.base_url = base
        req.api_type = apiType
      } else if (apiType !== src.api_type) {
        req.api_type = apiType
      }
      // Only a plain-http endpoint can carry the waiver; stating it otherwise
      // would put a meaningless flag on a row that speaks https.
      if (needsPlaintextConsent(base)) req.allow_plaintext = allowPlaintext
      // 0 means "put it back on the catalogue's answer" — sending nothing
      // would mean "leave what is stored", which is a different instruction.
      if (row.context > 0) req.context_window = row.context
      else if (!src.context_auto) req.context_window_reset = true
      if (row.maxTokens > 0) req.max_tokens = row.maxTokens
      else if (!src.max_tokens_auto) req.max_tokens_reset = true
      if (row.budget > 0) req.thinking_budget = row.budget
      else if ((src.thinking_budget ?? 0) > 0) req.thinking_budget_reset = true
      if (row.retries !== 0) req.max_retries = row.retries
      else if ((src.max_retries ?? 0) !== 0) req.max_retries_reset = true
      if (params) req.params = params
      if (headers) req.headers = headers
      await api.updateModel(src.alias, req, refOf(src))
      props.onDone({ edited: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy('')
    }
  }

  // A key already on file — the vendor's, the connection's or the edited
  // row's own — is not a missing one: leaving the field blank keeps it. But a
  // key belongs to the account it was typed for: pointing the row at another
  // vendor invalidates it, unless that vendor already has a key on file or the
  // two are sibling plans of one host (Zen and Go are one key). Saying "已存
  // 密钥" across a vendor switch would promise a save the backend refuses.
  const srcProvider = editing?.provider ?? pre?.provider ?? ''
  const vendorSame = srcProvider === '' || provider?.id === srcProvider
  const sameHost =
    editing !== null &&
    hostOf(editing.base_url) !== '' &&
    hostOf(editing.base_url) === hostOf(provider?.base_url ?? '')
  const keyOnFile = editing
    ? editing.key_set && (vendorSame || (provider?.key_saved ?? false) || sameHost)
    : pre
      ? (pre.keySet && vendorSame) || (provider?.key_saved ?? false)
      : (provider?.key_saved ?? false)
  const needsKey = provider !== null && !provider.no_auth && !noAuth && !keyOnFile
  const keyReady = !needsKey || apiKey.trim() !== ''
  const pickedCount = drafts.filter((x) => x.picked && x.model.trim() !== '').length
  const baseReady = provider !== null && (provider.id !== 'custom' || currentBase() !== '')
  const canSubmit = provider !== null && baseReady && keyReady && (editing ? drafts[0]!.model.trim() !== '' : pickedCount > 0)

  const title = editing
    ? t('models.editModel')
    : pre
      ? t('models.addModelTitle')
      : t('models.addConnection')
  const subtitle = editing
    ? t('models.editModelSub')
    : pre
      ? `${pre.providerLabel || t('models.customProvider')} · ${pre.baseURL || t('models.customEndpoint')}`
      : t('models.pickProviderShort')

  const keyPlaceholder = editing
    ? keyOnFile
      ? `${t('models.apiKeySet')} ${editing.key_hint ?? ''}`.trim()
      : 'sk-…'
    : pre?.keySet && vendorSame
      ? t('models.keyReuseConn')
      : provider?.key_saved
        ? t('models.keyReuseHint')
        : 'sk-…'
  const keyHelp = editing
    ? keyOnFile
      ? t('models.keyEmptyKeep')
      : t('models.keyHelp')
    : pre?.keySet && vendorSame
      ? t('models.keyReuseConnHelp')
      : provider?.key_saved
        ? t('models.keyReuseHelp')
        : t('models.keyHelp')

  const tuneCount = d
    ? [
        d.context > 0,
        d.maxTokens > 0,
        d.thinking !== 'auto',
        d.budget > 0,
        d.retries !== 0,
        d.params.trim() !== '',
        d.headers.trim() !== '',
      ].filter(Boolean).length
    : 0
  const paramsState = jsonState(d?.params ?? '')
  const headersState = jsonState(d?.headers ?? '')

  const ownerOffer =
    editing?.warning_code === 'endpoint-owner'
      ? props.providers.find((p) => p.id === editing.warning_arg && p.id !== provider?.id)
      : undefined

  /** The tuning block opens below the fold on a short sheet: bring it up so
   *  the fields it just revealed are the ones in view. */
  function toggleTune() {
    const next = !tuneOpen
    setTuneOpen(next)
    if (!next) return
    requestAnimationFrame(() => {
      const pane = box.current?.querySelector<HTMLElement>('.wizard-pane')
      const tune = box.current?.querySelector<HTMLElement>('.tune')
      if (!pane || !tune) return
      const outer = pane.getBoundingClientRect()
      const inner = tune.getBoundingClientRect()
      if (inner.bottom > outer.bottom) pane.scrollTop += inner.bottom - outer.bottom + 12
    })
  }

  // Only reachable when the catalogue itself is empty (a broken install):
  // every normal open lands on a provider, so the pane is never blank.
  if (!provider) {
    return (
      <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
        <div class="modal wizard-modal" role="dialog" aria-modal="true">
          <header class="wizard-head">
            <div class="wizard-head-text">
              <h3 class="wizard-title">{title}</h3>
              <p class="wizard-sub">{subtitle}</p>
            </div>
          </header>
          <p class="wizard-empty">{t('models.pickProvider')}</p>
        </div>
      </div>
    )
  }

  return (
    <div class="modal-backdrop" onClick={(e) => e.target === e.currentTarget && props.onClose()}>
      <div
        ref={box}
        class={`modal wizard-modal${editing ? '' : ' wizard-roomy'}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="model-sheet-title"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            props.onClose()
          }
        }}
      >
        <header class="wizard-head">
          <div class="wizard-head-text">
            <h3 id="model-sheet-title" class="wizard-title">
              {title}
            </h3>
            <p class="wizard-sub">{subtitle}</p>
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

        <form class="wizard-form" onSubmit={submit}>
          <div class="wizard-split">
            <ProviderRail providers={props.providers} current={provider.id} onPick={pick} />

            <div class="wizard-pane">
              <div class="wizard-provider">
                <span class="wizard-provider-logo">
                  <ProviderLogo id={provider.id} size={18} />
                </span>
                <div class="wizard-provider-text">
                  <span class="wizard-provider-name">{provider.label}</span>
                  <span class="wizard-provider-endpoint mono">
                    {currentBase() || t('models.customEndpoint')}
                  </span>
                </div>
                <span class="wizard-provider-type">
                  {provider.id === 'custom' ? apiType : provider.api_type}
                </span>
              </div>

              {ownerOffer && (
                <div class="fix-suggest">
                  <span>{t('models.warn.endpointOwnerShort', { owner: ownerOffer.label })}</span>
                  <button type="button" class="btn small" onClick={() => pick(ownerOffer)}>
                    {t('models.warn.useOwner', { owner: ownerOffer.label })}
                  </button>
                </div>
              )}

              {provider.id === 'custom' && (
                <div class="wizard-field">
                  <span class="wizard-label">{t('settings.apiType')}</span>
                  <div class="segmented" role="radiogroup" aria-label={t('settings.apiType')}>
                    {(['openai', 'anthropic', 'responses', 'google'] as const).map((v) => (
                      <button
                        key={v}
                        type="button"
                        role="radio"
                        aria-checked={apiType === v}
                        class={`seg${apiType === v ? ' on' : ''}`}
                        onClick={() => setApiType(v)}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                </div>
              )}

              {showBase && (
                <div class="wizard-field">
                  <div class="wizard-label-row">
                    <label for="ms-base">{t('models.baseURL')}</label>
                    {provider.id !== 'custom' && (
                      <button
                        type="button"
                        class="wizard-fetch"
                        onClick={() => setBaseURL('')}
                      >
                        {t('models.baseURLReset')}
                      </button>
                    )}
                  </div>
                  <input
                    id="ms-base"
                    class="input mono"
                    value={baseURL}
                    onInput={(e) => setBaseURL((e.target as HTMLInputElement).value)}
                    placeholder={provider.base_url || 'https://…/v1'}
                  />
                  {/* The guard's https rule exists so the key never travels
                      cleartext, so a plain-http endpoint has to ask for the
                      waiver explicitly. The backend refuses the row without
                      it; this box is the only place the consent can be given. */}
                  {needsPlaintextConsent(currentBase()) && (
                    <>
                      <label class="consent">
                        <input
                          type="checkbox"
                          class="check"
                          checked={allowPlaintext}
                          onChange={(e) =>
                            setAllowPlaintext((e.target as HTMLInputElement).checked)
                          }
                        />
                        <span>{t('settings.allowPlaintext')}</span>
                      </label>
                      <p class="wizard-help">{t('settings.allowPlaintextHelp')}</p>
                    </>
                  )}
                </div>
              )}

              {!provider.no_auth && (
                <>
                  <div class="wizard-field">
                    <div class="wizard-label-row">
                      <label for="ms-key">{t('settings.apiKey')}</label>
                      {keyOnFile && <span class="wizard-badge">{t('models.keySaved')}</span>}
                      {/* The waiver belongs to the key, not to a field of its
                          own: it answers "what if there is no key", so it sits
                          on the key's head line and the sentence that explains
                          it moves into the ? tip beside it. */}
                      <span class="wizard-label-end">
                        <label class="checkbox-label">
                          <input
                            type="checkbox"
                            class="check"
                            checked={noAuth}
                            onChange={(e) => setNoAuth((e.target as HTMLInputElement).checked)}
                          />
                          {t('models.noAuth')}
                        </label>
                        <button
                          type="button"
                          class="wizard-q tip-wrap tip-right"
                          aria-label={t('models.noAuthHelp')}
                          data-tip={t('models.noAuthHelp')}
                        >
                          ?
                        </button>
                      </span>
                    </div>
                    <div class="wizard-affix">
                      <input
                        id="ms-key"
                        class="input mono"
                        type={showKey ? 'text' : 'password'}
                        autocomplete="off"
                        value={apiKey}
                        onInput={(e) => setApiKey((e.target as HTMLInputElement).value)}
                        placeholder={keyPlaceholder}
                      />
                      <button
                        type="button"
                        class="wizard-affix-btn"
                        aria-label={t(showKey ? 'models.hideKey' : 'models.showKey')}
                        title={t(showKey ? 'models.hideKey' : 'models.showKey')}
                        aria-pressed={showKey}
                        onClick={() => setShowKey((v) => !v)}
                      >
                        <Icon name={showKey ? 'eye-off' : 'eye'} size={15} />
                      </button>
                    </div>
                    <p class="wizard-help">{keyHelp}</p>
                  </div>
                </>
              )}

              {editing && d ? (
                <>
                  <div class="wizard-field">
                    <div class="wizard-label-row">
                      <label for="ms-model">{t('models.col.model')}</label>
                      <button
                        type="button"
                        class="wizard-fetch"
                        disabled={busy !== ''}
                        onClick={fetchRemote}
                      >
                        <Icon name="refresh" size={13} />
                        {busy === 'fetch' ? t('models.fetching') : t('models.fetch')}
                      </button>
                    </div>
                    <ModelPicker
                      id="ms-model"
                      value={d.model}
                      onValue={(v) => patch(d.id, { model: v, alias: d.alias === d.model ? v : d.alias })}
                      options={remote ?? []}
                      placeholder={provider.default_model || 'model-id'}
                      required
                    />
                    <p class="wizard-help">
                      {remote
                        ? t('models.fetched', { n: remote.length })
                        : t('models.modelHelp')}
                    </p>
                  </div>

                  <div class="wizard-grid solo">
                    <div class="wizard-field">
                      <label for="ms-alias">{t('models.col.alias')}</label>
                      <input
                        id="ms-alias"
                        class="input mono"
                        value={d.alias}
                        onInput={(e) => patch(d.id, { alias: (e.target as HTMLInputElement).value })}
                        placeholder={d.model || t('models.aliasAuto')}
                      />
                      <p class="wizard-help">{t('models.aliasHelp')}</p>
                    </div>
                  </div>

                  <section class={`tune${tuneOpen ? ' open' : ''}`}>
                    <button
                      type="button"
                      class="tune-head"
                      aria-expanded={tuneOpen}
                      aria-controls="ms-tune"
                      onClick={toggleTune}
                    >
                      <span class="tune-chevron" aria-hidden="true">
                        <Icon name="chevron-right" size={14} />
                      </span>
                      <span class="tune-head-text">
                        <span class="tune-title">{t('models.advanced')}</span>
                        <span class="tune-desc">{t('models.advancedHelp')}</span>
                      </span>
                      <span class={`tune-count${tuneCount > 0 ? ' dirty' : ''}`}>
                        {tuneCount > 0
                          ? t('models.tuneCount', { n: tuneCount })
                          : t('models.tuneDefault')}
                      </span>
                    </button>

                    {tuneOpen && (
                      <div class="tune-body" id="ms-tune">
                        <div class="tune-group">
                          <div class="tune-group-title">{t('models.group.capacity')}</div>
                          <div class="tune-grid">
                            <div class="tune-field">
                              <label class="tune-label" for="ms-max">
                                {t('models.maxTokens')}
                              </label>
                              <LimitInput
                                id="ms-max"
                                value={d.maxTokens}
                                autoValue={editing.auto_max_tokens}
                                onChange={(v) => patch(d.id, { maxTokens: v })}
                              />
                            </div>
                            <div class="tune-field">
                              <label class="tune-label" for="ms-ctx">
                                {t('models.contextWindow')}
                              </label>
                              <LimitInput
                                id="ms-ctx"
                                value={d.context}
                                autoValue={editing.auto_context_window}
                                onChange={(v) => patch(d.id, { context: v })}
                              />
                            </div>
                          </div>
                          <p class="tune-help">{t('models.capacityHelp')}</p>
                        </div>

                        <div class="tune-group">
                          <div class="tune-group-title">{t('models.group.reasoning')}</div>
                          <div class="tune-grid">
                            <div class="tune-field">
                              <span class="tune-label">{t('models.thinking')}</span>
                              <div class="segmented" role="radiogroup" aria-label={t('models.thinking')}>
                                {(['auto', 'on', 'off'] as const).map((v) => (
                                  <button
                                    key={v}
                                    type="button"
                                    role="radio"
                                    aria-checked={d.thinking === v}
                                    class={`seg${d.thinking === v ? ' on' : ''}`}
                                    onClick={() => patch(d.id, { thinking: v })}
                                  >
                                    {t(`models.thinking.${v}`)}
                                  </button>
                                ))}
                              </div>
                            </div>
                            <div class="tune-field">
                              <label class="tune-label" for="ms-budget">
                                {t('models.thinkingBudget')}
                              </label>
                              <LimitInput
                                id="ms-budget"
                                value={d.budget}
                                onChange={(v) => patch(d.id, { budget: v })}
                              />
                              {d.thinking === 'off' && <p class="tune-help">{t('models.budgetOff')}</p>}
                            </div>
                          </div>
                        </div>

                        <div class="tune-group">
                          <div class="tune-group-title">{t('models.group.request')}</div>
                          <div class="tune-grid solo">
                            <div class="tune-field">
                              <label class="tune-label" for="ms-retries">
                                {t('models.maxRetries')}
                              </label>
                              <input
                                id="ms-retries"
                                class="input"
                                type="number"
                                min={-1}
                                value={d.retries !== 0 ? String(d.retries) : ''}
                                placeholder="0"
                                onInput={(e) =>
                                  patch(d.id, {
                                    retries: Number((e.target as HTMLInputElement).value) || 0,
                                  })
                                }
                              />
                              <p class="tune-help">{t('models.maxRetriesHelp')}</p>
                            </div>
                          </div>
                        </div>

                        <div class="tune-group">
                          <div class="tune-group-title">{t('models.group.overrides')}</div>
                          <div class="tune-field">
                            <div class="tune-json-head">
                              <label class="tune-label" for="ms-params">
                                {t('models.params')}
                              </label>
                              <JsonState state={paramsState} />
                            </div>
                            <textarea
                              id="ms-params"
                              class="input mono tune-json"
                              rows={2}
                              value={d.params}
                              aria-invalid={paramsState === 'bad'}
                              onInput={(e) =>
                                patch(d.id, { params: (e.target as HTMLTextAreaElement).value })
                              }
                              placeholder='{"temperature": 0.7}'
                            />
                            <p class="tune-help">{t('models.paramsHelp')}</p>
                          </div>
                          <div class="tune-field">
                            <div class="tune-json-head">
                              <label class="tune-label" for="ms-headers">
                                {t('models.headers')}
                              </label>
                              <JsonState state={headersState} />
                            </div>
                            <textarea
                              id="ms-headers"
                              class="input mono tune-json"
                              rows={2}
                              value={d.headers}
                              aria-invalid={headersState === 'bad'}
                              onInput={(e) =>
                                patch(d.id, { headers: (e.target as HTMLTextAreaElement).value })
                              }
                              placeholder='{"X-Tenant": "blue"}'
                            />
                            <p class="tune-help">{t('models.headersHelp')}</p>
                          </div>
                        </div>
                      </div>
                    )}
                  </section>
                </>
              ) : (
                <>
                  <div class="models-table-head">
                    <div class="section-title">{t('models.list')}</div>
                    <div class="models-table-actions">
                      <button
                        type="button"
                        class="btn small"
                        disabled={busy !== ''}
                        onClick={fetchRemote}
                      >
                        <Icon name="download" size={14} />
                        {busy === 'fetch' ? t('models.fetching') : t('models.fetch')}
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
                    <p class="hint">{t('models.fetched', { n: remote.length })}</p>
                  )}

                  <div class="models-table">
                    {drafts.length > 1 && (
                      <div class="draft-head">
                        <span class="draft-head-check" />
                        {/* Same skeleton as a row — same flex weights, same
                            gaps — so the labels land over the fields they name. */}
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
                    {drafts.map((draft) => (
                      <DraftRow
                        key={draft.id}
                        d={draft}
                        remote={remote}
                        // One header above the table beats a header per row:
                        // five fetched models were five copies of the same
                        // four labels.
                        bare={drafts.length > 1}
                        onPatch={(p) => patch(draft.id, p)}
                        onDrop={() => setDrafts((cur) => cur.filter((x) => x.id !== draft.id))}
                      />
                    ))}
                  </div>
                </>
              )}

              {error && (
                <p class="form-alert" role="alert">
                  <Icon name="alert" size={14} />
                  <span>{error}</span>
                </p>
              )}
            </div>
          </div>

          <footer class="wizard-foot">
            {editing ? (
              <span class="wizard-foot-note">
                {!busy && !keyReady ? t('models.keyRequired') : ''}
              </span>
            ) : (
              <label class="checkbox-label wizard-foot-note">
                <input
                  type="checkbox"
                  checked={useAfter}
                  onChange={(e) => setUseAfter((e.target as HTMLInputElement).checked)}
                />
                {t('models.useAfterAdd')}
              </label>
            )}
            <button type="button" class="btn" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
            <button type="submit" class="btn primary" disabled={!canSubmit || busy !== ''}>
              {busy === 'save'
                ? editing
                  ? '…'
                  : t('models.adding')
                : editing
                  ? t('common.save')
                  : pickedCount > 0
                    ? t('models.addConfirmN', { n: pickedCount })
                    : t('models.addModel')}
            </button>
          </footer>
        </form>
      </div>
    </div>
  )
}

/** The vendor catalogue as a column: region groups, the vendor's mark, and a
 *  dot for "a key for this one is already on file". Re-picking the current
 *  vendor is a no-op, so a stray click never wipes a half-filled form. */
function ProviderRail(props: {
  providers: ProviderInfo[]
  current: string
  onPick(p: ProviderInfo): void
}) {
  const rail = useRef<HTMLElement>(null)
  const regions = useMemo(() => {
    const groups: Record<string, ProviderInfo[]> = { global: [], cn: [] }
    for (const p of props.providers) (groups[p.region] ?? groups.global)!.push(p)
    return groups
  }, [props.providers])

  // The rail is longer than the sheet: the current vendor has to be visible
  // without hunting for it. Scroll the rail itself rather than the item —
  // scrollIntoView also moves ancestors.
  useEffect(() => {
    const box = rail.current
    const item = box?.querySelector<HTMLElement>('.wizard-rail-item.on')
    if (!box || !item) return
    const outer = box.getBoundingClientRect()
    const inner = item.getBoundingClientRect()
    if (inner.top < outer.top) box.scrollTop += inner.top - outer.top
    else if (inner.bottom > outer.bottom) box.scrollTop += inner.bottom - outer.bottom
  }, [props.current])

  return (
    <aside class="wizard-rail" aria-label={t('models.providerList')} ref={rail}>
      {(['global', 'cn'] as const).map(
        (r) =>
          regions[r]!.length > 0 && (
            <div class="wizard-rail-group" key={r}>
              <div class="wizard-rail-title">{t(`models.region.${r}`)}</div>
              {regions[r]!.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  class={`wizard-rail-item${props.current === p.id ? ' on' : ''}`}
                  aria-pressed={props.current === p.id}
                  title={p.label}
                  onClick={() => props.onPick(p)}
                >
                  <span class="wizard-rail-logo">
                    <ProviderLogo id={p.id} size={15} />
                  </span>
                  <span class="wizard-rail-name">{railLabel(p.label)}</span>
                  {p.key_saved && <span class="wizard-rail-dot" title={t('models.keySaved')} />}
                </button>
              ))}
            </div>
          ),
      )}
    </aside>
  )
}

/** One draft as a row of the multi-model table: id, alias, context, thinking,
 *  and the tuning block behind a disclosure. */
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
              <Select
                class="input"
                value={d.thinking}
                options={(['auto', 'on', 'off'] as const).map((v) => ({
                  value: v,
                  label: t(`models.thinking.${v}`),
                }))}
                onChange={(v) => props.onPatch({ thinking: v as Draft['thinking'] })}
                ariaLabel={t('models.col.thinking')}
              />
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
                    value={d.retries !== 0 ? String(d.retries) : ''}
                    placeholder="0"
                    onInput={(e) =>
                      props.onPatch({ retries: Number((e.target as HTMLInputElement).value) || 0 })
                    }
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

/** Live JSON-object check for the two override fields: `empty` keeps the
 *  field quiet, `bad` is shown inline instead of only on submit. */
function jsonState(v: string): 'empty' | 'ok' | 'bad' {
  if (!v.trim()) return 'empty'
  try {
    const o = JSON.parse(v) as unknown
    return typeof o === 'object' && o !== null && !Array.isArray(o) ? 'ok' : 'bad'
  } catch {
    return 'bad'
  }
}

function JsonState(props: { state: 'empty' | 'ok' | 'bad' }) {
  if (props.state === 'empty') return null
  const ok = props.state === 'ok'
  return (
    <span class={`tune-status ${ok ? 'ok' : 'bad'}`}>
      <Icon name={ok ? 'check' : 'alert'} size={12} />
      {t(ok ? 'models.validJSON' : 'models.invalidJSON')}
    </span>
  )
}

function parseJSON(v: string): Record<string, unknown> | undefined {
  if (!v.trim()) return undefined
  const o = JSON.parse(v)
  if (typeof o !== 'object' || o === null || Array.isArray(o)) throw new Error('not an object')
  return o as Record<string, unknown>
}

/** The rail is 224px wide: a vendor's parenthetical ("(Anthropic)", "(本地)")
 *  is the first thing to go, and the pane states the full label anyway. */
function railLabel(label: string): string {
  return label.replace(/\s*\([^)]*\)\s*$/, '')
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`
  return String(n)
}

/** A round trip at the precision a reader cares about: a fast answer in ms,
 *  a slow one in seconds. */
function fmtMs(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}
