// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"gopkg.in/yaml.v3"
)

// TestSyncPeerTrustConfig: the config mirror of a trust pin. A configured
// entry whose dial address is bound to the pinned row is rewritten — this is
// what keeps a SIGHUP reload from re-asserting a stale stricter hint and
// silently defeating a raise, including via the stable k: identity.
func TestSyncPeerTrustConfig(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := ledger.Register(db, ledger.Card{Device: "vps"}, "vps-row", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE employee_cache SET pub_key='aa11' WHERE id='vps-row'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO peer_addrs (node_id, addr, last_seen) VALUES ('vps-row','10.0.0.5:7836',1)`); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Network.Peers = []config.PeerEntry{{Addr: "10.0.0.5:7836", Trust: "open"}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if data, err := yaml.Marshal(cfg); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	ids := peerKeyRowIDs(db, "aa11")
	if len(ids) != 1 || !ids["vps-row"] {
		t.Fatalf("peerKeyRowIDs = %v, want {vps-row}", ids)
	}

	// Raise to mesh (stored as ""): the stale open hint must leave the file —
	// scalar form — or the next reload re-asserts open.
	if err := syncPeerTrustConfig(db, cfg, path, ids, ""); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if cfg.Network.Peers[0].Trust != "" {
		t.Fatalf("live entry = %q, want cleared", cfg.Network.Peers[0].Trust)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "trust: open") {
		t.Fatalf("stale hint survived the sync:\n%s", data)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Network.Peers) != 1 || reloaded.Network.Peers[0].Addr != "10.0.0.5:7836" ||
		reloaded.Network.Peers[0].Trust != "" {
		t.Fatalf("reloaded peers = %+v, want the scalar entry back", reloaded.Network.Peers)
	}

	// Idempotent: nothing changed → no error, no rewrite.
	if err := syncPeerTrustConfig(db, cfg, path, ids, ""); err != nil {
		t.Fatalf("idempotent sync: %v", err)
	}

	// An unbound id mirrors nothing and must not touch the file.
	before, _ := os.ReadFile(path)
	if err := syncPeerTrustConfig(db, cfg, path, map[string]bool{"ghost-row": true}, "guarded"); err != nil {
		t.Fatalf("ghost sync: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("an unbound id rewrote the config")
	}
}
