package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/util"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// Peer is an established connection to another node.
type Peer struct {
	id   string
	conn *bus.Conn
}

// Core wires the node lifecycle, task store, and WebSocket transport into a
// running daemon. It is the root type for the Phase 0 loop.
type Core struct {
	db     *sql.DB
	nodeID string
	card   ledger.Card
	tier   int
	logger *slog.Logger
	store  *TaskStore
	ctx    *ctxstore.Store
	node   *Node
	router *commander.Router
	// memory injects project memory into agent execution context (design §17.2
	// isolation wall). Nil disables injection; tests and minimal nodes leave it
	// nil and are unaffected.
	memory *memory.Injector
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
	// loop pauses a task that keeps failing instead of retrying it forever
	// (design §14.2 signal C, P2-18).
	loop *defense.LoopDetector
	// sleep is the retry backoff sleeper, injectable for tests. retryBackoff is
	// the base delay between task retries, doubling each retry.
	sleep        func(time.Duration)
	retryBackoff time.Duration
	// auditLog records high-risk operations (Tier-2 exec/denial, circuit trips)
	// for later review (P3-32).
	auditLog *security.Audit
	// workDir is the directory agents execute in and the root scope drift is
	// measured against (design §14.2 signal A). Default "." (the daemon's
	// working directory); a deploy can pin it to a project root.
	workDir string
	// hostStatePaths are the node's own bookkeeping paths (SQLite/memory trees,
	// the agent CLI's own config dir). Scope drift ignores changes under them,
	// since the node and its tools write them as a side effect of running a task
	// — not as the agent's work product.
	hostStatePaths []string

	// sharedSecret authenticates inbound hello messages (design §16 / P0-1).
	// Empty is fail-closed: no peer can authenticate until it is set.
	sharedSecret string

	// maxConns and maxConnsPerIP bound concurrent inbound WS connections (A2).
	maxConns      int
	maxConnsPerIP int

	mu    sync.RWMutex
	peers map[string]*Peer
	// greetedConns tracks the conns whose first hello we already replied
	// to, so the handshake terminates without ping-ponging while a hello on
	// any NEW conn (reconnect, replacement, mutual-dial loser) still gets
	// its identity-binding reply. Keyed by conn, not peer id: the peer id
	// key made the reply follow the registry entry, which a concurrent
	// tie-break replacement can swap mid-handshake.
	greetedConns map[*bus.Conn]bool

	// waiters maps task_id -> result channel for synchronous Submit calls
	// that forwarded a task and are blocked awaiting the outcome.
	waiters sync.Map // string -> chan bus.TaskResultPayload

	// pendingCtx maps task_id -> execution context awaiting a context_fetch
	// response, so handleContextAck can resume the task once the snapshot
	// arrives.
	pendingCtx sync.Map // string -> *pendingContext

	// artifacts is the node's content-addressed pool of task outputs: the data
	// plane that lets one node consume what another produced. Nil on a node
	// with no pool configured, which then cannot fetch stage inputs.
	artifacts *artifact.Store

	// pendingArt maps task_id|hash -> the in-flight inbound transfer, so
	// handleArtifactChunk can deliver a chunk to the fetch that asked for it
	// and reject one from any other node.
	pendingArt sync.Map // string -> *artifactTransfer

	// running maps task_id -> the CancelFunc of the context its execution runs
	// under, so a lease expiry, a cancel message or a shutdown can actually stop
	// the work instead of only rewriting the database row. Without it a task
	// reported failed keeps its agent subprocess writing files and committing
	// code, and the parent's re-route then runs the same work twice concurrently.
	running sync.Map // string -> context.CancelFunc

	// leaseTimeout is how long one task attempt may hold its lease before the
	// monitor treats its executor as dead. Renewed on a heartbeat during
	// execution (see renewLease), so it bounds silence rather than runtime.
	// Guarded by mu; SetTimeouts keeps it above the agent hard timeout.
	leaseTimeout time.Duration

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
}

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
		db:           db,
		nodeID:       nodeID,
		card:         card,
		tier:         tier,
		logger:       logger,
		store:        NewTaskStore(db, logger),
		ctx:          ctxstore.New(db, ctxstore.MaxEntriesForResourceClass(card.ResourceClass)),
		node:         NewNode(db, nodeID, card, tier, logger),
		peers:        make(map[string]*Peer),
		greetedConns: make(map[*bus.Conn]bool),
		breaker:      defense.NewCircuitBreaker(0, 0),
		loop:         defense.NewLoopDetector(2),
		auditLog:     security.NewAudit(db),
		workDir:      ".",

		superviseRounds: defaultSuperviseRounds,
		leaseTimeout:    defaultDelegateTimeout,
		sleep:           time.Sleep,
		retryBackoff:    time.Second,
	}
	// The commander needs at least one native ability to route; a zero card
	// yields a router that declines everything. The router starts with default
	// policy (auto injection, no preferred agents); SetRouterPolicy applies
	// the loaded config once the caller has it.
	if len(card.Native) > 0 || len(card.Agents) > 0 || len(card.Manual) > 0 {
		c.router = commander.NewRouter(card, commander.NewExecutor(), model, config.InjectionConfig{}, config.RoutingConfig{})
	}
	return c
}

