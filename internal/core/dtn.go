package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// dtn.go is the receiving half of the DTN plane (whitepaper §8.3, §9.1): a
// dtn_bundle envelope carries a signed CBOR bundle whose inner payload is the
// same task_delegate a live dispatch would carry. Verifying here — at the
// trust boundary — is what lets a bundle arrive over a link that never
// authenticated its contents: the HMAC proves the origin held the mesh secret
// and the TTL proves the work is still alive.

// handleDTNBundle verifies an incoming bundle and either delivers it (the
// destination EID is us) or takes custody as a relay — forwarding verbatim to
// the destination when that link is up, toward it along the link-state graph
// when it is not, or parking the signed blob in the task outbox until a route
// exists.
func (c *Core) handleDTNBundle(ctx context.Context, env bus.Envelope) {
	var p bus.DTNBundlePayload
	if err := env.PayloadInto(&p); err != nil || len(p.Blob) == 0 {
		c.logger.Warn("bad dtn_bundle", "from", env.From, "err", err)
		return
	}
	bnd, err := bus.UnmarshalBundle(p.Blob)
	if err != nil {
		c.logger.Warn("dtn_bundle: decode", "from", env.From, "err", err)
		return
	}
	if err := bnd.Verify([]byte(c.sharedSecret), time.Now().Unix()); err != nil {
		// Tampered or dead: drop without a reply — the origin's own deadline
		// sweep closes its copy, and a forged bundle earns no signal.
		c.logger.Warn("dtn_bundle: verify failed", "from", env.From,
			"bundle", bnd.BundleID, "err", err)
		return
	}
	src := strings.TrimPrefix(bnd.SourceEID, "panda://")
	dest := strings.TrimPrefix(bnd.DestEID, "panda://")

	if dest != c.nodeID {
		c.relayBundle(ctx, env.From, dest, bnd, p.Blob)
		return
	}

	// This node is the destination. Dedup on the bundle id: the same parked
	// row can legitimately arrive twice (hello flush + sweep flush racing),
	// and the inner task dedup would catch it — but only after paying the
	// handler. Replaying under a fresh outer msg_id is the whole point of the
	// check.
	inner := bus.Envelope{
		V: 1, Type: bnd.Kind, MsgID: bnd.BundleID,
		From: src, To: c.nodeID, TS: bnd.CreatedUnix,
		Payload: json.RawMessage(bnd.Payload),
	}
	if !c.claimMsgID(inner) {
		c.logger.Debug("dtn_bundle: duplicate bundle dropped", "bundle", bnd.BundleID, "src", src)
		return
	}
	switch bnd.Kind {
	case bus.MsgTaskDelegate:
		// env.From = src, not the relaying peer: the delegation chain ends at
		// the delegator, and the bundle signature — not the transport — is
		// what authenticates that identity on a relayed hop.
		c.handleDelegate(ctx, inner)
	default:
		c.logger.Warn("dtn_bundle: unsupported kind", "kind", bnd.Kind, "src", src)
	}
}

// relayBundle takes custody of a bundle addressed past this node (§9.1).
// Delivery preference: the destination itself when a link is up; otherwise
// the first hop of the cheapest advertised path toward it — the multi-hop
// case, where custody moves to an online node closer to the destination and
// waits there for the next contact window. The blob always crosses verbatim:
// the signature is the origin's, so a relay must not re-wrap. When no route
// exists the bundle is parked under its destination, recording via so the
// later flush never echoes it back the way it came.
func (c *Core) relayBundle(ctx context.Context, via, dest string, bnd *bus.Bundle, blob []byte) {
	if dest == "" || dest == c.nodeID {
		return
	}
	if c.connFor(dest) != nil && c.deliverBundle(ctx, dest, blob) {
		c.logger.Info("dtn_bundle: relayed direct", "bundle", bnd.BundleID, "via", via, "dest", dest)
		return
	}
	if hop := c.dtnNextHop(ctx, dest, map[string]bool{via: true}); hop != "" &&
		c.connFor(hop) != nil && c.relayForwardOK(bnd.BundleID, bnd.DeadlineUnix) {
		if c.deliverBundle(ctx, hop, blob) {
			c.logger.Info("dtn_bundle: relayed toward", "bundle", bnd.BundleID,
				"via", via, "hop", hop, "dest", dest)
			return
		}
	}
	c.parkBundle(ctx, via, dest, bnd, blob)
}

