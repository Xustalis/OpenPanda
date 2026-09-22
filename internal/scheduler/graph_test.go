package scheduler

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestGraphFirstHop(t *testing.T) {
	// A -- B -- C topology: only C has the required ability; B is the relay.
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"a", "c"}}
	cc := ledger.Node{ID: "c", Status: "online", Neighbors: []string{"b"},
		Native: []ledger.NativeAbility{{ID: "gpu-train"}}}
	employees := []ledger.Node{self, b, cc}
	hop := graphFirstHop(self, employees, map[string]bool{}, []string{"gpu-train"}, ledger.ResourceProfile{})
	if hop != "b" {
		t.Fatalf("first hop = %q, want b", hop)
	}
}

func TestGraphFirstHopSkipsOfflineAndSeen(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"c"}}
	cc := ledger.Node{ID: "c", Status: "online", Neighbors: []string{},
		Native: []ledger.NativeAbility{{ID: "x"}}}
	// c already on the delegation chain — not a valid goal... wait, c IS the
	// goal candidate; seen excludes visited nodes so the chain can't loop.
	seen := map[string]bool{"c": true}
	hop := graphFirstHop(self, []ledger.Node{self, b, cc}, seen, []string{"x"}, ledger.ResourceProfile{})
	if hop != "" {
		t.Fatalf("goal on chain should not be targeted, got %q", hop)
	}
}

func TestGraphFirstHopNoPath(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"a"}}
	island := ledger.Node{ID: "z", Status: "online", Neighbors: []string{},
		Native: []ledger.NativeAbility{{ID: "x"}}}
	hop := graphFirstHop(self, []ledger.Node{self, b, island}, nil, []string{"x"}, ledger.ResourceProfile{})
	if hop != "" {
		t.Fatalf("unreachable node routed: %q", hop)
	}
}
