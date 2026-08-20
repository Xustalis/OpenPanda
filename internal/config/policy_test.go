package config

import (
	"strings"
	"testing"
)

// TestPolicyDefaults verifies the A1/A2/config-base fields carry their
// documented defaults on a fresh config and on an old config file that
// predates them (missing sections must not break loading).
func TestPolicyDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, "node:\n  name: \"old-node\"\n"))
	if err != nil {
		t.Fatalf("load old-format config: %v", err)
	}
	if cfg.Injection.Model != InjectionModelAuto {
		t.Errorf("injection.model = %q, want auto", cfg.Injection.Model)
	}
	if len(cfg.Routing.PreferredAgents) != 0 {
		t.Errorf("routing.preferred_agents = %v, want empty", cfg.Routing.PreferredAgents)
	}
	if cfg.Memory.Limits.User != 5000 || cfg.Memory.Limits.Memory != 10000 || cfg.Memory.Limits.Project != 30000 {
		t.Errorf("memory.limits = %+v, want 5000/10000/30000", cfg.Memory.Limits)
	}
	if cfg.Approval.Mode != ApprovalModeOnRequest {
		t.Errorf("approval.mode = %q, want on-request", cfg.Approval.Mode)
	}
	// Default() carries the same values.
	d := Default()
	if d.Injection.Model != InjectionModelAuto || d.Approval.Mode != ApprovalModeOnRequest ||
		d.Memory.Limits.User != 5000 || d.Memory.Limits.Memory != 10000 || d.Memory.Limits.Project != 30000 {
		t.Errorf("Default() policy fields = %+v", d)
	}
}

// TestPolicyParsesYAML verifies every new field round-trips from YAML.
func TestPolicyParsesYAML(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "n"
injection:
  model: "never"
routing:
  preferred_agents: ["codex", "opencode"]
memory:
  limits:
    user: 100
    memory: 200
    project: 300
approval:
  mode: "always"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Injection.Model != InjectionModelNever {
		t.Errorf("injection.model = %q, want never", cfg.Injection.Model)
	}
	if len(cfg.Routing.PreferredAgents) != 2 || cfg.Routing.PreferredAgents[0] != "codex" {
		t.Errorf("preferred_agents = %v", cfg.Routing.PreferredAgents)
	}
	if cfg.Memory.Limits.User != 100 || cfg.Memory.Limits.Memory != 200 || cfg.Memory.Limits.Project != 300 {
		t.Errorf("memory.limits = %+v", cfg.Memory.Limits)
	}
	if cfg.Approval.Mode != ApprovalModeAlways {
		t.Errorf("approval.mode = %q, want always", cfg.Approval.Mode)
	}
}

// TestPolicyPartialSections: a file that sets only some of the new fields
// keeps defaults for the rest (old-config compatibility).
func TestPolicyPartialSections(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "n"
injection:
  model: "always"
memory:
  limits:
    project: 99999
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Injection.Model != InjectionModelAlways {
		t.Errorf("injection.model = %q, want always", cfg.Injection.Model)
	}
	if cfg.Memory.Limits.Project != 99999 || cfg.Memory.Limits.User != 5000 || cfg.Memory.Limits.Memory != 10000 {
		t.Errorf("memory.limits = %+v, want 5000/10000/99999", cfg.Memory.Limits)
	}
	if cfg.Approval.Mode != ApprovalModeOnRequest {
		t.Errorf("approval.mode = %q, want default on-request", cfg.Approval.Mode)
	}
}

// TestPolicyInvalidValuesRejected: typos in the enum fields fail loudly at
// load time instead of silently degrading.
func TestPolicyInvalidValuesRejected(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"injection", "node:\n  name: \"n\"\ninjection:\n  model: \"sometimes\"\n", "injection.model"},
		{"approval", "node:\n  name: \"n\"\napproval:\n  mode: \"maybe\"\n", "approval.mode"},
		{"negative limit", "node:\n  name: \"n\"\nmemory:\n  limits:\n    user: -5\n", "memory.limits.user"},
	} {
		if _, err := Load(writeTemp(t, tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want mention of %q", tc.name, err, tc.want)
		}
	}
}

// TestPolicyNormalizers: zero-value structs normalize to the documented
// defaults (the commander relies on this for zero InjectionConfig).
func TestPolicyNormalizers(t *testing.T) {
	if got := (InjectionConfig{}).NormalizedModel(); got != InjectionModelAuto {
		t.Errorf("NormalizedModel zero = %q, want auto", got)
	}
	if got := (InjectionConfig{Model: "bogus"}).NormalizedModel(); got != InjectionModelAuto {
		t.Errorf("NormalizedModel bogus = %q, want auto", got)
	}
	if got := (ApprovalConfig{}).NormalizedMode(); got != ApprovalModeOnRequest {
		t.Errorf("NormalizedMode zero = %q, want on-request", got)
	}
	if got := (ApprovalConfig{Mode: ApprovalModeNever}).NormalizedMode(); got != ApprovalModeNever {
		t.Errorf("NormalizedMode never = %q, want never", got)
	}
}

// TestUpdateModelSectionPreservesPolicySections: the comment-preserving
// write-back must not clobber the new sections.
func TestUpdateModelSectionPreservesPolicySections(t *testing.T) {
	p := writeTemp(t, `
node:
  name: "n"
injection:
  model: "never"
routing:
  preferred_agents: ["codex"]
`)
	if err := UpdateModelSection(p, ModelConfig{BaseURL: "https://x.example/anthropic", APIKey: "sk-1", Model: "m"}); err != nil {
		t.Fatalf("update model section: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Injection.Model != InjectionModelNever || len(cfg.Routing.PreferredAgents) != 1 {
		t.Fatalf("policy sections lost on write-back: %+v / %+v", cfg.Injection, cfg.Routing)
	}
	if cfg.Model.Model != "m" {
		t.Fatalf("model write-back failed: %+v", cfg.Model)
	}
}