// parkBundle stores custody of a bundle whose route does not exist yet.
// task_id keys the row — the inner payload carries it for task_delegate
// bundles; via records the inbound peer so a flush never sends it straight
// back. The sweep (or a better hop's next hello) finishes the route.
func (c *Core) parkBundle(ctx context.Context, via, dest string, bnd *bus.Bundle, blob []byte) {
	var probe struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(bnd.Payload, &probe)
	taskID := probe.TaskID
	if taskID == "" {
		taskID = bnd.BundleID
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, payload_blob, transport_type, ttl, via, created_at)
		 VALUES (?, ?, ?, ?, 'dtn-relay', ?, ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET payload_blob = excluded.payload_blob, ttl = excluded.ttl`,
		dest, taskID, string(bnd.Payload), blob, bnd.DeadlineUnix, via, storage.Now()); err != nil {
		c.logger.Warn("dtn_bundle: relay park", "bundle", bnd.BundleID, "dest", dest, "err", err)
		return
	}
	c.logger.Info("dtn_bundle: custody parked for relay", "bundle", bnd.BundleID, "via", via, "dest", dest)
}

// dtnNextHop answers the store-and-forward routing question: which online
// neighbor's advertised path reaches closest to dest. The graph is the whole
// directory — offline rows too, because their last neighbor advertisement is
// still topology — while the hop itself must be online-and-connected, which
// the caller verifies through connFor. exclude is consulted at the first hop
// only, which is what makes it a no-echo rule rather than a path constraint.
func (c *Core) dtnNextHop(ctx context.Context, dest string, exclude map[string]bool) string {
	if c.db == nil {
		return ""
	}
	self, nodes, err := c.dtnDirectory()
	if err != nil {
		c.logger.Warn("dtn: query directory", "err", err)
		return ""
	}
	return scheduler.DTNNextHop(self, nodes, dest, exclude)
}

// dtnDirectory loads the mesh directory and resolves this node's row in one
// pass — the pair scheduler.DTNNextHop needs. relayParked takes it once per
// sweep rather than re-querying for every parked row.
func (c *Core) dtnDirectory() (ledger.Node, []ledger.Node, error) {
	nodes, err := ledger.Query(c.db, "", "")
	if err != nil {
		return ledger.Node{}, nil, err
	}
	var self ledger.Node
	for _, n := range nodes {
		if scheduler.IsSelfRow(n.ID, c.nodeID) {
			self = n
			break
		}
	}
	if self.ID == "" {
		self = ledger.Node{ID: c.nodeID}
	}
	return self, nodes, nil
}

// relayForwardOK bounds a node's forwards of one bundle to dtnRelayMaxHops —
// the in-memory loop bound that substitutes for the hop list a signed bundle
// cannot carry. Entries expire with the bundle's deadline so the map does
// not grow past the fleet's live custody set.
func (c *Core) relayForwardOK(bundleID string, deadline int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.relayLog == nil {
		c.relayLog = make(map[string]dtnRelay)
	}
	now := time.Now().Unix()
	for id, e := range c.relayLog {
		if e.until > 0 && now > e.until {
			delete(c.relayLog, id)
		}
	}
	e := c.relayLog[bundleID]
	if e.until == 0 {
		e.until = deadline
		if e.until == 0 {
			// A bundle may carry no deadline (0 = no expiry), but the relay
			// record still has to die or the map grows with every bundle id
			// ever seen. The default DTN lifetime is the bound parked tasks
			// already live under — reuse it here.
			e.until = now + int64(defaultDTNTTL.Seconds())
		}
	}
	if e.hops >= dtnRelayMaxHops {
		return false
	}
	e.hops++
	c.relayLog[bundleID] = e
	return true
}
