// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"database/sql"
	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/hooks"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/self"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is an established connection to another node.
type Peer struct {
	id   string
	conn *bus.Conn
	// authoritative records the peer's own hello claim: its sending process
	// holds the node identity lock (the daemon), rather than borrowing the
	// identity the way a CLI/TUI sibling engine does. Same-id arbitration
	// lets an authoritative newcomer reclaim the edge from a live
	// non-authoritative incumbent — a stray sibling that claimed ownership
	// while the daemon was down otherwise holds it forever.
	authoritative bool
}

// Core wires the node lifecycle, task store, and WebSocket transport into a
// running daemon. It is the root type for the Phase 0 loop.
type Core struct {
	businessExecutor BusinessExecutor // mu
	agentConcurrency *commander.AgentConcurrency

	db     *sql.DB
	nodeID string
	card   ledger.Card
	tier   int
	logger *slog.Logger
	store  *TaskStore
	ctx    *ctxstore.Store
	node   *Node
	router *commander.Router
	// cardMu guards the card and the router built from it — the two things
	// ReloadCard swaps at runtime. Reads go through Card() and currentRouter()
	// so a hot reload never races a concurrent routing decision.
	cardMu sync.RWMutex
	// model / routerInjection / routerRouting hold the live execution defaults.
	// cardMu guards them, the router and supervisor so a run can snapshot
	// one coherent configuration while model/card settings are hot-swapped.
	modelRefresh    func(context.Context) error // optional persisted-config preflight; cardMu
	model           config.ModelConfig
	routerInjection config.InjectionConfig
	routerRouting   config.RoutingConfig
	// mcpPassthrough remembers the configured stdio MCP server (mcp.command)
	// so ReloadCard's router rebuild re-applies the extended-policy
	// passthrough; guarded by cardMu like the router policy above.
	mcpPassthrough string
	// selfConfigPath is the daemon's --config path forwarded into generated
	// .mcp.json files for `panda mcp`; remembered for the same rebuild.
	selfConfigPath string
	// memory injects project memory into agent execution context (design §17.2
	// isolation wall). Nil disables injection; tests and minimal nodes leave it
	// nil and are unaffected.
	memory *memory.Injector
	// hookRunner fires the operator's lifecycle hooks (see hooks.go). Nil when
	// none are configured, which is the default — the observer is only
	// registered once a node actually binds one. Read from dispatch goroutines
	// while a test or an embedder may detach, so it is an atomic pointer.
	hookRunner atomic.Pointer[hooks.Runner]
	// hookUnsub removes this core's event observer. Kept so a test (or an
	// embedder) that builds several cores on one database handle can detach
	// rather than accumulate listeners on the shared hub.
	hookUnsub func()
	// daily feeds the Dreaming engine with a line per completed task. Nil
	// disables daily logging.
	daily *memory.Daily
	// skills supplies progressive skill loading into agent execution context
	// (design §8.5). Nil disables skill injection.
	skills *skills.Store
	// tracker aggregates task history and generates skills when a task class
	// clears the quality gate (design §8.2). Nil disables self-evolution.
	tracker *skills.Tracker
	// breaker trips the circuit for a failing agent so the node stops routing
	// work to it (design §14 Layer 1, P2-27).
	breaker *defense.CircuitBreaker
	// supervisor judges whether an agent's result actually satisfies the task
	// (上级完成度判定). It drives the execute → judge → re-delegate loop for
	// agent tasks: nil disables supervision, so an agent task finishes in one
	// shot exactly as before.
	supervisor *entry.Client
	// superviseRounds caps how many execute → judge → re-delegate rounds one
	// agent task is allowed before it is parked in review for human help. A
	// value <= 0 falls back to defaultSuperviseRounds.
	superviseRounds int
	// acceptance bounds the delivery-contract checks this node runs for a
	// task (execution.acceptance). The zero value uses the contract
	// package's defaults: 10 minutes per check, 30 per phase, one retry.
	acceptance config.AcceptanceConfig
	// msgBudgetCap caps how many sibling-board posts one task episode may
	// send (the metering half of the message layer — a spent budget reports
	// back to the agent rather than hard-failing). <= 0 → defaultMsgBudget.
	msgBudgetCap int
	// delegLimits is the configured sub-agent orchestration bound
	// (delegation.max_children/fanout/max_depth). Zero struct → the
	// package defaults via NormalizedDelegation, so a config-free Core
	// behaves exactly like the documented policy.
	delegLimits config.DelegationConfig
	// delegateActive counts delegateChild calls in flight — spawned and
	// still awaited — across this Core. It is the observability leg of the
	// fan-out bound (and the assertion point its tests poll).
	delegateActive atomic.Int64
	// steerMu guards interrupts — the user-inject delivery state (steer.go):
	// the running round's cancel, so an inject can abort the in-flight agent
	// turn. The message text itself travels on the task's board (durable).
	steerMu    sync.Mutex
	interrupts map[string]context.CancelFunc
	// loop pauses a task that keeps failing instead of retrying it forever
	// (design §14.2 signal C, P2-18).
	loop *defense.LoopDetector
	// checkpointLoop is loop's sibling for resumable interruptions: a run
	// killed on its wall-clock bound with a live session attached is a
	// checkpoint the retry resumes, so it draws the larger checkpoint
	// budget (maxCheckpointRetries) rather than the hard-failure one.
	checkpointLoop *defense.LoopDetector
	// sleep is the retry backoff sleeper, injectable for tests. Nil uses the
	// production cancellable wait (waitBackoff), which wakes early when the
	// task's context ends or the row leaves the failed state. retryBackoff is
	// the base delay between task retries, doubling each retry.
	sleep        func(time.Duration)
	retryBackoff time.Duration
	// peerLivenessWindow is the bound ensurePeer's same-id duplicate
	// arbitration trusts an incumbent conn for: zero disables arbitration
	// (legacy always-replace), the default is the transport keepalive bound.
	peerLivenessWindow time.Duration
	// edgeAuthority is stamped on every hello this core sends: true means
	// this process holds the node identity lock (the daemon entry point,
	// which acquires it for the process lifetime). Engines only probe the
	// lock at init and release it immediately, so borrowed siblings always
	// send false — letting a real daemon reclaim its peer edge when it
	// redials behind a stray that claimed the identity while it was down.
	edgeAuthority atomic.Bool
	// auditLog records high-risk operations (Tier-2 exec/denial, circuit trips)
	// for later review (P3-32).
	auditLog *security.Audit
	// workDir is the directory agents execute in and the root scope drift is
	// measured against (design §14.2 signal A). Default "." (the daemon's
	// working directory); a deploy can pin it to a project root.
	workDir string
	// scratchDir is where panda's own runtime directories live — per-task
	// dirs, plan stage trees, and landed copies of remote projects. Empty
	// means "under workDir directly" (the node data-dir layout, where they
	// sit beside the database). A workDir that is a user-visible workspace
	// instead gets workDir/.openpanda wired in from main: panda's scratch
	// then sits in one hidden directory inside the project — the same
	// convention session worktrees already use — rather than tasks/, plans/
	// and projects/ appearing as three visible directories in the user's
	// tree. See SetScratchRoot.
	scratchDir string
	// hostStatePaths are the node's own bookkeeping paths (SQLite/memory trees,
	// the agent CLI's own config dir). Scope drift ignores changes under them,
	// since the node and its tools write them as a side effect of running a task
	// — not as the agent's work product.
	hostStatePaths []string
	// packProtectedPaths are host files that must never leave the node inside
	// an artifact — the config file (which carries the mesh secret and any
	// API keys) and the capability card. They are pruned at pack time and
	// write-protected at extract, the same as hostStatePaths, but they are
	// NOT masked from scope drift: an agent that edits the live config is
	// exactly the drift the detector exists to park for review.
	packProtectedPaths []string

	// netMu guards the hot-reloadable network policy below — the shared
	// secret and the cleartext gate. The setters run on a settings save
	// (Engine.applyNetworkConfig) while connection, discovery, DTN, UDP and
	// outbox goroutines read the values; every read goes through the
	// accessors (sharedSecretValue, cleartextPolicy), never the raw fields.
	netMu sync.RWMutex

	// sharedSecret authenticates inbound hello messages (design §16 / P0-1).
	// Empty is fail-closed: no peer can authenticate until it is set.
	sharedSecret string

	// allowCleartext is the operator's opt-out for the outbound plaintext
	// gate (cleartextOK, network.allow_cleartext). False is fail-closed:
	// ws:// dials only reach loopback/Tailscale-shaped targets.
	allowCleartext bool
	// cleartextFor is the scoped form (network.allow_cleartext_for): extra
	// hosts/CIDRs plaintext dials may reach without lifting the gate for the
	// whole internet — the LAN answer to "pairing refuses to dial".
	cleartextFor []string

	// nodePub/nodePriv are the node's Ed25519 identity, lazily loaded or
	// generated by nodeKeyPair (keyOnce). Nil when the database could not
	// hold a key: every signing site then degrades to the legacy wire form.
	// nodeXPriv is the X25519 half of the same identity (cross-network mesh
	// §3) — the key transit frames seal to — materialized in the same once
	// so key injection (ProbePeer) freezes both halves at once.
	keyOnce   sync.Once
	nodePub   ed25519.PublicKey
	nodePriv  ed25519.PrivateKey
	nodeXPriv *ecdh.PrivateKey

	// listenAddr is the WS address Listen bound, stored so the capability
	// summary can advertise it (M3): a peer that cannot hold this conn still
	// learns where to dial us directly before it spends a punch or a relay.
	listenAddr atomic.Value // stores string

	// maxConns and maxConnsPerIP bound concurrent inbound WS connections (A2).
	maxConns      int
	maxConnsPerIP int

	// Trust zones (experimental): trustCfg holds the operator's per-tier
	// send/accept policy tables, peerTrustHints the configured addr→tier map
	// applied once a hello binds an address to an identity, and reputationOn
	// the routing.reputation flag. All hot-reloadable via ApplyNetworkConfig;
	// read under trustMu, never mutated outside the setters.
	trustMu        sync.RWMutex
	trustCfg       config.TrustConfig
	peerTrustHints map[string]string
	reputationOn   atomic.Bool

	// mesh counts the cross-network plane's traffic (meshmetrics.go): relay
	// bytes, directory rows exchanged, punch and dial_check outcomes.
	mesh meshCounters

	// att holds the contact-point bindings for pair-over-transit (P4b):
	// stable identity → unauthenticated joiner conn, in attclients.go.
	att attState

	// Cross-network policy (network.*): the rendezvous target K, the ladder
	// and relay switches, and this node's own rendezvous role. Swapped whole
	// under polMu so ApplyNetworkConfig is one assignment; the zero value
	// means "never configured" and resolves to the built-in defaults, which
	// is what tests and embedders that never call SetNetworkPolicy get.
	polMu    sync.RWMutex
	netPol   networkPolicy
	polSet   bool
	relayLim relayBudget

	// selfMgr is the self-evolution patch-set manager (design §2–§5), wired by
	// SetSelfEvolution when the operator enables the feature. nil disables all
	// merge proposals — the disposition gate then fails closed. selfOneShot
	// marks a CLI-shaped process so the merge apply skips its in-process
	// re-exec.
	selfMgr       *self.Manager
	selfOneShot   bool
	selfProducers string // "self" | "mesh" (self_evolution.producers)

	mu    sync.RWMutex
	peers map[string]*Peer
	// greetedConns tracks the conns whose first hello we already replied
	// to, so the handshake terminates without ping-ponging while a hello on
	// any NEW conn (reconnect, replacement, mutual-dial loser) still gets
	// its identity-binding reply. Keyed by conn, not peer id: the peer id
	// key made the reply follow the registry entry, which a concurrent
	// tie-break replacement can swap mid-handshake.
	greetedConns map[*bus.Conn]bool
	// evictedConns are conns this registry already replaced and closed. A
	// conn's read loop can still surface frames buffered before the close —
	// notably the peer's hello reply to OUR hello-reply — and re-running
	// arbitration for that zombie hello would resurrect the dead conn over
	// the live replacement. Marked under the same lock as the registry
	// swap, so there is no window where eviction is invisible.
	evictedConns map[*bus.Conn]struct{}

	// waiters fans an inbound task_result out to every synchronous caller
	// blocked on that task id. It is a set, not one channel per task: plan
	// children and explicit Submit can wait on the same id at once, and the
	// old single-slot map silently displaced the first caller.
	waiters waiterSet

	// pendingCtx maps task_id -> execution context awaiting a context_fetch
	// response, so handleContextAck can resume the task once the snapshot
	// arrives.
	pendingCtx sync.Map // string -> *pendingContext

	// pendingCtxServe maps task_id|hash -> a context_fetch this node could
	// not answer and relayed to its chain predecessor instead. It is the
	// mid-chain half of the same fetch: the executor asks the node that
	// handed it the task (reachable by construction), that node either
	// serves from its own snapshot cache or relays one hop further toward
	// the origin, and the answer fans back out to every waiter.
	pendingCtxServe sync.Map // string -> *ctxServeEntry

	// pendingArtServe maps task_id|hash -> a relayed artifact pull this node
	// is forwarding up-chain for downstream consumers: fetches from the
	// registered requesters go to source, chunks from source fan back out.
	// The same partial-mesh gap as pendingCtxServe — a two-hop executor
	// cannot reach the producing node directly.
	pendingArtServe sync.Map // string -> *artServeEntry

	// execSlots reserves execution slots between the capacity check and the
	// moment the task's row actually claims one (running/waiting_context). A
	// bare count-then-spawn over-counts: N concurrent delegates that all read
	// "active < max" before any of them transitions would all accept and
	// over-commit the node (M14). The counter is in-memory because the check
	// it protects is advisory — a restart recounts from the rows.
	execSlots atomic.Int64

	// projects is the project metadata table, and projectsRoot the directory the
	// per-project memory trees live under. Set together by SetProjectStores; nil
	// means this node does not participate in project-aware delegation (it still
	// runs project tasks, just without carrying the project's context).
	projects     *projects.Store
	projectsRoot string

	// artifacts is the node's content-addressed pool of task outputs: the data
	// plane that lets one node consume what another produced. Nil on a node
	// with no pool configured, which then cannot fetch stage inputs.
	artifacts *artifact.Store

	// pendingArt maps task_id|hash -> the in-flight inbound transfer, so
	// handleArtifactChunk can deliver a chunk to the fetch that asked for it
	// and reject one from any other node.
	pendingArt sync.Map // string -> *artifactTransfer

	// artWG tracks the detached adoption and replica pulls result handlers
	// launch under context.WithoutCancel — work the task row already reports
	// terminal for while its bytes are still landing in the pool or a
	// worktree. WaitArtifacts drains it, so a shutdown — and a test's
	// TempDir cleanup — never races a write still in flight.
	artWG sync.WaitGroup
	// artMu pairs with artClosing to close the drain's edge window: a handler
	// that reached goArtifact while Shutdown was already waiting must not
	// Add past a Wait that can no longer see it.
	artMu      sync.Mutex
	artClosing bool

	// pushAck maps peer|hash -> the receiver's latest reported contiguous
	// waterline for an outbound push (§8.3). It lives beside the persisted
	// artifact_push_outbox.acked_through because status replies can outpace
	// SQL mid-stream: the stream loop consults this live value to skip bytes
	// the receiver already holds rather than resending them on every flap.
	pushAck sync.Map // string -> *atomic.Int64

	// pushAckWriteAt maps peer|hash -> the last time the receiver's
	// waterline was persisted to artifact_push_outbox.acked_through — the
	// pacing table for pushAckPersistDue. Cleared in pushRowDrop alongside
	// the live waterline.
	pushAckWriteAt sync.Map // string -> time.Time

	// artAuthCache maps task_id|hash|from -> the moment a positive
	// artifact-transfer authorization verdict expires. Both data-plane
	// handlers (artifact_push, artifact_fetch) re-run the participant +
	// binding queries per chunk — a 500 MiB transfer was ~1500 extra
	// SELECTs on the conn's read loop, where chunk handling must stay
	// cheap. The verdict's inputs (task row, dispatch target, plan stages,
	// artifact index) move far slower than chunk rate, so a short TTL turns
	// the per-MiB DB cluster into one map read. Only positives are cached:
	// a refused verdict re-checks every frame, so a peer whose participant
	// row just landed is never refused on a stale "no", and a revoked
	// participant's stale "yes" expires within artAuthTTL (staged bytes are
	// hash-verified no matter who delivered them, which bounds the damage
	// a window-old yes can do). A stranger cannot mint entries — storing
	// requires a real positive verdict. Screen verdicts re-check the peer's
	// tier on every hit, so a trust demotion cuts a display-content
	// transfer mid-flight instead of riding out the TTL.
	artAuthCache sync.Map // string -> artAuthVerdict

	// running maps task_id -> the CancelFunc of the context its execution runs
	// under, so a lease expiry, a cancel message or a shutdown can actually stop
	// the work instead of only rewriting the database row. Without it a task
	// reported failed keeps its agent subprocess writing files and committing
	// code, and the parent's re-route then runs the same work twice concurrently.
	running sync.Map // string -> context.CancelFunc

	// actuatorLocks serializes execution per physical actuator id. The
	// queue's resource keys order only the scheduled path — delegated work
	// runs straight through handleLocalDelegate -> execute and inline
	// submits run through runLocal, both bypassing that registry — so the
	// lock is the single choke point every actuator driver passes. Two
	// driver processes on one GPIO pin is a fault, not parallelism.
	// Per-process, like MaxConcurrent: a daemon and an embedded REPL core
	// on one host do not share it — the cross-process half of actuator
	// exclusivity is the exec_locks lease taken in claimForRun (see
	// execLockKeys), which carries the same actuator id as a key.
	actuatorLocks sync.Map // string(actuator id) -> *sync.Mutex

	// execHold tracks the exec_locks rows a run currently holds on this
	// node: task id -> execHoldEntry{keys, runID}. It is the in-process half
	// of the cross-process execution lease (the store's exec_locks table is
	// the other half): a duplicate invocation of a task whose run is in
	// flight here yields ErrAlreadyRunning instead of stealing the live
	// run's rows, the delegate wait (plan.delegateChildren) uses the
	// recorded keys to release and re-acquire around a fan-out — exactly
	// like the queue registry it mirrors — and the run nonce predicates
	// every release on this run's own rows. See execlock.go.
	execHold sync.Map // string -> execHoldEntry

	// execAcquireMu serializes the check-then-take window in execLockAcquire
	// so two same-task invocations landing together cannot both pass the
	// execHold pre-check and then take rows for one another.
	execAcquireMu sync.Mutex

	// orphanSeen records when a forwarded task was first sighted orphaned in
	// queued after a restart, so the rescue sweep gives it a grace window to
	// find a new route before failing it (S1-1). Guarded by mu.
	orphanSeen map[string]time.Time

	// stateWin is the per-project/plan sliding window of recent work-tree
	// content hashes backing the §6.2 monotonic-progress check (see
	// stateOscillates). In-memory by design: the window bounds the lifetime
	// of the checking process, and a restart rebuilding it from live trees
	// costs nothing but a slightly younger window. stateWinAt timestamps
	// each key so stale windows (one per plan UUID, forever, without this)
	// are pruned once the map outgrows its bound.
	stateMu    sync.Mutex
	stateWin   map[string][]string
	stateWinAt map[string]time.Time

	// nego is the §5/§6.3 peer-negotiation state: nego is the scope lock
	// table, negoWait the wait-for edges (waiter -> holders) whose cycles the
	// arbitrator breaks, and negoWaiters the synchronous outbound negotiate
	// calls awaiting a grant reply (lock id -> result chan). All lazily
	// initialized under negoMu.
	negoMu      sync.Mutex
	nego        map[string]*negoLock
	negoWait    map[string]map[string]bool
	negoWaiters map[string]chan bus.AgentGrantPayload

	// peerBlocked maps peer node id -> agent names that peer's heartbeats
	// report as circuit-open, so routing can strip them from the peer's
	// ability set and weigh failure history into candidate selection.
	// Guarded by mu; entries die with the peer's connection.
	peerBlocked map[string][]string

	// peerLoops runs one keepalive goroutine per configured peer address
	// (see SyncPeers): the daemon owns the set, a mesh-owning embedded
	// engine can hold it too, and a config reload diffs the map instead of
	// restarting the process. Keyed by the configured address string —
	// not the peer's node id, which is unknown until the hello lands.
	peerLoopsMu sync.Mutex
	peerLoops   map[string]context.CancelFunc

	// pairing holds the Bluetooth-style LAN pairing state: inbound
	// pair_sessions (ephemeral keys, conns, the join hook) and the
	// pair_hello rate bucket. See pairing.go.
	pairing pairingState

	// ownsNodeRow says whether this process owns the node's directory row.
	// The daemon does: it holds the identity lock and runs the heartbeat, so
	// its Shutdown marks the row offline. A short-lived CLI engine (task add,
	// ask, nodes) registers under the same stable node id but only borrows
	// the row — marking it offline on Close would flap the live daemon's
	// self row until the next beat, during which the scheduler sees the local
	// node as gone. Set once before serving; read unguarded afterward.
	ownsNodeRow bool

	// helloSeen maps nodeID|ts|sig|msg_id -> the time the hello was verified,
	// making each signed hello frame single-use within its validity window.
	// Without it a captured hello can be replayed on a second connection for
	// maxHelloAge — the timestamp only expires the replay, it does not prevent
	// it. The msg_id distinguishes an honest reconnect (fresh id, possibly the
	// same second) from a verbatim replay (same id). Guarded by mu; entries
	// age out with the window they close.
	helloSeen map[string]time.Time

	// msgSeen maps (from, msg_id) -> arrival time of the last message seen
	// under that id, the receive-side dedup the envelope's MsgID exists for
	// (design §10.3: "used for dedup at the receiving end"). A WS frame
	// delivered twice — a peer's retry after a timeout, a capture replayed
	// inside the conn — must not run its handler a second time: task_delegate
	// would adopt the task twice, artifact_fetch would restart a transfer.
	// Keyed by sender so two peers minting the same id cannot block each
	// other. Guarded by msgSeenMu — NOT mu: this is the one check every
	// inbound frame on every conn runs, and parking it behind the peer-
	// registry lock would serialize the whole read plane against conn churn.
	// Entries age out of a bounded window and the map is hard-capped so a
	// peer minting ids forever cannot grow memory without bound.
	msgSeenMu sync.Mutex
	msgSeen   map[msgSeenKey]time.Time
	// Bounded task decision locks serialize loopback control requests even
	// when an attempt's endpoint is recreated. Durable state fences processes.
	childDecisionLocks [64]sync.Mutex
	// lastHelloReject is the most recent hello_reject reason a peer sent us,
	// mirrored off the conn so one-shot probes without a conn handle (the
	// ProbePeer loop) can still report "online but refusing our credential"
	// instead of a generic handshake timeout.
	lastHelloReject atomic.Value // string
	// lastCleartextRefusal is the outbound address the link policy refused
	// for being unencrypted. Like lastHelloReject it exists for one-shot
	// probes: the gate runs after the socket opens, so its verdict never
	// reaches DialPeer's caller as an error.
	lastCleartextRefusal atomic.Value // string
	// lastVerifiedHello is the evidence a one-shot probe needs: the id a
	// verified hello reply announced and whether the session cipher armed on
	// that exchange. ProbePeer normally polls the peer registry, but a peer
	// holding a live same-id session (the daemon's own edge) answers our
	// hello reply and then closes the probe conn — the register-then-delete
	// race reads as unreachable even though the reply proved the peer is
	// online and authenticated. Tiny and informational on every core.
	lastVerifiedHello atomic.Value // probeHello
	// msgSeenSweep is the last expiry pass over msgSeen. Sweeping inside the
	// claim used to walk the whole map on every inbound frame — under a busy
	// mesh that is an O(capacity) scan per message. Amortized: stale entries
	// linger at most sweepInterval past their window, which only makes dedup
	// MORE conservative (an expired id still dedups, never re-executes).
	msgSeenSweep time.Time

	// leaseTimeout is how long one task attempt may hold its lease before the
	// monitor treats its executor as dead. Renewed on a heartbeat during
	// execution (see renewLease), so it bounds silence rather than runtime.
	// Guarded by mu; SetTimeouts keeps it above the agent hard timeout.
	leaseTimeout time.Duration
	// dirSweepEvery paces RunDirMonitor's directory sweep. A field rather than
	// a constant so a test can watch a sweep land without sleeping five
	// seconds; production never changes it.
	dirSweepEvery time.Duration
	// reviewHolds tracks in-flight upstream keepalives for admission-parked
	// tasks (keyed by task id) so holdReviewUpstream cannot stack a second
	// beat loop on a row already held — e.g. a restart-resumed hold racing a
	// re-delivered park. Entries are deleted when the hold loop exits.
	reviewHolds sync.Map
	// agentByKind maps plan kinds (e.g. "training", "qa") to their per-task
	// agent timeout overrides (seconds). A kind not present falls back to the
	// global agent timeout. Set by SetTimeouts from config.timeouts.agent_by_kind.
	agentByKind map[string]int
	// taskTokenBudget is the §6.1 quota stamped on top-level tasks created
	// locally when nothing upstream set one: >0 stamps that amount, 0 leaves
	// new tasks unbounded (config task_token_budget < 0 maps here via
	// SetTimeouts). Delegated children keep inheriting the parent's budget.
	// Guarded by mu.
	taskTokenBudget int64

	// queueSched is the node-local task queue scheduler (panel queue
	// redesign): it adopts queued-and-scheduled tasks when resources allow.
	// Nil until StartQueueScheduler runs; Enqueue still works without it (the
	// task waits in queued for any scheduler instance to pick it up).
	queueSched *queue.Scheduler

	// progressMu + lastProgress throttle EvProgress recordings across ALL
	// concurrently running tasks: they are one shared limiter, deliberately —
	// progress events are a convenience view, not per-task state, and a node
	// running several agents must not multiply its event writes.
	progressMu   sync.Mutex
	lastProgress time.Time

	// outboxFlushing marks peers whose outbox flush is currently running so
	// the monitor tick's sweep cannot stack a second flush on top of it.
	// Guarded by mu.
	outboxFlushing map[string]bool

	// relayLog bounds how many times this node forwards the same DTN bundle
	// (§8.3 loop bound). A bundle cannot carry a hop list — the signature is
	// the origin's and a relay must not re-wrap — so the bound lives in
	// relay memory, keyed by bundle id, expiring with the bundle's deadline.
	// Guarded by mu.
	relayLog map[string]dtnRelay

	// contacts is this node's configured contact plan (§8.4): the scheduled
	// transmission windows it advertises and routes custody by. Set once via
	// SetContacts before the serving goroutines start — the field is
	// immutable thereafter, which is what lets heartbeat/sweep readers go
	// lock-free.
	contacts []ledger.Contact

	// pendingSweepAt paces the pending_nodes TTL DELETE inside onBeacon —
	// unix seconds of the last sweep. The discovery read loop is its only
	// caller, so no lock: a DELETE per received beacon is WAL churn on a
	// chatty LAN, while the sweep itself only needs to run on the order of
	// pendingTTL.
	pendingSweepAt int64

	// autoDialCands holds LAN beacons safe to dial without an operator admit:
	// the resolved host IS the datagram's own source IP, so the entry can only
	// ever point back at the broadcaster itself — a forged beacon may name any
	// host, but it cannot change where its packet came from. The dial is still
	// gated by the hello's shared secret; auto-dial replaces typing the IP,
	// never the pairing proof. autoDialOn is armed by RunDiscovery once the
	// config gate passes.
	autoDialOn    atomic.Bool
	autoDialMu    sync.Mutex
	autoDialCands map[string]autoDialCand // node id -> dial candidate

	// discovery reports the LAN-discovery listener's state to whoever asks —
	// `panda doctor`, and the pairing panel through the engine. Without it a
	// node that could not bind the discovery port looked exactly like a LAN
	// with no peers: both render as "no devices found", and the operator has
	// no way to tell "my network filters broadcasts" from "my own listener
	// never came up". The bind error is kept rather than only logged, because
	// the one startup line scrolls away long before anyone reads the panel.
	discMu         sync.RWMutex
	discListening  bool
	discAddr       string
	discErr        string
	discLastBeacon int64 // unix seconds of the last accepted beacon; 0 = never

	// retentionDays is the configured age limit for settled task rows
	// (storage.task_retention_days): 0 keeps history forever. retentionSweepAt
	// paces the hourly pass inside taskSweeps — the monitor ticks every 5s
	// but a delete only needs to run on the order of the retention window.
	// Both atomics: SetRetentionDays may land after the monitor started
	// (config reload), and the sweep check runs on every tick.
	retentionDays    atomic.Int64
	retentionSweepAt atomic.Int64
	// taskDirSweepAt paces the orphan workdir GC the same way — reaped
	// only when the owning task row is gone, never while it lives.
	taskDirSweepAt atomic.Int64
	// stagedPruneAt paces the staging-dir prune inside taskSweeps: the
	// reclaim bound is a 24h sidecar age, so a per-tick ReadDir only pays
	// filesystem syscalls for precision nothing needs.
	stagedPruneAt atomic.Int64

	// Farsky datagram plane (§9.2). udp is the shared socket; udpPort its
	// bound port (advertised in hello). udpRoutes maps peer id -> confirmed
	// endpoint (a punch/ack or sealed envelope binds it; sendTo falls back
	// to it when no WS conn exists). udpCands/udpPeerPort/udpPeerIP hold the
	// raw reachability hints a peer has published: candidate strings, its
	// listener port, and the IP we observed its conn from. udpReflexive and
	// observedIP are OUR OWN public hints (STUN answer / peer's You field).
	// All guarded by udpMu; punching is the session table, guarded by
	// punchMu and keyed by session nonce.
	udpMu   sync.Mutex
	udp     *bus.UDPConn
	udpPort int
	// udpKeys derives the per-pair datagram seals (nil until the plane is
	// listening). See udpkeys.go for why the mesh key is not enough.
	udpKeys   *udpKeys
	udpRoutes map[string]*net.UDPAddr
	// udpHeard is the liveness timestamp behind each udpRoutes entry: the last
	// time authenticated inbound traffic (envelope, punch, or a keepalive from
	// the bound endpoint) arrived from that peer. A route with no fresh proof
	// is a blackhole — UDP sends always "succeed" at write time — so the
	// keepalive loop reaps silent routes and sendTo falls back to outbox
	// parking instead of losing envelopes into the void.
	udpHeard     map[string]time.Time
	udpCands     map[string][]string
	udpPeerPort  map[string]int
	udpPeerIP    map[string]string
	udpReflexive string
	observedIP   string
	punchMu      sync.Mutex
	punching     map[string]*punchSession
	// dir holds the epidemic-directory sync bookkeeping (M1): per-peer pull
	// rate limiting and continuation-page counts, in dirsync.go.
	dir dirState
	// path holds the reachability ladder's bookkeeping (M3): addr-dial
	// cooldowns, auto-punch pacing, nat_suspect marks, and peers'
	// advertised edge counts, in pathtry.go.
	path pathState
	// rendezvous holds the dynamic rendezvous-edge state (M2), in
	// rendezvous.go.
	rzv rendezvousState
}

