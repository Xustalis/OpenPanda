// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/updater"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SetSharedSecret sets the shared secret used to authenticate inbound hello
// messages (design §16 / P0-1). An empty secret is fail-closed: no peer can
// authenticate, so the WebSocket listener accepts no hello until a secret is
// configured. Hot-reloadable: readers take netMu via sharedSecretValue, so a
// settings save is safe against in-flight hellos, discovery and DTN traffic.
func (c *Core) SetSharedSecret(secret string) {
	c.netMu.Lock()
	defer c.netMu.Unlock()
	c.sharedSecret = secret
}

// sharedSecretValue is the synchronized read of the hot-reloadable shared
// secret (netMu). Every read site goes through it: the write side is a
// settings save that can land while connection, discovery, UDP, DTN and
// outbox goroutines are mid-flight.
func (c *Core) sharedSecretValue() string {
	c.netMu.RLock()
	defer c.netMu.RUnlock()
	return c.sharedSecret
}

// MeshSecretFingerprint is a short one-way label for a mesh secret. It is
// safe to log: an operator can compare two machines' fingerprints without
// ever pasting the credential into a terminal, ticket or chat. It is
// deliberately not reversible to the secret (and only 32 bits, so it is a
// comparison aid, never an authentication token). Exported because the
// standalone web console has no *Core at the point it logs its boot line,
// yet it is the mesh endpoint and must publish the same evidence.
func MeshSecretFingerprint(secret string) string {
	if secret == "" {
		return "unset"
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:4])
}

// SecretFingerprint reports this node's current mesh-secret fingerprint.
// Both ends of a link must print the same value; a mismatch is the whole
// explanation for a storm of "peer rejected our hello: auth" on one side and
// "rejected hello: bad signature" on the other.
func (c *Core) SecretFingerprint() string {
	return MeshSecretFingerprint(c.sharedSecretValue())
}

// helloFailureReason turns the wire's single fixed "auth" verdict into the
// actual local cause. It mirrors bus.VerifyHelloP's checks in the same order
// and is diagnostic only — the authentication decision stays in bus. Without
// it a shared-secret mismatch, a clock jump and a broken identity signature
// all look identical, and two operators keep staring at TCP connects that
// succeed while the mesh never links.
func helloFailureReason(secret, receiverID string, p bus.HelloPayload, now time.Time) string {
	skew := now.Sub(time.Unix(p.Ts, 0))
	switch {
	case skew > bus.MaxHelloAge || skew < -bus.MaxHelloAge:
		return "clock skew: hello timestamp outside the replay window"
	case secret == "":
		return "local shared_secret is empty"
	case p.To != "" && (receiverID == "" || p.To != receiverID || p.SigB == ""):
		return "hello bound for a different receiver"
	case p.To != "" && p.SigB != bus.HelloSigB(secret, p.NodeID, p.To, p.Ts, p.Nonce):
		return "shared_secret mismatch (bound HMAC)"
	case p.To == "" && p.Sig == "":
		return "hello carries no HMAC signature"
	case p.To == "" && p.Nonce != "" && p.Sig != bus.HelloSigN(secret, p.NodeID, p.Ts, p.Nonce):
		return "shared_secret mismatch (HMAC)"
	case p.To == "" && p.Nonce == "" && p.Sig != bus.HelloSigLegacy(secret, p.NodeID, p.Ts):
		return "shared_secret mismatch (legacy HMAC)"
	default:
		return "identity signature (Ed25519) invalid or half-present"
	}
}

// helloVersionHint surfaces a version skew an auth rejection alone cannot
// explain. Ver is self-asserted and outside the signature domain, so this is
// bookkeeping only: it never changes the HMAC verdict, it just names the
// build on the other end. A v0.0.11 peer talking to a v0.1.5 receiver can
// fail the handshake for protocol/schema reasons while both hold the same
// secret; without this line the operator keeps re-pairing two nodes that
// already agree. Fleet observability has shown version skew since v0.0.10
// (employee_cache.ver), but the handshake failure path never did.
func helloVersionHint(peerVer, localVer string) string {
	if peerVer == "" {
		return "peer sent no version (build predates Ver); align every node to the same release before debugging secrets"
	}
	norm := func(v string) string {
		return strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	}
	pv, lv := norm(peerVer), norm(localVer)
	if pv == lv {
		return ""
	}
	// Only order well-formed numeric versions. A dev/custom stamp such as
	// "dev" would otherwise compare as 0.0.0 and produce a false "upgrade
	// the peer" instruction.
	plain := func(v string) bool {
		v = strings.SplitN(strings.SplitN(v, "-", 2)[0], "+", 2)[0]
		parts := strings.Split(v, ".")
		if len(parts) < 2 || len(parts) > 3 {
			return false
		}
		for _, part := range parts {
			if part == "" {
				return false
			}
			for _, r := range part {
				if r < '0' || r > '9' {
					return false
				}
			}
		}
		return true
	}
	if !plain(pv) || !plain(lv) {
		return fmt.Sprintf("peer runs %s, this node runs %s: builds differ — align all nodes to one release before re-pairing", peerVer, localVer)
	}
	switch updater.CompareVersion(pv, lv) {
	case -1:
		return fmt.Sprintf("peer runs %s, this node runs %s: the peer is on an older build — upgrade it and re-check mesh protocol/db schema compatibility before re-pairing", peerVer, localVer)
	default:
		return fmt.Sprintf("peer runs %s, this node runs %s: this node is behind — upgrade it before re-pairing", peerVer, localVer)
	}
}

