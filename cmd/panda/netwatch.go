// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"reflect"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// networkSyncInterval paces the config.yaml observer for the mutable network
// half. It matches configsync's model observer: cheap enough to run always,
// slow enough that a fleet of nodes does not hammer the disk. Polling rather
// than fsnotify is deliberate — it also works on Windows and after an atomic
// file replacement, and it needs no signal to an unverified PID.
const networkSyncInterval = time.Second

// networkApplier is the slice of *core.Core this observer needs. Narrowing it
// keeps the poller testable without a live mesh.
type networkApplier interface {
	ApplyNetworkConfig(ctx context.Context, n config.NetworkConfig)
}

// runNetworkSync keeps the daemon's mesh in step with config.yaml.
//
// The gap it closes: the daemon reads network.peers once, at startup. Every
// other surface that adds a peer — the web console above all — writes the file
// and applies the change to its own process, and the daemon never learns. The
// peer then shows up in the directory (the console's hello updates the row)
// while the scheduler holds no track to it, so a task pinned there parks in
// task_outbox forever. `nodes add` sends SIGHUP to cover this; the console has
// no PID to signal and cannot, which is why observation lives here.
//
// A failed load keeps the last good mesh: a half-written file must never take
// the node off the network. applied starts at the boot configuration so the
// first tick does not re-apply what startup already installed.
func runNetworkSync(ctx context.Context, logger *slog.Logger, path string, node networkApplier, applied config.NetworkConfig) {
	if node == nil {
		return
	}
	// Resolve here, not at each call site: --config is normally omitted, so
	// the flag value is "" and config.Load("") transparently falls back to the
	// user config path. Passing that empty flag straight to os.ReadFile makes
	// the observer a no-op on exactly the default install — the config is
	// edited, the mesh never notices. ResolvePath is idempotent for explicit
	// paths, so tests and --config installs keep working.
	path = config.ResolvePath(path)
	if path == "" {
		return
	}
	last := fileDigest(path)
	tick := time.NewTicker(networkSyncInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		sum := fileDigest(path)
		if sum == "" || sum == last {
			continue
		}
		// Advance before loading: a revision that fails to parse is still a
		// revision, and retrying the same bytes every second would only spam.
		last = sum
		cfg, err := config.Load(path)
		if err != nil || cfg == nil {
			logger.Warn("network config reload: revision unreadable, keeping the live mesh", "path", path)
			continue
		}
		if reflect.DeepEqual(cfg.Network, applied) {
			continue
		}
		node.ApplyNetworkConfig(ctx, cfg.Network)
		applied = cfg.Network
		logger.Info("network config reloaded from file",
			"peers", len(cfg.Network.PeerAddrs()),
			"exposure", cfg.Network.Exposure())
	}
}

func fileDigest(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// networkMutator is the config surface the console exposes. Declared as an
// interface so the poller stays testable without an EngineHolder (which needs
// a live store and model endpoint).
type networkMutator interface {
	MutateConfig(func(*config.Config))
}

// engineNetworkApplier carries a file revision into the console's engine.
// The console owns no *core.Core of its own — the engine holds it — so the
// write has to go through MutateConfig, which also re-applies the mutable
// network half under the config lock. That path carries the ownsRow guard:
// beside a daemon the console is a borrowed node and must NOT start peer
// keepalives, while a standalone console (the common single-process install)
// is the row owner and must reconcile the dial set.
type engineNetworkApplier struct {
	holder networkMutator
}

func (a engineNetworkApplier) ApplyNetworkConfig(_ context.Context, n config.NetworkConfig) {
	if a.holder == nil {
		return
	}
	a.holder.MutateConfig(func(cfg *config.Config) {
		// A revision that lost network.shared_secret must not blank a live
		// mesh. netsetup.go mints an in-memory secret on such a boot, and
		// replacing it with "" here would make every subsequent hello fail
		// while TCP keeps connecting. core.ApplyNetworkConfig has the same
		// rule for the daemon path.
		if n.SharedSecret == "" {
			n.SharedSecret = cfg.Network.SharedSecret
		}
		cfg.Network = n
	})
}
