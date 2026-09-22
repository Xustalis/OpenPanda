package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

func TestParseDelegateRequest(t *testing.T) {
	out := "line one\nPANDA_DELEGATE {\"intent\":\"train the model\",\"requires\":[\"gpu\"],\"title\":\"train\"}\nline three"
	dr, kept, ok := parseDelegateRequest(out)
	if !ok {
		t.Fatal("valid marker not parsed")
	}
	if dr.Intent != "train the model" || dr.Title != "train" || len(dr.Requires) != 1 {
		t.Fatalf("bad parse: %+v", dr)
	}
	if kept != "line one\nline three" {
		t.Fatalf("marker not stripped: %q", kept)
	}
}

func TestParseDelegateRequestMalformedKept(t *testing.T) {
	out := "start\nPANDA_DELEGATE {not json}\nend"
	_, kept, ok := parseDelegateRequest(out)
	if ok {
		t.Fatal("malformed marker parsed")
	}
	if kept != out {
		t.Fatal("malformed marker line was eaten")
	}
}

func TestParseDelegateRequestFirstOnly(t *testing.T) {
	out := "PANDA_DELEGATE {\"intent\":\"a\"}\nPANDA_DELEGATE {\"intent\":\"b\"}"
	dr, kept, ok := parseDelegateRequest(out)
	if !ok || dr.Intent != "a" {
		t.Fatalf("first marker: %+v ok=%v", dr, ok)
	}
	// The second marker stays in output — one delegation per parse.
	if kept == "" || len(kept) == 0 {
		t.Fatal("second marker line dropped")
	}
}

func TestDelegationBudgetResolution(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-x", "127.0.0.1:0")
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

	entry := newCore(t, "entry-budget", "127.0.0.1:17848")
	worker := newCore(t, "worker-budget", "127.0.0.1:17849")
	startPair(t, ctx, entry, worker, "127.0.0.1:17848", "127.0.0.1:17849")

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
