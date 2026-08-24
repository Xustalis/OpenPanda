import { useState } from 'preact/hooks'
import { api, type AgentInfo, type NodeInfo, type SelfInfo, type Task } from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import { ErrorState, PageHeader } from '../components/page'

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
            <NodeCard key={n.id} node={n} isSelf={isSelfNode(self, n)} />
          ))}
        </div>
      )}

      {tasks !== null && <DelegationChains tasks={tasks} />}

      {agents !== null && <ControllableAgents agents={agents} />}
    </section>
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
 *  node advertises (capabilities / best at / not for). */
function NodeCard({ node, isSelf }: { node: NodeInfo; isSelf: boolean }) {
  const [open, setOpen] = useState(false)
  const agentNames = node.agents ? Object.keys(node.agents) : []

  return (
    <div class="card node-card">
      <div class="node-head">
        <span class="node-id mono">{node.id}</span>
        {node.name && node.name !== node.id && <span class="node-name">{node.name}</span>}
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
            <span key={a} class="badge">
              {a}
            </span>
          ))}
          {node.abilities.length > 8 && (
            <span class="badge">+{node.abilities.length - 8}</span>
          )}
        </div>
      )}

      <button class="btn small node-detail-toggle" onClick={() => setOpen(!open)}>
        {open ? t('nodes.detailsHide') : t('nodes.details')}
      </button>

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
