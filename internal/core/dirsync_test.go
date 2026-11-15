// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// mintDirRow builds and signs a directory row for id, returning the
// canonical wire bytes and the signer's pub hex. mut customizes fields
// before signing.
func mintDirRow(t *testing.T, id string, seenAt int64, mut func(*bus.DirRow)) (json.RawMessage, string) {
	t.Helper()
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatal(err)
	}
	r := &bus.DirRow{ID: id, PubKey: hex.EncodeToString(pub), SeenAt: seenAt}
	if mut != nil {
		mut(r)
	}
	bus.SignDirRow(priv, r)
	return bus.CanonicalDirRowBytes(r), hex.EncodeToString(pub)
}

// stripSig post-processes a minted row: parse, mutate (typically clearing or
// corrupting the signature), and re-marshal. Needed because mintDirRow signs
// after `mut` — post-sign edits belong here.
func stripSig(t *testing.T, raw json.RawMessage, mut func(*bus.DirRow)) json.RawMessage {
	t.Helper()
	var r bus.DirRow
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	mut(&r)
	return bus.CanonicalDirRowBytes(&r)
}

// dirField reads one column for assertions.
func dirField(t *testing.T, c *Core, id, col string) string {
	t.Helper()
	var v string
	if err := c.db.QueryRow(`SELECT COALESCE(`+col+`,'') FROM employee_cache WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("read %s for %s: %v", col, id, err)
	}
	return v
}

func TestSelfDirRowSignedAndStored(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	ctx := context.Background()
	row, err := c.selfDirRow(time.Now())
	if err != nil {
		t.Fatalf("selfDirRow: %v", err)
	}
	if row.ID != "node-a" || row.Sig == "" || row.PubKey == "" || row.XPub == "" {
		t.Fatalf("self row incomplete: %+v", row)
	}
	if !bus.VerifyDirRow(row) {
		t.Fatal("self row fails its own signature")
	}
	c.refreshSelfDirRow(ctx)
	if got := dirField(t, c, "node-a", "dir_sig"); got == "" {
		t.Fatal("self row not stored in gossip layer")
	}
	var r bus.DirRow
	if err := json.Unmarshal([]byte(dirField(t, c, "node-a", "dir_row_json")), &r); err != nil {
		t.Fatalf("stored row unparsable: %v", err)
	}
	if !bus.VerifyDirRow(&r) {
		t.Fatal("stored self row lost its signature")
	}
}

func TestApplyDirRowSignedLands(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	ctx := context.Background()
	card, _ := json.Marshal(ledger.CapabilitySummary{
		Device: "ghost", NodeKind: "physical",
		NativeIDs: []string{"sys:info"}, Addrs: []string{"10.9.9.9:7836"},
	})
	raw, _ := mintDirRow(t, "ghost", time.Now().Unix(), func(r *bus.DirRow) {
		r.Card = card
		r.XPub = hex.EncodeToString(make([]byte, 32))
		r.Addrs = []string{"10.9.9.9:7836"}
	})
	if !c.applyDirRow(ctx, raw, "via-peer") {
		t.Fatal("signed fresh row rejected")
	}
	// Gossip may not mint the first identity pin for a non-stable id; the
	// direct hello is the only attestation that binds "ghost" to a key.
	if got := dirField(t, c, "ghost", "pub_key"); got != "" {
		t.Fatalf("gossip row wrote a first pub_key %q", got)
	}
	if got := dirField(t, c, "ghost", "dir_sig"); got == "" {
		t.Fatal("dir_sig not stored")
	}
	if got := dirField(t, c, "ghost", "learned_via"); got != "via-peer" {
		t.Fatalf("learned_via = %q", got)
	}
	if got := dirField(t, c, "ghost", "status"); got != "online" {
		t.Fatalf("fresh signed row status = %q, want online", got)
	}
	if got := dirField(t, c, "ghost", "name"); got != "ghost" {
		t.Fatalf("card not applied: name = %q", got)
	}
}

// TestApplyDirRowExpiredOnArrival pins the door rule for gossip that is
// already past its shelf life. PruneOffline deletes unpinned offline rows whose
// seen_at is older than dirRowTTL, and a pull is allowed to CREATE a missing
// row — so storing an expired row starts an endless loop: the sweep deletes it,
// the peer's next pull puts it back, and the console paints a dead machine for
// a few seconds every sync window. A row that would not survive the sweep must
// not be stored at all.
func TestApplyDirRowExpiredOnArrival(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	rows := func() int {
		var n int
		if err := c.db.QueryRow(`SELECT COUNT(*) FROM employee_cache WHERE id LIKE 'verify-%'`).Scan(&n); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		return n
	}
	expired := now - int64(dirRowTTL.Seconds()) - 60

	// Signed, but long past the TTL: the sweep would delete it on its next
	// pass, so the frame is refused rather than stored for deletion.
	raw, _ := mintDirRow(t, "verify-log@vm-805eed2f664a", expired, nil)
	if c.applyDirRow(ctx, raw, "peer") {
		t.Fatal("row expired on arrival was stored")
	}
	// The real shape of the loop: a legacy peer forwards rows with no seen_at
	// at all (0), unsigned, so last_seen stays 0 and every sweep prunes them.
	zero, _ := mintDirRow(t, "verify-inject@vm-7f07a623f5e1", 0, nil)
	zero = stripSig(t, zero, func(r *bus.DirRow) { r.Sig = "" })
	if c.applyDirRow(ctx, zero, "peer") {
		t.Fatal("unsigned row with no seen_at was stored")
	}
	if got := rows(); got != 0 {
		t.Fatalf("expired rows landed in the directory: %d rows", got)
	}

	// A fresh row still lands.
	fresh, _ := mintDirRow(t, "fresh", now, nil)
	if !c.applyDirRow(ctx, fresh, "peer") {
		t.Fatal("fresh row rejected")
	}
	// A stale row whose id self-certifies is exempt: the frame pins its key,
	// so the sweep keeps it and there is no loop to break.
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatal(err)
	}
	stable := &bus.DirRow{ID: stableIDPrefix + hex.EncodeToString(pub), PubKey: hex.EncodeToString(pub), SeenAt: expired}
	bus.SignDirRow(priv, stable)
	if !c.applyDirRow(ctx, bus.CanonicalDirRowBytes(stable), "peer") {
		t.Fatal("stale self-certifying row rejected")
	}
}

// TestApplyDirRowIdentityBinding pins the two identity rules: gossip cannot
// establish a first pin for a non-stable id, and a k:<pub> row must be signed
// by that same pub. Both close the impersonation path where a member binds a
// victim id to its own key and then passes transit's From check.
func TestApplyDirRowIdentityBinding(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	c := newCoreOnDB(t, openTestDB(t), "node-a")

	// 1. Non-stable id: signed row lands, but writes no pub_key.
	raw, _ := mintDirRow(t, "ghost", now, nil)
	if !c.applyDirRow(ctx, raw, "p") {
		t.Fatal("signed gossip row rejected")
	}
	if got := dirField(t, c, "ghost", "pub_key"); got != "" {
		t.Fatalf("gossip minted first pub_key %q", got)
	}

	// 2. A verified hello pins the id; a later gossip row with the SAME key
	//    may refresh the pin (the row's signature is still just self-claim,
	//    but it no longer introduces a new binding).
	helloPub, helloPriv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatal(err)
	}
	c.recordPeerPubKey(ctx, "hello-node", hex.EncodeToString(helloPub), "")
	refresh := &bus.DirRow{ID: "hello-node", PubKey: hex.EncodeToString(helloPub), SeenAt: now}
	bus.SignDirRow(helloPriv, refresh)
	if !c.applyDirRow(ctx, bus.CanonicalDirRowBytes(refresh), "p") {
		t.Fatal("same-key refresh after a hello pin rejected")
	}
	if got := dirField(t, c, "hello-node", "pub_key"); got != hex.EncodeToString(helloPub) {
		t.Fatalf("refresh lost the pinned key: %q", got)
	}

	// 3. Stable k:<pub> id signed by a different key is dropped outright.
	otherPub, otherPriv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatal(err)
	}
	bad := &bus.DirRow{ID: stableIDPrefix + hex.EncodeToString(helloPub), PubKey: hex.EncodeToString(otherPub), SeenAt: now}
	bus.SignDirRow(otherPriv, bad)
	if c.applyDirRow(ctx, bus.CanonicalDirRowBytes(bad), "p") {
		t.Fatal("k:<pub> row signed by a different key applied")
	}

	// 4. The matching stable row is accepted and pins its own key.
	good := &bus.DirRow{ID: stableIDPrefix + hex.EncodeToString(helloPub), PubKey: hex.EncodeToString(helloPub), SeenAt: now}
	bus.SignDirRow(helloPriv, good)
	if !c.applyDirRow(ctx, bus.CanonicalDirRowBytes(good), "p") {
		t.Fatal("self-certifying stable row rejected")
	}
	if got := dirField(t, c, stableIDPrefix+hex.EncodeToString(helloPub), "pub_key"); got != hex.EncodeToString(helloPub) {
		t.Fatalf("stable row did not pin its key: %q", got)
	}
}

// TestDirRowsLimits pins the inbound flood bounds: per-sender frame budget
// and the new-id cap shared by one frame. Existing rows remain refreshable
// even when the directory is full.
func TestDirRowsLimits(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	ctx := context.Background()
	for i := 0; i < dirRowsBurst; i++ {
		if !c.dirRowsAllowed("peer") {
			t.Fatalf("frame %d denied inside the burst", i)
		}
	}
	if c.dirRowsAllowed("peer") {
		t.Fatal("frame beyond the burst allowed")
	}
	if !c.dirRowsAllowed("other") {
		t.Fatal("a different sender has its own budget")
	}

	// A full frame budget refuses a new id but lets an existing row refresh.
	raw, _ := mintDirRow(t, "fresh", time.Now().Unix(), nil)
	room := 0
	if c.applyDirRowLimited(ctx, raw, "p", &room) {
		t.Fatal("new id applied with no room left")
	}
	seed, _ := mintDirRow(t, "existing", time.Now().Unix()-10, nil)
	if !c.applyDirRow(ctx, seed, "p") {
		t.Fatal("seed row rejected")
	}
	refresh, _ := mintDirRow(t, "existing", time.Now().Unix(), nil)
	room = 0
	if !c.applyDirRowLimited(ctx, refresh, "p", &room) {
		t.Fatal("existing row refresh must not consume new-id room")
	}

	// Per-sender new-id budget: the reservation is taken under the lock, so
	// exhausting it closes the window's room and only a rolled window reopens
	// it.
	if got := c.dirNewRowsReserve("peer", dirRowsNewPerWindow); got != dirRowsNewPerWindow {
		t.Fatalf("reserved %d slots, want %d", got, dirRowsNewPerWindow)
	}
	if got := c.dirNewRowsReserve("peer", dirRowsNewPerWindow); got != 0 {
		t.Fatalf("reserved %d slots from an exhausted budget, want 0", got)
	}
	// An unused reservation is refunded, so a frame that carried no new id
	// does not burn the sender's window.
	c.dirNewRowsRefund("peer", dirRowsNewPerWindow)
	if got := c.dirNewRowsReserve("peer", dirRowsNewPerWindow); got != dirRowsNewPerWindow {
		t.Fatalf("new-id budget after refund = %d, want %d", got, dirRowsNewPerWindow)
	}
	// Roll the window: the next reservation starts a fresh one.
	c.dirNewRowsRefund("peer", dirRowsNewPerWindow)
	c.dir.mu.Lock()
	c.dir.rowsNewAt["peer"] = time.Now().Add(-2 * dirRowsEvery)
	c.dir.mu.Unlock()
	if got := c.dirNewRowsReserve("peer", dirRowsNewPerWindow); got != dirRowsNewPerWindow {
		t.Fatalf("new-id budget after window roll = %d, want %d", got, dirRowsNewPerWindow)
	}
}

func TestApplyDirRowConflicts(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	mkCore := func() *Core { return newCoreOnDB(t, openTestDB(t), "node-a") }

	t.Run("signed beats unsigned", func(t *testing.T) {
		c := mkCore()
		// unsigned first
		raw, _ := mintDirRow(t, "ghost", now-10, nil)
		raw = stripSig(t, raw, func(r *bus.DirRow) { r.Sig = "" })
		if !c.applyDirRow(ctx, raw, "p") {
			t.Fatal("unsigned row not applied")
		}
		// older signed still wins over newer unsigned
		signed, _ := mintDirRow(t, "ghost", now-20, nil)
		if !c.applyDirRow(ctx, signed, "p") {
			t.Fatal("signed row lost to unsigned incumbent")
		}
		if got := dirField(t, c, "ghost", "dir_sig"); got == "" {
			t.Fatal("unsigned incumbent survived")
		}
		// newer unsigned cannot displace the signed row
		raw2, _ := mintDirRow(t, "ghost", now+50, nil)
		raw2 = stripSig(t, raw2, func(r *bus.DirRow) { r.Sig = "" })
		if c.applyDirRow(ctx, raw2, "p") {
			t.Fatal("unsigned challenger displaced signed row")
		}
	})

	t.Run("newer seen_at wins same signedness", func(t *testing.T) {
		c := mkCore()
		old, _ := mintDirRow(t, "ghost", now-100, func(r *bus.DirRow) { r.Ver = "0.0.1" })
		if !c.applyDirRow(ctx, old, "p") {
			t.Fatal("first signed row rejected")
		}
		stale, _ := mintDirRow(t, "ghost", now-200, func(r *bus.DirRow) { r.Ver = "0.0.0" })
		if c.applyDirRow(ctx, stale, "p") {
			t.Fatal("stale row displaced newer incumbent")
		}
	})

	t.Run("forged sig drops", func(t *testing.T) {
		c := mkCore()
		raw, _ := mintDirRow(t, "ghost", now, nil)
		raw = stripSig(t, raw, func(r *bus.DirRow) {
			r.Sig = hex.EncodeToString(make([]byte, ed25519.SignatureSize))
		})
		if c.applyDirRow(ctx, raw, "p") {
			t.Fatal("row with garbage signature applied")
		}
		var n int
		if err := c.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id='ghost'`).Scan(&n); err != nil || n != 0 {
			t.Fatal("forged row stored")
		}
	})

	t.Run("pub contradicts pinned drops", func(t *testing.T) {
		c := mkCore()
		// A verified hello pinned 'ghost' to pub A.
		_, pubA, _ := bus.GenerateNodeKey()
		c.recordPeerPubKey(ctx, "ghost", hex.EncodeToString(pubA), "")
		// A signed row claiming pub B for the same id is a rekey claim
		// gossip cannot adjudicate — it must drop, not rekey.
		raw, _ := mintDirRow(t, "ghost", now, nil)
		if c.applyDirRow(ctx, raw, "p") {
			t.Fatal("contradicting-pub row applied")
		}
		if got := dirField(t, c, "ghost", "pub_key"); got != hex.EncodeToString(pubA) {
			t.Fatal("pinned pub_key was rotated by gossip")
		}
	})

	t.Run("self and session ids drop", func(t *testing.T) {
		c := mkCore()
		self, _ := mintDirRow(t, "node-a", now, nil)
		if c.applyDirRow(ctx, self, "p") {
			t.Fatal("row about ourselves applied")
		}
		sess, _ := mintDirRow(t, "laptop-1a2b3c4d", now, nil)
		if c.applyDirRow(ctx, sess, "p") {
			t.Fatal("session row applied")
		}
	})

	t.Run("unsigned never writes identity", func(t *testing.T) {
		c := mkCore()
		raw, pub := mintDirRow(t, "ghost", now, nil)
		raw = stripSig(t, raw, func(r *bus.DirRow) { r.Sig = "" })
		if !c.applyDirRow(ctx, raw, "p") {
			t.Fatal("unsigned row not applied")
		}
		if got := dirField(t, c, "ghost", "pub_key"); got != "" {
			t.Fatalf("unsigned row wrote pub_key %q (claimed %q)", got, pub)
		}
	})

	t.Run("unsigned never displaces a live row", func(t *testing.T) {
		c := mkCore()
		// A peer we learned from a direct hello: the live columns are ours,
		// the gossip layer is empty (no dir_sig, no seen_at).
		if _, err := c.db.Exec(`INSERT INTO employee_cache (id, name, addrs, status, last_seen)
			VALUES ('victim', 'victim-node', '["10.0.0.9:7836"]', 'online', ?)`,
			time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		// An unsigned gossip row for the same id, with a fresher seen_at: it
		// must not win, or any member could rewrite a peer's card and addrs
		// in the directory.
		card, _ := json.Marshal(ledger.CapabilitySummary{Device: "attacker", Addrs: []string{"10.6.6.6:7836"}})
		raw, _ := mintDirRow(t, "victim", time.Now().Unix()+300, func(r *bus.DirRow) {
			r.Card = card
			r.Addrs = []string{"10.6.6.6:7836"}
		})
		raw = stripSig(t, raw, func(r *bus.DirRow) { r.Sig = "" })
		if c.applyDirRow(ctx, raw, "p") {
			t.Fatal("unsigned row displaced a live directory row")
		}
		if got := dirField(t, c, "victim", "name"); got != "victim-node" {
			t.Fatalf("live row's card was rewritten: name = %q", got)
		}
		if got := dirField(t, c, "victim", "addrs"); got != `["10.0.0.9:7836"]` {
			t.Fatalf("live row's addrs were rewritten: %q", got)
		}
	})

	t.Run("future seen_at clamps", func(t *testing.T) {
		c := mkCore()
		// Use a fresh clock: the outer `now` predates the earlier subtests,
		// and under -race the gap can exceed the two-second slack.
		base := time.Now().Unix()
		raw, _ := mintDirRow(t, "ghost", base+3600, nil)
		if !c.applyDirRow(ctx, raw, "p") {
			t.Fatal("future row rejected instead of clamped")
		}
		var seen int64
		if err := c.db.QueryRow(`SELECT seen_at FROM employee_cache WHERE id='ghost'`).Scan(&seen); err != nil {
			t.Fatal(err)
		}
		if seen > base+int64(dirFutureSkew.Seconds())+2 {
			t.Fatalf("seen_at %d exceeded the clamp", seen)
		}
	})
}

