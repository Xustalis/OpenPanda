package security

import (
	"fmt"
	"net/url"
	"strings"
)

// NetworkGuard validates the endpoints an agent subprocess is pointed at
// (plan P3-30). The deterministic MVP enforces two things at spawn time:
// the model endpoint is HTTPS (so the API key never travels cleartext) and it
// is in an explicit allowlist. Hard Linux-level egress control (netns/iptables
// restricting what the subprocess can reach) is a deploy concern layered on
// top; this guards the one host PANDA itself hands to the adapter.
type NetworkGuard struct {
	allowed map[string]bool
}

// NewNetworkGuard builds a guard allowing the given hostnames (bare or
// host:port). Matching is case-insensitive and treats a host:port entry as
// allowing the bare host too.
func NewNetworkGuard(hosts ...string) *NetworkGuard {
	g := &NetworkGuard{allowed: make(map[string]bool, len(hosts))}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		g.allowed[strings.ToLower(h)] = true
	}
	return g
}

// CheckURL validates a URL against the guard: it must be https and its host
// must be in the allowlist. Localhost is always allowed for development.
func (g *NetworkGuard) CheckURL(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("network guard: parse endpoint: %w", err)
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return fmt.Errorf("network guard: model endpoint must be https (got %q)", u.Scheme)
	}
	host := strings.ToLower(u.Host)
	if g.allowed[host] || g.allowed[strings.ToLower(u.Hostname())] {
		return nil
	}
	if len(g.allowed) == 0 {
		// An empty allowlist means "no explicit restriction beyond https" — the
		// caller opted not to pin a host.
		return nil
	}
	return fmt.Errorf("network guard: host %q not in allowlist", u.Host)
}
