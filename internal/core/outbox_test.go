package core

import (
	"context"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// newCoreOnDB builds a Core over an existing store — the restart-continuity
// tests reuse one db across two Core instances to simulate a node that comes
// back with a new instance id but the same persisted Ed25519 identity.
func newCoreOnDB(t *testing.T, db *sql.DB, id string) *Core {
	t.Helper()
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, verboseTestLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	return c
}

// seedPeerPubKey writes a directory row the way an accepted signed hello
// would — the claim boundary tests need (instance, pub) pairs without
// standing up a transport.
func seedPeerPubKey(t *testing.T, c *Core, id, pub string) {
	t.Helper()
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, key_verified, status, last_seen)
		 VALUES (?, ?, 0, 'offline', 0)
		 ON CONFLICT(id) DO UPDATE SET pub_key = excluded.pub_key`, id, pub); err != nil {
		t.Fatalf("seed pubkey: %v", err)
	}
}

func TestStablePeerIDResolution(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-outbox", "")

	// Unknown peer and a peer with no recorded key: the instance id is its
	// own stable key (pre-key peer behavior).
	if got := c.stablePeerID(ctx, "stranger"); got != "stranger" {
		t.Fatalf("stablePeerID unknown = %q", got)
	}
	seedPeerPubKey(t, c, "keyless", "")
	if got := c.stablePeerID(ctx, "keyless"); got != "keyless" {
		t.Fatalf("stablePeerID keyless = %q", got)
	}

	pubB := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	seedPeerPubKey(t, c, "b-inst-1", pubB)
	want := stableIDPrefix + pubB
	if got := c.stablePeerID(ctx, "b-inst-1"); got != want {
		t.Fatalf("stablePeerID = %q, want %q", got, want)
	}
	// Round-trip: the stable key resolves to the current instance; a stable
	// input to stablePeerID passes through untouched.
	if got := c.instanceForStable(ctx, want); got != "b-inst-1" {
		t.Fatalf("instanceForStable = %q", got)
	}
	if got := c.stablePeerID(ctx, want); got != want {
		t.Fatalf("stablePeerID(stable) = %q", got)
	}
	if got := c.instanceForStable(ctx, stableIDPrefix+"deadbeef"); got != "" {
		t.Fatalf("instanceForStable unresolvable = %q, want ''", got)
	}
}

// A restarted node (same db → same identity, new instance id) must claim
// custody parked under BOTH its stable key and its previous instance id.
func TestOutboxClaimKeysAcrossInstances(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-outbox", "")

	pubB := hex.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	seedPeerPubKey(t, c, "b-inst-1", pubB)
	seedPeerPubKey(t, c, "b-inst-2", pubB) // the restart's new instance

	keys := c.claimKeys(ctx, c.stablePeerID(ctx, "b-inst-2"), "b-inst-2")
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	for _, want := range []string{stableIDPrefix + pubB, "b-inst-1", "b-inst-2"} {
		if !have[want] {
			t.Fatalf("claimKeys missing %q: %v", want, keys)
		}
	}
	// A foreign identity claims nothing of B's set.
	pubC := hex.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"))
	seedPeerPubKey(t, c, "c-inst", pubC)
	foreign := c.claimKeys(ctx, c.stablePeerID(ctx, "c-inst"), "c-inst")
	for _, k := range foreign {
		if have[k] {
			t.Fatalf("foreign claim overlaps B custody: %v vs %v", foreign, keys)
		}
	}
}

// An instance whose proven key does NOT match a row's stable key can never
// claim it — even if it reuses the victim's old instance name (the directory
// records only keys a hello actually proved).
func TestOutboxFlushRejectsForeignIdentity(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-outbox", "")

	pubB := hex.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1"))
	seedPeerPubKey(t, c, "victim", pubB)
	if _, err := c.db.Exec(
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES (?, 't1', '{}', 1)`,
		stableIDPrefix+pubB); err != nil {
		t.Fatalf("park: %v", err)
	}

	// Impostor takes the victim's name but proves a different key: the
	// directory re-keys the row to the impostor's pub, so its claim set no
	// longer reaches the parked custody.
	pubX := hex.EncodeToString([]byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx2"))
	seedPeerPubKey(t, c, "victim", pubX)
	c.outboxFlush(ctx, "victim")
	time.Sleep(300 * time.Millisecond)
	var n int
	if err := c.db.QueryRow(`SELECT count(*) FROM result_outbox WHERE peer = ?`,
		stableIDPrefix+pubB).Scan(&n); err != nil || n != 1 {
		t.Fatalf("foreign instance claimed custody (n=%d, err=%v)", n, err)
	}

	// The real owner returns (proves pubB again): custody becomes claimable.
	seedPeerPubKey(t, c, "victim", pubB)
	if got := c.stablePeerID(ctx, "victim"); got != stableIDPrefix+pubB {
		t.Fatalf("restored stable = %q", got)
	}
}

