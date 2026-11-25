// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// TestNetworkPolicyDefaults: a Core that never saw SetNetworkPolicy runs the
// documented defaults rather than a zero policy that would silently disable
// the ladder and the relay plane.
func TestNetworkPolicyDefaults(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "n1")
	pol := c.netPolicy()
	if pol.bootstrapEdges != rendezvousKDefault || !pol.autoPunch || !pol.transit || pol.rendezvous != "auto" {
		t.Fatalf("default policy = %+v", pol)
	}
	if got := c.rendezvousTarget(); got != rendezvousKDefault {
		t.Fatalf("target = %d, want %d", got, rendezvousKDefault)
	}
}

// TestNetworkPolicyInstall: the operator's knobs land, and an empty
// rendezvous mode resolves back to auto rather than "never".
func TestNetworkPolicyInstall(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "n1")
	c.SetNetworkPolicy(config.NetworkConfig{
		BootstrapEdges: 0,
		AutoPunch:      false,
		Transit:        false,
	})
	pol := c.netPolicy()
	if pol.bootstrapEdges != 0 || pol.autoPunch || pol.transit || pol.rendezvous != "auto" {
		t.Fatalf("installed policy = %+v", pol)
	}
	if got := c.rendezvousTarget(); got != 0 {
		t.Fatalf("target = %d, want manual mode", got)
	}
}

// TestRendezvousTargetClamps: an embedder that skips Validate cannot elect
// an unbounded set of edges.
func TestRendezvousTargetClamps(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "n1")
	c.SetNetworkPolicy(config.NetworkConfig{BootstrapEdges: 500, Transit: true, AutoPunch: true})
	if got := c.rendezvousTarget(); got != 8 {
		t.Fatalf("target = %d, want clamp to 8", got)
	}
	c.SetNetworkPolicy(config.NetworkConfig{BootstrapEdges: -3})
	if got := c.rendezvousTarget(); got != 0 {
		t.Fatalf("target = %d, want 0 for a negative K", got)
	}
}

// TestRendezvousManualModeClears: K=0 is manual mode — no set, and NOT
// reported as degraded, because the node did not fail to find a rendezvous,
// it declined to keep one.
func TestRendezvousManualModeClears(t *testing.T) {
	ctx := context.Background()
	c := newCoreOnDB(t, openTestDB(t), "n1")
	c.SetNetworkPolicy(config.NetworkConfig{BootstrapEdges: 0, Transit: true, AutoPunch: true})
	c.rzv.peers = map[string]bool{"stale": true}
	c.rendezvousEval(ctx)
	if got := c.RendezvousPeers(); len(got) != 0 {
		t.Fatalf("manual mode kept peers %v", got)
	}
	if c.RendezvousDegraded() {
		t.Fatal("manual mode must not report degraded")
	}
}

// TestRelayPolicyDefaults pins the design's docs/protocol.md defaults: we serve mesh and
// guarded members, open peers are not served, and we route our own frames
// only through mesh relays.
func TestRelayPolicyDefaults(t *testing.T) {
	var cfg config.TrustConfig
	cases := []struct {
		mode  string
		level trust.Level
		want  bool
	}{
		{"accept", trust.Mesh, true},
		{"accept", trust.Guarded, true},
		{"accept", trust.Open, false},
		{"serve", trust.Mesh, true},
		{"serve", trust.Guarded, false},
		{"serve", trust.Open, false},
	}
	for _, tc := range cases {
		var got bool
		if tc.mode == "accept" {
			got = cfg.RelayAcceptFor(tc.level)
		} else {
			got = cfg.RelayServeFor(tc.level)
		}
		if got != tc.want {
			t.Fatalf("%s(%s) = %v, want %v", tc.mode, tc.level, got, tc.want)
		}
	}
}

