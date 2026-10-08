// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// pinCore builds a node whose card advertises one marker native ability, so a
// test can prove WHERE a task ran by which marker string came back.
func pinCore(t *testing.T, id, marker string) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "pin:probe", Command: "echo", Args: []string{marker}},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(t.TempDir())
	return c
}

// wireTwo brings up listener+dialer and waits for the conn to exist on a.
func wireTwo(t *testing.T, ctx context.Context, a, b *Core, aAddr, bAddr string) {
	t.Helper()
	if err := a.Register(ctx); err != nil {
		t.Fatalf("register %s: %v", a.nodeID, err)
	}
	if err := b.Register(ctx); err != nil {
		t.Fatalf("register %s: %v", b.nodeID, err)
	}
	go func() { _ = a.Listen(ctx, aAddr) }()
	go func() { _ = b.Listen(ctx, bAddr) }()
	time.Sleep(150 * time.Millisecond)
	if err := a.DialPeer(ctx, bAddr); err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.connFor(b.nodeID) != nil && b.connFor(a.nodeID) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("peers never connected")
}

// — resolvePin unit tests: directory-only, no wire ————————————————————

func TestResolvePinSelfByNameAndID(t *testing.T) {
	c := pinCore(t, "self-node", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, ref := range []string{"self-node", "SELF-NODE"} {
		res := c.resolvePin(ctx, ref)
		if !res.self || !res.found {
			t.Fatalf("resolvePin(%q) = %+v, want self", ref, res)
		}
	}
}

func TestResolvePinOnlineMatch(t *testing.T) {
	c := pinCore(t, "root-x", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	leafCard := ledger.Card{Device: "leaf-name", ResourceClass: "Standard",
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}}
	if err := ledger.Register(c.db, leafCard, "leaf-inst-1", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	res := c.resolvePin(ctx, "leaf-name")
	if res.self || !res.found || !res.online || res.targetID != "leaf-inst-1" {
		t.Fatalf("resolvePin(leaf-name) = %+v, want online leaf-inst-1", res)
	}
	// The instance id resolves too — a pin is a pin whatever form the user typed.
	res = c.resolvePin(ctx, "leaf-inst-1")
	if res.self || !res.found || res.targetID != "leaf-inst-1" {
		t.Fatalf("resolvePin(leaf-inst-1) = %+v", res)
	}
}

func TestResolvePinAmbiguousOnlineRefuses(t *testing.T) {
	c := pinCore(t, "root-x", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	card := ledger.Card{Device: "dup-name", ResourceClass: "Standard",
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}}
	if err := ledger.Register(c.db, card, "dup-a", 5); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := ledger.Register(c.db, card, "dup-b", 5); err != nil {
		t.Fatalf("register b: %v", err)
	}
	res := c.resolvePin(ctx, "dup-name")
	if !res.ambiguous {
		t.Fatalf("resolvePin(dup-name) = %+v, want ambiguous", res)
	}
	if d := c.routePinned(ctx, "dup-name", []string{"root-x"}, nil, ledger.ResourceProfile{}); d.Action != "decline" {
		t.Fatalf("routePinned ambiguous = %+v, want decline", d)
	}
}

func TestResolvePinStaleRowTracksFreshest(t *testing.T) {
	c := pinCore(t, "root-x", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	card := ledger.Card{Device: "ghost-name", ResourceClass: "Standard",
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}}
	for _, id := range []string{"ghost-old", "ghost-new"} {
		if err := ledger.Register(c.db, card, id, 5); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := ledger.MarkOffline(c.db, id); err != nil {
			t.Fatalf("offline %s: %v", id, err)
		}
	}
	// ghost-new has the fresher last_seen: same-name offline ghosts resolve to
	// the node that most recently used the name, not a coin flip.
	if _, err := c.db.Exec(`UPDATE employee_cache SET last_seen = last_seen + 100 WHERE id = 'ghost-new'`); err != nil {
		t.Fatalf("bump last_seen: %v", err)
	}
	res := c.resolvePin(ctx, "ghost-name")
	if !res.found || res.online || res.ambiguous || res.targetID != "ghost-new" {
		t.Fatalf("resolvePin(ghost-name) = %+v, want stale ghost-new", res)
	}
	d := c.routePinned(ctx, "ghost-name", []string{"root-x"}, nil, ledger.ResourceProfile{})
	if d.Action != "forward" || d.Target != "ghost-new" {
		t.Fatalf("routePinned stale = %+v, want forward ghost-new", d)
	}
}

func TestResolvePinNotFoundDeclines(t *testing.T) {
	c := pinCore(t, "root-x", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	d := c.routePinned(ctx, "no-such-node", []string{"root-x"}, nil, ledger.ResourceProfile{})
	if d.Action != "decline" || !strings.Contains(d.Reason, "no-such-node") {
		t.Fatalf("routePinned unknown = %+v, want decline naming the ref", d)
	}
}

// — end-to-end pin behavior over real sockets —————————————————————————

// TestPinSubmitLandsOnNamedNode is the core guarantee: both nodes can run the
// task and the local node would win scoring, yet a TargetNode pin sends the
// work to the named peer — and the audit trail proves it landed there.
func TestPinSubmitLandsOnNamedNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "ran-on-root")
	leaf := pinCore(t, "pin-leaf", "ran-on-leaf")
	wireTwo(t, ctx, root, leaf, "127.0.0.1:18301", "127.0.0.1:18302")

	in := TaskInput{
		Title: "pinned probe", Intent: "run the marker",
		Requires: []string{"pin:probe"}, TargetNode: "pin-leaf",
	}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK || !strings.Contains(result.Stdout, "ran-on-leaf") {
		t.Fatalf("result = ok:%v out:%q, want ran-on-leaf", result.OK, result.Stdout)
	}
	if task.State != StateDone {
		t.Fatalf("root task state = %s, want done", task.State)
	}
	// The wire pin must be the leaf's stable identity or instance id — never
	// the display name the user typed (name matching is the resolver's job).
	if target, err := root.store.DispatchTarget(ctx, task.TaskID); err != nil || target != "pin-leaf" {
		t.Fatalf("dispatch target = %q (err %v), want pin-leaf", target, err)
	}
	// And the executor's own copy is the proof it ran there.
	lt, err := leaf.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("leaf has no task row: %v", err)
	}
	if lt.State != StateDone {
		t.Fatalf("leaf task state = %s, want done", lt.State)
	}
}