// SetRouterPolicy applies the configured injection/routing policy to the
// commander router (injection.model and routing.preferred_agents). Call it
// after NewCore with the loaded config sections; a nil router (empty card)
// makes it a no-op.
func (c *Core) SetRouterPolicy(injection config.InjectionConfig, routing config.RoutingConfig) {
	if c.router != nil {
		c.router.SetPolicy(injection, routing)
	}
}

// SetSupervisor attaches the entry model client that judges agent task
// completeness (上级完成度判定). A nil client disables supervision: agent
// tasks then finish in one shot exactly as before, so minimal nodes and tests
// are unaffected.
func (c *Core) SetSupervisor(client *entry.Client) { c.supervisor = client }

// SetSuperviseRounds bounds the execute → judge → re-delegate loop for agent
// tasks. A value <= 0 resets to defaultSuperviseRounds.
func (c *Core) SetSuperviseRounds(n int) {
	if n < 1 {
		n = defaultSuperviseRounds
	}
	c.superviseRounds = n
}

// AttachSupervisor builds a supervisor client from the model config and
// attaches it — but only when the model is actually configured (an API key is
// present), so a model-less node keeps single-shot agent execution instead of
// burning a wasted judge call per task. A build failure is non-fatal: agent
// tasks then finish in one shot exactly as before. The supervisor also gets
// the disk cache over the node database: identical (intent, result) pairs
// reuse the previous verdict without an LLM call.
func (c *Core) AttachSupervisor(model config.ModelConfig) {
	if strings.TrimSpace(model.APIKey) == "" {
		return
	}
	if client, err := entry.NewClient(model); err == nil {
		client.SetDiskCache(entry.NewDiskCache(c.db))
		c.supervisor = client
	}
}

// Register upserts this node in the local ledger.
func (c *Core) Register(ctx context.Context) error { return c.node.Register(ctx) }

// SetMemoryStores attaches the memory layer (design §17/§8): the Hermes
// injector (entry-model conversation context; no longer used for agent-prompt
// injection since A1), the daily log writer feeding the Dreaming engine, and
// the skill store for progressive loading. Any may be nil to disable its layer.
func (c *Core) SetMemoryStores(inj *memory.Injector, daily *memory.Daily, sk *skills.Store) {
	c.memory = inj
	c.daily = daily
	c.skills = sk
	if sk != nil {
		c.tracker = skills.NewTracker(sk)
	}
}

// SetWorkDir pins the directory agents execute in and scope drift is measured
// against. Empty resets to the default (the daemon's working directory).
func (c *Core) SetWorkDir(dir string) {
	if dir == "" {
		dir = "."
	}
	c.workDir = dir
}

