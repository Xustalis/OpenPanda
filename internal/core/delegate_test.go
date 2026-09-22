package core

import (
	"context"
	"testing"

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
