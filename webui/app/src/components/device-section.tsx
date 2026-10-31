// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { api, type NodesAddResult, type PeerEntry, type PeersView } from '../api/client'
import { useAsync } from '../hooks'
import { t } from '../i18n'
import { isPeerAddr } from '../utils/peer-address'
import { Icon } from './icons'
import { CardEditor } from './card-editor'
import { confirmDialog } from './confirm'
import { toast, toastError } from './toast'

/** The dial-list half of the connect card: add an address, see what is
 *  already configured, and rename or remove an entry in place. It replaces
 *  the old one-shot form whose only output was a join guide — managing the
 *  list afterwards meant editing config.yaml by hand. */
export function DeviceSection({ onChanged }: { onChanged(): void }) {
  const [tick, setTick] = useState(0)
  const { data, error, errorStatus } = useAsync<PeersView>(() => api.peers(), [], tick)
  // A panel older than this bundle has no /api/peers at all. Saying "failed to
  // load" invites a retry loop that cannot succeed; the honest answer is that
  // the console and the daemon are different versions, and the add form must
  // not be offered either (it posts to the same missing surface).
  const apiMissing = errorStatus === 404
  const [editing, setEditing] = useState<string | null>(null)
  const [removing, setRemoving] = useState('')
  // The raw capability card used to live on the nodes page, which this
  // console replaced. Without a mount point there was no web path left to
  // add an agent/native/manual ability or edit a hand-written card.
  const [showCard, setShowCard] = useState(false)
  const [result, setResult] = useState<NodesAddResult | null>(null)

  const peers = data?.peers ?? []

  function reload() {
    setTick((v) => v + 1)
    onChanged()
  }

  async function remove(peer: PeerEntry) {
    if (removing) return
    setResult(null)
    const ok = await confirmDialog({
      title: t('nodes.addDevice.removeTitle'),
      message: t('nodes.addDevice.removeConfirm', { addr: peer.name || peer.addr }),
      confirmLabel: t('nodes.addDevice.remove'),
    })
    if (!ok) return
    setRemoving(peer.addr)
    try {
      await api.removePeer(peer.addr)
      if (editing === peer.addr) setEditing(null)
      toast(t('nodes.addDevice.removed', { addr: peer.addr }), 'success')
      reload()
    } catch (e) {
      toastError(e)
    } finally {
      setRemoving('')
    }
  }

  return (
    <div class="connect-pane device-pane">
      {!apiMissing && (
      <AddDeviceForm
        hasSecret={data?.has_secret ?? true}
        onAdded={(res) => {
          setResult(res)
          reload()
        }}
      />
      )}

      {result && <AddResult result={result} onClose={() => setResult(null)} />}

      <div class="device-card-toggle">
        <button class="btn small ghost" type="button" onClick={() => setShowCard((v) => !v)}>
          {t('card.title')}
        </button>
      </div>
      {showCard && <CardEditor onChanged={onChanged} />}

      <section class="device-list" aria-labelledby="device-list-title">
        <div class="device-list-head">
          <h4 class="device-list-title" id="device-list-title">
            {t('nodes.addDevice.listTitle')}
          </h4>
          <span class="block-count">{peers.length}</span>
        </div>

        {apiMissing ? (
          <p class="dim device-list-state">{t('nodes.apiMissing')}</p>
        ) : error ? (
          <div class="device-list-state">
            <p class="node-remove-error">{t('nodes.addDevice.listError')}</p>
            <button class="btn small ghost" type="button" onClick={() => setTick((v) => v + 1)}>
              {t('common.retry')}
            </button>
          </div>
        ) : data === null ? (
          <p class="dim device-list-state">
            <span class="spinner spinner-inline" aria-hidden="true" />
            {t('common.loading')}
          </p>
        ) : peers.length === 0 ? (
          <p class="dim device-list-state">{t('nodes.addDevice.listEmpty')}</p>
        ) : (
          <div class="device-rows">
            {peers.map((peer) =>
              editing === peer.addr ? (
                <EditPeerForm
                  key={peer.addr}
                  peer={peer}
                  onCancel={() => setEditing(null)}
                  onSaved={(addr, res) => {
                    setEditing(null)
                    // Report what the server actually did. `updated` is false
                    // when the edit changed nothing, and a refused one (404 on
                    // a stale row, 409 on a rename onto another peer) throws
                    // before this runs — neither is "updated".
                    if (res.updated) toast(t('nodes.addDevice.updated', { addr }), 'success')
                    reload()
                  }}
                />
              ) : (
                <PeerRow
                  key={peer.addr}
                  peer={peer}
                  busy={removing === peer.addr}
                  onEdit={() => {
                    setResult(null)
                    setEditing(peer.addr)
                  }}
                  onRemove={() => remove(peer)}
                />
              ),
            )}
          </div>
        )}
      </section>
    </div>
  )
}

