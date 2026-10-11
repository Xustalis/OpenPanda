// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/commander"
)

// TestChildBlockedByParent pins the predicate behind the live deadlock fix:
// a queued child whose resource keys overlap the parent's can never start
// while the parent waits — the queue admits one holder per key and the
// parent holds its keys for the whole run. Without this check the fold
// looped on such a child forever (observed 7+ minutes in the live mesh).
func TestChildBlockedByParent(t *testing.T) {
	parent := Task{ResourceKeys: []string{"project:xenith", "gpu:0"}}
	if !childBlockedByParent(parent, Task{ResourceKeys: []string{"project:xenith"}}) {
		t.Fatal("shared project key must block")
	}
	if !childBlockedByParent(parent, Task{ResourceKeys: []string{"cpu:1", "gpu:0"}}) {
		t.Fatal("one overlapping key among several must still block")
	}
	if childBlockedByParent(parent, Task{ResourceKeys: []string{"project:other"}}) {
		t.Fatal("disjoint keys must not block")
	}
	if childBlockedByParent(parent, Task{}) {
		t.Fatal("keyless child must not block")
	}
	if childBlockedByParent(Task{}, Task{ResourceKeys: []string{"project:x"}}) {
		t.Fatal("keyless parent can serialize nothing")
	}
}

// TestMergeLiveChildrenPicksUpMidRunChild: pendingChildren is seeded from the
// opening snapshot, but an agent spawns children mid-run via
// `task add --parent-id $PANDA_TASK_ID` — rows that never passed through the
// snapshot. The merge re-reads the parent's live rows so such a child is
// folded/annotated instead of the parent reporting done over its head.
func TestMergeLiveChildrenPicksUpMidRunChild(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "fold-root", "x")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	mk := func(id string) {
		if _, err := c.store.CreateWithID(ctx, id, "t-parent", "", id, "fold-root",
			[]string{"fold-root"}, true); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("child-known")   // in the opening snapshot already
	mk("child-mid-run") // spawned during execution — the merge's whole point
	mk("child-done")    // already terminal: nothing to fold
	if err := c.store.Cancel(ctx, "child-done"); err != nil {
		t.Fatalf("cancel child-done: %v", err)
	}

	pending := []pendingChild{{id: "child-known", title: "child-known"}}
	c.mergeLiveChildren(ctx, "t-parent", &pending)

	got := map[string]int{}
	for _, pc := range pending {
		got[pc.id]++
	}
	if got["child-known"] != 1 {
		t.Fatalf("child-known duplicated or dropped: %v", pending)
	}
	if got["child-mid-run"] != 1 {
		t.Fatalf("mid-run child never entered the fold set: %v", pending)
	}
	if got["child-done"] != 0 {
		t.Fatalf("terminal child re-added to pending: %v", pending)
	}
}

// TestAwaitPendingChildrenSkipsSelfLockedChild is the deadlock's integration
// form: a queued child serialized behind the parent's own resource keys must
// read as "outstanding, will reconcile" — annotation, not a wait that can
// only end when the parent releases what it is waiting on. The control half
// shows a queued child NOT so blocked still gets the wait.
func TestAwaitPendingChildrenSkipsSelfLockedChild(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "fold-root", "x")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	parent := Task{TaskID: "t-parent", ResourceKeys: []string{"project:xenith"}}

	// Blocked case: the child needs the key the parent holds.
	if _, err := c.store.CreateWithID(ctx, "t-blocked", "t-parent", "", "blocked child",
		"fold-root", []string{"fold-root"}, true); err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	if err := c.store.SetQueueMeta(ctx, "t-blocked", 0, "", "", []string{"project:xenith"}); err != nil {
		t.Fatalf("queue meta: %v", err)
	}
	if err := c.store.Queue(ctx, "t-blocked", "fold-root"); err != nil {
		t.Fatalf("queue blocked: %v", err)
	}

	start := time.Now()
	notes := c.awaitPendingChildren(ctx, parent, []pendingChild{{id: "t-blocked", title: "blocked"}})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("await on a self-locked child blocked %s — the deadlock is back", elapsed)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "still outstanding") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want the outstanding-child annotation", notes)
	}

	// Control: a queued child whose keys do NOT collide is alive — the wait
	// must actually hold for it (here: until the caller's context gives up).
	if _, err := c.store.CreateWithID(ctx, "t-live", "t-parent", "", "live child",
		"fold-root", []string{"fold-root"}, true); err != nil {
		t.Fatalf("create live: %v", err)
	}
	if err := c.store.SetQueueMeta(ctx, "t-live", 0, "", "", []string{"project:other"}); err != nil {
		t.Fatalf("queue meta2: %v", err)
	}
	if err := c.store.Queue(ctx, "t-live", "fold-root"); err != nil {
		t.Fatalf("queue live: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	start = time.Now()
	_ = c.awaitPendingChildren(cctx, parent, []pendingChild{{id: "t-live", title: "live"}})
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("a live queued child returned in %s — the wait skipped it", elapsed)
	}
}

