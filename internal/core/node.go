// SPDX-License-Identifier: AGPL-3.0-or-later

// Package core hosts the node lifecycle: registration, heartbeat loop, and
// graceful shutdown. Message routing and task state live alongside it in
// later phases.
package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/util"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// NodeID is this node's stable identifier. Phase 0 uses the configured name;
// a generated UUID may be preferred once remote discovery exists.
func NodeID(cfgName string) string { return cfgName }

// RuntimeNodeID keeps legacy physical node IDs stable while preventing a VM
// configured with the same display name from colliding with its host node.
func RuntimeNodeID(name, kind, identity string) string {
	if kind != "vm" {
		return NodeID(name)
	}
	if identity == "" {
		return name + "@vm"
	}
	return name + "@vm-" + shortIdentity(identity)
}

func shortIdentity(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:6])
}

// EphemeralNodeID derives a short-lived identity from base for processes that
// dial peers but are not the long-running daemon (e.g. `panda ask`). It appends
// a random suffix so a concurrent daemon and ask session never share a node id
// in the grid — a collision would cause ensurePeer to drop the second
// connection and misroute replies.
func EphemeralNodeID(base string) string {
	suffix, err := util.UUIDv7()
	if err != nil {
		suffix = fmt.Sprintf("%x", time.Now().UnixNano())
	}
	// UUIDv7 is time-ordered: the leading bytes are the millisecond timestamp,
	// which two IDs minted in the same tick would share. The trailing 8 hex
	// chars are the random part, which is what actually distinguishes callers.
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return base + "-" + suffix
}

// Node owns this process's ledger identity.
type Node struct {
	db     *sql.DB
	id     string
	card   ledger.Card
	tier   int
	logger *slog.Logger
	hbTick time.Duration
	// beatMu guards lastBeatJSON/lastBeatAt — the change-gate that keeps an
	// idle heartbeat from rewriting an identical self row every tick.
	beatMu       sync.Mutex
	lastBeatJSON string
	lastBeatAt   time.Time
}

