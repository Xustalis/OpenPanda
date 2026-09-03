package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// projectCore builds a Core with the project plane attached: a projects table, a
// memory root, and an artifact pool.
func projectCore(t *testing.T) (*Core, *projects.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	c := &Core{db: db, nodeID: "node-a", logger: testLogger()}
	c.store = NewTaskStore(db, testLogger())
	c.workDir = filepath.Join(dir, "work")
	c.artifacts = artifact.NewStore(filepath.Join(dir, "pool"))
	ps := projects.NewStore(db)
	root := filepath.Join(dir, "projects")
	c.SetProjectStores(ps, root)
	return c, ps, root
}

// TestAttachProjectCarriesMemoryAndTree is the fix for the complaint that a task
// sent to another machine arrives not knowing what it is working on: the payload
// must carry the project's memory inline and its tree as a pullable reference.
func TestAttachProjectCarriesMemoryAndTree(t *testing.T) {
	c, ps, root := projectCore(t)
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed tree: %v", err)
	}
	if _, err := ps.Create("demo", tree, "d"); err != nil {
		t.Fatalf("create project: %v", err)
	}
	memDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("§ PROJECT-FACT: uses Go"), 0o644); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	p := bus.TaskDelegatePayload{TaskID: "t-1", Project: "demo"}
	c.attachProject(context.Background(), &p, "demo")

	if len(p.ProjectPack) == 0 {
		t.Fatal("project memory did not travel with the delegation")
	}
	if len(p.ProjectPack) > bus.MaxProjectPackBytes {
		t.Fatalf("pack is %d bytes, over the wire cap", len(p.ProjectPack))
	}
	if p.ProjectDir != tree {
		t.Errorf("project dir = %q, want %q", p.ProjectDir, tree)
	}
	if len(p.Inputs) != 1 {
		t.Fatalf("inputs = %+v, want the project tree", p.Inputs)
	}
	ref := p.Inputs[0]
	if ref.Hash == "" || ref.Source != "node-a" || ref.Stage != projectArtifactStage {
		t.Fatalf("input ref = %+v, want a hash held by node-a labelled as the project", ref)
	}
	// The bytes stay with the sender; the reference is how the executor pulls them.
	if _, held := c.artifacts.Has(ref.Hash); !held {
		t.Fatal("project tree was not packed into the local pool")
	}
}

// TestAttachProjectWithoutAProjectIsANoop keeps the common path free: a task that
// belongs to no project must not grow a payload.
func TestAttachProjectWithoutAProjectIsANoop(t *testing.T) {
	c, _, _ := projectCore(t)
	p := bus.TaskDelegatePayload{TaskID: "t-1"}
	c.attachProject(context.Background(), &p, "")
	if len(p.ProjectPack) != 0 || p.ProjectDir != "" || len(p.Inputs) != 0 {
		t.Fatalf("payload grew for a project-less task: %+v", p)
	}
}

// TestLandProjectPackWritesLocalMemory is the receiving half: the memory must land
// where a local task would read it, and the project must become visible locally.
func TestLandProjectPackWritesLocalMemory(t *testing.T) {
	sender, ps, root := projectCore(t)
	memDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("§ PROJECT-FACT: uses Go"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ps.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	pack, err := sender.packProjectMemory("demo")
	if err != nil || len(pack) == 0 {
		t.Fatalf("pack = %d bytes, err %v", len(pack), err)
	}

	receiver, rps, rroot := projectCore(t)
	receiver.landProjectPack("demo", pack)

	landed := filepath.Join(rroot, "demo", "MEMORY.md")
	body, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("memory did not land at %s: %v", landed, err)
	}
	if !strings.Contains(string(body), "PROJECT-FACT") {
		t.Fatalf("landed memory = %q", body)
	}
	// The executor adopts the project so its own listings show the work it is doing.
	if _, err := rps.Get("demo"); err != nil {
		t.Fatalf("delegated project was not adopted locally: %v", err)
	}
}

// TestLandProjectPackRejectsPathEscape: the project name arrives over the bus and
// becomes a path segment, so a peer must not be able to aim the write elsewhere.
func TestLandProjectPackRejectsPathEscape(t *testing.T) {
	c, _, root := projectCore(t)
	for _, name := range []string{"../escape", "a/b", "..", ".", ""} {
		c.landProjectPack(name, []byte("not a real pack"))
	}
	// Nothing outside the projects root may appear, and no directory named by a
	// traversal may be created inside it either.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("a delegated project name escaped the projects root")
	}
}

