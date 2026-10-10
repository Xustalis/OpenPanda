// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Peer-link ownership (one loop per configured address).
//
// The daemon, `panda web` and the standalone panel sidecar all need the
// same steady-state behavior: dial each configured peer, hold the edge,
// redial with backoff when it drops. SyncPeers makes that set declarative —
// call it with the full desired list at boot, then again on config reload;
// entries added are dialed, entries removed have their loop cancelled and
// the live conn closed. A loop keyed by address survives an addr→peer
// rename because the key is the configured string, not the node id.
//
// peerFailLogEvery bounds the per-peer WARN stream a dead peer produces:
// at the 30s steady-state backoff, one line every 20th failure is roughly
// one line per ~10 minutes.
const peerFailLogEvery = 20

// Redial backoff caps. The steady-state cap keeps a permanently offline
// peer cheap to probe; with custody in hand the cap collapses so a parked
// task — whose delivery IS the reconnect — waits seconds between attempts,
// not half-minutes, on the flappy links real deployments run over.
const (
	peerBackoffCap        = 30 * time.Second
	peerBackoffCapCustody = 5 * time.Second
)

// SyncPeers reconciles the running peer-keepalive set with the wanted
// list: new addresses get a keepalive loop, removed ones are cancelled and
// their live conns closed, untouched ones keep running. Safe to call at
// boot and on every config reload. ctx is the process lifetime — the loops
// die with it.
func (c *Core) SyncPeers(ctx context.Context, addrs []string) {
	c.peerLoopsMu.Lock()
	defer c.peerLoopsMu.Unlock()
	if c.peerLoops == nil {
		c.peerLoops = make(map[string]context.CancelFunc)
	}
	want := make(map[string]string, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a != "" {
			want[a] = a
		}
	}
	for addr, cancel := range c.peerLoops {
		if _, ok := want[addr]; ok {
			continue
		}
		cancel()
		delete(c.peerLoops, addr)
		c.logger.Info("peer removed from config — keepalive stopped", "peer", addr)
	}
	for addr := range want {
		if _, ok := c.peerLoops[addr]; ok {
			continue
		}
		pctx, cancel := context.WithCancel(ctx)
		c.peerLoops[addr] = cancel
		if strings.HasPrefix(addr, "punch:") {
			go c.punchPeerLoop(pctx, strings.TrimPrefix(addr, "punch:"))
		} else {
			go c.maintainPeerLoop(pctx, addr)
		}
		c.logger.Info("peer keepalive started", "peer", addr)
	}
}

// maintainPeerLoop holds one configured peer edge: MaintainPeer returns when
// the edge drops (or the dial failed), the loop backs off, and custody in
// the outbox collapses the cap so parked work meets the reconnect sooner.
func (c *Core) maintainPeerLoop(ctx context.Context, peer string) {
	backoff := 1 * time.Second
	dialFails := 0
	// Jitter spreads a fleet-wide reconnect over a window instead of having
	// every node redial in lockstep the second the peer returns — the classic
	// thundering herd after a shared outage.
	jitter := func(d time.Duration) time.Duration {
		return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	}
	for {
		err := c.MaintainPeer(ctx, peer)
		if err != nil {
			dialFails++
			// First failure and the recovery are the news — a line every
			// redial (~30s steady-state) grew an unbounded WARN stream for a
			// peer that is simply off. Keep a sparse beat every ~20th failure
			// so the outage stays greppable without owning the log.
			if dialFails == 1 || dialFails%peerFailLogEvery == 0 {
				if errors.Is(err, ErrAuthRejected) {
					// The peer is online and refusing our credential —
					// the remedy is fixing shared_secret, not the network.
					c.logger.Warn("peer rejecting our authentication — verify shared_secret matches the peer",
						"peer", peer, "err", err, "consecutive", dialFails)
				} else {
					c.logger.Warn("peer dial failed", "peer", peer, "err", err, "consecutive", dialFails)
				}
			}
			capTo := peerBackoffCap
			// Custody collapses the cap so parked work meets a reconnect
			// sooner — but an auth-rejected edge can never carry it, so the
			// refusal keeps the full 30s backoff instead of a 5s storm.
			if c.HasPendingCustody(ctx) && !errors.Is(err, ErrAuthRejected) {
				capTo = peerBackoffCapCustody
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(jitter(backoff)):
			}
			backoff = min(backoff*2, capTo)
			continue
		}
		if dialFails > 0 {
			c.logger.Info("peer reconnected", "peer", peer, "after_failures", dialFails)
			dialFails = 0
		}
		// The connection was established and later dropped; reset the
		// backoff and reconnect promptly.
		backoff = 1 * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
	}
}

