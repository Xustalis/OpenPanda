// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// TestCleartextGate pins the outbound plaintext policy: ws:// is refused for
// any host that does not already travel an encrypted-or-local underlay
// (loopback, Tailscale). The gate exists because post-hello frames are
// unsigned and unencrypted — a MITM on a plaintext link can inject a forged
// task_delegate, which is remote code execution, not just eavesdropping.
func TestCleartextGate(t *testing.T) {
	allowed := []string{
		"127.0.0.1:7836",                // loopback IPv4
		"127.0.0.2:7836",                // the whole /8, not just .1
		"[::1]:7836",                    // loopback IPv6
		"localhost:7836",                // loopback name
		"worker.tailnet.ts.net:7836",    // MagicDNS — resolves inside the tailnet
		"100.64.0.5:7836",               // Tailscale CGNAT range
		"100.127.255.254:7836",          // CGNAT top edge
		"[fd7a:115e:a214::1234]:7836",   // Tailscale IPv6 ULA
		"ws://127.0.0.1:7836/ws",        // explicit ws:// to a safe host
		"wss://peer.example.com:443/ws", // TLS-terminated anywhere
		"wss://203.0.113.10:7836/ws",    // TLS-terminated, public IP
	}
	for _, addr := range allowed {
		if err := cleartextOK(addr, false, nil); err != nil {
			t.Errorf("cleartextOK(%q) = %v, want allowed", addr, err)
		}
	}

	blocked := []string{
		"203.0.113.10:7836",      // bare public IP → ws://
		"ws://203.0.113.10:7836", // explicit ws:// to public IP — still cleartext
		"peer.example.com:7836",  // arbitrary hostname
		"100.63.255.255:7836",    // one below the CGNAT block
		"100.128.0.1:7836",       // one above it
		"10.0.0.5:7836",          // RFC1918 LAN is NOT an encrypted underlay
		"192.168.1.5:7836",
		"ws://attacker.example.com:80",
	}
	for _, addr := range blocked {
		err := cleartextOK(addr, false, nil)
		if err == nil {
			t.Errorf("cleartextOK(%q) = nil, want refusal", addr)
			continue
		}
		if !strings.Contains(err.Error(), "allow_cleartext") {
			t.Errorf("cleartextOK(%q) error %q does not name the escape hatch", addr, err)
		}
	}

	// The explicit opt-out restores the old behavior for every target.
	for _, addr := range blocked {
		if err := cleartextOK(addr, true, nil); err != nil {
			t.Errorf("cleartextOK(%q, allow) = %v, want allowed", addr, err)
		}
	}
}

// TestCleartextAllowlist pins the scoped opt-in (network.allow_cleartext_for):
// a listed CIDR/IP/hostname passes the gate while neighbours of it stay
// refused — the whole point of the knob over the global flag.
func TestCleartextAllowlist(t *testing.T) {
	allowFor := []string{"192.168.0.0/16", "10.1.2.3", "worker.lan"}

	listed := []string{
		"192.168.1.5:7836",        // inside the CIDR
		"192.168.255.254:7836",    // CIDR top edge
		"ws://192.168.0.1:7836",   // URL form, same net
		"10.1.2.3:7836",           // literal IP entry
		"worker.lan:7836",         // exact hostname
		"WORKER.LAN:7836",         // hostnames match case-insensitively
		"ws://worker.lan:7836/ws", // URL form, same host
	}
	for _, addr := range listed {
		if err := cleartextOK(addr, false, allowFor); err != nil {
			t.Errorf("cleartextOK(%q, allowFor) = %v, want allowed", addr, err)
		}
	}

	stillBlocked := []string{
		"192.167.1.5:7836",        // one outside the CIDR
		"10.1.2.4:7836",           // adjacent IP, not listed
		"other.lan:7836",          // hostname not listed
		"sub.worker.lan:7836",     // suffix match does NOT count — exact only
		"worker.lan.evil.com:443", // hostname must equal, not prefix-match
	}
	for _, addr := range stillBlocked {
		if err := cleartextOK(addr, false, allowFor); err == nil {
			t.Errorf("cleartextOK(%q, allowFor) = nil, want refusal", addr)
		}
	}

	// An allowlist entry is additive, not a replacement for the safe set:
	// loopback still passes with no overlap.
	if err := cleartextOK("127.0.0.1:7836", false, allowFor); err != nil {
		t.Errorf("cleartextOK(loopback, allowFor) = %v, want allowed", err)
	}
}
