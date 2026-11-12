// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"fmt"
	"net"
	"net/url"
	"os"
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
	// plaintext waives the https requirement for a caller that has explicitly
	// opted in (a model's own `allow_plaintext`). It never widens the
	// allowlist: the host still has to be the one that was pinned.
	plaintext bool
}

// NewNetworkGuard builds a guard allowing the given hostnames (bare or
// host:port). Matching is case-insensitive and treats a host:port entry as
// allowing the bare host too — the entry is stored verbatim (so an exact
// host:port match still works) plus its hostname leg (so a URL that names the
// host without a port, or on a different one, still matches the bare-host
// check).
func NewNetworkGuard(hosts ...string) *NetworkGuard {
	g := &NetworkGuard{allowed: make(map[string]bool, len(hosts))}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		g.allowed[h] = true
		if host, _, err := net.SplitHostPort(h); err == nil && host != "" {
			g.allowed[host] = true
		}
	}
	return g
}

// CheckURL validates a URL against the guard: it must be https and its host
// must be in the allowlist. Loopback endpoints (localhost, 127.0.0.0/8, ::1)
// are exempt from the https requirement (plaintext is fine for a local dev
// model), but are still subject to the host allowlist — so a guard pinned to
// a remote endpoint (D7) rejects loopback too.
func (g *NetworkGuard) CheckURL(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("network guard: parse endpoint: %w", err)
	}
	// Hostnames are case-insensitive and a trailing dot is the same host;
	// normalise before the loopback exemption or "LOCALHOST." would be
	// rejected for a plaintext URL that is plainly local.
	hostName := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if u.Scheme != "https" && hostName != "localhost" && !isLoopback(hostName) && !g.plaintext {
		return fmt.Errorf("network guard: model endpoint must be https (got %q) — "+
			"a plaintext endpoint needs allow_plaintext on that model", u.Scheme)
	}
	host := strings.ToLower(u.Host)
	if g.allowed[host] || g.allowed[hostName] {
		return nil
	}
	if len(g.allowed) == 0 {
		// An empty allowlist means "no explicit restriction beyond https" — the
		// caller opted not to pin a host.
		return nil
	}
	return fmt.Errorf("network guard: host %q not in allowlist", u.Host)
}

// ModelAllowedHostsEnv is an out-of-band host allowlist for model endpoints,
// as a comma-separated list of hostnames (host:port is accepted too). It is
// read from the process environment — a different trust source from the
// agent-writable config file that supplies base_url — so an operator can pin
// which hosts a model endpoint is allowed to name. When it is unset the
// legacy self-pinned behaviour applies and no existing deployment changes.
const ModelAllowedHostsEnv = "OPENPANDA_MODEL_ALLOWED_HOSTS"

// CheckModelEndpointPinned validates rawurl against an explicit allowlist.
// Deriving the pin from the URL being checked proves nothing about the URL,
// so a non-empty list replaces that self-pin; an empty list falls back to it
// for compatibility. The https rule (and the per-model plaintext opt-in) is
// enforced in both cases.
func CheckModelEndpointPinned(rawurl string, allowPlaintext bool, allowedHosts []string) error {
	var hosts []string
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{EndpointHost(rawurl)}
	}
	g := NewNetworkGuard(hosts...)
	g.plaintext = allowPlaintext
	return g.CheckURL(rawurl)
}

// CheckModelEndpoint is the one call the model paths make. It honours the
// model's plaintext opt-in and, when ModelAllowedHostsEnv is set, an
// operator-supplied allowlist; otherwise it pins the guard to this endpoint's
// own host as before. A model that never opted in keeps the https rule, so the
// waiver has to be asked for in the model's own config rather than inferred
// from the URL.
func CheckModelEndpoint(rawurl string, allowPlaintext bool) error {
	explicit := strings.FieldsFunc(os.Getenv(ModelAllowedHostsEnv), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	return CheckModelEndpointPinned(rawurl, allowPlaintext, explicit)
}

// isLoopback reports whether host parses as a loopback IP — covering ::1 and
// the whole 127.0.0.0/8, which the old literal check for "127.0.0.1" missed.
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// EndpointHost returns the lowercased host (host:port) of a URL, or "" when the
// URL cannot be parsed or has no host. Callers use it to pin the guard to the
// configured model endpoint so the allowlist is never empty in production.
func EndpointHost(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}
