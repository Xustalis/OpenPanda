// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"testing"
	"time"
)

// TestSetNodeVerIfChanged pins the cheap-beat contract: an empty hello leaves
// the row alone, a real ver lands, and a repeat write is a no-op the caller
// can issue every beat.
func TestSetNodeVerIfChanged(t *testing.T) {
	db := openLedgerDB(t)
	seedRow(t, db, "n1", "key1", "online", 100)

	if err := SetNodeVerIfChanged(db, "n1", ""); err != nil {
		t.Fatalf("empty ver: %v", err)
	}
	if err := SetNodeVerIfChanged(db, "n1", "0.0.11-beta"); err != nil {
		t.Fatalf("set ver: %v", err)
	}
	var ver string
	if err := db.QueryRow(`SELECT ver FROM employee_cache WHERE id='n1'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "0.0.11-beta" {
		t.Errorf("ver = %q", ver)
	}
	// An empty ver after a real stamp must not erase it (old-node hello).
	if err := SetNodeVerIfChanged(db, "n1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ver FROM employee_cache WHERE id='n1'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "0.0.11-beta" {
		t.Errorf("empty ver erased the stamp: %q", ver)
	}
}

// TestNodeStamps pins the digest contract: every row appears, in id order,
// with the status and freshness the digest inputs.
func TestNodeStamps(t *testing.T) {
	db := openLedgerDB(t)
	seedRow(t, db, "beta", "k", "offline", 55)
	seedRow(t, db, "alpha", "k2", "online", 99)
	seedRow(t, db, "gamma", "", "online", 0)

	stamps, err := NodeStamps(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps) != 3 {
		t.Fatalf("NodeStamps = %v", stamps)
	}
	if stamps[0].ID != "alpha" || stamps[1].ID != "beta" || stamps[2].ID != "gamma" {
		t.Errorf("order = %v,%v,%v", stamps[0].ID, stamps[1].ID, stamps[2].ID)
	}
	if stamps[0].Status != "online" || stamps[0].LastSeen != 99 {
		t.Errorf("alpha stamp = %+v", stamps[0])
	}
	if stamps[1].Status != "offline" || stamps[1].LastSeen != 55 {
		t.Errorf("beta stamp = %+v", stamps[1])
	}
}

// TestMarkVerified pins the TOFU gate: verifying a row with no proven key is
// refused — an empty pub_key row has nothing to have compared, and blessing
// a later first-seen key would attach trust to a key nobody looked at.
func TestMarkVerified(t *testing.T) {
	db := openLedgerDB(t)
	seedRow(t, db, "keyed", "abcdef", "online", 10)
	seedRow(t, db, "keyless", "", "online", 10)

	if ok, err := MarkVerified(db, "keyless"); err != nil || ok {
		t.Errorf("verify keyless = %v, %v; want refuse", ok, err)
	}
	if ok, err := MarkVerified(db, "keyed"); err != nil || !ok {
		t.Fatalf("verify keyed = %v, %v", ok, err)
	}
	var ts int64
	if err := db.QueryRow(`SELECT key_verified FROM employee_cache WHERE id='keyed'`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts == 0 {
		t.Error("key_verified not stamped")
	}
	if ok, err := MarkVerified(db, "missing"); err != nil || ok {
		t.Errorf("verify missing = %v, %v", ok, err)
	}
}

// TestSetPeerTrustByKey pins the identity-stable trust write: the tier lands
// on every row carrying the key (sibling rows from a restarted instance keep
// the zone from the start), and the return count distinguishes "stamped"
// from "no such identity in the directory".
func TestSetPeerTrustByKey(t *testing.T) {
	db := openLedgerDB(t)
	seedRow(t, db, "inst-a", "sharedkey", "online", 1)
	seedRow(t, db, "inst-b", "sharedkey", "offline", 2)
	seedRow(t, db, "other", "otherkey", "online", 3)

	n, err := SetPeerTrustByKey(db, "sharedkey", "guarded")
	if err != nil || n != 2 {
		t.Fatalf("SetPeerTrustByKey = %d rows, %v", n, err)
	}
	if level, _ := PeerTrust(db, "inst-a"); level != "guarded" {
		t.Errorf("inst-a trust = %q", level)
	}
	if level, _ := PeerTrust(db, "inst-b"); level != "guarded" {
		t.Errorf("inst-b trust = %q", level)
	}
	if level, _ := PeerTrust(db, "other"); level != "" {
		t.Errorf("unrelated row trust = %q", level)
	}
	if n, err := SetPeerTrustByKey(db, "nokey", "guarded"); err != nil || n != 0 {
		t.Errorf("unknown key = %d rows, %v", n, err)
	}
	if n, err := SetPeerTrustByKey(db, "", "guarded"); err != nil || n != 0 {
		t.Errorf("empty key = %d rows, %v", n, err)
	}
}

// TestPendingNodeFingerprint pins the 16-hex comparable prefix — the same
// form `panda nodes` prints, so an operator can eyeball-match the LAN hint
// against the paired identity before admitting it.
func TestPendingNodeFingerprint(t *testing.T) {
	p := PendingNode{PubKey: "0123456789abcdef0123"}
	if got := p.Fingerprint(); got != "0123456789abcdef" {
		t.Errorf("Fingerprint = %q", got)
	}
	short := PendingNode{PubKey: "abc"}
	if got := short.Fingerprint(); got != "abc" {
		t.Errorf("short Fingerprint = %q", got)
	}
	if got := (PendingNode{}).Fingerprint(); got != "" {
		t.Errorf("empty Fingerprint = %q", got)
	}
	exact := PendingNode{PubKey: "0123456789abcdef"}
	if got := exact.Fingerprint(); got != "0123456789abcdef" {
		t.Errorf("exact-16 Fingerprint = %q", got)
	}
}

// TestMarkOnlineOfflineRoundTrip pins the liveness writes the sweep and the
// connection lifecycle both rely on: offline stamps last_seen, online does
// not touch it.
func TestMarkOnlineOfflineRoundTrip(t *testing.T) {
	db := openLedgerDB(t)
	seedRow(t, db, "n1", "k", "online", 7)

	if err := MarkOffline(db, "n1"); err != nil {
		t.Fatal(err)
	}
	var status string
	var lastSeen int64
	if err := db.QueryRow(`SELECT status, last_seen FROM employee_cache WHERE id='n1'`).Scan(&status, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if status != "offline" {
		t.Errorf("status = %q", status)
	}
	if lastSeen <= 7 {
		t.Errorf("last_seen not refreshed: %d", lastSeen)
	}
	if err := MarkOnline(db, "n1"); err != nil {
		t.Fatal(err)
	}
	var lastSeen2 int64
	if err := db.QueryRow(`SELECT status, last_seen FROM employee_cache WHERE id='n1'`).Scan(&status, &lastSeen2); err != nil {
		t.Fatal(err)
	}
	if status != "online" || lastSeen2 != lastSeen {
		t.Errorf("online refresh touched last_seen: %d → %d", lastSeen, lastSeen2)
	}
}

// TestExpireStale pins the silence sweep: online rows past the cutoff flip
// offline and are returned; excluded ids (live connections) and never-beaten
// fixtures (last_seen=0) survive.
func TestExpireStale(t *testing.T) {
	db := openLedgerDB(t)
	old := time.Now().Unix() - 10000
	seedRow(t, db, "stale1", "k1", "online", old)
	seedRow(t, db, "stale2", "k2", "online", old)
	seedRow(t, db, "fresh", "k3", "online", time.Now().Unix())
	seedRow(t, db, "never", "k4", "online", 0)
	seedRow(t, db, "alreadyoff", "k5", "offline", old)

	flipped, err := ExpireStale(db, 60, []string{"stale2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(flipped) != 1 || flipped[0] != "stale1" {
		t.Errorf("flipped = %v", flipped)
	}
	check := func(id, want string) {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT status FROM employee_cache WHERE id=?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != want {
			t.Errorf("%s status = %q, want %q", id, s, want)
		}
	}
	check("stale1", "offline")
	check("stale2", "online") // excluded — live connection held
	check("fresh", "online")
	check("never", "online") // last_seen=0 fixtures untouched
	check("alreadyoff", "offline")
}
