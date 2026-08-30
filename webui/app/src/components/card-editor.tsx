import { useEffect, useState } from 'preact/hooks'
import {
  api,
  type CardAgent,
  type CardFile,
  type CardNative,
  type NodesAddResult,
} from '../api/client'
import { t } from '../i18n'
import { toast, toastError } from './toast'

/** — Capability card editor (stage 6): the web twin of `/card`. One card,
 *  three sections (agents / native / manual), each entry editable in place —
 *  add/remove for every kind, a tier toggle for agents and native abilities.
 *  Every write goes through the same cardmut pipeline the CLI uses
 *  (validate → .bak → install), then hot-reloads the engine's card, so an
 *  edit is in force for the next routed task with no restart. A raw YAML
 *  editor sits behind a toggle for everything the form does not model. */
export function CardEditor({ onChanged }: { onChanged(): void }) {
  const [tick, setTick] = useState(0)
  const { data: card, error, loading } = useCard(tick)

  if (error) {
    // 404 = no card yet (fresh machine): say how to make one instead of an error wall.
    return (
      <div class="card">
        <h2 class="block-title">{t('card.title')}</h2>
        <p class="dim">{error.status === 404 ? t('card.none') : error.message}</p>
      </div>
    )
  }

  return (
    <div class="card card-editor">
      <h2 class="block-title">{t('card.title')}</h2>
      <p class="hint">{t('card.hint', { path: card?.path ?? '' })}</p>

      {!card || loading ? (
        <p class="dim">
          <span class="spinner spinner-inline" aria-hidden="true" />
          {t('common.loading')}
        </p>
      ) : (
        <>
          <CardAgentsSection card={card} reload={() => setTick((v) => v + 1)} onChanged={onChanged} />
          <CardNativeSection card={card} reload={() => setTick((v) => v + 1)} onChanged={onChanged} />
          <CardManualSection card={card} reload={() => setTick((v) => v + 1)} onChanged={onChanged} />
          <CardRawSection card={card} reload={() => setTick((v) => v + 1)} onChanged={onChanged} />
        </>
      )}
    </div>
  )
}

/** Small local re-fetch hook: the editor re-reads /api/card after every
 *  successful mutation so the form reflects the file on disk (with its
 *  server-side normalisation), not an optimistic local guess. */