// TestAnnotateAgentSubstitution pins the visible-substitution contract: a
// task that asked for one harness and got another must say so at the top of
// its result, and a faithful run must stay untouched (no note, no noise).
func TestAnnotateAgentSubstitution(t *testing.T) {
	plan := commander.Plan{Kind: "agent", Agent: "claude_code"}

	// The live case from the mesh: windows has no claude, opencode runs it.
	got := annotateAgentSubstitution(plan, commander.Result{Agent: "opencode", Stdout: "done the work"})
	if !strings.HasPrefix(got.Stdout, "[panda] agent substitution:") ||
		!strings.Contains(got.Stdout, `"claude_code"`) || !strings.Contains(got.Stdout, `"opencode"`) ||
		!strings.Contains(got.Stdout, "done the work") {
		t.Fatalf("substitution note missing or wrong: %q", got.Stdout)
	}
	// A silent result still carries the note — that is the whole point.
	got = annotateAgentSubstitution(plan, commander.Result{Agent: "opencode"})
	if !strings.Contains(got.Stdout, "agent substitution") {
		t.Fatalf("empty-stdout substitution lost its note: %q", got.Stdout)
	}
	// The requested harness ran: nothing to say.
	got = annotateAgentSubstitution(plan, commander.Result{Agent: "claude_code", Stdout: "x"})
	if got.Stdout != "x" {
		t.Fatalf("faithful run got annotated: %q", got.Stdout)
	}
	// Non-agent plans (native/manual) never carry the note.
	got = annotateAgentSubstitution(commander.Plan{Kind: "native", Agent: "claude_code"},
		commander.Result{Agent: "opencode", Stdout: "x"})
	if got.Stdout != "x" {
		t.Fatalf("non-agent plan annotated: %q", got.Stdout)
	}
}