func TestDirStampsAndServedRows(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	ctx := context.Background()
	// A gossip-layer row (signed, stored by apply) hashes from its verbatim
	// bytes; a column-only row synthesizes unsigned; a session row never
	// enters the table at all.
	raw, _ := mintDirRow(t, "signed-node", time.Now().Unix(), nil)
	c.applyDirRow(ctx, raw, "p")
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, name, status, last_seen, neighbors_json)
		VALUES ('plain-node', 'plain', 'online', ?, '["x"]')`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, status) VALUES ('laptop-1a2b3c4d','online')`); err != nil {
		t.Fatal(err)
	}
	stamps, err := c.dirStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stamps["signed-node"]; !ok {
		t.Fatal("signed row missing from stamps")
	}
	if _, ok := stamps["plain-node"]; !ok {
		t.Fatal("plain row missing from stamps")
	}
	if _, ok := stamps["laptop-1a2b3c4d"]; ok {
		t.Fatal("session row leaked into stamps")
	}
	// dirRowBytes serves verbatim for the signed row (sig survives).
	b, ok := c.dirRowBytes(ctx, "signed-node")
	if !ok {
		t.Fatal("signed row not served")
	}
	var r bus.DirRow
	if err := json.Unmarshal(b, &r); err != nil || !bus.VerifyDirRow(&r) {
		t.Fatal("served signed row does not verify")
	}
	// …and unsigned synthesis for the plain row.
	b, ok = c.dirRowBytes(ctx, "plain-node")
	if !ok {
		t.Fatal("plain row not served")
	}
	var r2 bus.DirRow
	if err := json.Unmarshal(b, &r2); err != nil || r2.Sig != "" {
		t.Fatal("synthesized row should be unsigned")
	}
	// Serving a session row refuses.
	if _, ok := c.dirRowBytes(ctx, "laptop-1a2b3c4d"); ok {
		t.Fatal("session row served")
	}
}

