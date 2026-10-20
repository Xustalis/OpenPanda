// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The offline gate.
//
// A dropped link and a flaky provider surface as the same transport error, but
// they want opposite answers. A provider fault is what the retry ladder
// (model.max_retries, exponential backoff) exists for. A dead link burns that
// ladder in seconds — every attempt fails immediately against a dead route —
// and then fails a turn the user expected to keep waiting: the machine is
// offline for minutes, not for five backoffs.
//
// The gate tells them apart with two probes. The endpoint's own address
// answers "can this call reach the provider at all"; a public anchor answers
// "does this machine have the internet". Endpoint answering → the ladder
// replays as it always did. Only the anchor silent → the machine is offline,
// and the call waits, probing every few seconds and reporting its state to the
// surface, until the link returns — then replays without spending a retry
// attempt.
//
// Endpoints that need no internet are exempt: a local model (loopback, a LAN
// box, a single-label host name) is reachable exactly when its host is, so a
// world outage says nothing about it (Client.localEndpoint).
//
// Nothing here caps the wait. A long outage is a long wait by definition, and
// the caller's context remains the escape hatch — Esc in the TUI, Ctrl+C in
// the REPL, the console's stop button, the ask deadline.

const (
	// offlineEndpointTimeout bounds one endpoint probe. Short on purpose: a
	// provider that cannot finish a TCP handshake in two seconds is
	// unreachable as far as this decision is concerned, and the probe sits
	// on the critical path between "the call failed" and "we say so".
	offlineEndpointTimeout = 2 * time.Second
	// offlineAnchorTimeout bounds one anchor probe, and is shorter still
	// because three of them may run in sequence while the machine is down.
	offlineAnchorTimeout = time.Second
	// offlineProbeEvery is the poll cadence once the gate is waiting. Three
	// seconds keeps a returning uplink unnoticed for no longer than one
	// DHCP/TLS handshake, without turning a long outage into a probe storm.
	offlineProbeEvery = 3 * time.Second
)

// offlineAnchors are the public addresses the gate treats as an internet
// liveness signal: any one answering at the TCP layer means the uplink is up.
// Both are anycast DNS resolvers that answer on TCP as well as UDP, and two
// of them are probed so one blocked or retired address cannot pin a machine
// permanently offline (223.5.5.5 first — the catalogue's default vendors are
// Chinese, where that one is the nearest answer). A UDP "dial" of these would
// prove only that a default route exists, not that anything is reachable
// through it, which is the distinction a Wi-Fi-associated-but-dead-router
// link is made of.
var offlineAnchors = []string{"223.5.5.5:53", "1.1.1.1:53"}

// OfflineNote describes one state change of the offline gate: entering the
// wait, or coming out of it once the link returned. It exists so a surface can
// say "the network is down, waiting" instead of letting the turn look stalled
// — and, on the way out, report how long the outage cost.
type OfflineNote struct {
	// Waiting is true on entering the wait and false when the link is back.
	Waiting bool
	// Elapsed is how long the gate waited; meaningful only when Waiting is
	// false (it is zero on entry).
	Elapsed time.Duration
}

// offlineHookKey carries an OfflineNote sink through the ask's context to the
// client's retry loops — the same per-ask plumbing (and for the same reason)
// as retryHookKey: one Client serves many concurrent asks, so a field on it
// would leak one ask's outage into another's status line.
type offlineHookKey struct{}

// WithOfflineHook attaches fn to ctx. The retry loops call it when the gate
// enters and leaves a wait. A nil fn is a no-op.
func WithOfflineHook(ctx context.Context, fn func(OfflineNote)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, offlineHookKey{}, fn)
}

// notifyOffline delivers one note to the hook ctx carries, if any.
func notifyOffline(ctx context.Context, n OfflineNote) {
	fn, _ := ctx.Value(offlineHookKey{}).(func(OfflineNote))
	if fn == nil {
		return
	}
	fn(n)
}

// IsLocalEndpoint reports whether reaching base needs no internet: loopback,
// a private/link-local address (a LAN model box, a self-hosted gateway),
// a .local/.lan name, or a single-label host ("ollama", "gpu-box") that only
// a LAN resolver can answer. A model behind any of those keeps answering
// during an internet outage, so the offline gate must not wait on its behalf.
func IsLocalEndpoint(base string) bool {
	host := hostOf(base)
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".internal") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}
	// A bare name with no dot is not a public DNS name: it resolves through
	// the local resolver's search domains, i.e. inside the LAN.
	return !strings.Contains(host, ".")
}

