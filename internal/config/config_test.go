package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoadDefaultsOnMissingFile(t *testing.T) {
	// DefaultPath won't exist in CI; loading must return defaults, not error.
	orig := os.Getenv("PANDA_NODE_NAME")
	os.Setenv("PANDA_NODE_NAME", "")
	defer os.Setenv("PANDA_NODE_NAME", orig)

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if cfg.Node.Name == "" {
		t.Fatalf("expected default node name")
	}
	if cfg.Network.ListenAddr == "" {
		t.Fatalf("expected default listen addr")
	}
}

func TestLoadParsesYAML(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "test-node"
  resource_class: "Micro"

network:
  listen_addr: "127.0.0.1:9999"
  peers:
    - "peer1:9999"

storage:
  db_path: "/tmp/test.db"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Node.Name != "test-node" || cfg.Node.ResourceClass != "Micro" {
		t.Fatalf("node = %+v", cfg.Node)
	}
	if cfg.Network.ListenAddr != "127.0.0.1:9999" || len(cfg.Network.Peers) != 1 {
		t.Fatalf("network = %+v", cfg.Network)
	}
	if cfg.Storage.DBPath != "/tmp/test.db" {
		t.Fatalf("db = %q", cfg.Storage.DBPath)
	}
}

func TestLoadMalformedReturnsError(t *testing.T) {
	p := writeTemp(t, "node: [unclosed\n  bad: : yaml: [")
	if _, err := Load(p); err == nil {
		t.Fatalf("expected error for malformed yaml")
	}
}

func TestLoadEmptyNodeNameRejected(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"\"\n")
	if _, err := Load(p); err == nil {
		t.Fatalf("expected error for empty node name")
	}
}

func TestEnvOverrides(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"file-node\"\n")
	os.Setenv("PANDA_NODE_NAME", "env-node")
	defer os.Unsetenv("PANDA_NODE_NAME")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Node.Name != "env-node" {
		t.Fatalf("env override failed: %q", cfg.Node.Name)
	}
}