// TestPinSubmitUnknownNodeFails: naming a node that is not in the directory
// must fail loudly — the old behavior silently ran it wherever scored best.
func TestPinSubmitUnknownNodeFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "ran-on-root")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, _, err := root.Submit(ctx, TaskInput{
		Title: "pinned to ghost", Intent: "x",
		Requires: []string{"pin:probe"}, TargetNode: "ghost-node",
	})
	if err == nil || !strings.Contains(err.Error(), "ghost-node") {
		t.Fatalf("submit err = %v, want a decline naming ghost-node", err)
	}
}

// TestPinOfflineTargetWaitsForLink: the named node is in the directory but its
// row is stale (offline, no conn). The task must NOT run locally — it parks
// until the link exists, then executes on the named node.
func TestPinOfflineTargetWaitsForLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "ran-on-root")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register root: %v", err)
	}
	go func() { _ = root.Listen(ctx, "127.0.0.1:18311") }()
	time.Sleep(150 * time.Millisecond)

	// A stale directory row for the leaf: known to the mesh, currently gone.
	leafCard := ledger.Card{
		Device: "pin-leaf", ResourceClass: "Standard",
		Native:   []ledger.NativeAbility{{ID: "pin:probe", Command: "echo", Args: []string{"ran-on-leaf"}}},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	if err := ledger.Register(root.db, leafCard, "pin-leaf", 5); err != nil {
		t.Fatalf("register stale leaf: %v", err)
	}
	if err := ledger.MarkOffline(root.db, "pin-leaf"); err != nil {
		t.Fatalf("mark offline: %v", err)
	}

	root.StartQueueScheduler(ctx)
	in := TaskInput{
		Title: "pinned probe", Intent: "run the marker",
		Requires: []string{"pin:probe"}, TargetNode: "pin-leaf",
	}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Queued-for-the-pin, NOT a local execution: the receipt must say so, and
	// the row must not be done with the root's own marker.
	if result.State == StateDone || strings.Contains(result.Stdout, "ran-on-root") {
		t.Fatalf("pinned task ran locally: result %+v", result)
	}
	if result.State != StateQueued {
		t.Fatalf("result state = %s, want queued-for-link", result.State)
	}

	// The leaf comes online and connects — the parked task must flow to it.
	leaf := pinCore(t, "pin-leaf", "ran-on-leaf")
	if err := leaf.Register(ctx); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	go func() { _ = leaf.Listen(ctx, "127.0.0.1:18312") }()
	time.Sleep(150 * time.Millisecond)
	if err := leaf.DialPeer(ctx, "127.0.0.1:18311"); err != nil {
		t.Fatalf("leaf dial: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := root.store.Get(ctx, task.TaskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if got.State == StateDone {
			if !strings.Contains(got.ResultJSON, "ran-on-leaf") {
				t.Fatalf("result = %s, want ran-on-leaf", got.ResultJSON)
			}
			if lt, err := leaf.store.Get(ctx, task.TaskID); err != nil || lt.State != StateDone {
				t.Fatalf("leaf copy = %v (err %v), want done", lt, err)
			}
			return
		}
		if got.State == StateFailed || got.State == StateCancelled {
			t.Fatalf("task ended in %s (result %s) instead of waiting for the link",
				got.State, got.ResultJSON)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pinned task never delivered after the target came online")
}

// TestPinDeclineIsTerminal: the pinned node's refusal is the answer — the task
// fails with who-refused, it does not shop for a substitute. Two capable
// leaves; the pin names the saturated one.
func TestPinDeclineIsTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "ran-on-root")
	dbA := openTestDB(t)
	cardA := ledger.Card{
		Device: "leaf-a", ResourceClass: "Standard",
		Native:   []ledger.NativeAbility{{ID: "pin:probe", Command: "echo", Args: []string{"ran-on-a"}}},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 1},
	}
	leafA := NewCore(dbA, "leaf-a", cardA, 5, testLogger(), config.ModelConfig{})
	leafA.SetSharedSecret(testSharedSecret)
	leafA.SetWorkDir(t.TempDir())
	leafB := pinCore(t, "leaf-b", "ran-on-b")

	// Saturate leafA's only execution slot so it declines.
	busy, err := leafA.store.Create(ctx, "", "", "busy", "leaf-a", []string{"leaf-a"})
	if err != nil {
		t.Fatalf("create busy: %v", err)
	}
	if err := leafA.store.Queue(ctx, busy.TaskID, "leaf-a"); err != nil {
		t.Fatalf("queue busy: %v", err)
	}
	if err := leafA.store.Dispatch(ctx, busy.TaskID, "leaf-a", "leaf-a"); err != nil {
		t.Fatalf("dispatch busy: %v", err)
	}
	if err := leafA.store.Accept(ctx, busy.TaskID, "leaf-a"); err != nil {
		t.Fatalf("accept busy: %v", err)
	}

	for _, c := range []*Core{root, leafA, leafB} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register %s: %v", c.nodeID, err)
		}
	}
	go func() { _ = root.Listen(ctx, "127.0.0.1:18321") }()
	go func() { _ = leafA.Listen(ctx, "127.0.0.1:18322") }()
	go func() { _ = leafB.Listen(ctx, "127.0.0.1:18323") }()
	time.Sleep(150 * time.Millisecond)
	if err := root.DialPeer(ctx, "127.0.0.1:18322"); err != nil {
		t.Fatalf("dial A: %v", err)
	}
	if err := root.DialPeer(ctx, "127.0.0.1:18323"); err != nil {
		t.Fatalf("dial B: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	task, result, err := root.Submit(ctx, TaskInput{
		Title: "pinned to saturated leaf", Intent: "x",
		Requires: []string{"pin:probe"}, TargetNode: "leaf-a",
	})
	if err == nil && result.OK {
		t.Fatalf("pinned-to-declining submit succeeded: %+v", result)
	}
	if err != nil && !strings.Contains(err.Error(), "declin") &&
		!strings.Contains(err.Error(), "capacity") && !strings.Contains(err.Error(), "draining") {
		t.Fatalf("err = %v, want the pin's refusal", err)
	}
	if task.TaskID == "" {
		t.Fatal("submit returned no task row")
	}
	// leafB must never see this task — a substitute is the exact failure mode.
	if lt, err := leafB.store.Get(ctx, task.TaskID); err == nil {
		t.Fatalf("leafB holds a copy of the pinned task (state %s): substitute routing happened", lt.State)
	}
	// The origin row must be failed, not silently re-dispatched.
	tk := mustGetTask(t, root, ctx, task.TaskID)
	if tk.State != StateFailed {
		t.Fatalf("root task state = %s, want failed", tk.State)
	}
}

