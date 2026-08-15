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