// SetHostStatePaths records the node's own bookkeeping paths — its SQLite/memory
// trees and the agent CLI's own config dir — so scope-drift detection never
// flags the host's side-effect writes as agent drift. Paths may be relative to
// the working directory; they are normalized to absolute on entry.
func (c *Core) SetHostStatePaths(paths []string) {
	c.hostStatePaths = make([]string, 0, len(paths))
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			c.hostStatePaths = append(c.hostStatePaths, filepath.Clean(abs))
		}
	}
}

// filterHostDrift drops changed paths that live under a host-owned directory.
// Changes there (SQLite WAL, skill/dream files, the agent CLI's own config) are
// the node's own bookkeeping, not the agent's task output, so they must not
// pause a task for scope drift.
func (c *Core) filterHostDrift(drift []string) []string {
	if len(c.hostStatePaths) == 0 {
		return drift
	}
	wd, _ := filepath.Abs(c.workDir)
	out := make([]string, 0, len(drift))
	for _, p := range drift {
		ap := filepath.Join(wd, p)
		keep := true
		for _, h := range c.hostStatePaths {
			if ap == h || strings.HasPrefix(ap, h+string(filepath.Separator)) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, p)
		}
	}
	return out
}

// SetSharedSecret sets the shared secret used to authenticate inbound hello
// messages (design §16 / P0-1). An empty secret is fail-closed: no peer can
// authenticate, so the WebSocket listener accepts no hello until a secret is
// configured.
func (c *Core) SetSharedSecret(secret string) {
	c.sharedSecret = secret
}

// SetLimits configures the global and per-IP inbound connection limits. A
// limit <= 0 disables that limit.
func (c *Core) SetLimits(maxConns, maxConnsPerIP int) {
	c.maxConns = maxConns
	c.maxConnsPerIP = maxConnsPerIP
}

// Idle reports whether the node has no active (running, dispatched, or
// waiting-for-context) tasks. The Dreaming scheduler uses it to run
// consolidation only when the node is free, so dreaming never competes with
// real work.
func (c *Core) Idle(ctx context.Context) bool {
	return c.store.Idle(ctx)
}

// RunHeartbeat starts the heartbeat loop: each tick updates the local ledger
// (last_seen + live capacity) and broadcasts a wire heartbeat to every
// connected peer, so a peer's TMB freshness weight and DCPS capacity signals
// are fed by real traffic instead of going stale after the initial hello.
func (c *Core) RunHeartbeat(ctx context.Context) {
	t := time.NewTicker(c.node.hbTick)
	defer t.Stop()
	// Beat immediately so the directory is fresh on startup.
	c.node.beat(ctx)
	c.broadcastHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.node.beat(ctx)
			c.broadcastHeartbeat(ctx)
		}
	}
}

// broadcastHeartbeat sends this node's live status/capacity to every connected
// peer. Send failures are logged and skipped — a dead conn is reaped by the
// read loop, and the next tick reaches the replacement.
func (c *Core) broadcastHeartbeat(ctx context.Context) {
	capJSON, load := c.node.capacitySnapshot(ctx)
	c.mu.RLock()
	conns := make(map[string]*bus.Conn, len(c.peers))
	for id, p := range c.peers {
		conns[id] = p.conn
	}
	c.mu.RUnlock()
	for id, conn := range conns {
		msgID, err := newUUID()
		if err != nil {
			c.logger.Warn("mint heartbeat id", "err", err)
			return
		}
		env, err := bus.NewEnvelope(bus.MsgHeartbeat, c.nodeID, msgID, bus.HeartbeatPayload{
			Status: "online", Load: load, Capacity: capJSON,
		})
		if err != nil {
			c.logger.Warn("build heartbeat", "err", err)
			return
		}
		env.To = id
		if err := conn.Send(env); err != nil {
			c.logger.Debug("heartbeat send", "peer", id, "err", err)
		}
	}
}

