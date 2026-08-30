package core

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// applyPeerBlockers is how a peer's published failure history reaches Route:
// a circuit-open agent is stripped from the peer's ability set so the peer
// loses the match instead of earning a bounce-decline round trip. The self
// row must survive untouched — the local breaker is enforced at execution
// time, where a half-open trial can still clear the circuit.
func TestApplyPeerBlockersStripsCircuitOpenAgents(t *testing.T) {
	c := &Core{
		nodeID: "self",
		peerBlocked: map[string][]string{
			"peer-a": {"claude"},
		},
	}
	nodes := []ledger.Node{
		{ID: "self", Status: "online", Agents: map[string]ledger.Agent{"claude": {}}},
		{ID: "peer-a", Status: "online", Agents: map[string]ledger.Agent{"claude": {}, "codex": {}}},
		{ID: "peer-b", Status: "online", Agents: map[string]ledger.Agent{"claude": {}}},
	}
	out := c.applyPeerBlockers(nodes)

	if _, ok := out[0].Agents["claude"]; !ok {
		t.Fatalf("self row must keep its own agents (local breaker enforces at run time)")
	}
	if _, ok := out[1].Agents["claude"]; ok {
		t.Fatalf("peer-a's circuit-open agent must be stripped")
	}
	if _, ok := out[1].Agents["codex"]; !ok {
		t.Fatalf("peer-a's healthy agents must survive the strip")
	}
	if _, ok := out[2].Agents["claude"]; !ok {
		t.Fatalf("peer-b published no blocklist; its agents must survive")
	}
}

// A stripped peer whose only ability was the blocked agent no longer matches
// an agent requirement, so Route forwards to the healthy peer instead.
func TestRouteSkipsPeerWhoseOnlyAgentIsCircuitOpen(t *testing.T) {
	c := &Core{
		nodeID: "self",
		peerBlocked: map[string][]string{
			"peer-a": {"claude"},
		},
	}
	nodes := c.applyPeerBlockers([]ledger.Node{
		{ID: "peer-a", Status: "online", Agents: map[string]ledger.Agent{"claude": {}}},
		{ID: "peer-b", Status: "online", Agents: map[string]ledger.Agent{"claude": {}}},
	})
	if nodes[0].Matches([]string{"agent:claude"}) {
		t.Fatalf("peer-a must no longer match agent:claude after the strip")
	}
	if !nodes[1].Matches([]string{"agent:claude"}) {
		t.Fatalf("peer-b must still match agent:claude")
	}
}
