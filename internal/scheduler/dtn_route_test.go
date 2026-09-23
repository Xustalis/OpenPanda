package scheduler

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The basic multi-hop case: custody should move to the online neighbor whose
// advertised path reaches the offline destination — first hop b, never c
// itself (it cannot hold custody while offline is not the issue — c IS the
// dest and offline, which is legal as a terminal).
func TestDTNNextHopBasic(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"a", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"b"}}
	hop := DTNNextHop(self, []ledger.Node{self, b, d}, "d", nil)
	if hop != "b" {
		t.Fatalf("first hop = %q, want b", hop)
	}
}

// The no-echo rule: the only advertised path runs back through the peer the
// bundle arrived from. Returning it there is not routing, it is a loop, so
// no hop is found and the relay parks the bundle instead.
func TestDTNNextHopNoEcho(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"s"}}
	s := ledger.Node{ID: "s", Status: "online", Neighbors: []string{"a", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"s"}}
	hop := DTNNextHop(self, []ledger.Node{self, s, d}, "d", map[string]bool{"s": true})
	if hop != "" {
		t.Fatalf("echo hop = %q, want none", hop)
	}
}

// Exclusion applies at the first hop only: a path that passes the excluded
// node deeper in the graph is still legitimate — the echo guard must not
// become a path constraint.
func TestDTNNextHopExcludeFirstHopOnly(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b", "s"}}
	s := ledger.Node{ID: "s", Status: "online", Neighbors: []string{"a"}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"a", "s", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"s"}}
	// s is the via and is excluded as a first hop, but a→b→s→d remains valid.
	hop := DTNNextHop(self, []ledger.Node{self, s, b, d}, "d", map[string]bool{"s": true})
	if hop != "b" {
		t.Fatalf("first hop = %q, want b", hop)
	}
}

// An offline intermediate cannot hold custody: when the only path to dest
// crosses a dead node there is no route, even though the edge set reaches it.
func TestDTNNextHopOfflineIntermediate(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"}}
	b := ledger.Node{ID: "b", Status: "offline", Neighbors: []string{"a", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"b"}}
	hop := DTNNextHop(self, []ledger.Node{self, b, d}, "d", nil)
	if hop != "" {
		t.Fatalf("route through offline b = %q, want none", hop)
	}
}

// Link metrics steer the choice: a measured cheap long path beats a one-hop
// expensive one.
func TestDTNNextHopWeighted(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"x", "y"},
		LinkMetrics: map[string]int64{"x": 900, "y": 5}}
	x := ledger.Node{ID: "x", Status: "online", Neighbors: []string{"a", "d"}}
	y := ledger.Node{ID: "y", Status: "online", Neighbors: []string{"a", "z"},
		LinkMetrics: map[string]int64{"z": 5}}
	z := ledger.Node{ID: "z", Status: "online", Neighbors: []string{"y", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"x", "z"}}
	hop := DTNNextHop(self, []ledger.Node{self, x, y, z, d}, "d", nil)
	if hop != "y" {
		t.Fatalf("first hop = %q, want y (a→y→z→d is cheaper than a→x→d)", hop)
	}
}