function AddDeviceForm({
  hasSecret,
  onAdded,
}: {
  hasSecret: boolean
  onAdded(res: NodesAddResult): void
}) {
  const [addr, setAddr] = useState('')
  const [trust, setTrust] = useState('mesh')
  const [busy, setBusy] = useState(false)
  const [fieldErr, setFieldErr] = useState('')

  async function submit(e: Event) {
    e.preventDefault()
    if (busy) return
    const a = addr.trim()
    if (!isPeerAddr(a)) {
      setFieldErr(t('nodes.addDevice.badAddr'))
      return
    }
    setFieldErr('')
    setBusy(true)
    try {
      const res = await api.addNode(a, trust)
      setAddr('')
      setTrust('mesh')
      onAdded(res)
    } catch (err) {
      // Keep the address in the field: a typo or a transient failure should
      // not make the operator retype a 20-character host:port.
      setFieldErr(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form class="device-form" onSubmit={submit} noValidate>
      <p class="connect-pane-lead">{t('nodes.addDevice.hint')}</p>
      <label class="device-field">
        <span class="device-field-label">{t('nodes.addDevice.address')}</span>
        <input
          class="input"
          type="text"
          inputMode="url"
          spellcheck={false}
          autoComplete="off"
          value={addr}
          placeholder={t('nodes.addDevice.placeholder')}
          aria-invalid={fieldErr !== ''}
          onInput={(e) => {
            setAddr((e.target as HTMLInputElement).value)
            if (fieldErr) setFieldErr('')
          }}
        />
      </label>
      <div class="device-form-row">
        <label class="device-field device-field-trust">
          <span class="device-field-label">{t('nodes.trust')}</span>
          <TrustSelect value={trust} onChange={setTrust} />
        </label>
        <button class="btn primary device-add-btn" type="submit" disabled={busy || addr.trim() === ''}>
          {busy ? (
            <span class="spinner spinner-inline" aria-hidden="true" />
          ) : (
            <Icon name="plus" size={13} />
          )}
          {busy ? t('nodes.addDevice.adding') : t('nodes.addDevice.submit')}
        </button>
      </div>
      {fieldErr && <p class="device-error">{fieldErr}</p>}
      {!hasSecret && <p class="device-hint">{t('nodes.addDevice.secretNeeded')}</p>}
    </form>
  )
}

function TrustSelect({ value, onChange }: { value: string; onChange(v: string): void }) {
  return (
    <span class="device-select">
      <select
        class={`trust-sel ${value === 'open' ? 'open' : ''}`}
        value={value}
        aria-label={t('nodes.trust')}
        onChange={(e) => onChange((e.target as HTMLSelectElement).value)}
      >
        <option value="mesh">mesh</option>
        <option value="guarded">guarded</option>
        <option value="open">open</option>
      </select>
      <Icon name="chevron-down" size={12} />
    </span>
  )
}

function AddResult({ result, onClose }: { result: NodesAddResult; onClose(): void }) {
  const headline = result.added
    ? t('nodes.addDevice.added', { addr: result.addr })
    : result.updated
      ? t('nodes.addDevice.updated', { addr: result.addr })
      : t('nodes.addDevice.exists', { addr: result.addr })
  const dialLine = result.dialed
    ? t('nodes.addDevice.dialed', { addr: result.addr })
    : result.dial_error
      ? t('nodes.addDevice.dialFailed', { addr: result.addr })
      : t('nodes.addDevice.saved')

  return (
    <div class="device-result" role="status">
      <div class="device-result-head">
        <span class={`device-result-icon${result.dialed ? ' ok' : ''}`} aria-hidden="true">
          <Icon name={result.dialed ? 'check' : 'plug'} size={14} />
        </span>
        <p class="device-result-title">{headline}</p>
        <button
          class="icon-btn device-result-close"
          type="button"
          aria-label={t('common.close')}
          title={t('common.close')}
          onClick={onClose}
        >
          <Icon name="x" size={12} />
        </button>
      </div>
      <p class={result.dialed ? 'device-result-ok' : 'device-result-sub'}>{dialLine}</p>
      {result.secret_generated && <p class="device-result-sub">{t('nodes.addDevice.secretGen')}</p>}
      {result.cleartext_hint && <p class="device-result-sub">{t('nodes.addDevice.cleartextHint')}</p>}
      {(result.added || result.secret_generated) && (
        <details class="device-guide">
          <summary>{t('nodes.addDevice.guideToggle')}</summary>
          <ol class="join-steps">
            <li>
              <code class="mono">{result.install_command}</code>
            </li>
            {result.invite_steps.slice(1).map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ol>
        </details>
      )}
    </div>
  )
}

function PeerRow({
  peer,
  busy,
  onEdit,
  onRemove,
}: {
  peer: PeerEntry
  busy: boolean
  onEdit(): void
  onRemove(): void
}) {
  const named = peer.name?.trim() ?? ''
  // Without a directory row there is no name to show. Repeating "configured
  // peer" on every row makes the list unreadable; the address is the identity
  // the operator typed, so it becomes the primary line and the secondary
  // line disappears rather than echoing it.
  const label = named || peer.addr
  return (
    <div class={`device-row${peer.online ? ' on' : ''}`}>
      <span class={`node-dot${peer.online ? ' on' : ' off'}`} aria-hidden="true" />
      <div class="device-row-main">
        <span class={`device-row-name${named ? '' : ' mono'}`} title={label}>
          {label}
        </span>
        {named && (
          <span class="device-row-addr mono" title={peer.addr}>
            {peer.addr}
          </span>
        )}
      </div>
      <span class={`trust-pill ${peer.trust || 'mesh'}`}>{peer.trust || 'mesh'}</span>
      <div class="device-row-actions">
        <button
          class="icon-btn"
          type="button"
          aria-label={t('nodes.addDevice.edit')}
          title={t('nodes.addDevice.edit')}
          onClick={onEdit}
        >
          <Icon name="pencil" size={13} />
        </button>
        <button
          class="icon-btn danger"
          type="button"
          disabled={busy}
          aria-label={t('nodes.addDevice.remove')}
          title={t('nodes.addDevice.remove')}
          onClick={onRemove}
        >
          {busy ? (
            <span class="spinner spinner-inline" aria-hidden="true" />
          ) : (
            <Icon name="trash" size={13} />
          )}
        </button>
      </div>
    </div>
  )
}

function EditPeerForm({
  peer,
  onCancel,
  onSaved,
}: {
  peer: PeerEntry
  onCancel(): void
  onSaved(addr: string, res: NodesAddResult): void
}) {
  const [addr, setAddr] = useState(peer.addr)
  const [trust, setTrust] = useState(peer.trust || 'mesh')
  const [busy, setBusy] = useState(false)
  const [fieldErr, setFieldErr] = useState('')

  async function save(e: Event) {
    e.preventDefault()
    if (busy) return
    const a = addr.trim()
    if (!isPeerAddr(a)) {
      setFieldErr(t('nodes.addDevice.badAddr'))
      return
    }
    setFieldErr('')
    setBusy(true)
    try {
      const res = await api.addNode(a, trust, peer.addr)
      onSaved(a, res)
    } catch (err) {
      setFieldErr(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form class="device-edit" onSubmit={save} noValidate>
      <div class="device-edit-head">
        <span class="device-edit-title">{t('nodes.addDevice.editTitle')}</span>
        <button
          class="icon-btn"
          type="button"
          aria-label={t('nodes.addDevice.cancel')}
          title={t('nodes.addDevice.cancel')}
          onClick={onCancel}
        >
          <Icon name="x" size={12} />
        </button>
      </div>
      <input
        class="input"
        type="text"
        inputMode="url"
        spellcheck={false}
        autoComplete="off"
        value={addr}
        aria-label={t('nodes.addDevice.address')}
        aria-invalid={fieldErr !== ''}
        onInput={(e) => {
          setAddr((e.target as HTMLInputElement).value)
          if (fieldErr) setFieldErr('')
        }}
      />
      <div class="device-edit-row">
        <TrustSelect value={trust} onChange={setTrust} />
        <button class="btn small ghost" type="button" onClick={onCancel}>
          {t('nodes.addDevice.cancel')}
        </button>
        <button class="btn small primary" type="submit" disabled={busy || addr.trim() === ''}>
          {busy ? t('common.loading') : t('nodes.addDevice.save')}
        </button>
      </div>
      {fieldErr && <p class="device-error">{fieldErr}</p>}
    </form>
  )
}
