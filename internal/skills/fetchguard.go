package skills

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// fetchPolicy classifies what a remote-import URL may reach. The dialer it
// feeds is the actual guard: a DNS name that resolves to a disallowed address
// fails at connect time, so a rebind between check and dial gains nothing.
type fetchPolicy int

const (
	// fetchPublic requires https and dials only global-unicast addresses.
	fetchPublic fetchPolicy = iota
	// fetchLoopback allows http and dials only loopback addresses — a dev
	// hub or file server on the same machine cannot reach the network anyway.
	fetchLoopback
)

// classifyFetchURL validates rawURL for remote skill fetching and returns the
// dial policy the connection must satisfy. Any host that is not a loopback
// literal must use https: plaintext http to an arbitrary host is exactly the
// downgrade an SSRF probe wants, and it is also the cheap side of an
// agent-supplied URL pointed at a LAN service.
func classifyFetchURL(rawURL string) (*url.URL, fetchPolicy, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, fmt.Errorf("skills: parse URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, 0, fmt.Errorf("skills: URL scheme %q is not http(s)", u.Scheme)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return nil, 0, fmt.Errorf("skills: URL %q has no host", rawURL)
	}
	if host == "localhost" {
		return u, fetchLoopback, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return u, fetchLoopback, nil
		}
		if u.Scheme != "https" {
			return nil, 0, fetchURLSchemeErr(rawURL, u.Scheme)
		}
		return u, fetchPublic, nil
	}
	if u.Scheme != "https" {
		return nil, 0, fetchURLSchemeErr(rawURL, u.Scheme)
	}
	return u, fetchPublic, nil
}

func fetchURLSchemeErr(rawURL, scheme string) error {
	return fmt.Errorf("skills: remote URL %q must be https (got %q); only loopback hosts may use http", rawURL, scheme)
}

// publicDialable reports whether a resolved IP may be dialed under
// fetchPublic: global unicast minus every special-purpose range that turns an
// outbound fetch into an SSRF probe — loopback, RFC1918/ULA private,
// link-local (cloud metadata endpoints live there), multicast and
// unspecified. CGNAT space (100.64/10 — tailnets) stays dialable.
func publicDialable(ip net.IP) bool {
	return ip.IsGlobalUnicast() &&
		!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() &&
		!ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

// guardedDialContext resolves address and dials only an IP the policy allows,
// so every connection — including redirect hops, which share the transport —
// re-validates the destination at the moment bytes could move.
func guardedDialContext(policy fetchPolicy) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("skills: resolve %s: %w", host, err)
		}
		var firstErr error
		for _, ip := range ips {
			ok := publicDialable(ip)
			if policy == fetchLoopback {
				ok = ip.IsLoopback()
			}
			if !ok {
				continue
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("skills: %s resolves only to addresses a remote import may not reach", host)
	}
}

// fetchClient builds the client ImportURL uses: the dial guard plus a
// redirect check holding every hop to the same classification as the origin
// URL (an https→http or public→loopback redirect fails, not silently
// widens).
func fetchClient(policy fetchPolicy) *http.Client {
	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: &http.Transport{DialContext: guardedDialContext(policy)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("skills: too many redirects")
			}
			_, p, err := classifyFetchURL(req.URL.String())
			if err != nil {
				return err
			}
			if p != policy {
				return fmt.Errorf("skills: redirect to %q crosses the fetch policy boundary; refusing", req.URL)
			}
			return nil
		},
	}
}