// TestEnrichDeclineReasonRendersTrail: the user-facing decline string must
// name who refused and why — "no capability matches" alone hid a capacity
// decline behind a capability story during the live run.
func TestEnrichDeclineReasonRendersTrail(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "decline-root", "x")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	tk, err := c.store.Create(ctx, "", "", "declined task", "decline-root", []string{"decline-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, "decline-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	// No trail yet: the base reason stands untouched.
	if got := c.enrichDeclineReason(ctx, tk.TaskID, "no capability matches"); got != "no capability matches" {
		t.Fatalf("no-trail reason = %q", got)
	}
	// Two refusals render both, with their stated causes.
	if err := c.store.Dispatch(ctx, tk.TaskID, "decline-root", "peer-a"); err != nil {
		t.Fatalf("dispatch a: %v", err)
	}
	if err := c.store.Decline(ctx, tk.TaskID, "decline-root", "capacity full", "", "peer-a"); err != nil {
		t.Fatalf("decline a: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "decline-root", "peer-b"); err != nil {
		t.Fatalf("dispatch b: %v", err)
	}
	if err := c.store.Decline(ctx, tk.TaskID, "decline-root", "node draining", "drain", "peer-b"); err != nil {
		t.Fatalf("decline b: %v", err)
	}
	got := c.enrichDeclineReason(ctx, tk.TaskID, "no capability matches")
	if got != "no capability matches; declined by peer-a (capacity full); peer-b (node draining)" {
		t.Fatalf("enriched reason = %q", got)
	}
}

// TestRecoveredInterruptedTaskCarriesReason is the live Pi-kill regression:
// a task killed mid-run by a daemon restart is failed by Recover, and the
// reason must survive to the re-push and fold readers — the Mac showed a
// bare "task is failed" while the Pi's own log said "interrupted", so the
// operator upstream could not tell a restart from a crash.
func TestRecoveredInterruptedTaskCarriesReason(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "rec-root", "x")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	tk, err := c.store.Create(ctx, "", "", "interrupted bench", "rec-root", []string{"rec-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, "rec-root"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "rec-root", "rec-root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, "rec-root"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	if _, err := c.store.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	row := mustGetTask(t, c, ctx, tk.TaskID)
	if row.State != StateFailed {
		t.Fatalf("state = %s, want failed", row.State)
	}
	// The re-push payload builder reads the result trail — it must carry the
	// cause, not synthesize "task is failed".
	out := c.resumeOutcomeFor(ctx, row)
	if out.Stderr != "interrupted" {
		t.Fatalf("re-push stderr = %q, want the recover disposition", out.Stderr)
	}

	// The delegator's end of the same story: the re-pushed failure lands via
	// FailFromRemote, and the fold reader on the parent's copy then sees the
	// real cause instead of "failed with no detail".
	delegator := pinCore(t, "rec-delegator", "x")
	if err := delegator.Register(ctx); err != nil {
		t.Fatalf("register delegator: %v", err)
	}
	dtk, err := delegator.store.Create(ctx, "", "", "interrupted bench", "rec-delegator", []string{"rec-delegator", "rec-root"})
	if err != nil {
		t.Fatalf("create delegator copy: %v", err)
	}
	if err := delegator.store.Queue(ctx, dtk.TaskID, "rec-delegator"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := delegator.store.Dispatch(ctx, dtk.TaskID, "rec-delegator", "rec-root"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := delegator.store.FailFromRemote(ctx, dtk.TaskID, "rec-delegator", out.Stderr); err != nil {
		t.Fatalf("fail from remote: %v", err)
	}
	drow := mustGetTask(t, delegator, ctx, dtk.TaskID)
	p, ok := childRowResult(drow)
	if !ok || p.Stderr != "interrupted" {
		t.Fatalf("fold payload = %+v (ok=%v), want interrupted", p, ok)
	}
}

// TestNotePackSkippedEmitsDegradedEvent: a lenient pack that dropped entries
// must leave the omission on the task's audit trail — the tree that shipped
// is intact but not whole, and that distinction is exactly what
// context_degraded exists to record.
func TestNotePackSkippedEmitsDegradedEvent(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "pack-root", "x")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	tk, err := c.store.Create(ctx, "", "", "partial pack task", "pack-root", []string{"pack-root"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nothing skipped -> no noise on the timeline.
	c.notePackSkipped(ctx, tk.TaskID, "pack_tree", "/home/x", artifact.Manifest{})
	evs, err := c.store.Events(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == EvContextDegraded {
			t.Fatalf("empty skip list wrote a degraded event: %+v", ev)
		}
	}

	c.notePackSkipped(ctx, tk.TaskID, "pack_tree", "/home/x",
		artifact.Manifest{PackSkipped: []string{".Trash", "locked/secret.txt"}})
	evs, err = c.store.Events(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != EvContextDegraded {
			continue
		}
		if strings.Contains(ev.DataJSON, "pack_tree_partial") &&
			strings.Contains(ev.DataJSON, ".Trash") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pack_tree_partial event on the audit trail; events = %+v", evs)
	}
}
