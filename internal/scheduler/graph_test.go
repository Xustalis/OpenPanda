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

// §4.1 weighted routing: the path with more hops but cheaper links must beat
// the short-but-slow one. BFS used to pick by hop count alone, which is how a
// 1-hop satellite link would beat a 3-hop LAN.
func TestGraphFirstHopPrefersCheapestPath(t *testing.T) {
	// Topology: a--s1 (500ms, direct to goal), a--f1--f2--goal (5ms each hop).
	// Hop-count routing picks s1; weighted routing must pick f1.
	goal := ledger.Node{ID: "goal", Status: "online",
		Neighbors: []string{"s1", "f2"}, Native: []ledger.NativeAbility{{ID: "x"}}}
	s1 := ledger.Node{ID: "s1", Status: "online",
		Neighbors: []string{"a", "goal"}, LinkMetrics: map[string]int64{"a": 500, "goal": 500}}
	f1 := ledger.Node{ID: "f1", Status: "online",
		Neighbors: []string{"a", "f2"}, LinkMetrics: map[string]int64{"a": 5, "f2": 5}}
	f2 := ledger.Node{ID: "f2", Status: "online",
		Neighbors: []string{"f1", "goal"}, LinkMetrics: map[string]int64{"f1": 5, "goal": 5}}
	self := ledger.Node{ID: "a", Status: "online",
		Neighbors:   []string{"s1", "f1"},
		LinkMetrics: map[string]int64{"s1": 500, "f1": 5}}
	hop := graphFirstHop(self, []ledger.Node{self, s1, f1, f2, goal}, nil, []string{"x"}, ledger.ResourceProfile{})
	if hop != "f1" {
		t.Fatalf("weighted path first hop = %q, want f1 (5ms+5ms+5ms beats 500ms+500ms)", hop)
	}
}

// An advertised neighbor with no measured RTT costs the unknown-link default:
// a measured path still wins, but an unmeasured one beats no path.
func TestGraphFirstHopUnknownLinkCost(t *testing.T) {
	goal := ledger.Node{ID: "goal", Status: "online",
		Neighbors: []string{"slow", "mid"}, Native: []ledger.NativeAbility{{ID: "x"}}}
	// Direct link with no metric advertised -> 1000ms default.
	slow := ledger.Node{ID: "slow", Status: "online", Neighbors: []string{"a", "goal"}}
	// Two measured hops at 100ms each -> 200ms total beats 1000ms unknown.
	mid := ledger.Node{ID: "mid", Status: "online",
		Neighbors: []string{"a", "goal"}, LinkMetrics: map[string]int64{"a": 100, "goal": 100}}
	self := ledger.Node{ID: "a", Status: "online",
		Neighbors:   []string{"slow", "mid"},
		LinkMetrics: map[string]int64{"mid": 100}} // slow deliberately unmeasured
	hop := graphFirstHop(self, []ledger.Node{self, slow, mid, goal}, nil, []string{"x"}, ledger.ResourceProfile{})
	if hop != "mid" {
		t.Fatalf("first hop = %q, want mid (200ms measured beats 1000ms unknown)", hop)
	}
}

// Offline rows are never traversed: a stale advertisement must not carry a
// task onto a dead link.
func TestGraphFirstHopNeverTraversesOffline(t *testing.T) {
	goal := ledger.Node{ID: "goal", Status: "online",
		Neighbors: []string{"dead"}, Native: []ledger.NativeAbility{{ID: "x"}}}
	dead := ledger.Node{ID: "dead", Status: "offline", Neighbors: []string{"a", "goal"}}
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"dead"}}
	hop := graphFirstHop(self, []ledger.Node{self, dead, goal}, nil, []string{"x"}, ledger.ResourceProfile{})
	if hop != "" {
		t.Fatalf("routed through offline relay: %q", hop)
	}
}
