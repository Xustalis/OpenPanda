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
import { confirmDialog } from '../components/confirm'
import { Icon } from '../components/icons'
import { toast, toastError } from '../components/toast'
import { ProviderLogo } from '../brand/providers'
import { DiscoverSheet } from './model-discover'
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
  // The discovery sheet is not a SheetTarget: it writes rows the person never
  // typed, so it owns its own load and its own import call.
  const [discovering, setDiscovering] = useState(false)

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
  // 一张卡 = 一个连接（一个端点 + 一把密钥）。注册表本来就按模型存行、每行
  // 各带一份密钥，分组是从 (供应商, 端点, 密钥) 推出来的：一个中继上挂三个
  // 模型，就是三行、一个账号。按行画卡会把这件事抹掉——三张卡各自复述同一个
  // 账号，互相之间什么也没说。
  // 卡面只写账号；把鼠标放上去，卡面换成这个账号名下的模型，点一下即切换。
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
        copy_from_key: e.key_fingerprint,
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
        r.cleared
          ? t('models.removedLast', { alias: m.entry.alias })
          : r.switched_to
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

  /** 删一条连接 = 删掉它名下的每一行。注册表本来就按行存，逐行删既能复用
   *  后端的「当前模型被删就切换」规则，也能在半途失败时说清删掉几行。 */
  async function removeConn(c: Conn) {
    const rows = c.members.filter((m) => m.registry)
    if (!rows.length) return
    const ok = await confirmDialog({
      title: t('models.deleteConnection'),
      message: t('models.removeConnMessage', { n: rows.length }),
      confirmLabel: t('models.deleteConnection'),
    })
    if (!ok) return
    setBusy(`rmconn:${c.id}`)
    let done = 0
    let switched = ''
    try {
      for (const m of rows) {
        const r = await api.removeModel(m.entry.alias, refOf(m.entry))
        done++
        if (r.switched_to) switched = r.switched_to
      }
      toast(
        switched
          ? t('models.removedConnSwitched', { n: done, next: switched })
          : t('models.removedConn', { n: done }),
        'success',
      )
      afterWrite()
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      toastError(done > 0 ? t('models.removeConnPartial', { n: done, error: msg }) : msg)
    } finally {
      setBusy('')
    }
  }

  /** 卡片的「编辑」落在连接上：拿这条账号里第一行注册过的模型开表，表里
   *  既能改端点和密钥，也能在「更多模型」里继续给这个账号加型号。 */
  function editConn(c: Conn) {
    const e = c.members.find((m) => m.registry)?.entry ?? c.members[0]?.entry
    if (e) setSheet({ kind: 'edit', entry: e, conn: c })
  }

  return (
    <div class="models-section">
      {/* One tool row above the wall: the registry's size on the left, the one
          bulk action on the right. The collectors' names are a tooltip here, not
          a permanent caption — they belong to the sheet that actually scans,
          whose subtitle already explains them. */}
      <div class="model-toolbar">
        <span class="model-toolbar-meta">
          {t('models.registryMeta', { c: conns.length, m: registry.length })}
        </span>
        <button
          type="button"
          class="btn small model-discover tip-wrap tip-right tip-down"
          data-tip={t('models.discover.triggerHint')}
          aria-label={`${t('models.discover.trigger')} · ${t('models.discover.triggerHint')}`}
          onClick={() => setDiscovering(true)}
        >
          <Icon name="orbit" size={14} />
          <span>{t('models.discover.trigger')}</span>
          <Icon name="chevron-right" size={13} class="model-discover-arrow" />
        </button>
      </div>

      <div class="model-grid">
        {/* The first cell opens a *connection*: an endpoint and the key it
            dials with. An existing card edits/removes that account from its
            own footer, and one more model id is added from the edit sheet.
            Same square as the rest, so the wall stays a wall. */}
        <button
          type="button"
          class="model-tile model-add"
          aria-label={t('models.addConnection')}
          onClick={() => setSheet({ kind: 'add' })}
        >
          <span class="model-add-plus" aria-hidden="true">
            <Icon name="plus" size={26} />
          </span>
          <span class="model-add-label">{t('models.addConnection')}</span>
          <span class="model-add-sub">{t('models.addSub')}</span>
        </button>
        {conns.map((c) => (
          <ConnectionTile
            key={c.id}
            conn={c}
            busy={busy}
            results={results}
            providers={data.providers}
            onUse={(e) => void switchTo(e)}
            onTest={(e) => void test(e)}
            onEdit={(e) => setSheet({ kind: 'edit', entry: e })}
            onCopy={(m) => void copy(m)}
            onRemove={(m) => void remove(m)}
            onEditConn={editConn}
            onRemoveConn={(c) => void removeConn(c)}
          />
        ))}
      </div>

      {error && <p class="gate-error">{error}</p>}

      {discovering && (
        <DiscoverSheet
          onClose={() => setDiscovering(false)}
          onDone={(n, skipped) => {
            setDiscovering(false)
            toast(t('models.discover.imported', { n }), 'success')
            // A row the endpoint refused is the one thing this sheet must not
            // report as success: it was found in a config, not proven to work.
            if (skipped?.length) {
              const reason = skipped[0]!.length > 120 ? `${skipped[0]!.slice(0, 120)}…` : skipped[0]!
              toastError(t('models.discover.skipped', { n: skipped.length, reason }))
            }
            afterWrite()
          }}
        />
      )}

      {sheet && (
        <ModelSheet
          key={sheetKey(sheet)}
          target={sheet}
          providers={data.providers}
          active={active}
          onClose={() => {
            // A save that landed the edit but failed partway through the
            // appended rows already changed the store; the cards behind the
            // sheet must not keep showing the old arrangement.
            setSheet(null)
            reload()
          }}
          onDone={(r) => {
            setSheet(null)
            if (r.edited) {
              // The edit always lands first; rows appended to the connection
              // are reported in the same sentence rather than a second toast
              // the user has to catch.
              toast(
                r.added ? t('models.savedAndAdded', { n: r.added }) : t('models.saved'),
                'success',
              )
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

/** One card per connection: an endpoint plus the credential it dials with.
 *
 *  The card is the account, because that is what a person recognises and what
 *  the registry actually stores — one row per model, each carrying its own copy
 *  of the key, grouped by (provider, endpoint, key). Painting one card per row
 *  hid that: three models on one relay became three squares that each restated
 *  the same account and said nothing about each other.
 *
 *  The face is the account and nothing else. On hover (or keyboard focus) the
 *  face steps aside and the card shows what is behind it: the models that key
 *  can ask for, each one a click away from being live. The mark goes with the
 *  face — a preview crowded with a logo has less room for the thing being
 *  previewed. A card that carried its list at rest would be as tall as its
 *  account is wide, and a nine-model account would set the height of the wall.
 */
function ConnectionTile(props: {
  conn: Conn
  busy: string
  results: Record<string, TestResult>
  providers: ProviderInfo[]
  onUse(e: ModelEntry): void
  onTest(e: ModelEntry): void
  onEdit(e: ModelEntry): void
  onCopy(m: Member): void
  onRemove(m: Member): void
  onEditConn(conn: Conn): void
  onRemoveConn(conn: Conn): void
}) {
  const c = props.conn
  const name = c.providerLabel || labelOf(props.providers, c.provider) || t('models.customProvider')
  const host = hostOf(c.baseURL)
  // The card is ringed when one of its models is the live one: the answer to
  // "where is the request going" has to be visible before opening anything.
  const live = c.members.some((m) => m.entry.active)
  return (
    <div class={`model-conn${live ? ' live' : ''}`} tabindex={0}>
      <span class="model-conn-face">
        <span class="model-conn-logo">
          <ProviderLogo id={c.provider || c.apiType} size={56} />
        </span>
        <span class="model-conn-name" title={name}>
          {name}
        </span>
        {host && (
          <span class="model-conn-host mono" title={c.baseURL}>
            {host}
          </span>
        )}
        <span class="model-conn-count">{t('models.modelCount', { n: c.members.length })}</span>
        {live && <span class="model-conn-live">{t('models.inUse')}</span>}
      </span>

      <div class="model-conn-models">
        {c.members.map((m) => (
          <ModelRow
            key={keyOf(m.entry)}
            m={m}
            busy={props.busy}
            result={props.results[keyOf(m.entry)]}
            providers={props.providers}
            onUse={() => props.onUse(m.entry)}
            onTest={() => props.onTest(m.entry)}
            onEdit={() => props.onEdit(m.entry)}
            onCopy={() => props.onCopy(m)}
            onRemove={() => props.onRemove(m)}
          />
        ))}
        {/* Account-level verbs, kept as two plain rows under the model list.
            Adding another id lives in the edit sheet's "more models" table, so
            the card itself only offers the two things that address the card. */}
        <div class="model-conn-ops">
          <button type="button" class="model-conn-op" onClick={() => props.onEditConn(c)}>
            <Icon name="pencil" size={13} />
            <span>{t('models.edit')}</span>
            <Icon name="chevron-right" size={12} class="model-conn-op-arrow" />
          </button>
          <button
            type="button"
            class="model-conn-op danger"
            onClick={() => props.onRemoveConn(c)}
          >
            <Icon name="trash" size={13} />
            <span>{t('models.deleteConnection')}</span>
          </button>
        </div>
      </div>
    </div>
  )
}

/** One model inside a connection card.
 *
 *  The radio is the switch: clicking the row makes that model live, which is
 *  what this page is for. The alias is a name a person typed and can retype;
 *  the model id is what the request carries, so when the two differ both are
 *  printed — an account serving three ids under human names is exactly the case
 *  this page exists for. A test verdict rides on the same line, because it has
 *  to survive the pointer staying on the row that started the test.
 *
 *  Four verbs on a 220px row would squeeze every name, so they take the right
 *  edge on hover and the name gives up just that much room — the row never
 *  turns into four anonymous circles.
 */
function ModelRow(props: {
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
  // A row that signs in with a subscription token (the Codex login) has no
  // api_key on purpose: the dot is for "cannot dial at all", not "no secret".
  const keyOk = entry.no_auth || entry.key_set || !!entry.auth
  const testing = props.busy === `test:${k}`
  const verdict = props.result ? (props.result.ok ? 'ok' : 'bad') : ''
  const dot = verdict || testing ? '' : entry.warning_code || !keyOk ? 'warn' : ''
  const dotTitle = entry.warning_code
    ? t('models.warn.endpointOwnerShort', {
        owner: labelOf(props.providers, entry.warning_arg),
      })
    : t('models.keyMissing')
  const title = [entry.alias, entry.model, hostOf(entry.base_url), limitsText(entry)]
    .filter(Boolean)
    .join(' · ')
  const showId = !!entry.model && entry.model !== entry.alias

  return (
    <div class={`model-item${entry.active ? ' active' : ''}${confirming ? ' confirming' : ''}`}>
      <button
        type="button"
        class="model-item-use"
        title={title}
        disabled={props.busy !== '' || entry.active}
        onClick={props.onUse}
      >
        <span class="model-item-mark" aria-hidden="true" />
        <span class="model-item-text">
          <span class="model-item-name">{entry.alias}</span>
          {(showId || testing || props.result) && (
            <span class="model-item-sub">
              {showId && <span class="model-item-id mono">{entry.model}</span>}
              {showId && (testing || props.result) && <span class="model-item-sep">·</span>}
              {(testing || props.result) && (
                <span
                  class={`model-item-result ${testing ? 'testing' : verdict}`}
                  role="status"
                  title={
                    !testing && props.result && !props.result.ok
                      ? `${t('models.testFailed')} · ${props.result.detail}`
                      : undefined
                  }
                >
                  <Icon
                    name={testing ? 'refresh' : verdict === 'ok' ? 'check' : 'alert'}
                    size={11}
                    class={testing ? 'model-tile-result-spin' : undefined}
                  />
                  <span>
                    {testing
                      ? t('models.testing')
                      : props.result!.ok
                        ? fmtMs(props.result!.ms)
                        : t('models.testFailed')}
                  </span>
                </span>
              )}
            </span>
          )}
        </span>
        {entry.active && <span class="model-item-tag">{t('models.inUse')}</span>}
        {!props.m.registry && <span class="model-item-note">{t('models.notRegistered')}</span>}
        {dot && (
          <span class={`model-item-dot ${dot}`} title={dotTitle} role="img" aria-label={dotTitle} />
        )}
      </button>

      <div class="model-item-acts">
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
              <Icon name="check" size={13} />
            </button>
            <button
              type="button"
              class="model-tile-act"
              title={t('common.cancel')}
              aria-label={t('common.cancel')}
              onClick={() => setConfirming(false)}
            >
              <Icon name="x" size={13} />
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
              <Icon name={testing ? 'refresh' : 'activity'} size={13} />
            </button>
            <button
              type="button"
              class="model-tile-act"
              title={t('models.edit')}
              aria-label={t('models.edit')}
              disabled={props.busy !== ''}
              onClick={props.onEdit}
            >
              <Icon name="pencil" size={13} />
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
                <Icon name="copy" size={13} />
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
                <Icon name="trash" size={13} />
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
 *  models to it), or one registered row. An edit opened from the card carries
 *  the connection so the sheet can call itself "edit connection" and show the
 *  account it belongs to rather than pretending to edit a single model row. */
type SheetTarget =
  | { kind: 'add'; conn?: Conn }
  | { kind: 'edit'; entry: ModelEntry; conn?: Conn }

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
  const connEditing = props.target.kind === 'edit' ? props.target.conn : null
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
  /** Rows to register on this connection *while* editing it. Editing is a
   *  one-row form by design, but the second thing anyone does to a working
   *  relay is "same endpoint, same key, one more id" — and that used to mean
   *  closing the sheet and walking the add flow from the top. These rows are
   *  the add sheet's own table, appended below the form. */
  const [extra, setExtra] = useState<Draft[]>([])
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
    // Drafts appended to the previous connection belong to its endpoint and
    // key; carrying them onto the new vendor would POST model ids nobody
    // asked for here.
    setExtra([])
  }

  function patch(id: number, p: Partial<Draft>) {
    setDrafts((cur) => cur.map((x) => (x.id === id ? { ...x, ...p } : x)))
  }

  function patchExtra(id: number, p: Partial<Draft>) {
    setExtra((cur) => cur.map((x) => (x.id === id ? { ...x, ...p } : x)))
  }

  /** Append a row and bring it into view. The sheet is already scrolled to the
   *  form's bottom, so a row added under the fold would otherwise arrive
   *  invisible — the click would look like it did nothing. */
  function addExtraRow() {
    setExtra((cur) => [...cur, newDraft()])
    requestAnimationFrame(() => {
      const pane = box.current?.querySelector<HTMLElement>('.wizard-pane')
      if (pane) pane.scrollTop = pane.scrollHeight
    })
  }

  function requestFor(draft: Draft, borrowFrom?: string): AddModelRequest {
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
    if (!apiKey.trim() && vendorSame) {
      if (pre?.keySet) req.reuse_from = pre.members[0]?.entry.alias
      // A row appended in the edit sheet has no key of its own either: the row
      // being edited holds it, and borrowing by alias is what makes "one more
      // model on this account" a one-field job. Only a connection that has a
      // key can lend one, and the alias named here is the one in effect after
      // this save — a rename in the same submit lands before the POSTs.
      else if (editing && editing.key_set) req.reuse_from = borrowFrom ?? editing.alias
    }
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
    // The drafts behind the appended rows. The requests are built after the
    // PUT, because the alias in effect then is what a borrowed key is keyed
    // by; this pass only validates the overrides before anything is written.
    let picked: Draft[] = []
    try {
      params = parseJSON(row.params)
      headers = parseJSON(row.headers) as Record<string, string> | undefined
      picked = extra.filter((x) => x.picked && x.model.trim() !== '')
      picked.forEach((x) => requestFor(x))
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
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy('')
      return
    }
    // The edit is committed; the appended rows are a second write. A failure
    // there must not be reported as "nothing was saved" — say how many landed.
    // The alias in effect after the edit is what a borrowed key is keyed by:
    // a rename in this submit lands before these POSTs.
    const effectiveAlias = row.alias.trim() || src.alias
    let created = 0
    try {
      for (const r of picked) {
        await api.addModel(requestFor(r, effectiveAlias))
        created++
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      // Drop the rows that landed from the drafts: a retry would POST them
      // again, and the backend allocates a free alias instead of refusing a
      // duplicate model id.
      if (created > 0) {
        const done = new Set(picked.slice(0, created))
        setExtra((cur) => cur.filter((x) => !done.has(x)))
      }
      setError(created > 0 ? t('models.addPartial', { n: created, error: msg }) : msg)
      setBusy('')
      return
    }
    props.onDone({ edited: true, added: created })
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
  /** Appended rows that actually carry a model id — the count the button and
   *  the toast both speak in. The tick is honoured here too: unticking a row
   *  means "not this one", which is the only reason the box exists. */
  const extraCount = extra.filter((x) => x.picked && x.model.trim() !== '').length
  const baseReady = provider !== null && (provider.id !== 'custom' || currentBase() !== '')
  const canSubmit = provider !== null && baseReady && keyReady && (editing ? drafts[0]!.model.trim() !== '' : pickedCount > 0)

  const title = editing
    ? connEditing
      ? t('models.editConnection')
      : t('models.editModel')
    : pre
      ? t('models.addModelTitle')
      : t('models.addConnection')
  const subtitle = editing
    ? connEditing
      ? `${connEditing.providerLabel || t('models.customProvider')} · ${connEditing.baseURL || t('models.customEndpoint')}`
      : t('models.editModelSub')
    : pre
      ? `${pre.providerLabel || t('models.customProvider')} · ${pre.baseURL || t('models.customEndpoint')}`
      : t('models.pickProviderShort')

  const keyPlaceholder = editing
    ? editing.auth
      ? t('models.authLogin')
      : keyOnFile
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

                  {/* The add sheet's table, appended to the edit form: same
                      endpoint, same key, one more id is the second thing
                      anyone does to a relay that works, and it used to mean
                      closing this sheet and walking the add flow from the
                      top. Rows here are POSTs on save; the row above is the
                      PUT and keeps its alias. */}
                  <div class="models-table-head">
                    <div class="section-title">{t('models.moreOnConn')}</div>
                    <div class="models-table-actions">
                      <button type="button" class="btn small" onClick={addExtraRow}>
                        <Icon name="plus" size={14} />
                        {t('models.manualAdd')}
                      </button>
                    </div>
                  </div>
                  {extra.length === 0 ? (
                    <p class="wizard-help">{t('models.moreOnConnHelp')}</p>
                  ) : (
                    <div class="models-table">
                      {extra.length > 1 && <DraftHead />}
                      {extra.map((draft) => (
                        <DraftRow
                          key={draft.id}
                          d={draft}
                          remote={remote}
                          bare={extra.length > 1}
                          onPatch={(p) => patchExtra(draft.id, p)}
                          onDrop={() => setExtra((cur) => cur.filter((x) => x.id !== draft.id))}
                        />
                      ))}
                    </div>
                  )}
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
                    {drafts.length > 1 && <DraftHead />}
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
                  ? extraCount > 0
                    ? t('models.saveAndAdd', { n: extraCount })
                    : t('common.save')
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

/** Column names for a multi-row table. Same skeleton as a row — same flex
 *  weights, same gaps — so the labels land over the fields they name. One
 *  header above the table beats a header per row: five fetched models were
 *  five copies of the same four labels. */
function DraftHead() {
  return (
    <div class="draft-head">
      <span class="draft-head-check" />
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
