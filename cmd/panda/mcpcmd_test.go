package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// selfDeps builds the tool dependencies against a throwaway DB + skills
// root so handler tests never touch the operator's real store.
func selfDeps(t *testing.T) *selfToolsDeps {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &selfToolsDeps{
		cfg: &config.Config{
			Node:    config.NodeConfig{Name: "test-node", Kind: "physical"},
			Storage: config.StorageConfig{SkillsPath: filepath.Join(dir, "skills")},
		},
		db:     db,
		tasks:  core.NewTaskStore(db, nil),
		skills: skills.NewStore(filepath.Join(dir, "skills")),
	}
}

// TestSelfToolsStatus verifies the read path: version, node identity and
// task counts all decode out of the JSON payload.
func TestSelfToolsStatus(t *testing.T) {
	d := selfDeps(t)
	text, err := d.toolStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	var out struct {
		Version    string         `json:"version"`
		Node       string         `json:"node"`
		TaskCounts map[string]int `json:"task_counts"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("status not JSON: %v (%q)", err, text)
	}
	if out.Version == "" || out.Node != "test-node" {
		t.Fatalf("status payload wrong: %+v", out)
	}
}

// TestSelfToolsQueueEmpty covers the queue snapshot on an empty store: all
// six states report zero and the active list is empty, not nil-marshaled.
func TestSelfToolsQueueEmpty(t *testing.T) {
	d := selfDeps(t)
	text, err := d.toolQueue(context.Background(), nil)
	if err != nil {
		t.Fatalf("toolQueue: %v", err)
	}
	if !strings.Contains(text, `"submitted":0`) {
		t.Fatalf("queue counts missing: %s", text)
	}
}

// TestSelfToolsSkillRoundtrip imports a skill through the tool surface and
// finds it in the listing — the same pair an agent would use to persist a
// workflow it developed.
func TestSelfToolsSkillRoundtrip(t *testing.T) {
	d := selfDeps(t)
	const body = `---
name: mcp-made-skill
description: created through the self-tools surface
scope: global
---

## Steps
1. Do the thing
`
	text, err := d.toolSkillImport(context.Background(), map[string]any{"content": body})
	if err != nil {
		t.Fatalf("toolSkillImport: %v", err)
	}
	if !strings.Contains(text, "mcp-made-skill") {
		t.Fatalf("import result = %q", text)
	}
	text, err = d.toolSkillList(context.Background(), nil)
	if err != nil {
		t.Fatalf("toolSkillList: %v", err)
	}
	// Agent-initiated imports land pending: the skill is listed but cannot
	// steer tasks until the user approves it in the foreground.
	if !strings.Contains(text, "mcp-made-skill") || !strings.Contains(text, `"status":"pending"`) {
		t.Fatalf("list missing the imported skill: %s", text)
	}
}

// TestSelfToolsSubmitGuards covers the submit tool's refusal paths — a
// missing title and the per-session cap — without spawning the CLI.
func TestSelfToolsSubmitGuards(t *testing.T) {
	d := selfDeps(t)
	d.exe = "/nonexistent/panda" // never reached on the guard paths
	if _, err := d.toolTaskSubmit(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("empty title must be refused")
	}
	d.submits.Store(maxSelfTaskSubmits)
	_, err := d.toolTaskSubmit(context.Background(), map[string]any{"title": "x"})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("session cap must refuse, got %v", err)
	}
}

// TestSelfToolsNodes covers the mesh directory read on an empty ledger:
// valid JSON array, no error.
func TestSelfToolsNodes(t *testing.T) {
	d := selfDeps(t)
	text, err := d.toolNodes(context.Background(), nil)
	if err != nil {
		t.Fatalf("toolNodes: %v", err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal([]byte(text), &nodes); err != nil {
		t.Fatalf("nodes not a JSON array: %v (%q)", err, text)
	}
}

// TestSelfToolsSubtaskSpawnGuards verifies validation guards for subtask spawn.
func TestSelfToolsSubtaskSpawnGuards(t *testing.T) {
	d := selfDeps(t)
	d.exe = "/nonexistent/panda"
	if _, err := d.toolSubtaskSpawn(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("empty title must be refused")
	}
	d.submits.Store(maxSelfTaskSubmits)
	_, err := d.toolSubtaskSpawn(context.Background(), map[string]any{"title": "x"})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("session cap must refuse, got %v", err)
	}
}

// TestSelfToolsSubtaskAwait verifies subtask await behavior on terminal state and error.
func TestSelfToolsSubtaskAwait(t *testing.T) {
	d := selfDeps(t)
	// Missing task_id
	if _, err := d.toolSubtaskAwait(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("empty task_id must be refused")
	}
	// Nonexistent task
	if _, err := d.toolSubtaskAwait(context.Background(), map[string]any{"task_id": "nonexistent"}); err == nil {
		t.Fatalf("nonexistent task must fail")
	}

	// Create a completed task directly in store and await it
	task, err := d.tasks.Create(context.Background(), "parent-1", "test-proj", "child subtask", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := d.tasks.Queue(context.Background(), task.TaskID, "test-node"); err != nil {
		t.Fatalf("queue task: %v", err)
	}
	if err := d.tasks.Dispatch(context.Background(), task.TaskID, "test-node", "test-node"); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}
	if err := d.tasks.Accept(context.Background(), task.TaskID, "test-node"); err != nil {
		t.Fatalf("accept task: %v", err)
	}
	if err := d.tasks.Complete(context.Background(), task.TaskID, "test-node", "test result output"); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	text, err := d.toolSubtaskAwait(context.Background(), map[string]any{"task_id": task.TaskID, "timeout_s": 2})
	if err != nil {
		t.Fatalf("toolSubtaskAwait failed: %v", err)
	}
	var out struct {
		TaskID   string `json:"task_id"`
		ParentID string `json:"parent_id"`
		State    string `json:"state"`
		Result   string `json:"result"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("await output not json: %v (%s)", err, text)
	}
	if out.TaskID != task.TaskID || out.ParentID != "parent-1" || out.State != core.StateDone {
		t.Fatalf("unexpected await result: %+v", out)
	}
	if !strings.Contains(out.Result, "test result output") {
		t.Fatalf("missing result content in: %s", out.Result)
	}
}