// hostOf returns the lowercased host of an endpoint URL or bare host[:port].
func hostOf(base string) string {
	u := parseEndpoint(base)
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// parseEndpoint parses an endpoint URL, tolerating a bare "host:port" (which
// url.Parse would read as scheme "host").
func parseEndpoint(base string) *url.URL {
	s := strings.TrimSpace(base)
	if s == "" {
		return nil
	}
	if !strings.Contains(s, "://") {
		s = "//" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil
	}
	return u
}

// dialAddrOf turns an endpoint into the host:port a TCP probe dials, filling
// in the scheme's default port. "" when there is no host to dial.
func dialAddrOf(base string) string {
	u := parseEndpoint(base)
	if u == nil || u.Hostname() == "" {
		return ""
	}
	if u.Port() != "" {
		return net.JoinHostPort(u.Hostname(), u.Port())
	}
	port := "443"
	if strings.EqualFold(u.Scheme, "http") {
		port = "80"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// probeTargetFor resolves the address the gate dials, and whether dialing it
// answers the wrong question. A proxied client never dials the provider
// directly — the proxy does — so the probe follows the proxy instead (see
// probeTargetForProxy for when that is still an endpoint signal).
func probeTargetFor(base string) (target string, localProxy bool) {
	req, err := http.NewRequest(http.MethodPost, base, nil)
	if err != nil {
		return dialAddrOf(base), false
	}
	u, err := http.ProxyFromEnvironment(req)
	if err != nil || u == nil {
		return dialAddrOf(base), false
	}
	return probeTargetForProxy(u.String())
}

// probeTargetForProxy maps a proxy URL onto the gate's probe target. It is
// split out from the environment lookup so the mapping is testable without
// touching the process environment: net/http caches HTTPS_PROXY on first use,
// so a test that set the variable would inherit whatever ran before it.
func probeTargetForProxy(proxy string) (target string, localProxy bool) {
	addr := dialAddrOf(proxy)
	if addr == "" {
		return "", false
	}
	// A proxy on loopback or the LAN accepts connections whether or not the
	// uplink is alive: its dial proves that a process is listening, not that
	// the provider is reachable, so it cannot stand in for the endpoint. A
	// remote proxy host is itself out on the internet — reaching it is
	// exactly the signal the gate wants.
	return addr, IsLocalEndpoint(proxy)
}

// probeTCP dials addr and closes the connection: a completed handshake is the
// whole signal. It deliberately does not speak TLS or HTTP — the question is
// whether the host is reachable at all, and a certificate or status-code
// problem is a provider fault the retry ladder already handles.
func probeTCP(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// anchorReachable reports whether any public anchor answers, i.e. whether this
// machine has internet beyond its own link. It stops at the first answer, and
// at a cancelled context.
func anchorReachable(ctx context.Context) bool {
	for _, addr := range offlineAnchors {
		if probeTCP(ctx, addr, offlineAnchorTimeout) == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

// resolveOfflineWait maps model.offline_wait onto the gate: unset means on —
// the behaviour a laptop on a train wants — and only an explicit false
// restores the old fail-fast behaviour.
func resolveOfflineWait(configured *bool) bool {
	return configured == nil || *configured
}

// probeOnce dials one address through the client's probe hook (nil in
// production, swapped in tests).
func (c *Client) probeOnce(ctx context.Context, addr string, timeout time.Duration) error {
	if addr == "" {
		return errors.New("entry: offline probe: no address")
	}
	if c.probe != nil {
		return c.probe(ctx, addr, timeout)
	}
	return probeTCP(ctx, addr, timeout)
}

// anchorsUp reports whether any anchor answers, through the client's hook when
// a test supplied one.
func (c *Client) anchorsUp(ctx context.Context) bool {
	if c.anchor != nil {
		return c.anchor(ctx)
	}
	return anchorReachable(ctx)
}

// endpointReachable reports whether the client's own target answers. Behind a
// proxy on this machine or the LAN that dial proves nothing (the proxy answers
// whatever the uplink does), so it is skipped — see probeTargetForProxy.
func (c *Client) endpointReachable(ctx context.Context) bool {
	if c.localProxy {
		return false
	}
	return c.probeOnce(ctx, c.offlineTarget, offlineEndpointTimeout) == nil
}

// probeEvery is the gate's poll cadence, defaulting when a Client was built
// outside NewClient.
func (c *Client) probeEvery() time.Duration {
	if c.offlineEvery > 0 {
		return c.offlineEvery
	}
	return offlineProbeEvery
}

// offlineArmed reports whether this failure should consult the gate at all: it
// must be a transport drop (a 429/5xx means the provider answered, so the
// network is demonstrably fine), the endpoint must need the internet, and the
// gate must not be disabled.
func (c *Client) offlineArmed(cause error) bool {
	if !c.offlineWait || c.localEndpoint || c.offlineTarget == "" {
		return false
	}
	var te *transientError
	return errors.As(cause, &te)
}

// awaitNetwork is called by the retry loops after a retryable failure. It
// returns waited=true when it blocked until the link came back — the caller
// then replays the call without spending a retry attempt — and a non-nil error
// only when the caller's context was cancelled while waiting.
//
// It returns false when there is nothing to wait for: the endpoint answers
// again, or it never stopped — the anchors prove the machine still has
// internet, which makes the failure the provider's business, not the link's.
func (c *Client) awaitNetwork(ctx context.Context, cause error) (bool, error) {
	if !c.offlineArmed(cause) {
		return false, nil
	}
	if c.endpointReachable(ctx) || c.anchorsUp(ctx) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	start := time.Now()
	notifyOffline(ctx, OfflineNote{Waiting: true})
	ticker := time.NewTicker(c.probeEvery())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
		// Either signal ends the wait: the provider (or the remote proxy)
		// answering again is the most direct evidence the call can proceed,
		// and an answering anchor is the uplink's. A machine whose anchors
		// are blocked by policy still resumes on the target probe — except
		// behind a loopback/LAN proxy, where that probe is skipped and the
		// anchors are all there is.
		if c.endpointReachable(ctx) || c.anchorsUp(ctx) {
			notifyOffline(ctx, OfflineNote{Elapsed: time.Since(start)})
			return true, nil
		}
	}
}