// punchPeerLoop holds a "punch:<node-id>" mapping: the peer is NAT-bound and
// reachable only through the farsky pinhole handshake, so the loop re-offers
// until a UDP route exists and re-punches when the mapping is lost.
func (c *Core) punchPeerLoop(ctx context.Context, peerID string) {
	punchFails := 0
	for {
		if c.UDPPort() == 0 {
			c.logger.Warn("punch peer configured but the datagram plane is off (network.udp_listen)", "peer", peerID)
			return
		}
		if c.UDPRoute(peerID) == nil {
			if err := c.PunchPeer(ctx, peerID); err != nil {
				punchFails++
				if punchFails == 1 || punchFails%peerFailLogEvery == 0 {
					c.logger.Warn("punch offer failed", "peer", peerID, "err", err, "consecutive", punchFails)
				}
			}
		} else if punchFails > 0 {
			c.logger.Info("punch route established", "peer", peerID, "after_failures", punchFails)
			punchFails = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// ApplyNetworkConfig hot-applies the mutable half of network.* — the daemon's
// SIGHUP path and the engine's config-mutation path share it, so a `nodes
// add`, `pair`, or secret/cleartext edit lands on the running mesh without a
// restart. Secrets and cleartext policy land first: a peer loop spawned by
// SyncPeers right after reads them at dial time, and a live conn's framed
// cipher state is per-connection — it does not re-run the hello. ListenAddr
// and the discovery binding stay boot-time decisions: re-binding a socket
// hot is a restart-shaped operation.
func (c *Core) ApplyNetworkConfig(ctx context.Context, n config.NetworkConfig) {
	c.SetSharedSecret(n.SharedSecret)
	c.SetAllowCleartext(n.AllowCleartext)
	c.SetCleartextAllowlist(n.AllowCleartextFor)
	c.SyncPeers(ctx, n.Peers)
}

// ProbeResult is what a `nodes add`/`pair` self-check learned from the peer:
// the id its hello reply announced and whether session encryption armed.
type ProbeResult struct {
	PeerID    string
	Encrypted bool
}

// injectNodeKey installs a pre-loaded identity so nodeKeyPair serves it
// instead of materializing a fresh pair — the probe's path, where minting a
// throwaway key under the real node id would poison the peer's TOFU record
// (recordPeerPubKey clears key_verified on any key change). The once is
// fired with a no-op so the lazy branch never overwrites the injected key.
func (c *Core) injectNodeKey(pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	c.nodePub, c.nodePriv = pub, priv
	c.keyOnce.Do(func() {})
}

// ProbePeer runs the real outbound handshake against addr once — WS dial,
// signed hello, reply verification, session-AEAD negotiation, cleartext
// policy — on a throwaway core backed by an in-memory store, then closes.
// It answers "will this peer actually link?" synchronously at admit time
// instead of leaving the operator to grep keepalive logs. pub/priv are the
// node's persisted identity (LoadNodeKey on the real store); a nil priv
// falls back to an unsigned legacy hello — still a valid reachability check.
// Six seconds is the handshake budget: a peer that answers slower than that
// is a peer the keepalive would spend its whole first backoff on anyway.
func ProbePeer(ctx context.Context, nodeID string, card ledger.Card, model config.ModelConfig, n config.NetworkConfig, addr string, pub ed25519.PublicKey, priv ed25519.PrivateKey) (*ProbeResult, error) {
	db, err := storage.Open(":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		return nil, err
	}
	probe := NewCore(db, nodeID, card, 0, slog.New(slog.DiscardHandler), model)
	probe.SetSharedSecret(n.SharedSecret)
	probe.SetAllowCleartext(n.AllowCleartext)
	probe.SetCleartextAllowlist(n.AllowCleartextFor)
	if priv != nil {
		probe.injectNodeKey(pub, priv)
	}
	defer probe.Shutdown(context.Background())

	pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := probe.DialPeer(pctx, addr); err != nil {
		return nil, err
	}
	for {
		probe.mu.RLock()
		var hit *Peer
		for _, p := range probe.peers {
			hit = p
		}
		probe.mu.RUnlock()
		if hit != nil {
			return &ProbeResult{PeerID: hit.id, Encrypted: hit.conn.Encrypted()}, nil
		}
		// The peer explicitly refused our hello — fail fast and say so. A
		// generic timeout here reads as "peer unreachable" when the peer is
		// actually online and rejecting our credential; those need opposite
		// remedies (check the secret, not the network).
		if v := probe.lastHelloReject.Load(); v != nil {
			return nil, fmt.Errorf("peer online but rejected our authentication (hello_reject: %s) — check shared_secret matches", v.(string))
		}
		select {
		case <-pctx.Done():
			return nil, errors.New("handshake timed out — peer never completed the hello")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
