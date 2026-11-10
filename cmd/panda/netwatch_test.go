// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

type recordingApplier struct {
	applied chan config.NetworkConfig
}

func (r *recordingApplier) ApplyNetworkConfig(_ context.Context, n config.NetworkConfig) {
	select {
	case r.applied <- n:
	default:
	}
}

// writeNetworkConfig replaces config.yaml atomically. os.WriteFile truncates
// in place, so a one-second poller tick can land between the truncate and the
// write and read half a YAML document. The observer advances its digest before
// loading, so that unreadable revision is then skipped forever — a CI-only
// flake that looks exactly like "the default path was never observed".
// Rename is atomic on every platform the observer supports.
func writeNetworkConfig(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".next"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestNetworkSyncAppliesPeerAddedToFile is the regression for the split that
// parked pinned tasks: the web console writes network.peers to config.yaml and
// applies it to its own process, so the daemon must notice the file on its own
// or the peer never gets a track.
func TestNetworkSyncAppliesPeerAddedToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeNetworkConfig(t, path, "network:\n  shared_secret: s\n")
	bootCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load boot config: %v", err)
	}

	applier := &recordingApplier{applied: make(chan config.NetworkConfig, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go runNetworkSync(ctx, logger, path, applier, bootCfg.Network)

	// Nothing to apply while the file is unchanged.
	select {
	case got := <-applier.applied:
		t.Fatalf("applied before any edit: %+v", got)
	case <-time.After(1500 * time.Millisecond):
	}

	writeNetworkConfig(t, path, "network:\n  shared_secret: s\n  peers:\n    - 10.0.0.2:7836\n")

	select {
	case got := <-applier.applied:
		peers := got.PeerAddrs()
		if len(peers) != 1 || peers[0] != "10.0.0.2:7836" {
			t.Fatalf("applied peers = %v, want [10.0.0.2:7836]", peers)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("peer added to config.yaml was never applied")
	}
}

// TestNetworkSyncResolvesDefaultConfigPath is the regression for the default
// install: --config is omitted, so the flag is "". An observer that reads the
// raw flag silently never fires even though config.yaml is being edited.
// ResolvePath must be applied inside the observer, not at the call site.
func TestNetworkSyncResolvesDefaultConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeNetworkConfig(t, path, "network:\n  shared_secret: s\n")
	t.Setenv("OPENPANDA_CONFIG_PATH", path)
	bootCfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load boot config: %v", err)
	}

	applier := &recordingApplier{applied: make(chan config.NetworkConfig, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go runNetworkSync(ctx, logger, "", applier, bootCfg.Network)

	// The observer reads its initial digest asynchronously. Writing several
	// distinct revisions removes the race where the first edit lands before
	// that read and therefore looks like "unchanged since boot" forever.
	// CI runs 60+ packages in parallel and the observer only ticks once a
	// second, so allow enough ticks for a full revision to land. A broken
	// ResolvePath applies nothing and still fails this deadline.
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for n := 1; ; n++ {
		body := fmt.Sprintf("network:\n  shared_secret: s\n  peers:\n    - 10.0.0.%d:7836\n", n)
		writeNetworkConfig(t, path, body)
		select {
		case got := <-applier.applied:
			peers := got.PeerAddrs()
			if len(peers) != 1 {
				t.Fatalf("applied peers = %v, want exactly one", peers)
			}
			return
		case <-deadline:
			raw, _ := os.ReadFile(path)
			t.Fatalf("default config path was never observed: ResolvePath(\"\")=%q, file=%q",
				config.ResolvePath(""), string(raw))
		case <-tick.C:
		}
	}
}

type fakeMutator struct {
	cfg *config.Config
}

func (m *fakeMutator) MutateConfig(fn func(*config.Config)) { fn(m.cfg) }

// TestEngineNetworkApplierKeepsLiveSecret is the console-side twin of
// core.ApplyNetworkConfig's rule. An env-supplied (or in-memory minted) secret
// is deliberately absent from config.yaml; a reload that writes the parsed
// struct straight over the live one would blank it and make every hello fail
// while TCP still connects.
func TestEngineNetworkApplierKeepsLiveSecret(t *testing.T) {
	m := &fakeMutator{cfg: &config.Config{}}
	m.cfg.Network.SharedSecret = "live-secret"
	m.cfg.Network.Peers = []config.PeerEntry{{Addr: "10.0.0.1:7836"}}

	incoming := config.NetworkConfig{Peers: []config.PeerEntry{{Addr: "10.0.0.2:7836"}}}
	engineNetworkApplier{holder: m}.ApplyNetworkConfig(context.Background(), incoming)

	if m.cfg.Network.SharedSecret != "live-secret" {
		t.Fatalf("secret = %q, want the live one preserved", m.cfg.Network.SharedSecret)
	}
	if got := m.cfg.Network.PeerAddrs(); len(got) != 1 || got[0] != "10.0.0.2:7836" {
		t.Fatalf("peers = %v, want the reloaded edge", got)
	}

	// An explicit secret in the file is a real rotation and must be honoured.
	incoming.SharedSecret = "rotated"
	engineNetworkApplier{holder: m}.ApplyNetworkConfig(context.Background(), incoming)
	if m.cfg.Network.SharedSecret != "rotated" {
		t.Fatalf("secret = %q, want the rotated one", m.cfg.Network.SharedSecret)
	}
}

// TestNetworkSyncKeepsMeshOnBrokenRevision pins the failure posture: an
// unreadable revision must not clear the live mesh.
func TestNetworkSyncKeepsMeshOnBrokenRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeNetworkConfig(t, path, "network:\n  shared_secret: s\n")
	bootCfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load boot config: %v", err)
	}

	applier := &recordingApplier{applied: make(chan config.NetworkConfig, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go runNetworkSync(ctx, logger, path, applier, bootCfg.Network)

	writeNetworkConfig(t, path, "network:\n\tbroken: [\n")
	select {
	case got := <-applier.applied:
		t.Fatalf("broken revision applied: %+v", got)
	case <-time.After(1500 * time.Millisecond):
	}
}