function useCard(tick: number): {
  data: CardFile | null
  error: { status: number; message: string } | null
  loading: boolean
} {
  const [data, setData] = useState<CardFile | null>(null)
  const [error, setError] = useState<{ status: number; message: string } | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    setLoading(true)
    api
      .card()
      .then((c) => {
        if (!alive) return
        setData(c)
        setError(null)
      })
      .catch((e) => {
        if (!alive) return
        setError({ status: e?.status ?? 0, message: e instanceof Error ? e.message : String(e) })
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [tick])

  return { data, error, loading }
}

// ---- Agents ----

function CardAgentsSection({
  card,
  reload,
  onChanged,
}: {
  card: CardFile
  reload(): void
  onChanged(): void
}) {
  const agents = Object.entries(card.card.agents ?? {})
  const [name, setName] = useState('')
  const [adapter, setAdapter] = useState('')

  async function add() {
    const n = name.trim()
    if (!n || !adapter.trim()) {
      toast(t('card.agent.missing'), 'error')
      return
    }
    try {
      const res = await api.addCardAgent(n, { adapter: adapter.trim(), tier: 2 })
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      setName('')
      setAdapter('')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <div class="card-section">
      <h3 class="section-title">{t('card.agents.title')}</h3>
      {agents.length === 0 && <p class="dim">{t('card.agents.empty')}</p>}
      {agents.map(([agentName, ag]) => (
        <AgentRow key={agentName} name={agentName} agent={ag} reload={reload} onChanged={onChanged} />
      ))}
      <div class="card-add-row">
        <input
          class="input"
          placeholder={t('card.agent.name')}
          value={name}
          onInput={(e) => setName((e.target as HTMLInputElement).value)}
        />
        <input
          class="input"
          placeholder={t('card.agent.adapter')}
          value={adapter}
          onInput={(e) => setAdapter((e.target as HTMLInputElement).value)}
        />
        <button class="btn small" type="button" onClick={add}>
          {t('card.agent.add')}
        </button>
      </div>
    </div>
  )
}

function AgentRow({
  name,
  agent,
  reload,
  onChanged,
}: {
  name: string
  agent: CardAgent
  reload(): void
  onChanged(): void
}) {
  const [busy, setBusy] = useState(false)

  async function toggleTier() {
    if (busy) return
    setBusy(true)
    try {
      const next = agent.tier === 2 ? 1 : 2
      const res = await api.patchCardAgent(name, { tier: next })
      toast(
        (next === 2 ? t('card.agent.tierNow2') : t('card.agent.tierNow1')) +
          (res.live ? '' : ' ' + t('card.saved.restartSuffix')),
        'success',
      )
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (busy) return
    if (!window.confirm(t('card.agent.removeConfirm', { name }))) return
    setBusy(true)
    try {
      const res = await api.removeCardAgent(name)
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card-entry">
      <div class="card-entry-head">
        <span class="mono card-entry-name">{name}</span>
        <span class="badge dim">{agent.cost_tier || ''}</span>
        <button
          class={`btn small ${agent.tier === 2 ? 'warn' : ''}`}
          type="button"
          disabled={busy}
          title={t('card.agent.tierHint')}
          onClick={toggleTier}
        >
          tier {agent.tier}
        </button>
        <button class="btn small danger" type="button" disabled={busy} onClick={remove}>
          {t('card.remove')}
        </button>
      </div>
      {agent.capabilities && agent.capabilities.length > 0 && (
        <p class="dim">{agent.capabilities.join(' · ')}</p>
      )}
    </div>
  )
}

// ---- Native ----

function CardNativeSection({
  card,
  reload,
  onChanged,
}: {
  card: CardFile
  reload(): void
  onChanged(): void
}) {
  const natives: CardNative[] = card.card.native ?? []
  const [id, setId] = useState('')
  const [command, setCommand] = useState('')

  async function add() {
    const i = id.trim()
    if (!i || !command.trim()) {
      toast(t('card.native.missing'), 'error')
      return
    }
    try {
      const res = await api.addNativeAbility({ id: i, command: command.trim(), tier: 1 })
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      setId('')
      setCommand('')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <div class="card-section">
      <h3 class="section-title">{t('card.native.title')}</h3>
      {natives.length === 0 && <p class="dim">{t('card.native.empty')}</p>}
      {natives.map((ab) => (
        <div class="card-entry" key={ab.id}>
          <div class="card-entry-head">
            <span class="mono card-entry-name">{ab.id}</span>
            <span class="badge dim">tier {ab.tier}</span>
            <NativeRemove id={ab.id} reload={reload} onChanged={onChanged} />
          </div>
          <p class="dim mono">{ab.command}{ab.args?.length ? ' ' + ab.args.join(' ') : ''}</p>
        </div>
      ))}
      <div class="card-add-row">
        <input
          class="input"
          placeholder={t('card.native.id')}
          value={id}
          onInput={(e) => setId((e.target as HTMLInputElement).value)}
        />
        <input
          class="input"
          placeholder={t('card.native.command')}
          value={command}
          onInput={(e) => setCommand((e.target as HTMLInputElement).value)}
        />
        <button class="btn small" type="button" onClick={add}>
          {t('card.native.add')}
        </button>
      </div>
    </div>
  )
}

function NativeRemove({ id, reload, onChanged }: { id: string; reload(): void; onChanged(): void }) {
  const [busy, setBusy] = useState(false)
  async function remove() {
    if (busy) return
    if (!window.confirm(t('card.native.removeConfirm', { id }))) return
    setBusy(true)
    try {
      const res = await api.removeNativeAbility(id)
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <button class="btn small danger" type="button" disabled={busy} onClick={remove}>
      {t('card.remove')}
    </button>
  )
}

// ---- Manual ----

function CardManualSection({
  card,
  reload,
  onChanged,
}: {
  card: CardFile
  reload(): void
  onChanged(): void
}) {
  const manual = card.card.manual ?? []
  const [id, setId] = useState('')
  const [notify, setNotify] = useState('')

  async function add() {
    const i = id.trim()
    if (!i || !notify.trim()) {
      toast(t('card.manual.missing'), 'error')
      return
    }
    try {
      const res = await api.addManualAbility({ id: i, notify: notify.trim() })
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      setId('')
      setNotify('')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    }
  }

  async function remove(mid: string) {
    if (!window.confirm(t('card.manual.removeConfirm', { id: mid }))) return
    try {
      const res = await api.removeManualAbility(mid)
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      reload()
      onChanged()
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <div class="card-section">
      <h3 class="section-title">{t('card.manual.title')}</h3>
      {manual.length === 0 && <p class="dim">{t('card.manual.empty')}</p>}
      {manual.map((m) => (
        <div class="card-entry" key={m.id}>
          <div class="card-entry-head">
            <span class="mono card-entry-name">{m.id}</span>
            <span class="dim">{m.notify}</span>
            <button class="btn small danger" type="button" onClick={() => remove(m.id)}>
              {t('card.remove')}
            </button>
          </div>
        </div>
      ))}
      <div class="card-add-row">
        <input
          class="input"
          placeholder={t('card.manual.id')}
          value={id}
          onInput={(e) => setId((e.target as HTMLInputElement).value)}
        />
        <input
          class="input"
          placeholder={t('card.manual.notify')}
          value={notify}
          onInput={(e) => setNotify((e.target as HTMLInputElement).value)}
        />
        <button class="btn small" type="button" onClick={add}>
          {t('card.manual.add')}
        </button>
      </div>
    </div>
  )
}

// ---- Raw YAML editor ----

function CardRawSection({
  card,
  reload,
  onChanged,
}: {
  card: CardFile
  reload(): void
  onChanged(): void
}) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState(card.raw)
  const [busy, setBusy] = useState(false)

  // After save + reload the server's raw is authoritative (it round-tripped
  // the yaml.Node tree): resync the textarea so the editor shows exactly
  // what landed on disk.
  useEffect(() => {
    setText(card.raw)
  }, [card.raw])

  async function save() {
    if (busy) return
    setBusy(true)
    try {
      const res = await api.putCardRaw(text)
      toast(res.live ? t('card.saved.live') : t('card.saved.restart'), 'success')
      reload()
      onChanged()
    } catch (e) {
      toastError(e) // server message carries the LoadCard validation error
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card-section">
      <button class="btn small card-raw-toggle" type="button" onClick={() => setOpen(!open)}>
        {open ? t('card.raw.hide') : t('card.raw.edit')}
      </button>
      {open && (
        <div class="card-raw-editor">
          <textarea
            class="input mono"
            rows={16}
            spellcheck={false}
            value={text}
            onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
          />
          <p class="hint">{t('card.raw.hint')}</p>
          <button class="btn small" type="button" disabled={busy} onClick={save}>
            {busy ? t('common.loading') : t('card.raw.save')}
          </button>
        </div>
      )}
    </div>
  )
}

/** — Add device (stage 6): the join-a-second-machine form, the web twin of
 *  `panda nodes add`. Submits the address, then renders the same join guide
 *  the CLI prints — the three steps for whoever sets up the other machine,
 *  never the shared secret itself (only the file it lives in). */
export function AddDeviceCard({ onAdded }: { onAdded(): void }) {
  const [addr, setAddr] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<NodesAddResult | null>(null)

  async function submit(e: Event) {
    e.preventDefault()
    if (busy) return
    const a = addr.trim()
    if (!a) return
    setBusy(true)
    try {
      setResult(await api.addNode(a))
      setAddr('')
      onAdded()
    } catch (err) {
      toastError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card add-device-card">
      <h2 class="block-title">{t('nodes.addDevice.title')}</h2>
      <p class="hint">{t('nodes.addDevice.hint')}</p>
      <form class="card-add-row" onSubmit={submit}>
        <input
          class="input"
          placeholder={t('nodes.addDevice.placeholder')}
          value={addr}
          onInput={(e) => setAddr((e.target as HTMLInputElement).value)}
        />
        <button class="btn" type="submit" disabled={busy || !addr.trim()}>
          {busy ? t('common.loading') : t('nodes.addDevice.submit')}
        </button>
      </form>
      {result && (
        <div class="join-guide">
          <p>
            {result.added ? t('nodes.addDevice.added', { addr: result.addr }) : t('nodes.addDevice.exists', { addr: result.addr })}
            {result.secret_generated && ' · ' + t('nodes.addDevice.secretGen')}
          </p>
          {result.dialed ? (
            <p class="ok">{t('nodes.addDevice.dialed', { addr: result.addr })}</p>
          ) : (
            result.dial_error && <p class="node-remove-error">{t('nodes.addDevice.dialFailed', { addr: result.addr })}</p>
          )}
          <h3 class="section-title">{t('nodes.addDevice.guideTitle')}</h3>
          <ol class="join-steps">
            <li>
              <code class="mono">{result.install_command}</code>
            </li>
            {result.invite_steps.slice(1).map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ol>
        </div>
      )}
    </div>
  )
}