func TestDirPullRateLimits(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	// Outbound: one fresh pull allowed, second inside 30s blocked,
	// continuations bounded by page cap.
	if !c.dirPullAllowed("peer", "") {
		t.Fatal("first fresh pull denied")
	}
	if c.dirPullAllowed("peer", "") {
		t.Fatal("second fresh pull inside 30s allowed")
	}
	for i := 0; i < dirPullMaxPages; i++ {
		if !c.dirPullAllowed("peer", "cursor") {
			t.Fatalf("continuation %d denied", i)
		}
	}
	if c.dirPullAllowed("peer", "cursor") {
		t.Fatal("continuation beyond page cap allowed")
	}
	// Responder side mirrors the same budget.
	c2 := newCoreOnDB(t, openTestDB(t), "node-b")
	if !c2.dirAnswerAllowed("peer", "") {
		t.Fatal("first inbound pull denied")
	}
	if c2.dirAnswerAllowed("peer", "") {
		t.Fatal("second inbound pull inside 30s allowed")
	}
}

// TestDirSyncEndToEnd: A holds a signed row for a third node; B's pull must
// learn it — signature verified on arrival — and the want direction must
// carry B's rows back to A, all over the live WS edge.
func TestDirSyncEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	a := newCore(t, "da")
	b := newCore(t, "db")
	ad, aAddr := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, b, a, aAddr, ad)

	// Seed a signed third-party row into A's directory. Identity pins travel
	// only for self-certifying stable ids; a device-name id would need a
	// direct hello to establish its key.
	card, _ := json.Marshal(ledger.CapabilitySummary{Device: "ghost", NodeKind: "physical", NativeIDs: []string{"sys:info"}})
	raw, gpub := mintDirRow(t, "placeholder", time.Now().Unix(), func(r *bus.DirRow) {
		r.ID = stableIDPrefix + r.PubKey
		r.Card = card
		r.XPub = hex.EncodeToString(make([]byte, 32))
	})
	ghostID := stableIDPrefix + gpub
	if !a.applyDirRow(ctx, raw, "seed") {
		t.Fatal("seed row rejected")
	}

	// B pulls; the exchange runs over the live conn.
	b.sendDirPull("da", "")
	pollPub := func() string {
		var v string
		_ = b.db.QueryRow(`SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id=?`, ghostID).Scan(&v)
		return v
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pollPub() != gpub {
		time.Sleep(30 * time.Millisecond)
	}
	if got := pollPub(); got != gpub {
		t.Fatalf("B never learned ghost's key: %q", got)
	}
	if got := dirField(t, b, ghostID, "learned_via"); got != "da" {
		t.Fatalf("learned_via = %q, want da", got)
	}
	if got := dirField(t, b, ghostID, "dir_sig"); got == "" {
		t.Fatal("row lost its signature in transit")
	}
	// Want direction: A should have learned B's self row back.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := a.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id='db' AND dir_sig != ''`).Scan(&n); err == nil && n == 1 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id='db' AND dir_sig != ''`).Scan(&n)
	if n != 1 {
		t.Fatal("A never received B's signed row via want")
	}
}

