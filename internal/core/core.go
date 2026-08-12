package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/commander"
	"github.com/xenith/panda/internal/ledger"
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
	node   *Node
	router *commander.Router

	mu      sync.RWMutex
	peers   map[string]*Peer
	greeted map[string]bool // node ids we have replied hello to

	onResult func(ctx context.Context, taskID string, result bus.TaskResultPayload)
}

// NewCore constructs a Core. The card may be zero for a minimal node.
func NewCore(db *sql.DB, nodeID string, card ledger.Card, tier int, logger *slog.Logger) *Core {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Core{
		db:      db,
		nodeID:  nodeID,
		card:    card,
		tier:    tier,
		logger:  logger,
		store:   NewTaskStore(db, logger),
		node:    NewNode(db, nodeID, card, tier, logger),
		peers:   make(map[string]*Peer),
		greeted: make(map[string]bool),
	}
	// The commander needs at least one native ability to route; a zero card
	// yields a router that declines everything.
	if len(card.Native) > 0 || len(card.Agents) > 0 || len(card.Manual) > 0 {
		c.router = commander.NewRouter(card, commander.NewExecutor())
	}
	return c
}

// Register upserts this node in the local ledger.
func (c *Core) Register(ctx context.Context) error { return c.node.Register(ctx) }

// RunHeartbeat starts the heartbeat ticker.
func (c *Core) RunHeartbeat(ctx context.Context) { go c.node.RunHeartbeat(ctx) }

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
			n, err := c.store.ExpireTasks(ctx)
			if err != nil {
				c.logger.Warn("expire tasks", "err", err)
				continue
			}
			if n > 0 {
				c.logger.Info("monitor expired tasks", "count", n)
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

// handleInbound runs the read loop for a server-side connection. The first
// message must be hello, which supplies the peer's node id.
func (c *Core) handleInbound(ctx context.Context, conn *bus.Conn) {
	defer conn.Close()
	for {
		var env bus.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			c.logger.Debug("inbound read closed", "err", err)
			return
		}
		// Track peer by the from field of the first message.
		if env.From != "" {
			c.ensurePeer(env.From, conn)
		}
		c.dispatch(ctx, env)
	}
}

// DialPeer connects outbound to addr and registers the peer after hello.
func (c *Core) DialPeer(ctx context.Context, addr string) error {
	u := normalizeWSURL(addr)
	client := bus.NewClient(u, c.logger)
	conn, err := client.Dial(ctx)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	// Send hello to identify ourselves.
	env, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, mustUUID(), bus.HelloPayload{
		NodeID: c.nodeID,
		Ver:    "0.1.0-dev",
	})
	if err != nil {
		conn.Close()
		return err
	}
	if err := conn.Send(env); err != nil {
		conn.Close()
		return fmt.Errorf("send hello: %w", err)
	}
	c.logger.Info("connected to peer", "peer", addr)
	go c.handleInbound(ctx, conn)
	return nil
}

// ensurePeer registers conn under id if not already present.
func (c *Core) ensurePeer(id string, conn *bus.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.peers[id]; ok {
		return
	}
	c.peers[id] = &Peer{id: id, conn: conn}
	c.logger.Info("peer registered", "peer", id, "active", len(c.peers))
}

// dispatch routes an envelope to its handler.
func (c *Core) dispatch(ctx context.Context, env bus.Envelope) {
	switch env.Type {
	case bus.MsgHello:
		c.handleHello(ctx, env)
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
	default:
		c.logger.Warn("unhandled message type", "type", env.Type, "from", env.From)
	}
}

func (c *Core) handleHello(ctx context.Context, env bus.Envelope) {
	var p bus.HelloPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad hello", "err", err)
		return
	}
	c.ensurePeer(p.NodeID, c.connFor(env.From))
	c.logger.Info("peer hello", "peer", p.NodeID, "ver", p.Ver)

	// Reply with our own hello only once per peer, so the handshake
	// terminates instead of ping-ponging forever.
	c.mu.Lock()
	if c.greeted[p.NodeID] {
		c.mu.Unlock()
		return
	}
	c.greeted[p.NodeID] = true
	c.mu.Unlock()

	reply, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, mustUUID(), bus.HelloPayload{
		NodeID: c.nodeID,
		Ver:    "0.1.0-dev",
	})
	if err != nil {
		return
	}
	if err := c.reply(ctx, env, bus.MsgHello, reply.Payload); err != nil {
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

func mustUUID() string {
	id, err := util.UUIDv7()
	if err != nil {
		panic(err)
	}
	return id
}

func normalizeWSURL(addr string) string {
	if addr == "" {
		return ""
	}
	if u, err := url.Parse(addr); err == nil && u.Scheme != "" {
		return addr
	}
	return "ws://" + addr + "/ws"
}
