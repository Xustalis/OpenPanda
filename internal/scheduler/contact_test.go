package scheduler

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The basic scheduled hop: dest is unreachable live, but self holds a
// window to it — the contact peer IS the first hop, and the ETA is when the
// transfer finishes inside the window.
func TestContactNextHopDirectWindow(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "d", Start: 1000, End: 1600, RateBps: 800}}}
	d := ledger.Node{ID: "d", Status: "offline"}
	hop, eta := ContactNextHop(self, []ledger.Node{self, d}, "d", nil, 500, 100, 0)
	if hop != "d" || eta != 1001 { // 100B @ 800bps = 1s into the window
		t.Fatalf("hop,eta = (%q,%d), want (d,1001)", hop, eta)
	}
}

// A live neighbor path delivers NOW, so it must beat a window that opens
// later — earliest-arrival, not contact-preferring.
func TestContactNextHopLiveBeatsLaterWindow(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"},
		Contacts: []ledger.Contact{{Peer: "d", Start: 5000, End: 5600}}}
	b := ledger.Node{ID: "b", Status: "online", Neighbors: []string{"a", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"b"}}
	hop, eta := ContactNextHop(self, []ledger.Node{self, b, d}, "d", nil, 500, 100, 0)
	if hop != "b" || eta != 500 {
		t.Fatalf("hop,eta = (%q,%d), want (b,500) — live path arrives at once", hop, eta)
	}
}

// But when the live path is broken (intermediate offline), the window is
// what saves the route.
func TestContactNextHopWindowWhenLiveBroken(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online", Neighbors: []string{"b"},
		Contacts: []ledger.Contact{{Peer: "d", Start: 5000, End: 5600}}}
	b := ledger.Node{ID: "b", Status: "offline", Neighbors: []string{"a", "d"}}
	d := ledger.Node{ID: "d", Status: "offline", Neighbors: []string{"b"}}
	hop, eta := ContactNextHop(self, []ledger.Node{self, b, d}, "d", nil, 500, 0, 0)
	if hop != "d" || eta != 5000 {
		t.Fatalf("hop,eta = (%q,%d), want (d,5000)", hop, eta)
	}
}

// Custody through a sleeping relay: a→b only inside a window, then b→d is a
// live edge. b is offline NOW but published the plan it will wake for — the
// whole point of contact routing.
func TestContactNextHopSleepingRelay(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "b", Start: 1000, End: 1600, RateBps: 800}}}
	b := ledger.Node{ID: "b", Status: "offline", Neighbors: []string{"d"},
		Contacts: []ledger.Contact{{Peer: "d", Start: 2000, End: 2600, RateBps: 800}}}
	d := ledger.Node{ID: "d", Status: "offline"}
	hop, eta := ContactNextHop(self, []ledger.Node{self, b, d}, "d", nil, 500, 100, 0)
	// a→b finishes at 1001 while b is awake for the window; b's live edge to
	// d is then traversable at once, beating its own later contact (2001).
	if hop != "b" || eta != 1001 {
		t.Fatalf("hop,eta = (%q,%d), want (b,1001)", hop, eta)
	}
}

// Expiry prunes: a window that opens after the bundle's deadline is no path.
func TestContactNextHopExpiryPrunes(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "d", Start: 5000, End: 5600}}}
	d := ledger.Node{ID: "d", Status: "offline"}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, d}, "d", nil, 500, 0, 4000); hop != "" {
		t.Fatalf("hop = %q, want none — window opens after expiry", hop)
	}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, d}, "d", nil, 500, 0, 5000); hop != "d" {
		t.Fatalf("hop = %q, want d — arrival at expiry boundary still counts", hop)
	}
}

// The transfer that cannot finish inside any window strands the bundle —
// SendAt fails and there is no route.
func TestContactNextHopTooBigForWindow(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "d", Start: 1000, End: 1600, RateBps: 8}}} // 600s window, 8bps
	d := ledger.Node{ID: "d", Status: "offline"}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, d}, "d", nil, 500, 100000, 0); hop != "" {
		t.Fatalf("hop = %q, want none — 800kb @ 8bps outlives every window", hop)
	}
}

// The no-echo rule still applies at the first hop: the only window leads
// back to the peer the bundle arrived from.
func TestContactNextHopNoEcho(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "s", Start: 1000, End: 1600}}}
	s := ledger.Node{ID: "s", Status: "online",
		Contacts: []ledger.Contact{{Peer: "d", Start: 1000, End: 1600}}}
	d := ledger.Node{ID: "d", Status: "offline"}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, s, d}, "d", map[string]bool{"s": true}, 500, 0, 0); hop != "" {
		t.Fatalf("echo hop = %q, want none", hop)
	}
}

// A window beyond the trust horizon is a parked-bundle problem, not a route.
func TestContactNextHopHorizon(t *testing.T) {
	far := int64(500) + contactHorizonSec + 3600
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "d", Start: far, End: far + 600}}}
	d := ledger.Node{ID: "d", Status: "offline"}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, d}, "d", nil, 500, 0, 0); hop != "" {
		t.Fatalf("hop = %q, want none — window beyond horizon", hop)
	}
}

// An offline intermediate with NO plan still cannot hold custody — contacts
// must not resurrect dead nodes as parking lots.
func TestContactNextHopOfflineRelayNoPlan(t *testing.T) {
	self := ledger.Node{ID: "a", Status: "online",
		Contacts: []ledger.Contact{{Peer: "b", Start: 1000, End: 1600}}}
	b := ledger.Node{ID: "b", Status: "offline", Neighbors: []string{"d"}} // no plan
	d := ledger.Node{ID: "d", Status: "offline"}
	if hop, _ := ContactNextHop(self, []ledger.Node{self, b, d}, "d", nil, 500, 0, 0); hop != "" {
		t.Fatalf("hop = %q, want none — b can receive but never forward", hop)
	}
}
