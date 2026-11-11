// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchHubIndexFallback(t *testing.T) {
	// Empty URL should fall back to curated skills
	idx, err := FetchHubIndex(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchHubIndex with empty URL failed: %v", err)
	}
	if len(idx.Skills) == 0 {
		t.Fatalf("expected curated skills, got empty list")
	}

	// Broken URL should also fall back gracefully to curated skills
	idx2, err := FetchHubIndex(context.Background(), "http://127.0.0.1:54321/unreachable")
	if err != nil {
		t.Fatalf("FetchHubIndex unreachable returned error instead of fallback: %v", err)
	}
	if len(idx2.Skills) == 0 {
		t.Fatalf("expected curated skills on network error, got empty")
	}
}

func TestFetchHubIndexRemote(t *testing.T) {
	remoteIndex := HubIndex{
		Version: "1.0",
		Name:    "Remote Mock Hub",
		Skills: []HubSkill{
			{
				Name:        "remote-skill-1",
				Description: "Remote test skill",
				Scope:       ScopeGlobal,
				Version:     "1.0.0",
				Tags:        []string{"remote", "test"},
				Content: `---
name: remote-skill-1
description: Remote test skill
scope: global
---
Remote content`,
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(remoteIndex)
	}))
	defer ts.Close()

	idx, err := FetchHubIndex(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("FetchHubIndex remote failed: %v", err)
	}

	foundRemote := false
	for _, s := range idx.Skills {
		if s.Name == "remote-skill-1" {
			foundRemote = true
			break
		}
	}
	if !foundRemote {
		t.Errorf("remote skill not present in fetched index")
	}
}

func TestSearchHub(t *testing.T) {
	idx := &HubIndex{
		Skills: []HubSkill{
			{Name: "git-commit-helper", Description: "Help with git commits", Tags: []string{"git", "vcs"}},
			{Name: "docker-manager", Description: "Docker container tooling", Tags: []string{"docker", "devops"}},
			{Name: "k8s-deploy", Description: "Deploy pods to Kubernetes", Tags: []string{"k8s", "deploy"}},
		},
	}

	// Empty query returns all
	all := SearchHub(idx, "")
	if len(all) != 3 {
		t.Errorf("empty query expected 3, got %d", len(all))
	}

	// Match by name
	res := SearchHub(idx, "git")
	if len(res) != 1 || res[0].Name != "git-commit-helper" {
		t.Errorf("search 'git' failed: %v", res)
	}

	// Match by tag
	res2 := SearchHub(idx, "devops")
	if len(res2) != 1 || res2[0].Name != "docker-manager" {
		t.Errorf("search tag 'devops' failed: %v", res2)
	}

	// Multi-token match
	res3 := SearchHub(idx, "deploy kubernetes")
	if len(res3) != 1 || res3[0].Name != "k8s-deploy" {
		t.Errorf("search multi-token failed: %v", res3)
	}
}

func TestInstallFromHub(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()

	// Install a curated skill that has inline content
	sk, err := InstallFromHub(ctx, store, "", "git-workflow", ImportOptions{})
	if err != nil {
		t.Fatalf("InstallFromHub git-workflow failed: %v", err)
	}
	if sk.Name != "git-workflow" {
		t.Errorf("installed wrong skill name: %s", sk.Name)
	}

	// Verify it can be loaded from store
	loaded, err := store.Load(ScopeGlobal, "", "git-workflow")
	if err != nil || loaded == nil {
		t.Fatalf("could not load installed skill from store: %v", err)
	}
	if loaded.Status != StatusActive {
		t.Errorf("expected StatusActive, got %s", loaded.Status)
	}

	// Attempt to install nonexistent skill
	_, err = InstallFromHub(ctx, store, "", "non-existent-skill-xyz", ImportOptions{})
	if err == nil {
		t.Errorf("expected error installing nonexistent skill")
	}
}

func TestEnsureBuiltinsAndReset(t *testing.T) {
	store := NewStore(t.TempDir())

	if !IsBuiltinSkill("git-workflow") || !IsBuiltinSkill("git") {
		t.Errorf("expected git-workflow and git to be recognized as builtin")
	}
	if IsBuiltinSkill("random-nonexistent") {
		t.Errorf("random-nonexistent should not be recognized as builtin")
	}

	// Store initially has 0 skills
	idx, err := store.Index()
	if err != nil {
		t.Fatalf("index failed: %v", err)
	}
	if len(idx) != 0 {
		t.Fatalf("expected 0 skills initially, got %d", len(idx))
	}

	// Ensure built-in skills
	if err := store.EnsureBuiltins(); err != nil {
		t.Fatalf("EnsureBuiltins failed: %v", err)
	}

	idx, err = store.Index()
	if err != nil {
		t.Fatalf("index after EnsureBuiltins failed: %v", err)
	}
	if len(idx) != len(CuratedSkills) {
		t.Fatalf("expected %d skills after EnsureBuiltins, got %d", len(CuratedSkills), len(idx))
	}

	for _, entry := range idx {
		if !entry.Builtin {
			t.Errorf("expected skill %s to be marked Builtin", entry.Name)
		}
		if entry.Status != StatusActive {
			t.Errorf("expected skill %s to be active, got %s", entry.Name, entry.Status)
		}
	}

	// Modify one skill and verify ResetBuiltin restores it
	gitSkill, err := store.Load(ScopeGlobal, "", "git-workflow")
	if err != nil || gitSkill == nil {
		t.Fatalf("failed to load git-workflow: %v", err)
	}
	gitSkill.Description = "Modified description by user"
	if err := store.Save(gitSkill); err != nil {
		t.Fatalf("failed to save modified git-workflow: %v", err)
	}

	modified, _ := store.Load(ScopeGlobal, "", "git-workflow")
	if modified.Description != "Modified description by user" {
		t.Fatalf("expected modified description")
	}

	// Reset via alias
	resetSk, err := store.ResetBuiltin("git")
	if err != nil {
		t.Fatalf("ResetBuiltin failed: %v", err)
	}
	if resetSk.Description == "Modified description by user" {
		t.Fatalf("expected description to be restored to factory default")
	}
}

