// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// networkPolicy is the resolved cross-network half of network.* — the knobs
// the mesh design (§4.2) promises are the operator's to turn, rather than
// constants compiled into the transport. The zero value is not a valid
// policy: defaultNetworkPolicy is what an unconfigured node runs.
type networkPolicy struct {
	// revision keeps cached resolutions tied to the policy they read, even
	// when an older store query finishes after a hot reload.
	revision uint64
	// bootstrapEdges is the rendezvous-edge target K (0 = manual mode).
	bootstrapEdges int
	// autoPunch arms the ladder's hole-punch rung.
	autoPunch bool
	// transit arms this node's client side of the relay plane.
	transit bool
	// rendezvous is this node's own serving role: auto|always|never.
	rendezvous string
	// relayFallback is the ordered list of configured peers marked
	// relay_priority: fallback. It is the operator's local preference for
	// the hop a suspected-symmetric-NAT destination's frames leave on —
	// consulted before automatic relay election, never advertised.
	relayFallback []string
}

// defaultNetworkPolicy mirrors config.Default()'s network block. Keeping the
// two in step matters only for embedders and tests that construct a Core
// without ever calling SetNetworkPolicy; a node loaded from config always
// gets the operator's values through ApplyNetworkConfig.
func defaultNetworkPolicy() networkPolicy {
	return networkPolicy{
		bootstrapEdges: rendezvousKDefault,
		autoPunch:      true,
		transit:        true,
		rendezvous:     "auto",
	}
}

// SetNetworkPolicy installs the resolved cross-network policy. Called from
// the boot path and from ApplyNetworkConfig on a hot reload; it is a single
// swap so a reader never sees half of one policy and half of another.
func (c *Core) SetNetworkPolicy(n config.NetworkConfig) {
	pol := networkPolicy{
		bootstrapEdges: n.BootstrapEdges,
		autoPunch:      n.AutoPunch,
		transit:        n.Transit,
		rendezvous:     n.Rendezvous,
		relayFallback:  n.RelayFallbackAddrs(),
	}
	if pol.rendezvous == "" {
		pol.rendezvous = "auto"
	}
	c.polMu.Lock()
	pol.revision = c.netPol.revision + 1
	c.netPol = pol
	c.polSet = true
	c.polMu.Unlock()
	// The relay budget bounds live traffic, so a reload that widens or
	// narrows it takes effect on the next relayed frame — no restart.
	c.relayLim.reconfigure(c.relayConfig())
}

// netPolicy returns the active policy, falling back to the built-in defaults
// when no config was ever installed.
func (c *Core) netPolicy() networkPolicy {
	c.polMu.RLock()
	pol, set := c.netPol, c.polSet
	c.polMu.RUnlock()
	if !set {
		return defaultNetworkPolicy()
	}
	return pol
}

// rendezvousTarget is the configured K, clamped to the validated range so a
// policy installed by an embedder without Validate cannot elect an unbounded
// set of edges.
func (c *Core) rendezvousTarget() int {
	k := c.netPolicy().bootstrapEdges
	if k < 0 {
		return 0
	}
	if k > 8 {
		return 8
	}
	return k
}

// relayConfig reads the serving limits from the trust block under the same
// lock the policy tables live under.
func (c *Core) relayConfig() (maxKbps, maxSessions int) {
	c.trustMu.RLock()
	defer c.trustMu.RUnlock()
	return c.trustCfg.Relay.MaxKbps, c.trustCfg.Relay.MaxSessions
}

// relayServeAllowed reports whether this node may route its own transit
// frames through the given peer — the outbound half of trust.relay.serve.
// The tier is resolved the same way task admission resolves it, so a peer
// fenced to "open" cannot be used as a relay even if it advertises trnz.
func (c *Core) relayServeAllowed(ctx context.Context, peerID string) bool {
	c.trustMu.RLock()
	cfg := c.trustCfg
	c.trustMu.RUnlock()
	return cfg.RelayServeFor(c.peerTrustOf(ctx, peerID))
}

// relayAcceptAllowed reports whether this node will forward a transit frame
// that arrived from the given peer — the inbound half, trust.relay.transit.
func (c *Core) relayAcceptAllowed(ctx context.Context, peerID string) bool {
	c.trustMu.RLock()
	cfg := c.trustCfg
	c.trustMu.RUnlock()
	return cfg.RelayAcceptFor(c.peerTrustOf(ctx, peerID))
}
