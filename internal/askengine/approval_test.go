package askengine

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestGateAuthorizedModes pins the three-mode tier-2 consent semantics that
// submitTask relies on (the single gate for irreversible tasks):
//   - never       auto-consents regardless of any session grant;
//   - on-request  withholds consent until an explicit grant arrives;
//   - always      behaves like on-request at this layer (the "confirm every
//     run" strictness is a UI concern), and an explicit grant still passes.
//
// The empty mode must normalize to on-request behavior via NormalizedMode so a
// misconfigured node fails closed, never open.
func TestGateAuthorizedModes(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		sessionAuth bool
		want        bool
	}{
		{"never auto-consents without a grant", config.ApprovalModeNever, false, true},
		{"never auto-consents with a grant", config.ApprovalModeNever, true, true},
		{"on-request withholds without a grant", config.ApprovalModeOnRequest, false, false},
		{"on-request honors an explicit grant", config.ApprovalModeOnRequest, true, true},
		{"always withholds without a grant", config.ApprovalModeAlways, false, false},
		{"always honors an explicit grant", config.ApprovalModeAlways, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Route through NormalizedMode exactly as submitTask does, so the
			// test covers the real call shape (including empty-mode fallback).
			mode := config.ApprovalConfig{Mode: tc.mode}.NormalizedMode()
			if got := gateAuthorized(mode, tc.sessionAuth); got != tc.want {
				t.Fatalf("gateAuthorized(%q, %v) = %v, want %v", mode, tc.sessionAuth, got, tc.want)
			}
		})
	}
}

// TestGateAuthorizedEmptyModeFailsClosed guards the misconfiguration path: an
// unset approval mode must NOT auto-authorize tier-2 work. It normalizes to
// on-request, so without a session grant the gate withholds consent.
func TestGateAuthorizedEmptyModeFailsClosed(t *testing.T) {
	mode := config.ApprovalConfig{}.NormalizedMode()
	if gateAuthorized(mode, false) {
		t.Fatalf("empty approval mode auto-authorized tier-2 (fail-open); mode normalized to %q", mode)
	}
	if !gateAuthorized(mode, true) {
		t.Fatalf("empty approval mode ignored an explicit session grant; mode normalized to %q", mode)
	}
}