// handleHeartbeat refreshes the sender's directory row (last_seen + capacity).
// This is the TMB "new message overwrites the slot" mapping: each heartbeat
// upserts the sender's slot wholesale, and Route's freshness weight — not the
// heartbeat cadence — decides how much the data is worth.
func (c *Core) handleHeartbeat(ctx context.Context, env bus.Envelope) {
	var p bus.HeartbeatPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad heartbeat", "err", err)
		return
	}
	status := p.Status
	if status == "" {
		status = "online"
	}
	if err := ledger.Heartbeat(c.db, env.From, status, p.Capacity); err != nil {
		c.logger.Warn("apply heartbeat", "from", env.From, "err", err)
	}
}

// Recover normalizes tasks left active by a previous process instance.
func (c *Core) Recover(ctx context.Context) (int, error) { return c.store.Recover(ctx) }

// RunMonitor scans for expired leases and fails them. It returns when ctx
// is done.
func (c *Core) RunMonitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Plan convergence does not depend on who finished a stage: a review
			// approved from the CLI or the console moves the row in another
			// process, and only this sweep would notice.
			c.sweepPlans(ctx)
			expired, err := c.store.ExpireTasks(ctx)
			if err != nil {
				c.logger.Warn("expire tasks", "err", err)
				continue
			}
			if len(expired) > 0 {
				for _, id := range expired {
					// A force-fail that only rewrites the database row leaves the
					// agent subprocess running — still writing files, still
					// committing — under a task already reported failed upstream,
					// which the parent then re-routes to a second node. Abort the
					// local execution for real.
					c.cancelRunning(id)
					// A task that timed out while paused in waiting_context would
					// otherwise leak its entry in pendingCtx (P2-7).
					c.pendingCtx.Delete(id)
					// The lease expired on a task this node dispatched to a remote
					// executor: tell that executor to stop (review P1-4). Without
					// this the remote agent keeps burning tokens and writing files
					// under a task this node has already reported failed — work that
					// a re-route then duplicates. forwardCancelDownstream no-ops
					// when the task never left this node.
					c.forwardCancelDownstream(ctx, id)
					// Propagate the timeout up the delegation chain so a root
					// scheduler blocked in Submit unblocks (D3). relayToParent is
					// a no-op for a root task; signalResult no-ops without a waiter.
					if tk, err := c.store.Get(ctx, id); err == nil {
						res := bus.TaskResultPayload{
							TaskID: id, AttemptID: tk.AttemptID, State: StateFailed, OK: false, ExitCode: 1, Stderr: "lease expired",
						}
						c.relayToParent(ctx, bus.MsgTaskResult, tk.Chain, res)
						c.signalResult(id, res)
					}
				}
				c.logger.Info("monitor expired tasks", "count", len(expired))
			}
		}
	}
}

// TaskStore exposes the store for CLI/queue views.
func (c *Core) TaskStore() *TaskStore { return c.store }

// List returns the local capability directory.
func (c *Core) List(status, name string) ([]ledger.Node, error) { return c.node.List(status, name) }

// Shutdown marks the node offline and closes peers.
func (c *Core) Shutdown(ctx context.Context) {
	c.mu.Lock()
	for id, p := range c.peers {
		_ = p.conn.Close()
		delete(c.peers, id)
	}
	c.mu.Unlock()
	c.node.Shutdown(ctx)
}

// Listen starts the WebSocket server and accepts connections. Blocks until
// ctx is done.
func (c *Core) Listen(ctx context.Context, addr string) error {
	srv := bus.NewServer(addr, c.logger, func(conn *bus.Conn, _ string) {
		c.handleInbound(ctx, conn)
	})
	srv.SetLimits(c.maxConns, c.maxConnsPerIP)
	return srv.Listen(ctx)
}

