package askengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

func newSkillTestEngine(t *testing.T, hubURL string) (*Engine, *entry.Registry) {
	t.Helper()
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")

	cfg := &config.Config{}
	cfg.Storage.SkillsPath = skillsDir
	cfg.Skills.HubURL = hubURL

	store := skills.NewStore(skillsDir)
	if err := store.EnsureBuiltins(); err != nil {
		t.Fatalf("ensure builtins: %v", err)
	}

	e := &Engine{
		cfg:    cfg,
		skills: store,
	}
	reg := entry.NewRegistry()
	registerSkillTools(reg, e)
	e.registry = reg
	return e, reg
}

func runSkillTool(t *testing.T, reg *entry.Registry, name string, args map[string]any) string {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("tool %s failed: %v", name, err)
	}
	return out
}

func TestSkillTools_ListAndShow(t *testing.T) {
	_, reg := newSkillTestEngine(t, "")

	// 1. skill_list
	listOut := runSkillTool(t, reg, "skill_list", map[string]any{})
	if !strings.Contains(listOut, "git-workflow") {
		t.Fatalf("expected git-workflow in list output, got: %s", listOut)
	}
	if !strings.Contains(listOut, "code-review") {
		t.Fatalf("expected code-review in list output, got: %s", listOut)
	}

	// 2. skill_show
	showOut := runSkillTool(t, reg, "skill_show", map[string]any{"name": "git-workflow"})
	if !strings.Contains(showOut, "git-workflow") || !strings.Contains(showOut, "git") {
		t.Fatalf("expected skill content for git-workflow, got: %s", showOut)
	}
}

func TestSkillTools_SearchAndDiscover(t *testing.T) {
	mockIndex := skills.HubIndex{
		Version: "1.0",
		Name:    "Mock Hub",
		Skills: []skills.HubSkill{
			{
				Name:        "redis-cluster-ops",
				Description: "Redis cluster management and troubleshooting",
				Scope:       skills.ScopeGlobal,
				Version:     "1.0.0",
				Tags:        []string{"redis", "cache", "ops"},
				Content: `---
name: redis-cluster-ops
description: Redis cluster management and troubleshooting
scope: global
---
# Redis Cluster Ops
Step 1: Check cluster status.`,
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockIndex)
	}))
	defer ts.Close()

	_, reg := newSkillTestEngine(t, ts.URL)

	// 1. skill_search
	searchOut := runSkillTool(t, reg, "skill_search", map[string]any{"query": "redis"})
	if !strings.Contains(searchOut, "redis-cluster-ops") {
		t.Fatalf("expected redis-cluster-ops in search result, got: %s", searchOut)
	}

	// 2. skill_discover (autonomous search & install)
	discOut := runSkillTool(t, reg, "skill_discover", map[string]any{"query": "redis"})
	if !strings.Contains(discOut, "已自动从技能集市中找到并安装激活新技能") || !strings.Contains(discOut, "redis-cluster-ops") {
		t.Fatalf("unexpected discover result: %s", discOut)
	}

	// Verify it shows up in skill_list
	listOut := runSkillTool(t, reg, "skill_list", map[string]any{})
	if !strings.Contains(listOut, "redis-cluster-ops") {
		t.Fatalf("expected installed redis skill in list, got: %s", listOut)
	}

	// 3. Second skill_discover on same query should recognize already active
	discOut2 := runSkillTool(t, reg, "skill_discover", map[string]any{"query": "redis"})
	if !strings.Contains(discOut2, "已处于激活状态") {
		t.Fatalf("expected already active notification on repeat discover, got: %s", discOut2)
	}
}

func TestSkillTools_Install(t *testing.T) {
	mockIndex := skills.HubIndex{
		Version: "1.0",
		Name:    "Mock Hub",
		Skills: []skills.HubSkill{
			{
				Name:        "kafka-tuning",
				Description: "Kafka partition performance tuning",
				Scope:       skills.ScopeGlobal,
				Content: `---
name: kafka-tuning
description: Kafka partition performance tuning
scope: global
---
# Kafka Tuning Steps`,
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockIndex)
	}))
	defer ts.Close()

	_, reg := newSkillTestEngine(t, ts.URL)

	instOut := runSkillTool(t, reg, "skill_install", map[string]any{"target": "kafka-tuning"})
	if !strings.Contains(instOut, "成功从 Skills Hub 安装技能 kafka-tuning") {
		t.Fatalf("unexpected install result: %s", instOut)
	}

	showOut := runSkillTool(t, reg, "skill_show", map[string]any{"name": "kafka-tuning"})
	if !strings.Contains(showOut, "Kafka Tuning Steps") {
		t.Fatalf("expected kafka tuning content, got: %s", showOut)
	}
}
