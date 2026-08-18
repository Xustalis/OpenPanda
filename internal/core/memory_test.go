package core

import (
	"os"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// TestWithProjectMemory verifies the isolation wall (design §17.2) at the core
// execution layer: a project's agent prompt is prepended with that project's
// own memory only, never Hermes memory.
func TestWithProjectMemory(t *testing.T) {
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	projects := memory.NewProjects(root)

	// Distinct markers to prove the two stores cannot cross the wall.
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"HERMES-SECRET: dark theme"}}); err != nil {
		t.Fatalf("save hermes: %v", err)
	}
	if err := projects.Save("panda", memory.MemFile{Entries: []string{"PROJECT-MEM: Go core + Python glue"}}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	intent := "refactor the router"

	// No memory configured: the intent passes through unchanged.
	c := &Core{logger: testLogger()}
	if got := withProjectMemory(c, intent, "panda"); got != intent {
		t.Errorf("nil memory should leave intent unchanged, got %q", got)
	}

	// Memory configured: project memory is prepended, Hermes memory is not.
	c.memory = memory.NewInjector(hermes, projects)
	got := withProjectMemory(c, intent, "panda")
	if !strings.Contains(got, "PROJECT-MEM") {
		t.Errorf("prompt should contain project memory, got %q", got)
	}
	if strings.Contains(got, "HERMES-SECRET") {
		t.Errorf("prompt must not leak Hermes memory, got %q", got)
	}
	if !strings.Contains(got, intent) {
		t.Errorf("prompt should still contain the intent, got %q", got)
	}

	// Empty project: no project memory to pack, so the intent is unchanged.
	if got := withProjectMemory(c, intent, ""); got != intent {
		t.Errorf("empty project should leave intent unchanged, got %q", got)
	}
}

// TestWithSkills verifies progressive skill loading at the core layer: a
// matched active skill's body is prepended to the intent, and project-scoped
// skills never leak into a different project (design §8.5).
func TestWithSkills(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	global := &skills.Skill{Name: "deploy-panda", Description: "Deploy to Orange Pi", Scope: skills.ScopeGlobal, Status: skills.StatusActive, Body: "run deploy.sh"}
	proj := &skills.Skill{Name: "build-panda", Description: "Build the Go core", Scope: skills.ScopeProject, Project: "panda", Status: skills.StatusActive, Body: "make build"}
	if err := store.Save(global); err != nil {
		t.Fatalf("save global: %v", err)
	}
	if err := store.Save(proj); err != nil {
		t.Fatalf("save project: %v", err)
	}

	c := &Core{logger: testLogger(), skills: store}

	// A deploy title matches the global skill.
	got, used := withSkills(c, "deploy the release", "other-project", "deploy")
	if !strings.Contains(got, "deploy-panda") || len(used) != 1 {
		t.Errorf("should match global skill, got %q used=%d", got, len(used))
	}
	// A build title in the wrong project must not surface the panda skill.
	got, _ = withSkills(c, "build the core", "other-project", "build")
	if strings.Contains(got, "build-panda") {
		t.Errorf("project skill leaked into another project: %q", got)
	}
	// The right project does surface it.
	got, _ = withSkills(c, "build the core", "panda", "build")
	if !strings.Contains(got, "build-panda") {
		t.Errorf("project skill should match its own project, got %q", got)
	}
	// No skill store: intent unchanged.
	if got, _ := withSkills(&Core{logger: testLogger()}, "anything", "panda", "x"); got != "anything" {
		t.Errorf("nil skills should leave intent unchanged, got %q", got)
	}
}

// TestLogTask verifies that a completed task writes a daily-log line feeding
// the Dreaming engine.
func TestLogTask(t *testing.T) {
	root := t.TempDir()
	daily := memory.NewDaily(root + "/daily")
	c := &Core{logger: testLogger(), daily: daily}
	c.logTask("deploy panda", true)

	entries, err := os.ReadDir(root + "/daily")
	if err != nil {
		t.Fatalf("list daily: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("logTask should write a daily log line")
	}
}