// TestRelayPolicyOverrides: a matrix that names one tier leaves the others
// at their design values.
func TestRelayPolicyOverrides(t *testing.T) {
	yes, no := true, false
	cfg := config.TrustConfig{}
	cfg.Relay.Transit.Open = &yes
	cfg.Relay.Serve.Guarded = &yes
	cfg.Relay.Serve.Mesh = &no
	if !cfg.RelayAcceptFor(trust.Open) {
		t.Fatal("open tier override not applied")
	}
	if !cfg.RelayAcceptFor(trust.Mesh) {
		t.Fatal("unnamed tier lost its default")
	}
	if !cfg.RelayServeFor(trust.Guarded) {
		t.Fatal("guarded serve override not applied")
	}
	if cfg.RelayServeFor(trust.Mesh) {
		t.Fatal("mesh serve override not applied")
	}
}

// TestRelayGateResolvesPeerTier: the gate reads the peer's recorded tier, so
// an operator who disabled serving for guarded peers actually stops relaying
// their frames.
func TestRelayGateResolvesPeerTier(t *testing.T) {
	ctx := context.Background()
	c := newCoreOnDB(t, openTestDB(t), "n1")
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, trust) VALUES ('friend', 'guarded'), ('stranger', 'open')`); err != nil {
		t.Fatal(err)
	}
	if !c.relayAcceptAllowed(ctx, "friend") {
		t.Fatal("guarded relay should be accepted by default")
	}
	if c.relayAcceptAllowed(ctx, "stranger") {
		t.Fatal("open relay must be refused by default")
	}
	if c.relayServeAllowed(ctx, "friend") {
		t.Fatal("guarded relay must not be used outbound by default")
	}
	no := false
	c.SetTrustConfig(config.TrustConfig{Relay: config.TrustRelayConfig{
		Transit: config.TrustRelayMatrix{Guarded: &no},
	}})
	if c.relayAcceptAllowed(ctx, "friend") {
		t.Fatal("guarded relay override not honored")
	}
}

// TestRelayBudgetSessions: max_sessions caps concurrent forwards and the
// release path hands the slot back.
func TestRelayBudgetSessions(t *testing.T) {
	var b relayBudget
	b.reconfigure(0, 2)
	rel1, ok1 := b.acquire()
	rel2, ok2 := b.acquire()
	if !ok1 || !ok2 {
		t.Fatal("two slots under a cap of two")
	}
	if _, ok := b.acquire(); ok {
		t.Fatal("third acquire must fail under a cap of two")
	}
	rel1()
	if b.sessionsActive() != 1 {
		t.Fatalf("sessions = %d, want 1", b.sessionsActive())
	}
	if _, ok := b.acquire(); !ok {
		t.Fatal("freed slot must be reusable")
	}
	rel2()

	var unlimited relayBudget
	unlimited.reconfigure(0, 0)
	for i := 0; i < 100; i++ {
		if _, ok := unlimited.acquire(); !ok {
			t.Fatal("zero max_sessions must mean unlimited")
		}
	}
}

// TestRelayBudgetBandwidth: a framed cost larger than one burst is refused
// outright; a small frame passes until the bucket drains and recovers after
// a refill interval.
func TestRelayBudgetBandwidth(t *testing.T) {
	var b relayBudget
	b.reconfigure(64, 0) // 64 kbit/s = 8 KiB/s, floored to a 16 KiB burst
	if !b.allow(4 << 10) {
		t.Fatal("small frame should fit the burst")
	}
	if b.allow(1 << 20) {
		t.Fatal("a frame larger than the burst must never pass")
	}
	if b.allow(14 << 10) {
		t.Fatal("bucket should be exhausted after the first spend")
	}
	b.mu.Lock()
	b.last = time.Now().Add(-2 * time.Second) // simulate two seconds of idle
	b.mu.Unlock()
	if !b.allow(4 << 10) {
		t.Fatal("bucket must refill over time")
	}
	var unlimited relayBudget
	unlimited.reconfigure(0, 0)
	if !unlimited.allow(1 << 30) {
		t.Fatal("zero max_kbps must mean unlimited")
	}
}

// TestHasPublicAddr: the auto rendezvous proof accepts a globally routable
// address or a hostname and refuses LAN, loopback and CGNAT bindings.
func TestHasPublicAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"203.0.113.5:7836", true},
		{"node.example.com:7836", true},
		{"[2001:db8::5]:7836", true},
		{"192.168.1.20:7836", false},
		{"10.0.0.4:7836", false},
		{"127.0.0.1:7836", false},
		{"100.101.102.103:7836", false}, // Tailscale CGNAT band
		{"[fd7a:115e:a214::1]:7836", false},
	}
	for _, tc := range cases {
		c := newCoreOnDB(t, openTestDB(t), "n1")
		c.listenAddr.Store(tc.addr)
		if got := c.hasPublicAddr(); got != tc.want {
			t.Fatalf("hasPublicAddr(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestSummaryAppliesRendezvousRole: the card's relay block is rewritten by
// the policy — "never" strips the role, transit:false strips the serving
// flag, and auto drops an unprovable rendezvous claim.
func TestSummaryAppliesRendezvousRole(t *testing.T) {
	card := ledger.Card{
		Device: "n1",
		Relay:  &ledger.RelayProfile{Rendezvous: true, Transit: true},
		Capacity: ledger.Capacity{
			CPUCores: 4, RAMGB: 8, MaxConcurrent: 2,
		},
	}
	build := func(policy config.NetworkConfig, listen string) *ledger.CapabilitySummary {
		db := openTestDB(t)
		c := NewCore(db, "n1", card, 5, testLogger(), config.ModelConfig{})
		c.SetWorkDir(t.TempDir())
		c.SetSharedSecret(testSharedSecret)
		c.SetNetworkPolicy(policy)
		c.listenAddr.Store(listen)
		s := c.summary()
		return &s
	}

	// never: no rendezvous claim, but the transit offer survives.
	s := build(config.NetworkConfig{Transit: true, AutoPunch: true, Rendezvous: "never"}, "203.0.113.5:7836")
	if s.Relay == nil || s.Relay.Rendezvous || !s.Relay.Transit {
		t.Fatalf("never: relay = %+v", s.Relay)
	}

	// auto + a LAN-only address: the claim is unprovable, so it is dropped.
	s = build(config.NetworkConfig{Transit: true, AutoPunch: true, Rendezvous: "auto"}, "192.168.1.20:7836")
	if s.Relay == nil || s.Relay.Rendezvous {
		t.Fatalf("auto/lan: relay = %+v", s.Relay)
	}

	// auto + a public address: the claim stands.
	s = build(config.NetworkConfig{Transit: true, AutoPunch: true, Rendezvous: "auto"}, "203.0.113.5:7836")
	if s.Relay == nil || !s.Relay.Rendezvous {
		t.Fatalf("auto/public: relay = %+v", s.Relay)
	}

	// always: the operator asserts reachability behind a NAT we cannot see.
	s = build(config.NetworkConfig{Transit: true, AutoPunch: true, Rendezvous: "always"}, "192.168.1.20:7836")
	if s.Relay == nil || !s.Relay.Rendezvous {
		t.Fatalf("always: relay = %+v", s.Relay)
	}

	// transit:false but an asserted rendezvous role: coordination and data
	// relay are separate services, so the block survives with only the
	// forwarding flag cleared.
	s = build(config.NetworkConfig{Transit: false, AutoPunch: true, Rendezvous: "always"}, "203.0.113.5:7836")
	if s.Relay == nil || !s.Relay.Rendezvous || s.Relay.Transit {
		t.Fatalf("transit off: relay = %+v", s.Relay)
	}

	// Both roles off: there is nothing to advertise, so the block is absent
	// rather than present-and-empty.
	s = build(config.NetworkConfig{Transit: false, AutoPunch: true, Rendezvous: "never"}, "203.0.113.5:7836")
	if s.Relay != nil {
		t.Fatalf("both off: relay = %+v, want nil", s.Relay)
	}
}

// TestHelloCapsDropTransitWhenDisabled: a node that will not relay must not
// advertise trnz, or peers route through it and lose the frame.
func TestHelloCapsDropTransitWhenDisabled(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "n1")
	if !containsStr(c.helloCaps(), "trnz") {
		t.Fatal("default policy must advertise trnz")
	}
	c.SetNetworkPolicy(config.NetworkConfig{Transit: false, AutoPunch: true, Rendezvous: "auto", BootstrapEdges: 2})
	if containsStr(c.helloCaps(), "trnz") {
		t.Fatal("transit:false must not advertise trnz")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