// SetAllowCleartext lifts the outbound plaintext gate (network.allow_cleartext).
// See cleartextOK for what it opts back into.
func (c *Core) SetAllowCleartext(allow bool) {
	c.netMu.Lock()
	defer c.netMu.Unlock()
	c.allowCleartext = allow
}

// SetCleartextAllowlist scopes plaintext dialing to extra hosts/nets
// (network.allow_cleartext_for). Entries are CIDRs, literal IPs, or exact
// hostnames — validated at config load, stored verbatim here.
func (c *Core) SetCleartextAllowlist(entries []string) {
	c.netMu.Lock()
	defer c.netMu.Unlock()
	c.cleartextFor = slices.Clone(entries)
}

// cleartextPolicy returns the cleartext gate and its scope as one consistent
// snapshot (netMu). The slice is replaced, never mutated, on write, so the
// returned reference stays safe to read.
func (c *Core) cleartextPolicy() (bool, []string) {
	c.netMu.RLock()
	defer c.netMu.RUnlock()
	return c.allowCleartext, c.cleartextFor
}

// enforceLinkPolicy is the post-handshake cleartext gate: reached only when
// session encryption was NOT negotiated (peer lacks sessaead or no shared
// secret exists). Outbound conns are judged on the configured dial address;
// inbound conns on the source IP — a verified-mesh peer from an untrusted
// network is still refused plaintext, because a hello signature proves
// membership, not that the link itself is free of relays.
// ErrCleartextRefused identifies the one probe failure an operator's admit
// command can legitimately fix by opening network.allow_cleartext_for. A
// timeout, a DNS failure or hello_reject(auth) must never open it.
var ErrCleartextRefused = errors.New("cleartext link refused by policy")

func (c *Core) enforceLinkPolicy(conn *bus.Conn, peerID string) error {
	allow, allowFor := c.cleartextPolicy()
	if conn.Outbound() {
		addr := conn.DialAddr()
		if addr == "" {
			addr = conn.RemoteAddr()
		}
		if err := cleartextOK(addr, allow, allowFor); err != nil {
			// The gate runs after the socket opens, so this verdict never
			// reaches DialPeer's caller. Publish it for ProbePeer, which
			// otherwise sees an ordinary timeout and cannot tell a
			// pre-sessaead neighbour from a machine that is simply offline.
			c.lastCleartextRefusal.Store(addr)
			c.logger.Warn("refusing cleartext link", "peer", peerID, "addr", addr)
			return fmt.Errorf("peer %s cannot encrypt (needs sessaead) and %w", peerID, err)
		}
		return nil
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr())
	if err != nil {
		host = conn.RemoteAddr()
	}
	if allow || cleartextSafeHost(host) || cleartextListed(host, allowFor) {
		return nil
	}
	c.logger.Warn("refusing cleartext inbound", "peer", peerID, "remote", conn.RemoteAddr())
	return fmt.Errorf("peer %s cannot encrypt (needs sessaead): cleartext refused for inbound from %s — upgrade the peer or list its address in network.allow_cleartext_for", peerID, host)
}

// NormalizeDialAddr canonicalizes a configured peer address for the
// addr→node_id binding (peer_addrs): scheme and trailing slash off, so the
// config's "ws://host:7836/" and a bare "host:7836" compare equal. Shared
// with `panda status`, whose configured-peer-online count joins the same
// strings against the same table.
func NormalizeDialAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "ws://")
	addr = strings.TrimPrefix(addr, "wss://")
	return strings.TrimSuffix(addr, "/")
}

