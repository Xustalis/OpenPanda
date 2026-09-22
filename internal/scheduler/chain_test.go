package scheduler

import (
	"errors"
	"testing"
)

func TestAppendChainGraphRevisit(t *testing.T) {
	// A -> B -> C -> A is a valid 3-node cycle across the graph.
	chain := []string{"a", "b", "c"}
	got, err := AppendChain(chain, "a")
	if err != nil {
		t.Fatalf("expected legal graph revisit of 'a', got error: %v", err)
	}
	if len(got) != 4 || got[3] != "a" {
		t.Fatalf("unexpected chain: %v", got)
	}

	// Immediate self-loop: 'a' -> 'a' should fail with ErrLoop.
	if _, err := AppendChain(got, "a"); !errors.Is(err, ErrLoop) {
		t.Fatalf("expected ErrLoop for immediate self-loop, got %v", err)
	}

	// Immediate 2-node ping-pong: 'a' -> 'b' -> 'a' should fail.
	if _, err := AppendChain([]string{"a", "b"}, "a"); !errors.Is(err, ErrLoop) {
		t.Fatalf("expected ErrLoop for direct ping-pong, got %v", err)
	}

	// 'a' has now been visited 2 times in got: ["a", "b", "c", "a"].
	// Adding another node 'd' is fine:
	got2, err := AppendChain(got, "d")
	if err != nil {
		t.Fatalf("failed to append 'd': %v", err)
	}
	// But visiting 'a' a 3rd time exceeds MaxVisitsPerNode (2):
	if _, err := AppendChain(got2, "a"); !errors.Is(err, ErrLoop) {
		t.Fatalf("expected ErrLoop when exceeding MaxVisitsPerNode, got %v", err)
	}
}

func TestAppendChainDepthLimit(t *testing.T) {
	chain := []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8"}
	if _, err := AppendChain(chain, "n9"); !errors.Is(err, ErrChainTooDeep) {
		t.Fatalf("expected ErrChainTooDeep at depth 8, got %v", err)
	}
}

func TestDelegationBudget(t *testing.T) {
	b := DefaultBudget()
	if b.MaxDelegations != MaxDelegationBudget {
		t.Fatalf("expected budget %d, got %d", MaxDelegationBudget, b.MaxDelegations)
	}

	for i := 0; i < MaxDelegationBudget; i++ {
		if err := b.DecrementDelegation(); err != nil {
			t.Fatalf("decrement %d failed: %v", i, err)
		}
	}
	if err := b.DecrementDelegation(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}