// NewNode builds a Node with an optional card. A nil card is allowed for a
// minimal core that only manages its own heartbeat.
func NewNode(db *sql.DB, id string, card ledger.Card, tier int, logger *slog.Logger) *Node {
	if logger == nil {
		logger = slog.Default()
	}
	return &Node{
		db:     db,
		id:     id,
		card:   card,
		tier:   tier,
		logger: logger,
		hbTick: 15 * time.Second,
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

// SetCard swaps the card a reload just installed, so the next Register or
// capacity snapshot reflects it. The daemon serializes reloads; this method is
// only the node-local half of Core.ReloadCard.
func (n *Node) SetCard(card ledger.Card) { n.card = card }

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
	capJSON, _ := n.capacitySnapshot(ctx)
	status := "online"
	if n.draining(ctx) {
		status = "draining"
	}
	n.beatMu.Lock()
	defer n.beatMu.Unlock()
	// Skip the rewrite when the advertised payload is unchanged and the row
	// is still fresh: the same UPDATE every 15s is pure WAL churn on an idle
	// node, and every peer's view of our liveness comes from the wire
	// heartbeat frames (sent regardless), not this row. 30s stays under the
	// panel's 45s self-liveness bound and the 90s stale-peer sweep, so no
	// consumer sees the row age out.
	// The drain flag joins the dedup key: a toggle must rewrite the row on the
	// next beat, not wait out the 30s refresh floor with a stale status.
	if capJSON+status == n.lastBeatJSON && time.Since(n.lastBeatAt) < selfRowRefresh {
		return
	}
	if err := ledger.Heartbeat(n.db, n.id, status, capJSON); err != nil {
		n.logger.Warn("heartbeat", "err", err)
		return
	}
	// Stamp our own version on the self row too (Track 3): peers learn it
	// from the wire beat, but anything reading this node's directory
	// directly — the panel's skew check, panda status — needs it here.
	if err := ledger.SetNodeVerIfChanged(n.db, n.id, version.Version); err != nil {
		n.logger.Warn("stamp self version", "err", err)
	}
	n.lastBeatJSON, n.lastBeatAt = capJSON+status, time.Now()
	n.logger.Debug("heartbeat", "node", n.id, "capacity", capJSON)
}

// draining reports whether this node is in maintenance drain (Track 2): the
// heartbeat advertises "draining" instead of "online" so peers stop routing
// new work here, inbound delegates are declined, and the local queue stops
// claiming fresh tasks — while in-flight work runs to completion. The flag
// lives in the shared settings table so `panda nodes drain` (a separate
// process) can flip a running daemon without a control channel.
func (n *Node) draining(ctx context.Context) bool {
	var v string
	err := n.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key='node_drain'`).Scan(&v)
	return err == nil && v == "1"
}

// selfRowRefresh is the floor under which an unchanged self row is still
// rewritten: consumers that read last_seen (the panel's 45s "running" test,
// the 90s ExpireStale sweep, freshness-weighted scoring) must not watch the
// self row age out just because the payload was identical.
const selfRowRefresh = 30 * time.Second

// capacitySnapshot returns the live capacity JSON (with the real active-task
// count, not the static card value) plus the derived 0-1 load. The DCPS
// weighted score and the TMB freshness discount on peers are only as good as
// the capacity data heartbeats actually carry (review §4.3 "容量数据空转").
func (n *Node) capacitySnapshot(ctx context.Context) (string, float64) {
	capacity := n.card.Capacity
	var active int
	if err := n.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE state IN ('running','waiting_context')`).Scan(&active); err != nil {
		n.logger.Warn("count active tasks for heartbeat", "err", err)
	} else {
		capacity.CurrentTasks = active
	}
	// Queue depth (Track 3): the accepted-but-waiting backlog. Peers and the
	// fleet panel read it alongside CurrentTasks — an idle-looking node with
	// a deep queue is not actually idle.
	var queued int
	if err := n.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE state='queued'`).Scan(&queued); err == nil {
		capacity.QueuedTasks = queued
	}
	// Live compute metrics (Track 2): the static card declares the machine;
	// these samples say what it can absorb right now. A failed probe reports
	// -1 ("unmeasured"), never a false zero a peer would read as exhausted.
	live := ledger.LiveMetrics{MemFreeGB: -1, DiskFreeGB: -1, GPUUtil: -1}
	if v, ok := hwinfo.MemFreeGB(); ok {
		live.MemFreeGB = v
	}
	if dir, err := os.Getwd(); err == nil {
		if v, ok := hwinfo.DiskFreeGB(dir); ok {
			live.DiskFreeGB = v
		}
	}
	if v, ok := hwinfo.GPUUtilPercent(); ok {
		live.GPUUtil = v
	}
	capacity.Live = &live
	capJSON, err := json.Marshal(capacity)
	if err != nil {
		n.logger.Warn("marshal capacity", "err", err)
		return "", 0
	}
	load := 0.0
	if capacity.MaxConcurrent > 0 {
		load = float64(capacity.CurrentTasks) / float64(capacity.MaxConcurrent)
		if load > 1 {
			load = 1
		}
	}
	return string(capJSON), load
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

// LinkState represents the 3-state link classification (whitepaper §8.1):
// Live, Opportunistic, or Offline.
type LinkState string

const (
	LinkLive          LinkState = "live"
	LinkOpportunistic LinkState = "opportunistic"
	LinkOffline       LinkState = "offline"
)

// EvaluateLinkState calculates the link state towards targetNode based on
// live connection status and heartbeat exponential freshness discount (whitepaper §8.1).
func (c *Core) EvaluateLinkState(ctx context.Context, targetNode string) LinkState {
	conn := c.connFor(targetNode)
	var lastSeen int64
	if c.db != nil {
		_ = c.db.QueryRowContext(ctx, `SELECT last_seen FROM employee_cache WHERE id = ?`, targetNode).Scan(&lastSeen)
	}
	freshness := scheduler.Freshness(lastSeen, time.Now().Unix())
	if conn != nil && freshness >= 0.5 {
		return LinkLive
	}
	if freshness >= 0.5 || conn != nil {
		return LinkOpportunistic
	}
	return LinkOffline
}