// dtnRelay is one bundle's forwarding record on this node.
type dtnRelay struct {
	hops  int   // forwards performed
	until int64 // bundle deadline; the entry expires with it
}

// dtnRelayMaxHops caps a node's forwards of one bundle. Combined with the
// no-echo rule (never first-hop to the arriving peer) this bounds every
// residual loop — a triangle that slips past the via check can spend at most
// this many sends per node before the bundle must park and wait for a real
// contact with its destination.
const dtnRelayMaxHops = 3

// defaultSuperviseRounds is the maximum number of execute → judge →
// re-delegate rounds an agent task is allowed before it is parked in review
// rather than looping indefinitely on a task the supervisor keeps rejecting.
const defaultSuperviseRounds = 5

// NewCore constructs a Core. The card may be zero for a minimal node. The
// model config is forwarded to the commander for agent adapter subprocesses.
func NewCore(db *sql.DB, nodeID string, card ledger.Card, tier int, logger *slog.Logger, model config.ModelConfig) *Core {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Core{
		agentConcurrency: commander.NewAgentConcurrency(),
		delegLimits:      config.DelegationConfig{}.NormalizedDelegation(),
		db:               db,
		nodeID:           nodeID,
		card:             card,
		tier:             tier,
		logger:           logger,
		store:            NewTaskStore(db, logger),
		ctx:              ctxstore.New(db, ctxstore.MaxEntriesForResourceClass(card.ResourceClass)),
		node:             NewNode(db, nodeID, card, tier, logger),
		peers:            make(map[string]*Peer),
		autoDialCands:    make(map[string]autoDialCand),
		greetedConns:     make(map[*bus.Conn]bool),
		evictedConns:     make(map[*bus.Conn]struct{}),
		orphanSeen:       make(map[string]time.Time),
		peerBlocked:      make(map[string][]string),
		helloSeen:        make(map[string]time.Time),
		msgSeen:          make(map[msgSeenKey]time.Time),
		breaker:          defense.NewCircuitBreaker(0, 0),
		loop:             defense.NewLoopDetector(2),
		checkpointLoop:   defense.NewLoopDetector(maxCheckpointRetries),
		auditLog:         security.NewAudit(db),
		workDir:          ".",
		model:            model,
		ownsNodeRow:      true,

		superviseRounds: defaultSuperviseRounds,
		leaseTimeout:    defaultDelegateTimeout,
		retryBackoff:    time.Second,
		dirSweepEvery:   5 * time.Second,

		peerLivenessWindow: bus.KeepaliveTimeout(),
	}
	// The commander needs at least one routable ability; a zero card yields a
	// router that declines everything. Actuators count: a hardware-only edge
	// node (a Pi with just a servo, no agents, no native abilities) must still
	// get a router or every actuator task routed to it declines at localMatch
	// as "no capability matches". The router starts with default policy (auto
	// injection, no preferred agents); SetRouterPolicy applies the loaded
	// config once the caller has it.
	if len(card.Native) > 0 || len(card.Agents) > 0 || len(card.Manual) > 0 || len(card.Actuators) > 0 {
		c.router = commander.NewRouter(card, commander.NewExecutor(), model, config.InjectionConfig{}, config.RoutingConfig{})
		c.agentConcurrency.Configure(card.Capacity.MaxConcurrent, config.RoutingConfig{}, config.DelegationConfig{})
		c.router.SetConcurrency(c.agentConcurrency)
		// Local ranking consults the same breaker whose open circuits are
		// published to peers as blocked_agents: an agent this node already
		// ruled out must not keep winning its own dispatch plan. The predicate
		// is non-mutating (Blocked, not Allow), so ranking never spends a
		// half-open trial slot on an agent that may not run.
		c.router.SetAgentBlocked(func(name string) bool { return c.breaker.Blocked("agent:" + name) })
	}
	// P2-9: hand the task store and the audit log this node's identity so
	// every event and high-risk record they write carries the Ed25519
	// attestation. The key materializes here — outside any transaction — so
	// the tx-bound signer never touches the DB.
	if pub, priv, ok := c.nodeKeyPair(); ok {
		c.store.SetEventSigner(pub, priv)
		c.auditLog.SetSigner(pub, priv)
	}
	return c
}

// Register upserts this node in the local ledger.
func (c *Core) Register(ctx context.Context) error { return c.node.Register(ctx) }

// TaskStore exposes the store for CLI/queue views.
func (c *Core) TaskStore() *TaskStore { return c.store }

// List returns the local capability directory.
func (c *Core) List(status, name string) ([]ledger.Node, error) { return c.node.List(status, name) }