// handleInbound runs the read loop for a connection (server- or client-
// side). It also starts the transport ping loop so idle connections stay
// alive and dead peers are detected by pong timeout.
func (c *Core) handleInbound(ctx context.Context, conn *bus.Conn) {
	defer func() {
		// Remove any peer entry that used this conn so sendTo rejects it and
		// the reconnect loop can re-dial.
		c.removePeerForConn(conn)
		conn.Close()
	}()
	go conn.StartPingLoop(ctx, 30*time.Second)
	helloSeen := false
	for {
		var env bus.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			c.logger.Debug("inbound read closed", "err", err)
			return
		}
		// Identity binding (design §16 / P0-1): the hello handshake binds an
		// authenticated node id to this conn. Before binding, only hello is
		// accepted; after binding, every message must carry the bound id or the
		// sender is spoofed and the conn is dropped.
		if id := conn.PeerID(); id != "" {
			if env.From != id {
				c.logger.Warn("spoofed sender on connection", "bound", id, "from", env.From)
				return
			}
		} else if env.Type != bus.MsgHello {
			c.logger.Warn("message before hello", "type", env.Type, "from", env.From)
			return
		}
		c.dispatch(ctx, conn, env)
		if !helloSeen {
			helloSeen = true
			// Hello completed: switch from the short server-side hello deadline
			// to the normal pong/keepalive deadline.
			_ = conn.ResetReadDeadline()
		}
	}
}

// removePeerForConn deletes any peer whose conn matches the given one and
// marks it offline in the local capability directory so routing stops
// considering it.
func (c *Core) removePeerForConn(conn *bus.Conn) {
	c.mu.Lock()
	// Drop the per-conn greeting marker: a reconnect arrives on a NEW conn,
	// which must get a fresh hello reply to bind our identity.
	delete(c.greetedConns, conn)
	var gone []string
	for id, p := range c.peers {
		if p.conn == conn {
			delete(c.peers, id)
			gone = append(gone, id)
		}
	}
	c.mu.Unlock()
	for _, id := range gone {
		c.logger.Info("peer disconnected", "peer", id)
		if err := ledger.MarkOffline(c.db, id); err != nil {
			c.logger.Warn("mark peer offline", "peer", id, "err", err)
		}
	}
}

// DialPeer connects outbound to addr, sends hello, and starts reading in the
// background. It returns once the connection is established (or fails); the
// read loop keeps the connection alive. Short-lived schedulers (the ask CLI)
// use this so they can dial every peer and then proceed to routing. A daemon
// that must hold the connection and reconnect on drop should use MaintainPeer.
func (c *Core) DialPeer(ctx context.Context, addr string) error {
	conn, err := c.dial(ctx, addr)
	if err != nil {
		return err
	}
	go c.handleInbound(ctx, conn)
	return nil
}

