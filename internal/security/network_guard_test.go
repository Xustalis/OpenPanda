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
