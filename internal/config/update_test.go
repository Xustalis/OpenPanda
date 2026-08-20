package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateSectionFieldPreservesComments verifies the nested upsert keeps
// unrelated sections and their comments byte-for-byte, creates missing
// sections, and reloads into the expected values.
func TestUpdateSectionFieldPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "# top comment\nnode:\n  name: macbook # keep me\n  resource_class: Standard\napproval:\n  mode: on-request # gate\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UpdateSectionFieldInt(path, []string{"memory", "limits"}, "user", 8000); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSectionField(path, []string{"approval"}, "mode", "never"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSectionField(path, []string{"injection"}, "model", "always"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# top comment", "keep me", "# gate"} {
		if !strings.Contains(text, want) {
			t.Errorf("comment lost: %q\n%s", want, text)
		}
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.Limits.User != 8000 {
		t.Errorf("user limit = %d, want 8000", cfg.Memory.Limits.User)
	}
	if cfg.Approval.Mode != ApprovalModeNever {
		t.Errorf("approval mode = %q, want never", cfg.Approval.Mode)
	}
	if cfg.Injection.Model != InjectionModelAlways {
		t.Errorf("injection model = %q, want always", cfg.Injection.Model)
	}
}

// TestUpdateSectionFieldEmptyRemoves verifies an empty value drops the field
// so defaults apply again on reload.
func TestUpdateSectionFieldEmptyRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("node:\n  name: n\napproval:\n  mode: never\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSectionField(path, []string{"approval"}, "mode", ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval.Mode != ApprovalModeOnRequest {
		t.Errorf("approval mode = %q, want default on-request", cfg.Approval.Mode)
	}
}

// TestUpdateSectionList verifies preferred_agents round-trips through the
// list upsert, including replacement and clearing.
func TestUpdateSectionList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("node:\n  name: n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSectionList(path, []string{"routing"}, "preferred_agents", []string{"codex", "claude_code"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routing.PreferredAgents) != 2 || cfg.Routing.PreferredAgents[0] != "codex" {
		t.Errorf("preferred_agents = %v", cfg.Routing.PreferredAgents)
	}

	// Replace, then clear.
	if err := UpdateSectionList(path, []string{"routing"}, "preferred_agents", []string{"opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSectionList(path, []string{"routing"}, "preferred_agents", nil); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routing.PreferredAgents) != 0 {
		t.Errorf("preferred_agents = %v, want empty", cfg.Routing.PreferredAgents)
	}
}

// TestUpdateSectionFieldMissingFile verifies a missing config file is created
// from defaults and then updated.
func TestUpdateSectionFieldMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := UpdateSectionField(path, []string{"approval"}, "mode", "always"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval.Mode != ApprovalModeAlways {
		t.Errorf("approval mode = %q, want always", cfg.Approval.Mode)
	}
}
