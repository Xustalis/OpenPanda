// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'preact/hooks'
import {
  api,
  type AgentInfo,
  type NodeInfo,
  type PairView,
  type SelfInfo,
  type Task,
} from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'
import { confirmDialog } from '../components/confirm'
import { AddDeviceCard, CardEditor } from '../components/card-editor'

/** Devices & nodes (C3): which device this console runs on (/api/self),
 *  the capability directory with expandable hardware/resources/agents
 *  cards, the delegation chains derived from the task ledger, and the agent
 *  CLIs this node can drive. */
export function NodesView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: nodes, error } = useAsync(() => api.nodes(), [], change + tick)
  const { data: self } = useAsync(() => api.self(), [], tick)
  const { data: tasks } = useAsync(() => api.tasks(), [], change + tick)
  const { data: agents } = useAsync(() => api.agents(), [], tick)

  if (error)
    return (
      <ErrorState
        title={t('nav.nodes')}
        sub={t('nodes.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  return (
    <section>
      <PageHeader title={t('nav.nodes')} sub={t('nodes.subtitle')} />

      {self && <SelfCard self={self} />}

      {/* Bluetooth-style pairing (`panda pair`'s web twin): discovered
          devices, inbound requests awaiting this operator, and outgoing
          sessions. The manual add form below stays as the SSH fallback. */}
      <PairCard onChanged={() => setTick((v) => v + 1)} />

      {/* Stage 6: the join-a-device form (add a peer to the dial list) and
          the local card editor (/card's web twin) — both live on the fleet
          page because that's where "what can this fleet do" is managed. */}
      <AddDeviceCard onAdded={() => setTick((v) => v + 1)} />
      <CardEditor onChanged={() => setTick((v) => v + 1)} />

      {nodes === null ? (
        <p class="dim">
          <span class="spinner spinner-inline" aria-hidden="true" />
          {t('common.loading')}
        </p>
      ) : nodes.length === 0 ? (
        <div class="card">{t('nodes.empty')}</div>
      ) : (
        <div class="node-grid">
          {nodes.map((n) => (
            <NodeCard
              key={n.id}
              node={n}
              isSelf={isSelfNode(self, n)}
              onRemoved={() => setTick((v) => v + 1)}
            />
          ))}
        </div>
      )}

      {tasks !== null && <DelegationChains tasks={tasks} />}

      {agents !== null && <ControllableAgents agents={agents} />}
    </section>
  )
}

/** Bluetooth-style pairing surface (web twin of `panda pair`): inbound
 *  requests to answer with the code on both screens, LAN-discovered
 *  devices to start a session with, and the outgoing sessions' live
 *  state — waiting → done / refused / expired. Polls faster while any
 *  session is still waiting on the remote human. */
function PairCard({ onChanged }: { onChanged(): void }) {
  const [tick, setTick] = useState(0)
  const { data } = useAsync<PairView>(() => api.pair(), [], tick)
  const [manual, setManual] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const waiting = (data?.outgoing ?? []).some((o) => o.state === 'waiting')
  const pendingReqs = data?.requests ?? []

  // While a session waits on the remote human — or a request sits
  // unanswered — poll so state flips show without a manual refresh.
  useEffect(() => {
    if (!waiting && pendingReqs.length === 0) return
    const id = setInterval(() => setTick((v) => v + 1), 2500)
    return () => clearInterval(id)
  }, [waiting, pendingReqs.length])

  async function start(target: string) {
    if (busy) return
    setErr('')
    setBusy(target)
    try {
      await api.pairInitiate(target)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
      setManual('')
      setTick((v) => v + 1)
    }
  }

  async function answer(id: string, confirm: boolean) {
    if (busy) return
    setErr('')
    setBusy(id)
    try {
      await api.pairAnswer(id, confirm)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
      setTick((v) => v + 1)
      onChanged()
    }
  }

  const discovered = data?.discovered ?? []
  const outgoing = data?.outgoing ?? []

  return (
    <div class="card">
      <h2 class="block-title">{t('nodes.pair.title')}</h2>
      <p class="hint">{t('nodes.pair.hint')}</p>

      {pendingReqs.length > 0 && (
        <div style="margin-bottom: 12px">
          <p class="dim">{t('nodes.pair.requests')}</p>
          {pendingReqs.map((r) => (
            <div class="node-head" key={r.id} style="gap: 8px; align-items:center; margin: 6px 0">
              <span class="node-name">{r.peer_name || r.peer_addr}</span>
              <span class="dim mono">{r.peer_addr}</span>
              <span class="badge">{t('nodes.pair.code')}: {r.code}</span>
              <button class="btn small" disabled={busy !== ''} onClick={() => answer(r.id, true)}>
                {t('nodes.pair.confirm')}
              </button>
              <button class="btn small danger" disabled={busy !== ''} onClick={() => answer(r.id, false)}>
                {t('nodes.pair.reject')}
              </button>
            </div>
          ))}
        </div>
      )}

      {outgoing.length > 0 && (
        <div style="margin-bottom: 12px">
          <p class="dim">{t('nodes.pair.outgoing')}</p>
          {outgoing.map((o) => (
            <div class="node-head" key={o.session} style="gap: 8px; align-items:center; margin: 6px 0">
              <span class="node-name">{o.target || o.addr}</span>
              <span class="dim mono">{o.addr}</span>
              <span class="badge">{t('nodes.pair.code')}: {o.code}</span>
              {o.state === 'waiting' && (
                <span class="dim">
                  <span class="spinner spinner-inline" aria-hidden="true" /> {t('nodes.pair.waiting')}
                </span>
              )}
              {o.state === 'done' && <span class="badge green">{t('nodes.pair.done')}</span>}
              {o.state === 'rejected' && <span class="badge red">{t('nodes.pair.rejected')}</span>}
              {o.state === 'expired' && <span class="badge">{t('nodes.pair.expired')}</span>}
              {o.state === 'error' && (
                <span class="badge red">
                  {t('nodes.pair.error')}: {o.err}
                </span>
              )}
            </div>
          ))}
          {outgoing.some((o) => o.state === 'waiting') && (
            <p class="hint">{t('nodes.pair.compare')}</p>
          )}
        </div>
      )}

      <p class="dim">{t('nodes.pair.discovered')}</p>
      {discovered.length === 0 && outgoing.length === 0 && pendingReqs.length === 0 ? (
        <p class="dim">{t('nodes.pair.empty')}</p>
      ) : (
        discovered.map((d) => (
          <div class="node-head" key={d.id} style="gap: 8px; align-items:center; margin: 6px 0">
            <span class={`dot node-dot`} aria-hidden />
            <span class="node-name">{d.id}</span>
            <span class="dim mono">{d.addr}</span>
            {!d.verified && <span class="badge">{t('nodes.pair.unverified')}</span>}
            <button
              class="btn small"
              disabled={busy !== ''}
              onClick={() => start(d.id)}
            >
              {busy === d.id ? t('common.loading') : t('nodes.pair.start')}
            </button>
          </div>
        ))
      )}

      <div class="node-head" style="gap: 8px; margin-top: 10px">
        <input
          type="text"
          value={manual}
          placeholder={t('nodes.pair.manual')}
          onInput={(e) => setManual((e.target as HTMLInputElement).value)}
          style="max-width: 240px"
        />
        <button
          class="btn small"
          disabled={busy !== '' || manual.trim() === ''}
          onClick={() => start(manual.trim())}
        >
          {t('nodes.pair.start')}
        </button>
      </div>
      {err && <p class="node-remove-error">{err}</p>}
    </div>
  )
}

/** Does the capability card belong to the machine this console runs on? */
function isSelfNode(self: SelfInfo | null, n: NodeInfo): boolean {
  if (!self) return false
  if (self.node && self.node.id === n.id) return true
  if (self.node_id && self.node_id === n.id) return true
  return self.node_name !== '' && (n.name === self.node_name || n.id === self.node_name)
}

/** This machine's device profile — hostname / OS / chip / cores / RAM, plus
 *  the registered node name when the ledger knows it. */
function SelfCard({ self }: { self: SelfInfo }) {
  return (
    <div class="card self-card">
      <div class="node-head">
        <span class="node-name">{self.hostname}</span>
        <span class="badge green">{t('nodes.self')}</span>
        {self.node_name && <span class="badge">{t('nodes.nodeName')}: {self.node_name}</span>}
        {self.node_kind && <span class="badge">{self.node_kind}</span>}
        <span class={`badge ${self.node_running ? 'green' : 'red'}`}>
          {self.node_running ? 'running' : 'not running'}
        </span>
      </div>
      <p class="dim">
        {self.os}/{self.arch}
        {self.chip ? ` · ${self.chip}` : ''} · {t('nodes.cores', { n: self.cpu_cores })}
        {self.ram_gb ? ` · ${self.ram_gb} GB` : ''}
      </p>
    </div>
  )
}

/** One capability card: the summary line, and an expandable breakdown of
 *  hardware capacity, the declared resource profile, and each agent the
 *  node advertises (capabilities / best at / not for). Offline remote
 *  cards carry a remove action: their row is not backed by a live peer
 *  (renamed machine, changed identity, decommissioned node). */
function NodeCard({
  node,
  isSelf,
  onRemoved,
}: {
  node: NodeInfo
  isSelf: boolean
  onRemoved(): void
}) {
  const [open, setOpen] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [removeErr, setRemoveErr] = useState('')
  const agentNames = node.agents ? Object.keys(node.agents) : []
  const displayName = (node.name && node.name !== node.id) ? node.name : node.id
  const canRemove = !isSelf && node.status !== 'online'

  async function remove() {
    if (removing) return
    const ok = await confirmDialog({
      title: t('nodes.removeTitle'),
      message: t('nodes.removeConfirm', { name: displayName }),
      confirmLabel: t('nodes.remove'),
    })
    if (!ok) return
    setRemoving(true)
    setRemoveErr('')
    try {
      await api.removeNode(node.id)
      onRemoved()
    } catch (e) {
      setRemoveErr(e instanceof Error ? e.message : String(e))
      setRemoving(false)
    }
  }

  return (
    <div class="card node-card">
      <div class="node-head" style="width: 100%;">
        <span
          class={`dot node-dot${node.status === 'online' ? '' : ' off'}`}
          aria-hidden
          title={node.status}
        />
        <span class="node-name" title={displayName}>{displayName}</span>
      </div>
      {node.name && node.name !== node.id && (
        <span class="node-id mono" title={node.id}>{node.id}</span>
      )}
      <div class="node-badges">
        <span class={`badge ${node.status === 'online' ? 'green' : ''}`}>{node.status}</span>
        <span class="badge">{node.node_kind}</span>
        <span class={`badge ${node.running ? 'green' : 'red'}`}>{node.running ? 'running' : 'stopped'}</span>
        {isSelf && <span class="badge green">{t('nodes.self')}</span>}
      </div>
      {node.chip && <p class="dim">{node.chip}</p>}
      <p class="dim">
        {t('nodes.lastSeen')}:{' '}
        {node.last_seen === 'never' ? t('nodes.never') : new Date(node.last_seen).toLocaleString()}
      </p>
      {node.abilities.length > 0 && (
        <div class="node-abilities">
          {node.abilities.slice(0, 8).map((a) => (
            <span key={a} class="ability-tag">
              {a}
            </span>
          ))}
          {node.abilities.length > 8 && (
            <span class="ability-tag more">+{node.abilities.length - 8}</span>
          )}
        </div>
      )}

      <div class="node-actions">
        <button class="btn small node-detail-toggle" onClick={() => setOpen(!open)}>
          {open ? t('nodes.detailsHide') : t('nodes.details')}
        </button>
        {canRemove && (
          <button class="btn small danger" type="button" disabled={removing} onClick={remove}>
            {removing ? t('common.loading') : t('nodes.remove')}
          </button>
        )}
      </div>
      {removeErr && <p class="node-remove-error">{removeErr}</p>}

      {open && (
        <div class="node-detail">
          <div class="node-detail-row">
            <span class="node-detail-label">{t('nodes.hardware')}</span>
            <span class="dim">
              {t('nodes.cores', { n: node.capacity.cpu_cores })} · {node.capacity.ram_gb} GB ·{' '}
              {t('nodes.concurrency', {
                cur: node.capacity.current_tasks,
                max: node.capacity.max_concurrent_tasks,
              })}
            </span>
          </div>
          {node.resource_profile && (
            <div class="node-detail-row">
              <span class="node-detail-label">{t('nodes.resources')}</span>
              <span class="dim">
                CPU {node.resource_profile.cpu} · RAM {node.resource_profile.ram_gb} GB
                {node.resource_profile.gpu_vram_gb > 0 &&
                  ` · GPU ${node.resource_profile.gpu_vram_gb} GB`}
                {node.resource_profile.duration_hint && ` · ${node.resource_profile.duration_hint}`}
              </span>
            </div>
          )}
          <div class="node-detail-row">
            <span class="node-detail-label">{t('nodes.tier')}</span>
            <span class="dim">{node.scheduler_tier}</span>
          </div>
          {agentNames.length > 0 && (
            <div class="node-detail-row">
              <span class="node-detail-label">{t('nodes.cardAgents')}</span>
              <div>
                {agentNames.map((name) => {
                  const a = node.agents![name]
                  if (!a) return null
                  return (
                    <div class="node-agent" key={name}>
                      <span class="node-agent-name mono">{name}</span>
                      {a.cost_tier && <span class="badge">{a.cost_tier}</span>}
                      {a.capabilities && a.capabilities.length > 0 && (
                        <p class="dim">{a.capabilities.join(' · ')}</p>
                      )}
                      {a.best_at && a.best_at.length > 0 && (
                        <p class="dim">
                          {t('nodes.bestAt')}: {a.best_at.join(' · ')}
                        </p>
                      )}
                      {a.not_for && a.not_for.length > 0 && (
                        <p class="dim">
                          {t('nodes.notFor')}: {a.not_for.join(' · ')}
                        </p>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** Delegation chains, derived from the task ledger: parent_id links form a
 *  chain root → delegated → …, each hop labeled with its owner. Only chains
 *  that actually delegated (at least one child) are shown, most recent
 *  first, so "which device chain did this run through" is visible. */
function DelegationChains({ tasks }: { tasks: Task[] }) {
  const byId = new Map(tasks.map((task) => [task.id, task]))
  const childrenOf = new Map<string, Task[]>()
  for (const task of tasks) {
    if (!task.parent_id) continue
    const list = childrenOf.get(task.parent_id) ?? []
    list.push(task)
    childrenOf.set(task.parent_id, list)
  }
  // Roots that delegated at least once; newest activity first.
  const roots = tasks
    .filter((task) => !task.parent_id && childrenOf.has(task.id))
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    .slice(0, 8)

  return (
    <div class="card">
      <h2 class="block-title">{t('nodes.chains')}</h2>
      {roots.length === 0 ? (
        <p class="dim">{t('nodes.chainsEmpty')}</p>
      ) : (
        <div class="chain-list">
          {roots.map((root) => (
            <Chain key={root.id} root={root} byId={byId} childrenOf={childrenOf} />
          ))}
        </div>
      )}
    </div>
  )
}

/** One chain: BFS the parent/child links (depth-capped) and render each
 *  hop as a pill with its owner. */
function Chain({
  root,
  byId,
  childrenOf,
}: {
  root: Task
  byId: Map<string, Task>
  childrenOf: Map<string, Task[]>
}) {
  const hops: Task[] = [root]
  const queue: Task[] = [root]
  while (queue.length > 0 && hops.length < 12) {
    const cur = queue.shift()!
    for (const child of childrenOf.get(cur.id) ?? []) {
      if (!byId.has(child.id)) continue
      hops.push(child)
      queue.push(child)
    }
  }

  return (
    <div class="chain">
      {hops.map((hop, i) => (
        <span key={hop.id} style="display:contents">
          {i > 0 && (
            <span class="chain-arrow" aria-hidden="true">
              →
            </span>
          )}
          <span class={`chain-node${i === 0 ? ' root' : ''}`} title={hop.title}>
            {hop.owner || t('queue.owner')}
          </span>
        </span>
      ))}
      <span class="chain-title">{root.title}</span>
    </div>
  )
}

/** The agent CLIs this node can drive (/api/agents) — a quick glance at
 *  what this device can hand work to, without leaving the devices page. */
function ControllableAgents({ agents }: { agents: AgentInfo[] }) {
  return (
    <div class="card">
      <h2 class="block-title">{t('nodes.drivable')}</h2>
      <p class="hint">{t('nodes.drivableHint')}</p>
      {agents.length === 0 ? (
        <p class="dim">{t('nodes.drivableEmpty')}</p>
      ) : (
        <div class="node-abilities">
          {agents.map((a) => (
            <span key={a.name} class={`badge ${a.installed ? 'green' : 'red'}`}>
              {a.binary}
              {a.installed && a.version ? ` ${a.version}` : ''}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
