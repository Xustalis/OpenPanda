package scheduler

import (
	"errors"
	"testing"

	"github.com/xenith/panda/internal/ledger"
)

func TestAppendChainLoop(t *testing.T) {
	got, err := AppendChain([]string{"a", "b"}, "c")
	if err != nil {
		t.Fatalf("append new: %v", err)
	}
	if len(got) != 3 || got[2] != "c" {
		t.Fatalf("chain = %v, want [a b c]", got)
	}

	if _, err := AppendChain([]string{"a", "b"}, "a"); !errors.Is(err, ErrLoop) {
		t.Fatalf("expected ErrLoop, got %v", err)
	}
}

func TestPredecessor(t *testing.T) {
	cases := []struct {
		chain []string
		self  string
		want  string
	}{
		{[]string{"a"}, "a", ""},            // root
		{[]string{"a", "b"}, "b", "a"},      // middle
		{[]string{"a", "b", "c"}, "c", "b"}, // leaf
		{[]string{"a", "b"}, "x", ""},       // absent
		{[]string{"a", "b", "a"}, "a", "b"}, // last occurrence
	}
	for _, c := range cases {
		if got := Predecessor(c.chain, c.self); got != c.want {
			t.Fatalf("Predecessor(%v, %q) = %q, want %q", c.chain, c.self, got, c.want)
		}
	}
}

func matchAny(required []string) bool { return len(required) > 0 && required[0] == "local:ok" }

func TestRouteLocalWins(t *testing.T) {
	d := Route("self", []string{"self"}, nil, matchAny, []string{"local:ok"})
	if d.Action != ActionLocal {
		t.Fatalf("action = %s, want local", d.Action)
	}
}

func TestRouteForwardsToMatchingPeer(t *testing.T) {
	employees := []ledger.Node{
		{ID: "z", Status: "online", Native: []ledger.NativeAbility{{ID: "gpio:read"}}},
		{ID: "a", Status: "online", Native: []ledger.NativeAbility{{ID: "gpio:read"}}},
		{ID: "off", Status: "offline", Native: []ledger.NativeAbility{{ID: "gpio:read"}}},
		{ID: "self", Status: "online", Native: []ledger.NativeAbility{{ID: "gpio:read"}}},
	}
	neverLocal := func([]string) bool { return false }

	d := Route("self", []string{"self"}, employees, neverLocal, []string{"gpio:read"})
	if d.Action != ActionForward || d.Target != "a" {
		t.Fatalf("decision = %+v, want forward to a (lowest id, online, non-self)", d)
	}
}

func TestRouteDeclinesWhenNoMatch(t *testing.T) {
	employees := []ledger.Node{
		{ID: "a", Status: "online", Native: []ledger.NativeAbility{{ID: "sys:info"}}},
	}
	neverLocal := func([]string) bool { return false }

	d := Route("self", []string{"self"}, employees, neverLocal, []string{"code:modify"})
	if d.Action != ActionDecline || d.Reason == "" {
		t.Fatalf("decision = %+v, want decline with reason", d)
	}
}

func TestRouteSkipsNodeAlreadyOnChain(t *testing.T) {
	// The only capable peer is already on the chain, so a forward would loop.
	employees := []ledger.Node{
		{ID: "a", Status: "online", Native: []ledger.NativeAbility{{ID: "gpio:read"}}},
	}
	neverLocal := func([]string) bool { return false }

	d := Route("self", []string{"root", "a", "self"}, employees, neverLocal, []string{"gpio:read"})
	if d.Action != ActionDecline {
		t.Fatalf("decision = %+v, want decline (candidate already on chain)", d)
	}
}

func TestRouteForwardsToSubScheduler(t *testing.T) {
	// No peer matches the required ability, but a Standard-tier peer can route
	// the task further downstream, so we forward to it rather than declining.
	employees := []ledger.Node{
		{ID: "worker", Status: "online", SchedulerTier: 1, Native: []ledger.NativeAbility{{ID: "sys:info"}}},
		{ID: "sub", Status: "online", SchedulerTier: 5, Native: []ledger.NativeAbility{{ID: "sys:info"}}},
	}
	neverLocal := func([]string) bool { return false }

	d := Route("self", []string{"self"}, employees, neverLocal, []string{"code:modify"})
	if d.Action != ActionForward || d.Target != "sub" {
		t.Fatalf("decision = %+v, want forward to sub-scheduler (tier>1)", d)
	}
}

func TestRoutePrefersMatchingOverSubScheduler(t *testing.T) {
	// A direct match beats a sub-scheduler even when the sub-scheduler has a
	// lower id — capability match is the primary routing signal.
	employees := []ledger.Node{
		{ID: "a-sub", Status: "online", SchedulerTier: 10, Native: []ledger.NativeAbility{{ID: "sys:info"}}},
		{ID: "z-match", Status: "online", Native: []ledger.NativeAbility{{ID: "code:modify"}}},
	}
	neverLocal := func([]string) bool { return false }

	d := Route("self", []string{"self"}, employees, neverLocal, []string{"code:modify"})
	if d.Action != ActionForward || d.Target != "z-match" {
		t.Fatalf("decision = %+v, want forward to matching peer (z-match)", d)
	}
}
