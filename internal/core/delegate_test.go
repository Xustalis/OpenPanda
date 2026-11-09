// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

func TestParseDelegateRequest(t *testing.T) {
	out := "line one\nPANDA_DELEGATE {\"intent\":\"train the model\",\"requires\":[\"gpu\"],\"title\":\"train\"}\nline three"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 1 {
		t.Fatalf("valid marker not parsed: %d requests", len(drs))
	}
	dr := drs[0]
	if dr.Intent != "train the model" || dr.Title != "train" || len(dr.Requires) != 1 {
		t.Fatalf("bad parse: %+v", dr)
	}
	if kept != "line one\nline three" {
		t.Fatalf("marker not stripped: %q", kept)
	}
}

// TestParseDelegateRequestPaths pins the scope claim field: a marker may
// declare the project-relative paths the child owns, and the parsed request
// carries them for the key derivation.
func TestParseDelegateRequestPaths(t *testing.T) {
	out := `PANDA_DELEGATE {"intent":"refactor api","paths":["pkg/api","web/src"],"title":"api"}`
	drs, _ := parseDelegateRequests(out)
	if len(drs) != 1 {
		t.Fatalf("marker not parsed: %d", len(drs))
	}
	if got := drs[0].Paths; len(got) != 2 || got[0] != "pkg/api" || got[1] != "web/src" {
		t.Fatalf("paths = %v", got)
	}
}

// TestParseDelegateRequestIsolate pins the isolation flag: a marker may ask
// for a private-copy run, and the parsed request carries it to dispatch.
func TestParseDelegateRequestIsolate(t *testing.T) {
	out := `PANDA_DELEGATE {"intent":"rewrite the renderer","isolate":true,"title":"renderer"}`
	drs, _ := parseDelegateRequests(out)
	if len(drs) != 1 {
		t.Fatalf("marker not parsed: %d", len(drs))
	}
	if !drs[0].Isolate {
		t.Fatal("isolate flag not parsed")
	}
}

func TestParseDelegateRequestMalformedKept(t *testing.T) {
	out := "start\nPANDA_DELEGATE {not json}\nend"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 0 {
		t.Fatal("malformed marker parsed")
	}
	if kept != out {
		t.Fatal("malformed marker line was eaten")
	}
}

// TestParseDelegateRequestsMulti covers the second half of the 2026-09-29
// audit finding: a turn may legitimately ask for several children, and every
// well-formed marker parses — not just the first.
func TestParseDelegateRequestsMulti(t *testing.T) {
	out := "PANDA_DELEGATE {\"intent\":\"a\"}\nsome prose\nPANDA_DELEGATE {\"intent\":\"b\",\"requires\":[\"gpu\"]}"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 2 || drs[0].Intent != "a" || drs[1].Intent != "b" {
		t.Fatalf("multi markers: %+v", drs)
	}
	if kept != "some prose" {
		t.Fatalf("kept = %q, want only the prose line", kept)
	}
}

// TestParseDelegateRequestMultiline covers the markdown-tolerance half of
// the audit finding: an agent that pretty-prints its payload must still
// parse — the marker line plus continuation lines form the request.
func TestParseDelegateRequestMultiline(t *testing.T) {
	out := "before\nPANDA_DELEGATE {\n  \"intent\": \"train\",\n  \"requires\": [\"gpu\"]\n}\nafter"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 1 || drs[0].Intent != "train" || len(drs[0].Requires) != 1 {
		t.Fatalf("multiline parse: %+v", drs)
	}
	if kept != "before\nafter" {
		t.Fatalf("continuation lines not consumed: %q", kept)
	}
}

// TestParseDelegateRequestFenced covers the fenced-code-block shape: the
// marker parses inside the fence, and the fence pair wrapping only the
// marker leaves with it.
func TestParseDelegateRequestFenced(t *testing.T) {
	out := "prose\n```json\nPANDA_DELEGATE {\"intent\":\"snap\"}\n```\nmore"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 1 || drs[0].Intent != "snap" {
		t.Fatalf("fenced parse: %+v", drs)
	}
	if kept != "prose\nmore" {
		t.Fatalf("fence not stripped: %q", kept)
	}
}

// TestParseDelegateRequestNextLine covers the bare-marker shape: the marker
// stands alone and the JSON object begins on the next line.
func TestParseDelegateRequestNextLine(t *testing.T) {
	out := "PANDA_DELEGATE\n{\"intent\":\"read sensor\"}\ndone"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 1 || drs[0].Intent != "read sensor" {
		t.Fatalf("next-line parse: %+v", drs)
	}
	if kept != "done" {
		t.Fatalf("kept = %q", kept)
	}
}

// TestParseDelegateRequestBoundary: prose that shares the marker's prefix is
// not a request — same contract as PANDA_QUESTION's word-boundary rule.
func TestParseDelegateRequestBoundary(t *testing.T) {
	out := "PANDA_DELEGATED: this is prose\nPANDA_DELEGATE {\"intent\":\"real\"}"
	drs, kept := parseDelegateRequests(out)
	if len(drs) != 1 || drs[0].Intent != "real" {
		t.Fatalf("boundary parse: %+v", drs)
	}
	if !strings.Contains(kept, "PANDA_DELEGATED") {
		t.Fatalf("prose line eaten: %q", kept)
	}
}