func mustGetTask(t *testing.T, c *Core, ctx context.Context, id string) Task {
	t.Helper()
	tk, err := c.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return tk
}

// TestLateResultLeavesAuditTrail: a task_result landing on a terminal row must
// not vanish — the timeline records that the executor DID report, and why the
// outcome was kept closed.
func TestLateResultLeavesAuditTrail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := pinCore(t, "pin-root", "x")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	tk, err := root.store.Create(ctx, "", "", "late-result probe", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := root.store.Queue(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := root.store.Dispatch(ctx, tk.TaskID, "pin-root", "pin-leaf"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Close the row: cancelled wins over anything the wire later says.
	if err := root.store.Cancel(ctx, tk.TaskID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	env, err := bus.NewEnvelope(bus.MsgTaskResult, "pin-leaf", "m-1", bus.TaskResultPayload{
		TaskID: tk.TaskID, AttemptID: tk.AttemptID,
		State: StateDone, OK: true, Stdout: "ran-on-leaf",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	root.handleResult(ctx, env)

	got := mustGetTask(t, root, ctx, tk.TaskID)
	if got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled (late result must not reopen)", got.State)
	}
	evs, err := root.store.Events(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type != EvResult {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(ev.DataJSON), &d); err == nil {
			if d["late"] == true && d["dropped"] == "terminal_state" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no late-result audit event recorded; events = %+v", evs)
	}
}

// TestPinSpecPersistsAcrossStoreRoundTrip: the pin survives persistence — it
// is spec.node on the row, which is what queue re-dispatch, retry and resume
// all read back. A pinned task re-loaded from the DB still routes pinned.
func TestPinSpecPersistsAcrossStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "pin-root", "x")
	tk, _, _, err := c.createTask(ctx, TaskInput{
		Title: "x", Intent: "x", TargetNode: "some-leaf", PreferredNode: "soft-hint",
	})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	got := mustGetTask(t, c, ctx, tk.TaskID)
	if pin := targetNodeOf(got); pin != "some-leaf" {
		t.Fatalf("targetNodeOf = %q, want some-leaf", pin)
	}
	if pref := preferredNodeOf(got); pref != "soft-hint" {
		t.Fatalf("preferredNodeOf = %q, want soft-hint", pref)
	}
	if got2 := PinnedNode(got); got2 != "some-leaf" {
		t.Fatalf("PinnedNode = %q", got2)
	}
}

// TestPinWirePayloadCarriesStableIdentity: the TargetNode leaving the origin
// is the stable directory identity, so a target restart between dispatch and
// delivery still resolves — and the executor's self-check matches it.
func TestPinWirePayloadCarriesStableIdentity(t *testing.T) {
	// resolvePin level: a k:-prefixed stable ref must resolve against pub_key.
	c := pinCore(t, "pin-root", "x")
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	card := ledger.Card{Device: "stable-leaf", ResourceClass: "Standard",
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}}
	if err := ledger.Register(c.db, card, "stable-inst", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	pub := strings.Repeat("ab", 32)
	if _, err := c.db.Exec(`UPDATE employee_cache SET pub_key=? WHERE id=?`, pub, "stable-inst"); err != nil {
		t.Fatalf("set pub: %v", err)
	}
	res := c.resolvePin(ctx, "k:"+pub)
	if !res.found || res.targetID != "stable-inst" {
		t.Fatalf("resolvePin(k:pub) = %+v, want stable-inst", res)
	}
	// Bare pubkey hex resolves too.
	res = c.resolvePin(ctx, pub)
	if !res.found || res.targetID != "stable-inst" {
		t.Fatalf("resolvePin(pub) = %+v, want stable-inst", res)
	}
}

// TestPinSelfResourceMisfitDeclines: pinning THIS node still honors the
// resource contract — a declared card profile that positively cannot fit the
// task's requirement declines, exactly as a remote target would. An
// undeclared self row (no profile written) stays permissive.
func TestPinSelfResourceMisfitDeclines(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:          "fit-self",
		ResourceClass:   "Standard",
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 16},
		Native:          []ledger.NativeAbility{{ID: "pin:probe", Command: "echo", Args: []string{"x"}}},
		Capacity:        ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, "fit-self", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	over := ledger.ResourceProfile{RAMGB: 512}
	if d := c.routePinned(ctx, "fit-self", []string{"fit-self"}, nil, over); d.Action != scheduler.ActionDecline {
		t.Fatalf("self pin with 512GB requirement = %+v, want decline", d)
	}
	fits := ledger.ResourceProfile{RAMGB: 4}
	if d := c.routePinned(ctx, "fit-self", []string{"fit-self"}, nil, fits); d.Action != scheduler.ActionLocal {
		t.Fatalf("self pin with 4GB requirement = %+v, want local", d)
	}
}

// TestPinParkedSyncHoldsNoLease: a pinned task whose link is dead at dispatch
// parks in the outbox — and must hold NO lease, or the lease expiry would
// fail the local copy while custody still intends to deliver the work.
func TestPinParkedSyncHoldsNoLease(t *testing.T) {
	ctx := context.Background()
	root := pinCore(t, "pin-root", "x")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	leafCard := ledger.Card{
		Device: "pin-leaf", ResourceClass: "Standard",
		Native:   []ledger.NativeAbility{{ID: "pin:probe", Command: "echo", Args: []string{"ran-on-leaf"}}},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	if err := ledger.Register(root.db, leafCard, "pin-leaf", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	if err := ledger.MarkOffline(root.db, "pin-leaf"); err != nil {
		t.Fatalf("mark offline: %v", err)
	}
	tk, err := root.store.Create(ctx, "", "", "parked pin", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := root.store.Queue(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	p := bus.TaskDelegatePayload{
		TaskID: tk.TaskID, Intent: "x", Requires: []string{"pin:probe"},
		TargetNode: "pin-leaf",
	}
	if err := root.dispatchDelegated(ctx, tk.TaskID, "pin-leaf", p, []string{"pin-root"}); err != nil {
		t.Fatalf("dispatchDelegated: %v", err)
	}
	var leaseAt *int64
	if err := root.db.QueryRow(`SELECT lease_expires_at FROM tasks WHERE task_id=?`,
		tk.TaskID).Scan(&leaseAt); err != nil {
		t.Fatalf("lease query: %v", err)
	}
	if leaseAt != nil {
		t.Fatalf("parked pinned task holds lease expiring at %v — expiry would kill the parked copy", *leaseAt)
	}
	var kind string
	if err := root.db.QueryRow(`SELECT transport_type FROM task_outbox WHERE task_id=?`,
		tk.TaskID).Scan(&kind); err != nil {
		t.Fatalf("no custody row for parked pinned task: %v", err)
	}
	if kind != "pin" {
		t.Fatalf("custody kind = %q, want pin", kind)
	}
}

// TestPinForwardBudgetExhaustedFails: when the queue forward of a pinned task
// dies before the send path (here: mesh delegation budget spent), the task
// must fail honestly — the pre-fix path returned false and ran it locally,
// the exact silent fallback the pin exists to forbid.
func TestPinForwardBudgetExhaustedFails(t *testing.T) {
	ctx := context.Background()
	root := pinCore(t, "pin-root", "ran-on-root")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	leafCard := ledger.Card{
		Device: "pin-leaf", ResourceClass: "Standard",
		Native:   []ledger.NativeAbility{{ID: "pin:probe", Command: "echo", Args: []string{"ran-on-leaf"}}},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	if err := ledger.Register(root.db, leafCard, "pin-leaf", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	// A chain longer than self plus a zero remaining budget make
	// delegationBudget refuse the hop before any send is attempted.
	tk, err := root.store.CreateWithID(ctx, "t-pin-budget", "", "", "budget pin",
		"pin-root", []string{"peer-up", "pin-root"}, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	specJSON := `{"node":"pin-leaf"}`
	if err := root.store.SetDetail(ctx, tk.TaskID, TaskDetail{SpecJSON: specJSON}); err != nil {
		t.Fatalf("set detail: %v", err)
	}
	if err := root.store.SetDelegationBudget(ctx, tk.TaskID, 0); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	if err := root.store.Queue(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := root.store.ClaimLocal(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err := root.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !root.forwardScheduled(ctx, got) {
		t.Fatal("pinned forward failure must consume the task, not fall through to local run")
	}
	got = mustGetTask(t, root, ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed — a pinned task must never silently run here", got.State)
	}
	if strings.Contains(got.ResultJSON, "ran-on-root") {
		t.Fatalf("pinned task executed locally: %s", got.ResultJSON)
	}
}

// TestDelegateDetailPersistsCanonicalPin: the executor's persisted row must
// carry the canonical pin identity the wire resolved (k:<pubkey>), not the
// display name the origin user typed — a re-dispatch on THIS node's directory
// resolves the name against possibly different ghost rows.
func TestDelegateDetailPersistsCanonicalPin(t *testing.T) {
	p := bus.TaskDelegatePayload{
		TaskID:   "t-x",
		SpecJSON: `{"node":"macbook","requires":["pin:probe"]}`,
		// The origin already resolved "macbook" to the target's stable key.
		TargetNode: "k:" + strings.Repeat("cd", 32),
	}
	d := delegateDetail(p)
	var m map[string]any
	if err := json.Unmarshal([]byte(d.SpecJSON), &m); err != nil {
		t.Fatalf("spec not JSON: %v", err)
	}
	if m["node"] != p.TargetNode {
		t.Fatalf("spec.node = %v, want canonical %q", m["node"], p.TargetNode)
	}
	// An unpinned payload keeps spec.node untouched.
	p2 := bus.TaskDelegatePayload{TaskID: "t-y", SpecJSON: `{"node":"macbook"}`}
	d2 := delegateDetail(p2)
	var m2 map[string]any
	if err := json.Unmarshal([]byte(d2.SpecJSON), &m2); err != nil {
		t.Fatalf("spec not JSON: %v", err)
	}
	if m2["node"] != "macbook" {
		t.Fatalf("unpinned spec.node = %v, want preserved macbook", m2["node"])
	}
}

// TestOrphanSweepSparesParkedPin: after a restart a parked pinned task's row
// is queued again (Recover) while custody still waits in task_outbox. The
// orphan sweep must leave it alone — failing it would let the flush deliver
// work the local copy already declared dead.
func TestOrphanSweepSparesParkedPin(t *testing.T) {
	ctx := context.Background()
	root := pinCore(t, "pin-root", "x")
	if err := root.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	tk, err := root.store.Create(ctx, "", "", "parked pin", "pin-root", []string{"pin-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := root.store.Queue(ctx, tk.TaskID, "pin-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := root.store.Dispatch(ctx, tk.TaskID, "pin-root", "pin-leaf"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Simulate the restart: dispatched → queued, custody row still parked.
	if _, err := root.store.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	got := mustGetTask(t, root, ctx, tk.TaskID)
	if got.State != StateQueued {
		t.Fatalf("post-recover state = %s, want queued", got.State)
	}
	p := bus.TaskDelegatePayload{TaskID: tk.TaskID, TargetNode: "k:" + strings.Repeat("ef", 32)}
	root.taskOutboxPersist(ctx, "pin-leaf", p, "pin", time.Now().Add(time.Hour).Unix())

	// Past the grace window: without custody the sweep would fail it now.
	root.mu.Lock()
	root.orphanSeen[tk.TaskID] = time.Now().Add(-orphanedForwardGrace - time.Minute)
	root.mu.Unlock()
	root.rescueOrphanedForwards(ctx)
	got = mustGetTask(t, root, ctx, tk.TaskID)
	if got.State == StateFailed || got.State == StateCancelled {
		t.Fatalf("parked pinned task was killed by the orphan sweep: state %s", got.State)
	}

	// Custody gone (delivered or expired) — the sweep's normal semantics resume.
	root.db.Exec(`DELETE FROM task_outbox WHERE task_id=?`, tk.TaskID)
	root.mu.Lock()
	root.orphanSeen[tk.TaskID] = time.Now().Add(-orphanedForwardGrace - time.Minute)
	root.mu.Unlock()
	root.rescueOrphanedForwards(ctx)
	got = mustGetTask(t, root, ctx, tk.TaskID)
	if got.State != StateFailed {
		t.Fatalf("custody-less orphan state = %s, want failed (control case)", got.State)
	}
}

var _ = fmt.Sprintf // silence unused-import if helpers evolve
