// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnsureSharedSecretPersistsOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("node:\n  name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if !ensureSharedSecret(cfg, db, "test-node", path, quietLogger()) {
		t.Fatal("ensureSharedSecret reported no usable secret")
	}
	if cfg.Network.SharedSecret == "" {
		t.Fatal("in-memory config has no secret")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Network.SharedSecret != cfg.Network.SharedSecret {
		t.Fatalf("persisted secret %q != generated %q", loaded.Network.SharedSecret, cfg.Network.SharedSecret)
	}
}

func TestEnsureSharedSecretKeepsConfiguredSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("network:\n  shared_secret: keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Network.SharedSecret = "keep-me"
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if !ensureSharedSecret(cfg, db, "test-node", path, quietLogger()) {
		t.Fatal("ensureSharedSecret rejected an existing secret")
	}
	if cfg.Network.SharedSecret != "keep-me" {
		t.Fatalf("secret was replaced: %q", cfg.Network.SharedSecret)
	}
}

// A node with configured peers but no secret has almost certainly lost its
// credential. The fresh secret may back a fail-closed local start, but it must
// never reach disk — persisting it would exile the node from its own mesh on
// every future start.
func TestEnsureSharedSecretDoesNotPersistForMeshedNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("node:\n  name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Network.Peers = []config.PeerEntry{{Addr: "peer-1:7836"}}
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if !ensureSharedSecret(cfg, db, "test-node", path, quietLogger()) {
		t.Fatal("ensureSharedSecret should still provide an in-memory secret")
	}
	if cfg.Network.SharedSecret == "" {
		t.Fatal("no in-memory secret")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Network.SharedSecret != "" {
		t.Fatalf("lost-credential secret was persisted: %q", loaded.Network.SharedSecret)
	}
}