func TestDiscoverAndInstall(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()

	// 1. Discover docker when not installed
	sk, isNew, err := store.DiscoverAndInstall(ctx, "", "docker compose container", ImportOptions{})
	if err != nil {
		t.Fatalf("DiscoverAndInstall failed: %v", err)
	}
	if !isNew {
		t.Errorf("expected isNew=true for first install")
	}
	if sk.Name != "docker-compose" {
		t.Errorf("expected docker-compose, got %s", sk.Name)
	}

	// 2. Discover again: already installed
	sk2, isNew2, err := store.DiscoverAndInstall(ctx, "", "docker", ImportOptions{})
	if err != nil {
		t.Fatalf("second DiscoverAndInstall failed: %v", err)
	}
	if isNew2 {
		t.Errorf("expected isNew=false when already installed")
	}
	if sk2.Name != "docker-compose" {
		t.Errorf("expected docker-compose, got %s", sk2.Name)
	}

	// 3. Discover nonexistent
	_, _, err = store.DiscoverAndInstall(ctx, "", "completely_unknown_xyz123", ImportOptions{})
	if err == nil {
		t.Errorf("expected error for nonexistent skill")
	}
}

// CuratedSkills is the only source of the built-in SKILL.md bodies, but its
// Content field is a hand-written string. A YAML typo there used to be skipped
// silently by EnsureBuiltins: the skill simply never appeared. Pin the whole
// catalog so every template parses and agrees with its catalog row.
func TestCuratedSkillTemplatesAreConsistent(t *testing.T) {
	for _, cur := range CuratedSkills {
		sk, err := ParseSkill([]byte(cur.Content))
		if err != nil {
			t.Errorf("built-in %q has an invalid template: %v", cur.Name, err)
			continue
		}
		if sk.Name != cur.Name || sk.Description != cur.Description {
			t.Errorf("built-in %q template disagrees with catalog: name=%q description=%q", cur.Name, sk.Name, sk.Description)
		}
		if len(cur.Triggers) == 0 {
			t.Errorf("built-in %q has no curated triggers", cur.Name)
		}
		if cur.Alias != "" && ResolveSkillAlias(cur.Alias) != cur.Name {
			t.Errorf("built-in %q alias %q does not resolve back", cur.Name, cur.Alias)
		}
	}
}

// SearchHub used to return catalog order among entries whose text contained
// every query token, which made DiscoverAndInstall install whichever skill
// was listed first rather than the most relevant one.
func TestSearchHubRanksByRelevance(t *testing.T) {
	idx := &HubIndex{Skills: []HubSkill{
		{Name: "k8s-helper", Description: "Misc Kubernetes helpers", Tags: []string{"k8s", "deploy"}},
		{Name: "kubernetes-deploy", Description: "Deploy to Kubernetes", Tags: []string{"k8s", "deploy"},
			Triggers: []string{"kubernetes", "deploy", "部署"}},
	}}
	res := SearchHub(idx, "kubernetes deploy")
	if len(res) != 2 {
		t.Fatalf("expected both candidates, got %d", len(res))
	}
	if res[0].Name != "kubernetes-deploy" {
		t.Errorf("top match = %q, want the name/trigger-weighted skill", res[0].Name)
	}
	// Curated triggers are bilingual: a Chinese query still reaches the entry.
	if res := SearchHub(idx, "部署"); len(res) == 0 || res[0].Name != "kubernetes-deploy" {
		t.Errorf("Chinese trigger did not reach the catalog entry: %v", res)
	}
}

func TestVisualDeliveryBuiltinLoadsOnDemand(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.EnsureBuiltins(); err != nil {
		t.Fatal(err)
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"build a responsive website", "设计一个精美的网页"} {
		matches := MatchScopes(idx, "", nil, query, "")
		found := false
		for _, m := range matches {
			if m.Name == "visual-delivery" {
				found = true
			}
		}
		if !found {
			t.Fatalf("visual workflow missing for %q", query)
		}
	}
	for _, m := range MatchScopes(idx, "", nil, "fix database schema migration", "") {
		if m.Name == "visual-delivery" {
			t.Fatal("visual skill loaded for nonvisual task")
		}
	}
}
