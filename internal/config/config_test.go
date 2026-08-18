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
	orig := os.Getenv("OPENPANDA_NODE_NAME")
	os.Setenv("OPENPANDA_NODE_NAME", "")
	defer os.Setenv("OPENPANDA_NODE_NAME", orig)

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
	os.Setenv("OPENPANDA_NODE_NAME", "env-node")
	defer os.Unsetenv("OPENPANDA_NODE_NAME")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Node.Name != "env-node" {
		t.Fatalf("env override failed: %q", cfg.Node.Name)
	}
}

func TestModelDefaults(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Model.BaseURL == "" || cfg.Model.Model == "" {
		t.Fatalf("expected model defaults, got %+v", cfg.Model)
	}
}

func TestModelParsesYAML(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "n"
model:
  base_url: "https://example.com/anthropic"
  api_key: "sk-test"
  model: "deepseek-reasoner"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Model.BaseURL != "https://example.com/anthropic" ||
		cfg.Model.APIKey != "sk-test" ||
		cfg.Model.Model != "deepseek-reasoner" {
		t.Fatalf("model = %+v", cfg.Model)
	}
}

func TestModelEnvOverrides(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	os.Setenv("OPENPANDA_MODEL_BASE_URL", "https://env.example.com")
	os.Setenv("OPENPANDA_MODEL_API_KEY", "sk-env")
	os.Setenv("OPENPANDA_MODEL", "deepseek-chat")
	defer os.Unsetenv("OPENPANDA_MODEL_BASE_URL")
	defer os.Unsetenv("OPENPANDA_MODEL_API_KEY")
	defer os.Unsetenv("OPENPANDA_MODEL")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Model.BaseURL != "https://env.example.com" ||
		cfg.Model.APIKey != "sk-env" ||
		cfg.Model.Model != "deepseek-chat" {
		t.Fatalf("model = %+v", cfg.Model)
	}
}

// TestSecretFilePermsTightened verifies P1-19: a config file carrying secrets
// (api_key / shared_secret / panel_token) with group/world-readable bits is
// tightened to 0600 at load time.
func TestSecretFilePermsTightened(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "test-node"
network:
  shared_secret: "s3cret"
model:
  api_key: "sk-test"
`)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("load: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}

// TestSecretlessFilePermsUntouched is the negative control: a config without
// secrets keeps whatever permissions the deployer chose.
func TestSecretlessFilePermsUntouched(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"test-node\"\n")
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("load: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Fatalf("perm = %o, want untouched 644", got)
	}
}