// normalizeWSURL turns a bare host[:port] peer address into a WebSocket URL,
// preserving an explicit scheme (ws:// or wss://) when the caller supplied one.
// The plaintext default is deliberate: the node listener has no TLS termination
// of its own, and link encryption for node-to-node traffic is delegated to the
// network layer (Tailscale). A deployment that reaches peers over an untrusted
// plain network must specify wss:// explicitly and terminate TLS in front of
// the listener.
func normalizeWSURL(addr string) string {
	if addr == "" {
		return ""
	}
	// Only a real "scheme://" string is a URL. url.Parse alone would happily
	// read "peer.example.com:7836" as scheme "peer.example.com" (dots are
	// legal in schemes) and hand it back verbatim — a bare hostname:port
	// peer then dialed an address with no host at all.
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil && u.Scheme != "" && u.Host != "" {
			return addr
		}
	}
	return "ws://" + addr + "/ws"
}

// cleartextOK is the outbound plaintext gate. The hello handshake proves mesh
// membership, but it authenticates the handshake ONLY — every frame after it
// is unsigned and unencrypted, so a network MITM on a ws:// link can read the
// task plane and inject frames (a forged task_delegate is remote code
// execution). ws:// is therefore refused by default unless the target already
// travels an encrypted underlay:
//
//   - loopback (127.0.0.0/8, ::1, localhost) — the attacker is already local;
//   - Tailscale — the CGNAT range (100.64.0.0/10), its IPv6 ULA
//     (fd7a:115e:a214::/48) and *.ts.net MagicDNS names only resolve inside a
//     tailnet, where WireGuard carries the encryption ws lacks;
//   - allowFor entries — the operator's scoped opt-in (CIDR, literal IP or
//     exact hostname): plaintext is permitted toward just those targets, so
//     a home LAN opens without disarming the gate for every other dial;
//   - anything else must come in as wss://, as a punch: peer (AEAD
//     datagrams), or under the operator's explicit network.allow_cleartext.
func cleartextOK(addr string, allowCleartext bool, allowFor []string) error {
	if strings.HasPrefix(addr, "punch:") {
		return nil // punch peers ride the AEAD datagram plane, not TCP/ws
	}
	u, err := url.Parse(normalizeWSURL(addr))
	if err != nil {
		return fmt.Errorf("bad peer address %q: %w", addr, err)
	}
	if u.Scheme != "ws" || allowCleartext {
		return nil // wss:// (or explicit operator opt-out) passes through
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("peer address %q has no host", addr)
	}
	if cleartextSafeHost(host) || cleartextListed(host, allowFor) {
		return nil
	}
	return fmt.Errorf("cleartext ws:// to %q refused — the task plane would be readable and forgeable by a network MITM; use wss:// with a TLS terminator, a punch: peer, network.allow_cleartext_for for just this net/host, or network.allow_cleartext for everything", u.Host)
}

// cleartextListed reports whether host matches an allow_cleartext_for entry:
// a CIDR the IP falls inside, a literal IP equal to it, or an exact
// (case-insensitive) hostname. Malformed entries were rejected at config
// load; a stray one that slips through simply never matches.
func cleartextListed(host string, allowFor []string) bool {
	if len(allowFor) == 0 {
		return false
	}
	ip := net.ParseIP(host)
	for _, entry := range allowFor {
		if _, n, err := net.ParseCIDR(entry); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if e := net.ParseIP(entry); e != nil {
			if ip != nil && e.Equal(ip) {
				return true
			}
			continue
		}
		if strings.EqualFold(entry, host) {
			return true
		}
	}
	return false
}

// CleartextDialError surfaces the dial-time gate to code paths that only
// *record* a peer address (nodes add/admit, pair): they can warn at write
// time instead of letting the refusal surface days later inside keepalive
// logs. Non-nil means the daemon would refuse to dial addr under the given
// cleartext settings; the error text already names the escape hatches.
func CleartextDialError(addr string, allowCleartext bool, allowFor []string) error {
	return cleartextOK(addr, allowCleartext, allowFor)
}

// cleartextSafeHost reports whether host can only be reached over an
// encrypted-or-local path, so ws:// adds no exposure the underlay doesn't
// already prevent.
func cleartextSafeHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if strings.HasSuffix(host, ".ts.net") {
		return true // Tailscale MagicDNS — resolves to a WireGuard endpoint
	}
	// A numeric host may carry an IPv6 zone id (fe80::1%eth0) that ParseIP
	// rejects; strip it before the address check.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || tailscaleNet4.Contains(ip) || tailscaleNet6.Contains(ip)
}

// Tailscale address space: the IPv4 CGNAT block every tailnet lives on and
// the IPv6 ULA the overlay assigns. Neither is reachable off a tailnet, so a
// ws:// target inside them is already WireGuard-encrypted.
var (
	tailscaleNet4 = mustCIDR("100.64.0.0/10")
	tailscaleNet6 = mustCIDR("fd7a:115e:a214::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("bad tailnet CIDR " + s)
	}
	return n
}
