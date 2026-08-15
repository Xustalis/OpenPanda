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

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/commander"
	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ctxstore"
	"github.com/xenith/panda/internal/defense"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/memory"
	"github.com/xenith/panda/internal/security"
	"github.com/xenith/panda/internal/skills"
	"github.com/xenith/panda/internal/util"
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

	mu      sync.RWMutex
	peers   map[string]*Peer
	greeted map[string]bool // node ids we have replied hello to

	// waiters maps task_id -> result channel for synchronous Submit calls
	// that forwarded a task and are blocked awaiting the outcome.
	waiters sync.Map // string -> chan bus.TaskResultPayload

	// pendingCtx maps task_id -> execution context awaiting a context_fetch
	// response, so handleContextAck can resume the task once the snapshot
	// arrives.
	pendingCtx sync.Map // string -> *pendingContext
}

// NewCore constructs a Core. The card may be zero for a minimal node. The
// model config is forwarded to the commander for agent adapter subprocesses.
func NewCore(db *sql.DB, nodeID string, card ledger.Card, tier int, logger *slog.Logger, model config.ModelConfig) *Core {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Core{
		db:       db,
		nodeID:   nodeID,
		card:     card,
		tier:     tier,
		logger:   logger,
		store:    NewTaskStore(db, logger),
		ctx:      ctxstore.New(db, ctxstore.MaxEntriesForResourceClass(card.ResourceClass)),
		node:     NewNode(db, nodeID, card, tier, logger),
		peers:    make(map[string]*Peer),
		greeted:  make(map[string]bool),
		breaker:  defense.NewCircuitBreaker(0, 0),
		loop:     defense.NewLoopDetector(2),
		auditLog: security.NewAudit(db),
		workDir:  ".",

		sleep:        time.Sleep,
		retryBackoff: time.Second,
	}
	// The commander needs at least one native ability to route; a zero card
	// yields a router that declines everything.
	if len(card.Native) > 0 || len(card.Agents) > 0 || len(card.Manual) > 0 {
		c.router = commander.NewRouter(card, commander.NewExecutor(), model)
	}
	return c
}

// Register upserts this node in the local ledger.
func (c *Core) Register(ctx context.Context) error { return c.node.Register(ctx) }

// SetMemoryStores attaches the memory layer (design §17/§8): the injector for
// project memory, the daily log writer feeding the Dreaming engine, and the
// skill store for progressive loading. Any may be nil to disable its layer.
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

