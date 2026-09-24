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
	// check. The payload is opened here, not at the relay: version-2 bundles
	// carry ciphertext whose AAD is the signed header, so an opened payload
	// is bound to exactly this destination and lifetime.
	payload, err := bnd.Open([]byte(c.sharedSecret))
	if err != nil {
		c.logger.Warn("dtn_bundle: open payload failed", "from", env.From,
			"bundle", bnd.BundleID, "err", err)
		return
	}
	inner := bus.Envelope{
		V: 1, Type: bnd.Kind, MsgID: bnd.BundleID,
		From: src, To: c.nodeID, TS: bnd.CreatedUnix,
		Payload: json.RawMessage(payload),
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
	if c.sendableTo(dest) && c.deliverBundle(ctx, dest, blob) {
		c.logger.Info("dtn_bundle: relayed direct", "bundle", bnd.BundleID, "via", via, "dest", dest)
		return
	}
	now := time.Now().Unix()
	expiresAt := bnd.LocalExpiry(now)
	if hop, _ := c.dtnNextHop(ctx, dest, map[string]bool{via: true}, int64(len(blob)), expiresAt); hop != "" &&
		c.sendableTo(hop) && c.relayForwardOK(ctx, bnd.BundleID, expiresAt) {
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
	// task_id lives in the opened payload. A relay holds the mesh secret so
	// opening is always possible; a payload that will not open fails closed
	// rather than parking a row keyed wrong.
	payload, err := bnd.Open([]byte(c.sharedSecret))
	if err != nil {
		c.logger.Warn("dtn_bundle: relay park open failed", "bundle", bnd.BundleID, "err", err)
		return
	}
	_ = json.Unmarshal(payload, &probe)
	taskID := probe.TaskID
	if taskID == "" {
		taskID = bnd.BundleID
	}
	// The upsert carries via too: the same bundle may arrive again over a
	// different path (a relay parked it, the topology shifted, it bounced
	// back) and the no-echo rule must exclude the peer it came from LAST,
	// not the first sender on record. payload_json stays empty: the blob is
	// the authority, and a relay's disk should not keep a plaintext copy of
	// work it only holds in custody. The ttl column is this node's own
	// expiry — custody runs on the holder's clock, not the origin's.
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, payload_blob, transport_type, ttl, via, created_at)
		 VALUES (?, ?, '', ?, 'dtn-relay', ?, ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET payload_blob = excluded.payload_blob, ttl = excluded.ttl, via = excluded.via`,
		dest, taskID, blob, bnd.LocalExpiry(time.Now().Unix()), via, storage.Now()); err != nil {
		c.logger.Warn("dtn_bundle: relay park", "bundle", bnd.BundleID, "dest", dest, "err", err)
		return
	}
	c.logger.Info("dtn_bundle: custody parked for relay", "bundle", bnd.BundleID, "via", via, "dest", dest)
}

// dtnNextHop answers the store-and-forward routing question: which neighbor's
// advertised path delivers a bundle to dest earliest, and when. When any row
// advertises a contact plan — or this node has one configured — the
// schedule-aware search runs: a sleeping node that published windows is a
// valid custody hop even while offline, and the ETA bounds how long a
// forward can still wait. With no plan anywhere the live-topology search
// answers exactly as before (eta 0). sizeBytes feeds each window's fit
// check; expiresAt prunes paths that would arrive after the bundle's
// lifetime lapses. exclude applies at the first hop only — the no-echo rule.
func (c *Core) dtnNextHop(ctx context.Context, dest string, exclude map[string]bool, sizeBytes, expiresAt int64) (string, int64) {
	if c.db == nil {
		return "", 0
	}
	self, nodes, err := c.dtnDirectory()
	if err != nil {
		c.logger.Warn("dtn: query directory", "err", err)
		return "", 0
	}
	if !c.dtnPlanActive(nodes) {
		return scheduler.DTNNextHop(self, nodes, dest, exclude), 0
	}
	// The local config is authoritative for this node's own windows — the
	// directory row lags until refreshSelfNeighbors lands it.
	if len(c.contacts) > 0 {
		self.Contacts = c.contacts
	}
	return scheduler.ContactNextHop(self, nodes, dest, exclude, time.Now().Unix(), sizeBytes, expiresAt)
}

// dtnPlanActive reports whether the contact graph has any plan to route by:
// this node's configured windows, or any directory row's advertisement.
func (c *Core) dtnPlanActive(nodes []ledger.Node) bool {
	if len(c.contacts) > 0 {
		return true
	}
	for _, n := range nodes {
		if len(n.Contacts) > 0 {
			return true
		}
	}
	return false
}

// sendableTo reports whether any track can carry an envelope to peer right
// now: a WS conn, or a punched datagram route. The second case is what lets
// a peer that only exists inside a contact window still collect its parked
// bundles while the window is up.
func (c *Core) sendableTo(peer string) bool {
	return c.connFor(peer) != nil || c.udpRouteFor(peer) != nil
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
// the loop bound that substitutes for the hop list a signed bundle cannot
// carry. The count lives in dtn_relay_log so a restart cannot re-arm it: a
// rebooted node that forgot its spends could otherwise be ping-ponged by a
// pair of peers until the lifetime ran out, which is exactly the loop the
// bound exists to kill. until is the caller-computed local expiry — relay
// records live under this node's clock (see Bundle.LocalExpiry). The
// in-memory map remains only as the no-database fallback.
func (c *Core) relayForwardOK(ctx context.Context, bundleID string, until int64) bool {
	now := time.Now().Unix()
	if c.db != nil {
		return c.relayForwardOKDB(ctx, bundleID, until, now)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.relayLog == nil {
		c.relayLog = make(map[string]dtnRelay)
	}
	for id, e := range c.relayLog {
		if e.until > 0 && now > e.until {
			delete(c.relayLog, id)
		}
	}
	e := c.relayLog[bundleID]
	if e.until == 0 {
		e.until = until
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

// relayForwardOKDB spends one forward of bundleID atomically against the
// persisted bound. Two statements cover every case: the UPDATE spends a hop
// only on a live row that still has budget; the INSERT seeds a fresh record —
// or, on conflict, resurrects a dead one — only when no live record exists.
// A live row at the bound fails both writes and the bundle must park. A DB
// error fails closed: a node whose bound ledger is unreadable must not
// forward, same posture as an unverifiable bundle.
func (c *Core) relayForwardOKDB(ctx context.Context, bundleID string, until, now int64) bool {
	if until <= 0 {
		until = now + int64(defaultDTNTTL.Seconds())
	}
	// Opportunistic expiry keeps the table no larger than the live custody
	// set; the per-call cost is one indexed delete on a tiny table.
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM dtn_relay_log WHERE until < ?`, now); err != nil {
		c.logger.Warn("dtn: relay log expiry", "err", err)
	}
	res, err := c.db.ExecContext(ctx,
		`UPDATE dtn_relay_log SET hops = hops + 1 WHERE bundle_id = ? AND hops < ? AND until >= ?`,
		bundleID, dtnRelayMaxHops, now)
	if err != nil {
		c.logger.Warn("dtn: relay bound spend", "bundle", bundleID, "err", err)
		return false
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true
	}
	res, err = c.db.ExecContext(ctx,
		`INSERT INTO dtn_relay_log (bundle_id, hops, until) VALUES (?, 1, ?)
		 ON CONFLICT(bundle_id) DO UPDATE SET hops = 1, until = excluded.until
		 WHERE dtn_relay_log.until < ?`,
		bundleID, until, now)
	if err != nil {
		c.logger.Warn("dtn: relay bound seed", "bundle", bundleID, "err", err)
		return false
	}
	// Rows affected is the verdict: a fresh insert or a resurrected dead row
	// writes 1; a live row already at the bound makes the conflict-update's
	// WHERE false and writes 0 — that is the refusal.
	if n, _ := res.RowsAffected(); n > 0 {
		return true
	}
	return false
}