// MaintainPeer connects outbound to addr and blocks serving the connection
// until the peer EDGE is down, not just our conn: after a mutual-dial dedup
// (see ensurePeer) our outbound conn may be the loser while the peer's own
// inbound conn keeps the edge alive — redialing then would only recreate the
// one-second flap the dedup exists to stop. A nil return means the edge
// dropped (or ctx is done) and the caller may redial; a non-nil return means
// the dial (or hello) failed.
func (c *Core) MaintainPeer(ctx context.Context, addr string) error {
	conn, err := c.dial(ctx, addr)
	if err != nil {
		return err
	}
	c.handleInbound(ctx, conn)
	// Our outbound conn ended. If the peer still reaches us on its own conn,
	// wait for that conn to die too before handing control back.
	//
	// A nil connFor at the first check is not proof the edge is gone: in a
	// mutual dial our outbound can lose the dedup and be torn down while the
	// peer's surviving conn is still mid-handshake on our side — checking
	// once at that instant reads "no peer" and returns, and the caller
	// redials into the exact one-second flap the dedup exists to stop. So
	// absent conns are waited out through a short grace window; a peer that
	// is genuinely gone costs at most that window before the redial.
	if id := conn.PeerID(); id != "" {
		graceUntil := time.Now().Add(2 * time.Second)
		for c.connFor(id) != nil || time.Now().Before(graceUntil) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

// dial establishes a WS connection to addr and sends hello, advertising this
// node's capability card. The caller owns the returned conn and must read it
// (synchronously via handleInbound, or asynchronously via go handleInbound).
func (c *Core) dial(ctx context.Context, addr string) (*bus.Conn, error) {
	u := normalizeWSURL(addr)
	client := bus.NewClient(u, c.logger)
	conn, err := client.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// Locally-initiated: peer dedup needs the direction (see ensurePeer).
	conn.MarkOutbound()
	// Send hello to identify ourselves and advertise our capability card.
	card, err := c.helloCard()
	if err != nil {
		conn.Close()
		return nil, err
	}
	msgID, err := newUUID()
	if err != nil {
		conn.Close()
		return nil, err
	}
	ts := time.Now().Unix()
	env, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, msgID, bus.HelloPayload{
		NodeID: c.nodeID,
		Ver:    version.Version,
		Card:   card,
		Ts:     ts,
		Sig:    bus.HelloSig(c.sharedSecret, c.nodeID, ts),
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(env); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}
	c.logger.Info("connected to peer", "peer", addr)
	return conn, nil
}

// ensurePeer registers conn under id. A repeated hello on the SAME conn is a
// no-op. A hello on a NEW conn takes over in two distinct situations:
//
//   - Same direction (both conns locally-initiated, or both inbound): the
//     fresh, authenticated hello proves liveness, while the old conn may be a
//     half-dead socket whose read loop has not noticed yet (P1-7). Without
//     replacement a reconnecting peer could never reclaim its identity —
//     sends kept going to the dead conn, and when the dead conn's read loop
//     finally exited, removePeerForConn would delete the identity even though
//     the live replacement conn was right there.
//
//   - Opposite directions: two nodes dialed each other simultaneously, so
//     each side holds one outbound and one inbound conn to the same peer.
//     Both cannot survive — the second registration used to close the first,
//     whose MaintainPeer redialed a second later, replacing the other side's
//     conn in turn: an endless one-second connect/disconnect flap that
//     churned the capability directory offline/online. The tie-break is
//     deterministic: the conn initiated by the lexicographically smaller
//     node id wins, and both sides compute the same winner, so exactly one
//     TCP conn survives and both registries agree.
//
// The old conn on a replacement is closed AFTER the swap and outside the
// mutex; its read-loop cleanup calls removePeerForConn(oldConn), which
// matches by conn identity — the registry now holds the new conn, so the new
// registration survives. A losing (deduped) conn is closed by the hello
// handler after the identity-binding reply that handleHello already sent on
// it (greeting is per-conn, so the loser's first hello always earned one), so
// the losing dialer can bind our identity and quiesce instead of redialing
// blind.
//
// Returns accepted=false when this conn lost the mutual-dial tie-break and
// must not be registered; true otherwise.
func (c *Core) ensurePeer(id string, conn *bus.Conn) (accepted bool) {
	c.mu.Lock()
	old := c.peers[id]
	if old != nil && old.conn == conn {
		c.mu.Unlock()
		return true
	}
	if old != nil && old.conn.Outbound() != conn.Outbound() {
		// Opposite directions: deterministic mutual-dial dedup. The winner
		// is the conn initiated by the smaller node id.
		if conn.Outbound() != (c.nodeID < id) {
			c.mu.Unlock()
			c.logger.Info("peer connection deduped", "peer", id)
			return false
		}
	}
	c.peers[id] = &Peer{id: id, conn: conn}
	n := len(c.peers)
	c.mu.Unlock()

	if old != nil {
		c.logger.Info("peer connection replaced", "peer", id)
		old.conn.Close()
	}
	c.logger.Info("peer registered", "peer", id, "active", n)
	return true
}

// dispatch routes an envelope to its handler. conn is the connection the
// message arrived on, needed so the hello handler can bind the authenticated
// peer identity to it.
func (c *Core) dispatch(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	switch env.Type {
	case bus.MsgHello:
		c.handleHello(ctx, conn, env)
	case bus.MsgTaskDelegate:
		c.handleDelegate(ctx, env)
	case bus.MsgTaskAccept:
		c.handleAccept(ctx, env)
	case bus.MsgTaskDecline:
		c.handleDecline(ctx, env)
	case bus.MsgTaskProgress:
		c.handleProgress(ctx, env)
	case bus.MsgTaskResult:
		c.handleResult(ctx, env)
	case bus.MsgTaskCancel:
		c.handleCancel(ctx, env)
	case bus.MsgContextFetch:
		c.handleContextFetch(ctx, env)
	case bus.MsgContextAck:
		c.handleContextAck(ctx, env)
	case bus.MsgArtifactFetch:
		c.handleArtifactFetch(ctx, env)
	case bus.MsgArtifactChunk:
		c.handleArtifactChunk(ctx, env)
	case bus.MsgHeartbeat:
		c.handleHeartbeat(ctx, env)
	default:
		c.logger.Warn("unhandled message type", "type", env.Type, "from", env.From)
	}
}

func (c *Core) handleHello(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	var p bus.HelloPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad hello", "err", err)
		return
	}
	// Verify the transport signature before trusting the claimed identity
	// (design §16 / P0-1). Fail closed: an unauthenticated or stale hello
	// registers nothing and receives no reply.
	if !bus.VerifyHello(c.sharedSecret, p.NodeID, p.Ts, p.Sig, time.Now()) {
		c.logger.Warn("rejected hello: bad signature", "peer", p.NodeID)
		return
	}
	// The claimed identity must match the envelope's from field; both become the
	// identity bound to this conn, so later messages may only carry this id.
	if env.From != p.NodeID {
		c.logger.Warn("rejected hello: from mismatch", "from", env.From, "peer", p.NodeID)
		return
	}
	conn.SetPeerID(p.NodeID)

	// Send our hello reply BEFORE registering the conn, and directly on the
	// conn this hello arrived on — never via the registry (c.reply). The far
	// end of THIS conn is the read loop waiting to bind our identity, and
	// the registry is a moving target under mutual dials: if the tie-break
	// swaps this peer's registry entry between the greeted check and a
	// registry-routed send, the reply lands on the surviving conn while
	// this one dies hello-less — the losing dialer never learns the edge is
	// alive through its inbound conn, its MaintainPeer returns immediately
	// (PeerID was never bound), and the caller redials into the one-second
	// connect/disconnect flap the dedup exists to stop. Replying before
	// ensurePeer also means a concurrent replacement cannot close this conn
	// out from under the reply. Greeting is tracked per CONN, so every new
	// conn — a reconnect, a replacement, a tie-break loser — gets exactly
	// one identity-binding reply, while a repeat hello on an already-greeted
	// conn gets none (handshake termination).
	c.mu.Lock()
	already := c.greetedConns[conn]
	c.greetedConns[conn] = true
	c.mu.Unlock()
	if !already {
		c.sendHelloReply(conn, p.NodeID)
	}

	accepted := c.ensurePeer(p.NodeID, conn)
	if !accepted {
		// Lost the mutual-dial tie-break: the reply above already left on
		// this conn, so the losing dialer bound our identity and its
		// MaintainPeer will hold the edge through its inbound conn instead
		// of redialing. The surviving registration already ingested the
		// card; drop the loser.
		conn.Close()
		return
	}
	// Ingest the peer's advertised capability card so routing can consider it.
	if len(p.Card) > 0 {
		var sum ledger.CapabilitySummary
		if err := json.Unmarshal(p.Card, &sum); err != nil {
			c.logger.Warn("bad capability card in hello", "peer", p.NodeID, "err", err)
		} else if err := ledger.UpsertRemote(c.db, p.NodeID, sum); err != nil {
			c.logger.Warn("upsert remote card", "peer", p.NodeID, "err", err)
		}
	}
	c.logger.Info("peer hello", "peer", p.NodeID, "ver", p.Ver)
	// A return channel to this peer exists again: redeliver any terminal
	// results parked while it was disconnected (review P0-2). This is the
	// reconciliation point that keeps the two ends' task histories consistent
	// across a disconnect.
	c.outboxFlush(ctx, p.NodeID)
}

// sendHelloReply transmits this node's hello on conn so the far end can bind
// our identity. Direct send on the given conn, never registry-routed — see
// handleHello for why the handshake reply must follow the conn it answers.
func (c *Core) sendHelloReply(conn *bus.Conn, to string) {
	card, err := c.helloCard()
	if err != nil {
		return
	}
	ts := time.Now().Unix()
	msgID, err := newUUID()
	if err != nil {
		return
	}
	envOut, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, msgID, bus.HelloPayload{
		NodeID: c.nodeID,
		Ver:    version.Version,
		Card:   card,
		Ts:     ts,
		Sig:    bus.HelloSig(c.sharedSecret, c.nodeID, ts),
	})
	if err != nil {
		return
	}
	envOut.To = to
	if err := conn.Send(envOut); err != nil {
		c.logger.Debug("hello reply failed", "peer", to, "err", err)
	}
}