// TestProjectWorkDirPrefersTheLocalTree: a node that has the project configured
// works in the user's own tree; one that does not derives a contained directory
// rather than trusting the path the sender named.
func TestProjectWorkDirPrefersTheLocalTree(t *testing.T) {
	c, ps, _ := projectCore(t)
	own := t.TempDir()
	if _, err := ps.Create("mine", own, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if dir, err := c.projectWorkDir("mine"); err != nil || dir != own {
		t.Fatalf("work dir = %q, %v; want the local tree %q", dir, err, own)
	}

	dir, err := c.projectWorkDir("theirs")
	if err != nil {
		t.Fatalf("derive work dir: %v", err)
	}
	if !strings.HasPrefix(dir, filepath.Clean(c.workDir)+string(os.PathSeparator)) {
		t.Fatalf("derived work dir %q escaped the node work dir %q", dir, c.workDir)
	}
	if _, err := c.projectWorkDir("../escape"); err == nil {
		t.Fatal("a traversing project name produced a work dir")
	}
}

// TestProjectInputsOnlyClaimsStandaloneTasks: a plan stage's inputs belong to the
// plan path, which handles them already.
func TestProjectInputsOnlyClaimsStandaloneTasks(t *testing.T) {
	refs := []bus.ArtifactRef{{Hash: "h"}}
	if !projectInputs(Task{Project: "demo", Inputs: refs}) {
		t.Error("a standalone project task with inputs should claim the project path")
	}
	if projectInputs(Task{Project: "demo", PlanID: "p", Inputs: refs}) {
		t.Error("a plan stage must stay on the stage path")
	}
	if projectInputs(Task{Project: "demo"}) {
		t.Error("no inputs means nothing to pull")
	}
	if projectInputs(Task{Inputs: refs}) {
		t.Error("no project means this is not a project pull")
	}
}

// TestAttachProjectRecordsContextDegraded verifies that if packing the project's
// work tree fails (e.g. the configured directory does not exist), an EvContextDegraded
// event is recorded on the task row so callers and UI can observe the degradation.
func TestAttachProjectRecordsContextDegraded(t *testing.T) {
	c, ps, _ := projectCore(t)
	badDir := filepath.Join(t.TempDir(), "nonexistent")
	if _, err := ps.Create("broken", badDir, "d"); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "task-degraded-test"
	if _, err := c.store.CreateWithID(context.Background(), taskID, "", "broken", "title", c.nodeID, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}

	p := bus.TaskDelegatePayload{TaskID: taskID, Project: "broken"}
	c.attachProject(context.Background(), &p, "broken")

	events, err := c.store.Events(context.Background(), taskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == EvContextDegraded {
			found = true
			if !strings.Contains(ev.DataJSON, "pack_tree") {
				t.Fatalf("event data = %s, want pack_tree stage", ev.DataJSON)
			}
			break
		}
	}
	if !found {
		t.Fatalf("no %s event recorded on task after tree pack failure; events: %+v", EvContextDegraded, events)
	}
}

// TestLandProjectPackRecordsContextDegraded verifies that when a delegated project pack
// cannot be extracted (corrupted pack payload), handleDelegate records an EvContextDegraded
// event with stage "land_memory".
func TestLandProjectPackRecordsContextDegraded(t *testing.T) {
	c, _, _ := projectCore(t)
	ctx := context.Background()

	taskID := "task-land-degraded"
	payload := bus.TaskDelegatePayload{
		TaskID:      taskID,
		Project:     "demo",
		Title:       "test task",
		ProjectPack: []byte("invalid non-tar corrupted data"),
	}

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "source-node", "msg-1", payload)
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}

	c.handleDelegate(ctx, env)

	events, err := c.store.Events(ctx, taskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == EvContextDegraded {
			found = true
			if !strings.Contains(ev.DataJSON, "land_memory") {
				t.Fatalf("event data = %s, want land_memory stage", ev.DataJSON)
			}
			break
		}
	}
	if !found {
		t.Fatalf("no %s event recorded after corrupted landProjectPack; events: %+v", EvContextDegraded, events)
	}
}
