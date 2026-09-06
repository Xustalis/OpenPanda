package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// TestSplitConfig guards the skill subcommand's --config parsing: the flag may
// appear anywhere, and bare/flag-only invocations must not panic (the former
// `args[1:]` in runSkill did).
func TestSplitConfig(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		configPath string
		positional []string
	}{
		{name: "bare", args: nil, configPath: "", positional: nil},
		{name: "list only", args: []string{"list"}, configPath: "", positional: []string{"list"}},
		{name: "config first", args: []string{"--config", "c.yaml"}, configPath: "c.yaml", positional: nil},
		{name: "config last", args: []string{"list", "--config", "c.yaml"}, configPath: "c.yaml", positional: []string{"list"}},
		{name: "config equals", args: []string{"--config=c.yaml", "list"}, configPath: "c.yaml", positional: []string{"list"}},
		{name: "config between positionals", args: []string{"approve", "--config", "c.yaml", "foo"}, configPath: "c.yaml", positional: []string{"approve", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, positional := splitConfig(tc.args)
			if path != tc.configPath {
				t.Fatalf("configPath = %q, want %q", path, tc.configPath)
			}
			if !reflect.DeepEqual(positional, tc.positional) {
				t.Fatalf("positional = %v, want %v", positional, tc.positional)
			}
		})
	}
}

func TestSkillImportAndHubCLI(t *testing.T) {
	skillsDir := t.TempDir()
	store := skills.NewStore(skillsDir)
	cfg := &config.Config{}
	cfg.Storage.SkillsPath = skillsDir

	// 1. Test skillHub install curated skill
	skillHub(cfg, store, []string{"install", "git-workflow"})

	loaded, err := store.Load(skills.ScopeGlobal, "", "git-workflow")
	if err != nil || loaded == nil {
		t.Fatalf("failed to load installed git-workflow: %v", err)
	}
	if loaded.Status != skills.StatusActive {
		t.Errorf("status = %s, want active", loaded.Status)
	}

	// 2. Test skillImport on a local file
	samplePath := filepath.Join(t.TempDir(), "local-skill.md")
	content := `---
name: local-cli-skill
description: Tested via CLI import
scope: global
---
Local steps`
	if err := os.WriteFile(samplePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write sample file: %v", err)
	}

	skillImport(store, []string{"--name", "renamed-cli-skill", samplePath})
	imported, err := store.Load(skills.ScopeGlobal, "", "renamed-cli-skill")
	if err != nil || imported == nil {
		t.Fatalf("failed to load imported skill: %v", err)
	}
	if imported.Description != "Tested via CLI import" {
		t.Errorf("imported description mismatch: %s", imported.Description)
	}

	// 3. Test skillInstall shorthand with local path
	samplePath2 := filepath.Join(t.TempDir(), "second-skill.md")
	content2 := `---
name: second-cli-skill
description: Second skill
scope: global
---
Second steps`
	if err := os.WriteFile(samplePath2, []byte(content2), 0o644); err != nil {
		t.Fatalf("write sample file: %v", err)
	}
	skillInstall(cfg, store, []string{samplePath2})
	imported2, err := store.Load(skills.ScopeGlobal, "", "second-cli-skill")
	if err != nil || imported2 == nil {
		t.Fatalf("failed to load shorthand installed skill: %v", err)
	}

	// 4. Test skillInstall shorthand with hub skill name
	skillInstall(cfg, store, []string{"code-review"})
	cr, err := store.Load(skills.ScopeGlobal, "", "code-review")
	if err != nil || cr == nil {
		t.Fatalf("failed to load code-review installed via shorthand: %v", err)
	}

	// 5. Test skillReset
	skillReset(store, []string{"code-review"})
	resetCr, err := store.Load(skills.ScopeGlobal, "", "code-review")
	if err != nil || resetCr == nil {
		t.Fatalf("failed to load code-review after reset: %v", err)
	}
}