func TestParallelDelegateBatchDefaultsUnscopedSiblingsToIsolation(t *testing.T) {
	input := []delegateRequest{
		{Intent: "probe one"},
		{Intent: "edit api", Paths: []string{"internal/api"}},
		{Intent: "broad rewrite", Isolate: true},
		{Intent: "other tree", WorkDir: "/tmp/other-tree"},
	}
	got := parallelDelegateBatch("panda", 4, input)
	if len(got) != len(input) {
		t.Fatalf("batch size = %d, want %d", len(got), len(input))
	}
	if !got[0].Isolate {
		t.Fatal("unscoped sibling did not get default isolation")
	}
	if got[1].Isolate || len(got[1].Paths) != 1 {
		t.Fatalf("explicit path scope changed: %+v", got[1])
	}
	if !got[2].Isolate {
		t.Fatal("explicit isolation was lost")
	}
	if got[3].Isolate || got[3].WorkDir != input[3].WorkDir {
		t.Fatalf("explicit workdir changed: %+v", got[3])
	}
	if input[0].Isolate {
		t.Fatal("helper mutated caller's request slice")
	}

	for name, tc := range map[string]struct {
		project string
		fanout  int
		drs     []delegateRequest
	}{
		"single child": {project: "panda", fanout: 1, drs: []delegateRequest{{Intent: "one"}}},
		"no project":   {project: "", fanout: 4, drs: []delegateRequest{{Intent: "one"}, {Intent: "two"}}},
	} {
		t.Run(name, func(t *testing.T) {
			got := parallelDelegateBatch(tc.project, tc.fanout, tc.drs)
			for i, dr := range got {
				if dr.Isolate {
					t.Fatalf("request %d unexpectedly isolated: %+v", i, dr)
				}
			}
		})
	}
}

func TestDelegationBudgetResolution(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-x")
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	mkTask := func(chain []string, budget int) Task {
		t.Helper()
		tk, err := c.store.Create(ctx, "", "", "t", c.nodeID, chain)
		if err != nil {
			t.Fatal(err)
		}
		if budget > 0 {
			if err := c.store.SetDelegationBudget(ctx, tk.TaskID, budget); err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	// Origin task (chain of self, no budget): seeds the default.
	org := mkTask([]string{"node-x"}, 0)
	rem, err := c.delegationBudget(ctx, org.TaskID, 0)
	if err != nil || rem != scheduler.MaxDelegationBudget-1 {
		t.Fatalf("origin: rem=%d err=%v", rem, err)
	}
	// Received task (chain of two, wire budget seeds the row).
	rcv := mkTask([]string{"node-a", "node-x"}, 5)
	rem, err = c.delegationBudget(ctx, rcv.TaskID, 0)
	if err != nil || rem != 4 {
		t.Fatalf("received: rem=%d err=%v", rem, err)
	}
	// Exhausted mid-chain task: refused — decrementing would launder the cap.
	dead := mkTask([]string{"node-a", "node-x"}, 0)
	if _, err := c.delegationBudget(ctx, dead.TaskID, 0); err == nil {
		t.Fatal("exhausted budget not refused")
	}
}

// A delegate payload without a budget field comes from a pre-budget peer:
// the receiver must seed the default rather than persisting a zero that would
// refuse every onward hop. An explicit zero is the opposite — a new-protocol
// peer reporting the quota spent, which must persist as exhaustion.
func TestDelegateBudgetWireCompat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-budget")
	worker := newCore(t, "worker-budget")
	startPair(t, ctx, entry, worker)

	// Legacy shape: no delegation_budget key on the wire at all.
	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-budget", "m-legacy", bus.TaskDelegatePayload{
		TaskID: "legacy-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-budget"},
	})
	if err := entry.sendTo("worker-budget", env); err != nil {
		t.Fatalf("send legacy: %v", err)
	}
	// New-protocol shape: the field present and explicitly exhausted.
	zero := 0
	env2, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-budget", "m-zero", bus.TaskDelegatePayload{
		TaskID: "zero-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-budget"}, DelegationBudget: &zero,
	})
	if err := entry.sendTo("worker-budget", env2); err != nil {
		t.Fatalf("send zero: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	legacy, err := worker.store.Get(ctx, "legacy-task")
	if err != nil {
		t.Fatalf("legacy row: %v", err)
	}
	if legacy.DelegationBudget != scheduler.MaxDelegationBudget {
		t.Fatalf("legacy wire seeded %d, want %d", legacy.DelegationBudget, scheduler.MaxDelegationBudget)
	}
	zeroed, err := worker.store.Get(ctx, "zero-task")
	if err != nil {
		t.Fatalf("zero row: %v", err)
	}
	if zeroed.DelegationBudget != 0 {
		t.Fatalf("explicit zero laundered to %d", zeroed.DelegationBudget)
	}
	if _, err := worker.delegationBudget(ctx, "zero-task", 0); err == nil {
		t.Fatal("explicit-zero task allowed to route onward")
	}
}
