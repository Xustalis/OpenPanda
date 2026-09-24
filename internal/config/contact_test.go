package config

import (
	"strings"
	"testing"
)

// RFC3339 and unix-seconds timestamps both resolve to the same instant.
func TestContactConfigResolveTimeForms(t *testing.T) {
	byRFC := ContactConfig{Peer: "b", Start: "2026-09-25T02:00:00Z", End: "2026-09-25T02:10:00Z"}
	byUnix := ContactConfig{Peer: "b", Start: "1790301600", End: "1790302200"}
	a, err := byRFC.Resolve()
	if err != nil {
		t.Fatalf("RFC3339 resolve: %v", err)
	}
	b, err := byUnix.Resolve()
	if err != nil {
		t.Fatalf("unix resolve: %v", err)
	}
	if a.Start != b.Start || a.End != b.End {
		t.Fatalf("time forms disagree: rfc %d..%d unix %d..%d", a.Start, a.End, b.Start, b.End)
	}
}

// Period accepts a Go duration or plain seconds; both must land positive.
func TestContactConfigResolvePeriod(t *testing.T) {
	c := ContactConfig{Peer: "b", Start: "1000", End: "1600", Period: "24h"}
	e, err := c.Resolve()
	if err != nil || e.Period != 86400 {
		t.Fatalf("period 24h = %d, err %v", e.Period, err)
	}
	c.Period = "86400"
	if e, err := c.Resolve(); err != nil || e.Period != 86400 {
		t.Fatalf("period seconds = %d, err %v", e.Period, err)
	}
	// Omitted period is a one-shot window.
	c.Period = ""
	if e, err := c.Resolve(); err != nil || e.Period != 0 {
		t.Fatalf("empty period = %d, err %v", e.Period, err)
	}
}

// Every malformed entry must fail — a silently dropped window strands the
// bundles the operator scheduled.
func TestContactConfigResolveRejects(t *testing.T) {
	cases := []struct {
		name string
		c    ContactConfig
	}{
		{"no peer", ContactConfig{Start: "1000", End: "1600"}},
		{"empty start", ContactConfig{Peer: "b", Start: "", End: "1600"}},
		{"bad start", ContactConfig{Peer: "b", Start: "tomorrow", End: "1600"}},
		{"bad end", ContactConfig{Peer: "b", Start: "1000", End: "soon"}},
		{"end before start", ContactConfig{Peer: "b", Start: "1600", End: "1000"}},
		{"degenerate", ContactConfig{Peer: "b", Start: "1600", End: "1600"}},
		{"bad period", ContactConfig{Peer: "b", Start: "1", End: "2", Period: "daily"}},
		{"zero period", ContactConfig{Peer: "b", Start: "1", End: "2", Period: "0"}},
		{"negative period", ContactConfig{Peer: "b", Start: "1", End: "2", Period: "-1h"}},
	}
	for _, tc := range cases {
		if _, err := tc.c.Resolve(); err == nil {
			t.Fatalf("%s: Resolve(%+v) succeeded, want error", tc.name, tc.c)
		}
	}
}

// Validate must surface contact errors at config check, not first bundle.
func TestValidateRejectsBadContacts(t *testing.T) {
	cfg := Default()
	cfg.Network.Contacts = []ContactConfig{{Peer: "b", Start: "1600", End: "1000"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "contacts") {
		t.Fatalf("Validate = %v, want a contacts error", err)
	}
	cfg.Network.Contacts = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate without contacts = %v", err)
	}
}