// Idle reports whether the node has no active (running, dispatched, or
// waiting-for-context) tasks. The Dreaming scheduler uses it to run
// consolidation only when the node is free, so dreaming never competes with
// real work.
func (c *Core) Idle(ctx context.Context) bool {
	running, _ := c.store.ListByState(ctx, StateRunning)
	dispatched, _ := c.store.ListByState(ctx, StateDispatched)
	waiting, _ := c.store.ListByState(ctx, StateWaitingCtx)
	return len(running) == 0 && len(dispatched) == 0 && len(waiting) == 0
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
			expired, err := c.store.ExpireTasks(ctx)
			if err != nil {
				c.logger.Warn("expire tasks", "err", err)
				continue
			}
			if len(expired) > 0 {
				for _, id := range expired {
					// A task that timed out while paused in waiting_context would
					// otherwise leak its entry in pendingCtx (P2-7).
					c.pendingCtx.Delete(id)
					// Propagate the timeout up the delegation chain so a root
					// scheduler blocked in Submit unblocks (D3). relayToParent is
					// a no-op for a root task; signalResult no-ops without a waiter.
					if tk, err := c.store.Get(ctx, id); err == nil {
						res := bus.TaskResultPayload{
							TaskID: id, AttemptID: tk.AttemptID, OK: false, ExitCode: 1, Stderr: "lease expired",
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
		go c.handleInbound(ctx, conn)
	})
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
	}
}

// removePeerForConn deletes any peer whose conn matches the given one and
// marks it offline in the local capability directory so routing stops
// considering it.
func (c *Core) removePeerForConn(conn *bus.Conn) {
	c.mu.Lock()
	var gone []string
	for id, p := range c.peers {
		if p.conn == conn {
			delete(c.peers, id)
			// Clear the hello-reply marker so a reconnect redoes the
			// handshake; otherwise a reconnecting peer never receives our
			// hello back and cannot register us on its side.
			delete(c.greeted, id)
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
// until it drops or ctx is done. It is the body of a daemon's reconnect loop:
// dial, serve, and return on drop so the caller can back off and redial. A nil
// return means the connection was established and later dropped; a non-nil
// return means the dial (or hello) failed.
func (c *Core) MaintainPeer(ctx context.Context, addr string) error {
	conn, err := c.dial(ctx, addr)
	if err != nil {
		return err
	}
	c.handleInbound(ctx, conn)
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
		Ver:    "0.1.0-dev",
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
// no-op; a hello on a NEW conn replaces the stale registration (P1-7): the
// fresh, authenticated hello proves liveness, while the old conn may be a
// half-dead socket whose read loop has not noticed yet. Without replacement a
// reconnecting peer could never reclaim its identity — sends kept going to
// the dead conn, and when the dead conn's read loop finally exited,
// removePeerForConn would delete the identity even though the live
// replacement conn was right there.
//
// The old conn is closed AFTER the swap and outside the mutex. Its read-loop
// cleanup calls removePeerForConn(oldConn), which matches by conn identity —
// the registry now holds the new conn, so the new registration survives.
//
// Returns true when a stale registration was replaced, so the hello handler
// knows to reply on the new conn even if this peer was already greeted —
// otherwise the reconnecting side never learns our identity on its new conn
// and drops our first inbound message as "message before hello".
func (c *Core) ensurePeer(id string, conn *bus.Conn) bool {
	c.mu.Lock()
	old := c.peers[id]
	if old != nil && old.conn == conn {
		c.mu.Unlock()
		return false
	}
	c.peers[id] = &Peer{id: id, conn: conn}
	n := len(c.peers)
	c.mu.Unlock()

	if old != nil {
		c.logger.Info("peer connection replaced", "peer", id)
		old.conn.Close()
	}
	c.logger.Info("peer registered", "peer", id, "active", n)
	return old != nil
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
	case bus.MsgTaskResult:
		c.handleResult(ctx, env)
	case bus.MsgTaskCancel:
		c.handleCancel(ctx, env)
	case bus.MsgContextFetch:
		c.handleContextFetch(ctx, env)
	case bus.MsgContextAck:
		c.handleContextAck(ctx, env)
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
	replaced := c.ensurePeer(p.NodeID, conn)
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

	// Reply with our own hello only once per peer, so the handshake
	// terminates instead of ping-ponging forever. Exception: a hello that
	// REPLACED a stale conn (P1-7) gets a fresh reply — the reconnecting peer
	// must bind our identity to its new conn, or it will drop our first
	// message as arriving before hello.
	c.mu.Lock()
	if c.greeted[p.NodeID] && !replaced {
		c.mu.Unlock()
		return
	}
	c.greeted[p.NodeID] = true
	c.mu.Unlock()

	card, err := c.helloCard()
	if err != nil {
		return
	}
	ts := time.Now().Unix()
	if err := c.reply(ctx, env, bus.MsgHello, bus.HelloPayload{
		NodeID: c.nodeID,
		Ver:    "0.1.0-dev",
		Card:   card,
		Ts:     ts,
		Sig:    bus.HelloSig(c.sharedSecret, c.nodeID, ts),
	}); err != nil {
		c.logger.Debug("hello reply failed", "peer", env.From, "err", err)
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
// advertises over hello. Remote nodes need ability IDs and capacity to route,
// not the executable commands themselves.
func (c *Core) summary() ledger.CapabilitySummary {
	s := ledger.CapabilitySummary{
		Device:        c.card.Device,
		ResourceClass: c.card.ResourceClass,
		SchedulerTier: c.tier,
		Chip:          c.card.Chip,
		Capacity:      c.card.Capacity,
	}
	for _, n := range c.card.Native {
		s.NativeIDs = append(s.NativeIDs, n.ID)
	}
	s.AgentCaps = make(map[string][]string, len(c.card.Agents))
	for name, ag := range c.card.Agents {
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
