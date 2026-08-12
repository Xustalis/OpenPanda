// Package core hosts the node lifecycle: registration, heartbeat loop, and
// graceful shutdown. Message routing and task state live alongside it in
// later phases.
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/xenith/panda/internal/ledger"
)

// NodeID is this node's stable identifier. Phase 0 uses the configured name;
// a generated UUID may be preferred once remote discovery exists.
func NodeID(cfgName string) string { return cfgName }

// Node owns this process's ledger identity.
type Node struct {
	db      *sql.DB
	id      string
	card    ledger.Card
	tier    int
	logger  *slog.Logger
	hbTick  time.Duration
	timeout time.Duration
}

// NewNode builds a Node with an optional card. A nil card is allowed for a
// minimal core that only manages its own heartbeat.
func NewNode(db *sql.DB, id string, card ledger.Card, tier int, logger *slog.Logger) *Node {
	if logger == nil {
		logger = slog.Default()
	}
	return &Node{
		db:      db,
		id:      id,
		card:    card,
		tier:    tier,
		logger:  logger,
		hbTick:  15 * time.Second,
		timeout: 3 * 15 * time.Second, // offline after ~45s of missed heartbeats
	}
}

// Register writes this node into the local capability directory.
func (n *Node) Register(ctx context.Context) error {
	if err := ledger.Register(n.db, n.card, n.id, n.tier); err != nil {
		return fmt.Errorf("register node %s: %w", n.id, err)
	}
	n.logger.Info("node registered", "node", n.id)
	return nil
}

// RunHeartbeat starts a ticker that updates last_seen + capacity until ctx
// is done. It is safe to call concurrently with other core loops.
func (n *Node) RunHeartbeat(ctx context.Context) {
	t := time.NewTicker(n.hbTick)
	defer t.Stop()
	// Emit one immediately so the directory is fresh on startup.
	n.beat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.beat(ctx)
		}
	}
}

func (n *Node) beat(ctx context.Context) {
	capJSON, err := json.Marshal(n.card.Capacity)
	if err != nil {
		n.logger.Warn("marshal capacity", "err", err)
		return
	}
	if err := ledger.Heartbeat(n.db, n.id, "online", string(capJSON)); err != nil {
		n.logger.Warn("heartbeat", "err", err)
		return
	}
	n.logger.Debug("heartbeat", "node", n.id, "capacity", string(capJSON))
}

// Shutdown marks the node offline. Idempotent.
func (n *Node) Shutdown(ctx context.Context) {
	if err := ledger.MarkOffline(n.db, n.id); err != nil {
		n.logger.Warn("mark offline", "err", err)
	} else {
		n.logger.Info("node offline", "node", n.id)
	}
}

// List returns the local capability directory, filtered by status/name
// ("" matches all).
func (n *Node) List(status, name string) ([]ledger.Node, error) {
	return ledger.Query(n.db, status, name)
}