// TestDirRowShareAttributesByAuthor pins the share accounting: signed rows
// belong to the key that signed them (not the relay that delivered them), and
// unsigned rows belong to the sender's stable identity, so a reconnection
// under a fresh session id does not mint a fresh allowance.
func TestDirRowShareAttributesByAuthor(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	c := newCoreOnDB(t, openTestDB(t), "node-a")

	relayPub, _, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatal(err)
	}
	relayPubHex := hex.EncodeToString(relayPub)
	seedPeerPubKey(t, c, "relay", relayPubHex)

	// A row authored by someone else, delivered by the relay.
	raw, authorPub := mintDirRow(t, "author", now, nil)
	if !c.applyDirRow(ctx, raw, "relay") {
		t.Fatal("signed row rejected")
	}
	seedPeerPubKey(t, c, "author", authorPub)

	if _, share := c.dirRowBudget(ctx, "relay"); share != 0 {
		t.Fatalf("relay charged %d for a row it only delivered", share)
	}
	if _, share := c.dirRowBudget(ctx, "author"); share != 1 {
		t.Fatalf("author share = %d, want 1", share)
	}

	// Unsigned rows have no author: they are charged to the sender.
	unsigned, _ := mintDirRow(t, "ghost", now, nil)
	unsigned = stripSig(t, unsigned, func(r *bus.DirRow) { r.Sig = "" })
	if !c.applyDirRow(ctx, unsigned, "relay") {
		t.Fatal("unsigned row rejected")
	}
	if _, share := c.dirRowBudget(ctx, "relay"); share != 1 {
		t.Fatalf("relay share after its unsigned row = %d, want 1", share)
	}

	// The same identity reconnecting under a new session id keeps accruing to
	// the stable key rather than starting over.
	seedPeerPubKey(t, c, "relay-1234abcd", relayPubHex)
	rotated, _ := mintDirRow(t, "ghost2", now, nil)
	rotated = stripSig(t, rotated, func(r *bus.DirRow) { r.Sig = "" })
	if !c.applyDirRow(ctx, rotated, "relay-1234abcd") {
		t.Fatal("unsigned row via rotated session rejected")
	}
	if _, share := c.dirRowBudget(ctx, "relay"); share != 2 {
		t.Fatalf("relay share after a session rotation = %d, want 2", share)
	}
}

// TestDirStampsMemoInvalidatesOnRowChange pins the memo's invalidation key:
// a refresh rewrites the stored bytes, and the next fold must hash the new
// content instead of serving the memoized value.
func TestDirStampsMemoInvalidatesOnRowChange(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "node-a")
	ctx := context.Background()
	now := time.Now().Unix()
	raw, _ := mintDirRow(t, "peer", now, nil)
	if !c.applyDirRow(ctx, raw, "p") {
		t.Fatal("seed row rejected")
	}
	first, err := c.dirStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := mintDirRow(t, "peer", now+1, func(r *bus.DirRow) {
		r.Card = json.RawMessage(`{"device":"changed"}`)
	})
	if !c.applyDirRow(ctx, changed, "p") {
		t.Fatal("refresh rejected")
	}
	second, err := c.dirStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first["peer"] == second["peer"] {
		t.Fatal("memoized hash survived a content change")
	}
}