// connFor returns the connection associated with from, if any.
func (c *Core) connFor(from string) *bus.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.peers[from]; ok {
		return p.conn
	}
	return nil
}

// sendTo sends env to peer id. Returns ErrNoPeer if unknown.
func (c *Core) sendTo(id string, env bus.Envelope) error {
	conn := c.connFor(id)
	if conn == nil {
		return errors.New("peer not connected: " + id)
	}
	return conn.Send(env)
}

// newUUID mints a fresh message id. It returns an error rather than panicking
// so a transient entropy/time failure degrades the affected message instead of
// crashing the whole daemon (which would take the node permanently offline).
func newUUID() (string, error) {
	return util.UUIDv7()
}

// signalResult delivers an inbound task_result to any synchronous Submit
// waiter for that task. A non-blocking send means a waiter that already gave
// up (ctx done) simply drops the late result.
func (c *Core) signalResult(taskID string, p bus.TaskResultPayload) {
	if v, ok := c.waiters.Load(taskID); ok {
		if ch, ok := v.(chan bus.TaskResultPayload); ok {
			select {
			case ch <- p:
			default:
			}
		}
	}
}

// summary reduces this node's capability card to the compact profile it
// advertises over hello. Remote nodes need ability IDs, capacity and the declared
// hardware profile to route — not the executable commands themselves.
func (c *Core) summary() ledger.CapabilitySummary {
	s := ledger.CapabilitySummary{
		Device:          c.card.Device,
		ResourceClass:   c.card.ResourceClass,
		NodeKind:        c.card.NodeKind,
		NodeIdentity:    c.card.NodeIdentity,
		SchedulerTier:   c.tier,
		Chip:            c.card.Chip,
		Capacity:        c.card.Capacity,
		ResourceProfile: c.card.ResourceProfile,
	}
	for _, n := range c.card.Native {
		s.NativeIDs = append(s.NativeIDs, n.ID)
	}
	s.AgentCaps = make(map[string][]string, len(c.card.Agents))
	for name, ag := range c.card.Agents {
		// Advertise only agents that can actually run here (CLI present and
		// a reachable model — own credentials or injection). A card entry
		// whose CLI is installed but locked out would otherwise attract
		// cross-device routing and fail at runtime after a long hang.
		if c.router != nil && !c.router.AgentViable(name, ag) {
			continue
		}
		s.AgentCaps[name] = ag.Capabilities
	}
	for _, m := range c.card.Manual {
		s.ManualIDs = append(s.ManualIDs, m.ID)
	}
	return s
}

// helloCard marshals the capability summary for the hello payload.
func (c *Core) helloCard() (json.RawMessage, error) {
	return json.Marshal(c.summary())
}

// normalizeWSURL turns a bare host[:port] peer address into a WebSocket URL,
// preserving an explicit scheme (ws:// or wss://) when the caller supplied one.
// The plaintext default is deliberate: the node listener has no TLS termination
// of its own, and link encryption for node-to-node traffic is delegated to the
// network layer (Tailscale). A deployment that reaches peers over an untrusted
// plain network must specify wss:// explicitly and terminate TLS in front of
// the listener.
func normalizeWSURL(addr string) string {
	if addr == "" {
		return ""
	}
	if u, err := url.Parse(addr); err == nil && u.Scheme != "" {
		return addr
	}
	return "ws://" + addr + "/ws"
}
