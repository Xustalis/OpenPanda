package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShippedConfigsParseStrictly guards the configs users actually copy or that
// the deploy scripts ship. Strict decoding rejects unknown keys, so a key left
// behind in one of them after a rename would make a fresh install fail to start
// with a message about a field the user never wrote. Local-only fixtures are
// skipped when absent (deploy/windows/config.yaml is gitignored, for one).
func TestShippedConfigsParseStrictly(t *testing.T) {
	paths := []string{
		"../../config.example.yaml",
		"../../config.example.local.yaml",
		"../../testdata/node-a.yaml",
		"../../testdata/node-b.yaml",
		"../../testdata/deploy-opi.yaml",
		"../../deploy/windows/config.yaml",
	}
	checked := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		checked++
		if _, err := Load(p); err != nil {
			t.Errorf("%s must parse under strict decoding: %v", p, err)
		}
	}
	if checked == 0 {
		t.Fatal("no shipped config was found to check")
	}
}

// TestLoadRejectsUnknownFields pins the behavior itself: a misspelled key has to
// be an error, not a silent fallback to the default. `shared_secrett` is the
// motivating case — dropped, the node starts local-only and only logs a line.
func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "network:\n  shared_secrett: \"abc\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a misspelled key must be rejected, not silently ignored")
	}
}

// TestLoadKeepsCommentedKeysOut confirms the guard above does not reject the
// example's commented-out options: they are comments, so they never reach the
// decoder, and the shipped example must keep parsing.
func TestLoadKeepsCommentedKeysOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "network:\n  # max_connections: 64\n  listen_addr: \"127.0.0.1:7836\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("commented options must not be treated as unknown keys: %v", err)
	}
}
