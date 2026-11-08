// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// ProxyTargetMetadata reports whether rawurl names a cloud metadata service
// that an egress proxy would resolve outside this process. GuardedDialContext
// only sees the proxy address on such a connection, so the target has to be
// resolved and judged here, before the request carries a credential into the
// proxy. A name the local resolver cannot answer is allowed through: a
// proxy-only split-DNS deployment still has to work.
func ProxyTargetMetadata(rawurl string) bool {
	if !proxyConfigured() {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return MetadataHost(host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if MetadataHost(ip.String()) {
			return true
		}
	}
	return false
}

// proxyConfigured reports an egress proxy in the order net/http reads one.
func proxyConfigured() bool {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// cleanHost trims whitespace, strips any port, and folds to lowercase.
func cleanHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// MetadataHost reports whether host names an address a cloud instance uses
// to hand out its own credentials — the 169.254.169.254 (AWS, Azure, GCP,
// DigitalOcean, OpenStack) and 100.100.100.200 (Alibaba Cloud) metadata
// services, plus the whole link-local range they live in.
//
// It matters because the panel takes an endpoint URL from an HTTP request
// body and then dials it from the host. A caller who holds the panel token
// can point a model endpoint at the metadata service and read the answer
// back through the console's own "test this model" round trip: a token
// leak becomes a cloud-credential leak, with the daemon as the confused
// deputy.
//
// Link-local covers fe80::/10 as well, where the same trick works over
// IPv6.
func MetadataHost(host string) bool {
	host = cleanHost(host)
	if host == "" {
		return false
	}
	// The Alibaba metadata address sits in CGNAT space (100.64/10), which is
	// otherwise legitimate — so it needs its own name, not a range rule.
	if host == "100.100.100.200" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	return ip.Equal(net.IPv4(169, 254, 169, 254))
}

// PrivateHost reports whether host resolves to an address inside the
// host's own network: RFC1918 space, carrier-grade NAT, and IPv6 unique
// local addresses. A browser push endpoint or a model endpoint should
// normally name a public service, so a request that names a private address
// is either a mistake or a probe — but note that a *model* endpoint on the
// LAN (a self-hosted Ollama, a vLLM box) is a legitimate use, which is why
// this is separate from MetadataHost.
func PrivateHost(host string) bool {
	host = cleanHost(host)
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified()
}

// CheckEndpointHost validates the host of an endpoint a request asked the
// daemon to dial. Metadata addresses are refused outright: no legitimate
// model or push service lives there, and reaching one turns the daemon into
// a credential proxy.
//
// allowPrivate keeps RFC1918 and loopback addressable — that is what makes
// a self-hosted model on the LAN or on this machine configurable at all.
func CheckEndpointHost(host string, allowPrivate bool) error {
	host = cleanHost(host)
	if host == "" {
		return fmt.Errorf("endpoint: missing host")
	}
	if MetadataHost(host) {
		return fmt.Errorf("endpoint: %q is a cloud metadata address and must not be dialed", host)
	}
	if !allowPrivate && PrivateHost(host) {
		return fmt.Errorf("endpoint: %q is a private address and must not be dialed", host)
	}
	return nil
}

// GuardedDialContext wraps a dialer so no connection is ever made to a cloud
// metadata address. The host checks above see the name as written; this one
// sees the address actually dialed, which closes DNS rebinding (a public name
// whose answer is 169.254.169.254) and a redirect to such an address.
//
// Private and loopback addresses stay dialable on purpose: a self-hosted model
// or push service on the LAN is a normal deployment, and the metadata range is
// the one that hands out the host's own credentials.
func GuardedDialContext(d *net.Dialer) func(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil {
		d = &net.Dialer{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		// A literal address is checked as given.
		if net.ParseIP(host) != nil {
			if MetadataHost(host) {
				return nil, fmt.Errorf("endpoint: refusing to dial the metadata address %s", host)
			}
			return d.DialContext(ctx, network, address)
		}
		// A name is resolved here and every candidate address is checked, so a
		// rebind between the name check and the connection gains nothing.
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("endpoint: resolve %s: %w", host, err)
		}
		var firstErr error
		for _, ip := range ips {
			if MetadataHost(ip.String()) {
				continue
			}
			conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if derr == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = derr
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("endpoint: %s resolves only to addresses that must not be dialed", host)
	}
}
