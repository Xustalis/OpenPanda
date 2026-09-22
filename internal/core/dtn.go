package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
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
// the destination when that link is up, or parking the signed blob in the
// task outbox until it is.
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

// relayBundle takes custody of a bundle addressed past this node (§9.1). A
// live link to the destination gets the blob verbatim — the signature is the
// origin's, so a relay must not re-wrap. Otherwise the bundle is parked in
// the same task outbox locally-originated work uses, where the sweep retries
// it until the TTL runs out.
func (c *Core) relayBundle(ctx context.Context, via, dest string, bnd *bus.Bundle, blob []byte) {
	if dest == "" || dest == c.nodeID {
		return
	}
	if c.connFor(dest) != nil && c.deliverBundle(ctx, dest, blob) {
		c.logger.Info("dtn_bundle: relayed", "bundle", bnd.BundleID, "via", via, "dest", dest)
		return
	}
	// Custody transfer: park under the destination so the periodic sweep (or
	// that peer's next hello) finishes the route. task_id keys the row — the
	// inner payload carries it for task_delegate bundles.
	var probe struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(bnd.Payload, &probe)
	taskID := probe.TaskID
	if taskID == "" {
		taskID = bnd.BundleID
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO task_outbox (peer, task_id, payload_json, payload_blob, transport_type, ttl, created_at)
		 VALUES (?, ?, ?, ?, 'dtn-relay', ?, ?)
		 ON CONFLICT(peer, task_id) DO UPDATE SET payload_blob = excluded.payload_blob, ttl = excluded.ttl`,
		dest, taskID, string(bnd.Payload), blob, bnd.DeadlineUnix, storage.Now()); err != nil {
		c.logger.Warn("dtn_bundle: relay park", "bundle", bnd.BundleID, "dest", dest, "err", err)
		return
	}
	c.logger.Info("dtn_bundle: custody parked for relay", "bundle", bnd.BundleID, "via", via, "dest", dest)
}
