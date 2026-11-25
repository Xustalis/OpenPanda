# Routing, Scoring & Conflict Negotiation

Public architectural summary of [`internal/scheduler/`](../../internal/scheduler/) and the peer-negotiation and DTN scheduling paths in [`internal/core/`](../../internal/core/). See also [the three-layer routing contract](../routing-layers.md) and [the P2P bus protocol](../protocol.md).

## Node Capability Scoring

[`internal/scheduler/score.go`](../../internal/scheduler/score.go) computes a candidate node's raw desirability as a weighted sum and scales it by heartbeat freshness:

- **Core weights**:
  - `resource_efficiency` (`0.4`): unoccupied concurrency slots (`(MaxConcurrent - CurrentTasks) / MaxConcurrent`), scaled by measured free-memory headroom (floored at `0.1` when live telemetry is present; `0.5` neutral when capacity limits are absent).
  - `user_priority` (`0.3`): `1.0` when the node matches the user's soft `--preferred` hint, `0.0` otherwise.
  - `scheduler_tier` (`0.2`): normalized device tier (`Micro = 1`, `Standard = 5`, `Full = 10`).
  - `wait_time` (`0.1`): `1 / (1 + CurrentTasks)` so smaller nodes with short queues are distinguished from wide nodes.
- **Additional terms**:
  - `project_residence` (`0.2`): bonus when the node already holds a checkout of the task's project.
  - `link_latency` (`0.15`): derived from measured ping/pong RTT (`1.0` for self, `0.5` neutral when no sample exists).
  - `reputation` (`0.1`): opt-in locally observed delegation success rate (`routing.reputation`).
- **Heartbeat freshness decay**: `Freshness(lastSeen, now) = exp(-ln(2) * age / 30m)`. A 5-minute-old heartbeat keeps ~0.89 of its weight; a 2-hour-old heartbeat keeps ~0.06, avoiding abrupt online/offline cutoffs for recoverable nodes.
- **Wire contract fields**: `ScoreBreakdown` exposes `heartbeat_age` (seconds) and flat `total_score` alongside nested score components.

## Capacity-Driven Delegation & Chain Bounds

[`internal/scheduler/chain.go`](../../internal/scheduler/chain.go) and [`internal/core/handlers.go`](../../internal/core/handlers.go) enforce loop-free, bounded forwarding:

- **Loop prevention (`AppendChain`)**: rejects immediate self-hops, 2-node ping-pongs, exceeding `MaxVisitsPerNode` (`2`), and exceeding `MaxChainDepth` (`8`).
- **Mesh budgets**:
  - `MaxDelegationBudget` (`20` by default) bounds total forward hops across the mesh (`DecrementDelegation` spends one hop per forward).
  - `DelegationDepth` / `MaxDepth` (`Depth = Parent.Depth + 1`) bounds recursive sub-task promotion (`internal/core/state.go`, `internal/core/plan.go`).
  - `TokenBudget` tracks remaining mesh-wide token quota; `0` denotes an unbounded legacy/pre-budget row, while newly created top-level tasks stamp the node's safety floor (`internal/core/submit.go`).
- **Admission (`task_accept` / `task_decline`)**: receiving peers evaluate capability match, declared resource fit (`ledger.Fits`), and live concurrency slots before accepting a delegation.

## Multi-Hop Graph Routing & DTN Contact Plans

- **Link-state graph (`internal/scheduler/route.go`)**: nodes gossip neighbor edges and measured `LinkMetrics` RTTs in heartbeats. When no capable direct peer exists, `graphFirstHop` runs Dijkstra over the link-state graph (`unknownLinkCost` penalizes unmeasured links without making them unreachable) and forwards via the first hop while preserving the final destination identity.
- **DTN store-and-forward (`internal/scheduler/contact.go`, `internal/core/outbox.go`, `internal/core/dtn.go`)**:
  - Live tasks fail when peers are unreachable; DTN tasks carry an absolute `DeadlineUnix` and park in `task_outbox` for custody relay (`internal/bus/bundle.go`).
  - Scheduled transmission windows (`ledger.Contact`, modeled on CCSDS SABR 734.2) allow contact-plan-aware next-hop selection (`DTNNextHop`).
  - `artifact_push` proactively chunks input artifacts into the remote pool so DTN tasks do not require interactive round-trip pulls.

## Horizontal Peer Negotiation & Shadow Work Copies

[`internal/core/negotiate.go`](../../internal/core/negotiate.go) and [`internal/defense/shadow.go`](../../internal/defense/shadow.go) coordinate concurrent scoped tasks across peers:

- **Scope locks**: tasks declaring `TargetScope` (`repo|file|symbol`) arbitrate locks locally and across live peers (`agent_negotiate` / `agent_grant`).
- **Arbitration & deadlock breaking**: lock weight is derived from queue priority (`negoWeightOf`), bounded by `maxNegoWeight`. Contested requests record wait-for edges (`requester -> holder`); when a cycle forms, the weakest holder is preempted.
- **Shadow resume**: a task preempted mid-edit parks its scoped modified files under `.panda-shadow` (`SaveShadow`) so it can resume cleanly at the next round boundary without corrupting the working tree.