func TestTaskOutboxPersistAndDrop(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-outbox", "")

	p := bus.TaskDelegatePayload{
		TaskID: "task-dtn-1",
		Title:  "offline delegation",
		Intent: "run job",
	}

	// Persist to outbox
	c.taskOutboxPersist(ctx, "target-peer", p, "dtn", 60000)

	// Verify row exists in task_outbox
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"target-peer", "task-dtn-1").Scan(&count)
	if err != nil {
		t.Fatalf("query task_outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row in task_outbox, got %d", count)
	}

	// Drop from outbox
	c.taskOutboxDrop(ctx, "target-peer", "task-dtn-1")

	err = c.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"target-peer", "task-dtn-1").Scan(&count)
	if err != nil {
		t.Fatalf("query task_outbox after drop: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows in task_outbox after drop, got %d", count)
	}
}

func TestTaskOutboxFlushOnHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17961")
	b := newCore(t, "node-b", "127.0.0.1:17962")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(a.Register(ctx))
	must(b.Register(ctx))

	go func() { _ = a.Listen(ctx, "127.0.0.1:17961") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17962") }()
	time.Sleep(100 * time.Millisecond)

	// Park task in a's outbox for b BEFORE b connects
	taskID := "task-parked-1"
	p := bus.TaskDelegatePayload{
		TaskID: taskID,
		Title:  "delayed task",
		Intent: "dtn delayed delivery",
	}
	a.taskOutboxPersist(ctx, "node-b", p, "dtn", 60000)

	// Now connect a -> b
	must(a.DialPeer(ctx, "127.0.0.1:17962"))
	waitPeer(t, a, "node-b")
	waitPeer(t, b, "node-a")
	time.Sleep(300 * time.Millisecond)

	// Verify task_outbox row in a was flushed and dropped
	var count int
	_ = a.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"node-b", taskID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected task_outbox to be flushed and empty, but got %d", count)
	}
}

// Batch-6 restart continuity, end to end: a result parked for b's FIRST
// instance is claimed and delivered to b's SECOND instance — the two Cores
// share one database, so they carry the same persisted Ed25519 identity
// under different instance ids, exactly like a daemon that restarted. Before
// the stable-key migration the row would have stayed keyed to a dead
// instance id until its own TTL buried it.
func TestOutboxFlushAfterPeerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17971")
	bDB := openTestDB(t)
	b1 := newCoreOnDB(t, bDB, "b-inst-1")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(a.Register(ctx))
	must(b1.Register(ctx))
	go func() { _ = a.Listen(ctx, "127.0.0.1:17971") }()
	time.Sleep(100 * time.Millisecond)

	must(b1.DialPeer(ctx, "127.0.0.1:17971"))
	waitPeer(t, a, "b-inst-1")

	// a must have recorded b's proven key before the park is keyed stably.
	var pubB string
	for deadline := time.Now().Add(5 * time.Second); ; {
		_ = a.db.QueryRowContext(ctx,
			`SELECT pub_key FROM employee_cache WHERE id = 'b-inst-1'`).Scan(&pubB)
		if pubB != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer key never recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}

	a.outboxPersist(ctx, "b-inst-1", bus.TaskResultPayload{TaskID: "t-restart", OK: true})
	a.outboxCancelPersist(ctx, "b-inst-1", "t-restart-cancel", "user aborted")

	// Custody is keyed by the stable identity, not the instance name.
	var storedKey string
	if err := a.db.QueryRowContext(ctx,
		`SELECT peer FROM result_outbox WHERE task_id = 't-restart'`).Scan(&storedKey); err != nil {
		t.Fatalf("read parked row: %v", err)
	}
	if storedKey != stableIDPrefix+pubB {
		t.Fatalf("parked key = %q, want %q", storedKey, stableIDPrefix+pubB)
	}

	// The restart: a new Core over the same store is a new instance carrying
	// the same node identity — the exact shape of a daemon relaunch.
	b2 := newCoreOnDB(t, bDB, "b-inst-2")
	must(b2.Register(ctx))
	must(b2.DialPeer(ctx, "127.0.0.1:17971"))
	waitPeer(t, a, "b-inst-2")

	// The new instance's hello claims the old instance's parked rows.
	var left int
	for deadline := time.Now().Add(5 * time.Second); ; {
		_ = a.db.QueryRowContext(ctx,
			`SELECT (SELECT count(*) FROM result_outbox) + (SELECT count(*) FROM cancel_outbox)`).Scan(&left)
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted instance never claimed its custody (left=%d)", left)
		}
		time.Sleep(30 * time.Millisecond)
	}
}
