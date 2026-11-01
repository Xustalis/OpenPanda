// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import "testing"

func TestNetworkGuardRequiresHTTPS(t *testing.T) {
	g := NewNetworkGuard()
	if err := g.CheckURL("https://api.deepseek.com/anthropic"); err != nil {
		t.Fatalf("https endpoint should pass: %v", err)
	}
	if err := g.CheckURL("http://api.deepseek.com/anthropic"); err == nil {
		t.Fatalf("cleartext endpoint must be rejected")
	}
	if err := g.CheckURL("http://localhost:8080/v1"); err != nil {
		t.Fatalf("localhost dev endpoint should pass: %v", err)
	}
	// Every loopback form is exempt, not just the two literals: IPv6 ::1 and
	// any 127/8 address are the same "plaintext stays on this host" case.
	for _, u := range []string{"http://127.0.0.1:11434/v1", "http://127.0.0.2:11434/v1", "http://[::1]:11434/v1"} {
		if err := g.CheckURL(u); err != nil {
			t.Fatalf("loopback endpoint %s should pass: %v", u, err)
		}
	}
	// A non-loopback literal IP still requires https.
	if err := g.CheckURL("http://192.168.1.10:11434/v1"); err == nil {
		t.Fatalf("cleartext non-loopback endpoint must be rejected")
	}
}

func TestNetworkGuardAllowlist(t *testing.T) {
	g := NewNetworkGuard("api.deepseek.com")
	if err := g.CheckURL("https://api.deepseek.com/anthropic"); err != nil {
		t.Fatalf("allowlisted host should pass: %v", err)
	}
	if err := g.CheckURL("https://evil.example.com"); err == nil {
		t.Fatalf("non-allowlisted host must be rejected")
	}
}

func TestNetworkGuardRejectsBadURL(t *testing.T) {
	if err := NewNetworkGuard().CheckURL("://not a url"); err == nil {
		t.Fatalf("malformed url must be rejected")
	}
}

// TestCheckModelEndpointPlaintext covers the per-model opt-in: a relay on a
// plain-http address is reachable only when the model itself says so, and the
// waiver stays pinned to that endpoint's host — it is a statement about one
// endpoint, not a licence to reach anything.
func TestCheckModelEndpointPlaintext(t *testing.T) {
	const relay = "http://106.53.40.245:8080"
	if err := CheckModelEndpoint(relay, false); err == nil {
		t.Fatal("plaintext endpoint without the opt-in must be rejected")
	}
	if err := CheckModelEndpoint(relay, true); err != nil {
		t.Fatalf("plaintext endpoint with the opt-in should pass: %v", err)
	}
	// https is unaffected by the flag in either direction.
	if err := CheckModelEndpoint("https://api.deepseek.com/anthropic", false); err != nil {
		t.Fatalf("https endpoint should pass: %v", err)
	}
	// The opt-in never widens the host pin: the guard is built from this
	// endpoint, so a different host is still refused.
	g := NewNetworkGuard(EndpointHost(relay))
	g.plaintext = true
	if err := g.CheckURL("http://10.0.0.5:8080"); err == nil {
		t.Fatal("the opt-in must not admit a different host")
	}
	// Loopback keeps needing no opt-in at all.
	if err := CheckModelEndpoint("http://127.0.0.1:11434/v1", false); err != nil {
		t.Fatalf("loopback endpoint should pass without the opt-in: %v", err)
	}
}

func TestEndpointHost(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/anthropic": "api.deepseek.com",
		"https://API.DeepSeek.Com:443/x":     "api.deepseek.com:443",
		"https://localhost:8080/v1":          "localhost:8080",
		"http://127.0.0.1:11434":             "127.0.0.1:11434",
		"://not a url":                       "",
		"https://":                           "",
	}
	for in, want := range cases {
		if got := EndpointHost(in); got != want {
			t.Fatalf("EndpointHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetworkGuardPinsToEndpointHost(t *testing.T) {
	// The production path derives the pin from the configured endpoint, so the
	// guard rejects any other https host rather than running with an empty
	// allowlist (D7).
	g := NewNetworkGuard(EndpointHost("https://api.deepseek.com/anthropic"))
	if err := g.CheckURL("https://api.deepseek.com/anthropic"); err != nil {
		t.Fatalf("configured host should pass: %v", err)
	}
	if err := g.CheckURL("https://evil.example.com/anthropic"); err == nil {
		t.Fatalf("non-configured host must be rejected")
	}
}
