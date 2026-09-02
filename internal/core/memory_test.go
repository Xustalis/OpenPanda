package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// TestAgentPromptOmitsProjectMemory verifies the A1 decision at the core
// execution layer: the project's MEMORY.md content is never packed into the
// agent prompt (token savings). The prompt may point at the file — that is the
// manifest — but the entries themselves stay on disk until the agent reads them.
func TestAgentPromptOmitsProjectMemory(t *testing.T) {
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	projects := memory.NewProjects(root)

	// Distinct markers to prove neither memory class reaches the prompt.
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"HERMES-SECRET: dark theme"}}); err != nil {
		t.Fatalf("save hermes: %v", err)
	}
	if err := projects.Save("panda", memory.MemFile{Entries: []string{"PROJECT-MEM: Go core + Python glue"}}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	intent := "refactor the router"

	// Memory configured: the agent prompt must NOT contain project memory
	// (nor Hermes memory), only the intent itself.
	c := &Core{logger: testLogger()}
	c.memory = memory.NewInjector(hermes, projects)
	got, used := buildAgentPrompt(c, intent, "panda", "refactor", "")
	if strings.Contains(got, "PROJECT-MEM") {
		t.Errorf("agent prompt must no longer contain project memory, got %q", got)
	}
	if strings.Contains(got, "HERMES-SECRET") {
		t.Errorf("prompt must not leak Hermes memory, got %q", got)
	}
	if !strings.Contains(got, intent) {
		t.Errorf("prompt should still contain the intent, got %q", got)
	}
	if len(used) != 0 {
		t.Errorf("no skills configured, used = %d", len(used))
	}
}

// TestAgentPromptCarriesMemoryManifest verifies the A3 replacement path at
// the core execution layer: outside a project the agent prompt carries the
// memory file manifest (paths + summaries, fenced as data) instead of the
// memory content itself; inside a project nothing memory-related is injected.
func TestAgentPromptCarriesMemoryManifest(t *testing.T) {
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	projects := memory.NewProjects(root)
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"HERMES-FACT: dark theme", "HERMES-SECOND: standing desk"}}); err != nil {
		t.Fatalf("save hermes: %v", err)
	}

	c := &Core{logger: testLogger()}
	c.memory = memory.NewInjector(hermes, projects)

	got, _ := buildAgentPrompt(c, "fix the build", "", "fix build", "")
	if !strings.Contains(got, "记忆文件清单") || !strings.Contains(got, "MEMORY.md") {
		t.Errorf("manifest missing from non-project prompt: %q", got)
	}
	if !strings.Contains(got, filepath.Join(root, "MEMORY.md")) {
		t.Errorf("manifest must carry the absolute file path: %q", got)
	}
	// Manifest mode lists files, it does not dump their entries.
	if strings.Contains(got, "HERMES-SECOND") {
		t.Errorf("prompt must not dump full memory content: %q", got)
	}

	// Inside a project the wall still holds — the personal-memory manifest must
	// not appear — but the project gets a manifest of its own now. A project task
	// used to receive strictly less context than a loose one, which is what left a
	// delegated project task unable to tell what it was working on.
	got, _ = buildAgentPrompt(c, "fix the build", "panda", "fix build", "/tmp/panda-tree")
	if strings.Contains(got, "记忆文件清单") {
		t.Errorf("project prompt must not carry the personal-memory manifest: %q", got)
	}
	if strings.Contains(got, "HERMES-FACT") {
		t.Errorf("project prompt must not leak Hermes memory: %q", got)
	}
	if !strings.Contains(got, "当前项目：panda") {
		t.Errorf("project prompt should name its project: %q", got)
	}
	if !strings.Contains(got, "/tmp/panda-tree") {
		t.Errorf("project prompt should name the work dir it runs in: %q", got)
	}
	if !strings.Contains(got, filepath.Join(root, "panda", "MEMORY.md")) {
		t.Errorf("project prompt should point at the project memory file: %q", got)
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
	got, used := withSkills(c, "deploy the release", "other-project", "deploy", agentPromptBudget)
	if !strings.Contains(got, "deploy-panda") || len(used) != 1 {
		t.Errorf("should match global skill, got %q used=%d", got, len(used))
	}
	// A build title in the wrong project must not surface the panda skill.
	got, _ = withSkills(c, "build the core", "other-project", "build", agentPromptBudget)
	if strings.Contains(got, "build-panda") {
		t.Errorf("project skill leaked into another project: %q", got)
	}
	// The right project does surface it.
	got, _ = withSkills(c, "build the core", "panda", "build", agentPromptBudget)
	if !strings.Contains(got, "build-panda") {
		t.Errorf("project skill should match its own project, got %q", got)
	}
	// No skill store: intent unchanged.
	if got, _ := withSkills(&Core{logger: testLogger()}, "anything", "panda", "x", agentPromptBudget); got != "anything" {
		t.Errorf("nil skills should leave intent unchanged, got %q", got)
	}
}

// TestWithSkillsBudgetDegrades verifies the prompt-budget guard: a skill body
// that does not fit the remaining budget degrades to an index line (name +
// description) instead of being dropped or overflowing the agent's window.
// A budget that fits keeps the full body.
func TestWithSkillsBudgetDegrades(t *testing.T) {
	root := t.TempDir()
	store := skills.NewStore(filepath.Join(root, "skills"))
	big := &skills.Skill{Name: "big-skill", Description: "A large playbook", Scope: skills.ScopeGlobal, Status: skills.StatusActive, Body: strings.Repeat("playbook line\n", 500)}
	if err := store.Save(big); err != nil {
		t.Fatalf("save: %v", err)
	}
	c := &Core{logger: testLogger(), skills: store}

	// Tight budget: the body degrades to an index line but stays visible.
	got, used := withSkills(c, "run the playbook", "", "playbook", 100)
	if len(used) != 1 {
		t.Fatalf("used = %d, want 1", len(used))
	}
	if !strings.Contains(got, "big-skill") {
		t.Fatalf("degraded skill lost its name: %q", got)
	}
	if strings.Contains(got, "playbook line\nplaybook line") {
		t.Fatalf("full body survived the budget: %d bytes", len(got))
	}
	if !strings.Contains(got, "A large playbook") {
		t.Fatalf("index line lost the description: %q", got)
	}

	// Roomy budget: the full body rides.
	got, _ = withSkills(c, "run the playbook", "", "playbook", agentPromptBudget)
	if !strings.Contains(got, "playbook line\nplaybook line") {
		t.Fatalf("roomy budget should keep the full body (%d bytes)", len(got))
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
