import { t } from '../i18n'
import { NodeInfo } from '../api/client'

/**
 * FleetTopologyCard — device network overview (D4).
 *
 * Two modes:
 *
 *   • Multi-node (n > 1): node row list with green online dot, load bar,
 *     and "this device" tag for self. Used in the empty-state 50/50 split
 *     alongside the chat CTA.
 *
 *   • Single-node (n ≤ 1): a soft CTA block that explains why a second
 *     device is useful + how to join (`panda pair`). Never show a one-row
 *     node list in this mode — it reads like "Panda only has one option"
 *     rather than "your network is tiny, invite a friend."
 *
 * This component makes zero API calls — feed in nodes from the caller's
 * existing store.
 */
export interface FleetTopologyCardProps {
  /** Node directory rows, as returned by GET /api/nodes (or an empty array). */
  nodes: NodeInfo[]
  /** Optional: explicit self id. When omitted, falls back to node.is_local. */
  selfNodeId?: string
  /** Called when the user taps "Invite second device". If undefined, the CTA
   *  still paints but is aria-disabled (does nothing on click). */
  onAddDevice?: () => void
}

export default function FleetTopologyCard(props: FleetTopologyCardProps) {
  const { nodes, selfNodeId, onAddDevice } = props
  const onlineCount = nodes.filter((n) => onlineStatus(n.status)).length
  const isSingle = nodes.length <= 1

  return (
    <div class="fleet-card" role="region" aria-label={t('fleet.title')}>
      <div class="fleet-head">
        <h3>{t('fleet.title')}</h3>
        <span class="online">{t('fleet.onlineN', { n: onlineCount })}</span>
      </div>

      {isSingle ? (
        <SingleNodeBody onAddDevice={onAddDevice} />
      ) : (
        <NodeList nodes={nodes} selfNodeId={selfNodeId} />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function SingleNodeBody(props: { onAddDevice?: () => void }) {
  const { onAddDevice } = props
  return (
    <div class="fleet-single">
      <h2>{t('fleet.single.title')}</h2>
      <p>{t('fleet.single.body')}</p>
      <div class="cta-row">
        <button
          type="button"
          class="fleet-cta-btn"
          onClick={() => onAddDevice?.()}
          aria-disabled={onAddDevice ? undefined : true}
          disabled={onAddDevice ? undefined : true}
        >
          {t('fleet.single.cta')}
        </button>
        <div class="fleet-cta-sub">{t('fleet.single.ctaSub')}</div>
      </div>
    </div>
  )
}

function NodeList(props: { nodes: NodeInfo[]; selfNodeId?: string }) {
  // Online-first, then by name so the list feels stable.
  const sorted = [...props.nodes].sort((a, b) => {
    const ao = onlineStatus(a.status) ? 0 : 1
    const bo = onlineStatus(b.status) ? 0 : 1
    if (ao !== bo) return ao - bo
    return (a.name ?? a.id).localeCompare(b.name ?? b.id)
  })
  return (
    <div class="fleet-nodes" role="list">
      {sorted.map((n) => {
        const online = onlineStatus(n.status)
        const self =
          (typeof props.selfNodeId === 'string' && n.id === props.selfNodeId) ||
          Boolean(n.is_local)
        const cap = n.capacity ?? { max_concurrent_tasks: 0, current_tasks: 0 }
        return (
          <div
            key={n.id}
            class={'fleet-node' + (online ? ' online' : ' offline')}
            role="listitem"
          >
            <span class="dot" aria-hidden />
            <span class="name">
              {self ? `${n.name} · ${t('fleet.node.self')}` : n.name}
            </span>
            <span class="load" aria-label={t('fleet.node.tasks', {
              cur: cap.current_tasks ?? 0,
              max: cap.max_concurrent_tasks ?? 0,
            })}>
              {online
                ? t('fleet.node.tasks', {
                    cur: cap.current_tasks ?? 0,
                    max: cap.max_concurrent_tasks ?? 0,
                  })
                : t('fleet.node.offline')}
            </span>
          </div>
        )
      })}
    </div>
  )
}

/** Accepts a handful of online-ish statuses so the green dot survives API
 *  re-wording of the node status column. */
function onlineStatus(s: string | undefined): boolean {
  if (!s) return false
  const v = s.toLowerCase()
  return v === 'online' || v === 'running' || v === 'connected' || v === 'active' || v === 'ok'
}

export { FleetTopologyCard as FleetTopologyCard_ }
