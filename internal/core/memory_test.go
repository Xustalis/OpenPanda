// SPDX-License-Identifier: AGPL-3.0-or-later

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
	got, used := buildAgentPrompt(c, intent, "panda", "refactor", "", 0)
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

	got, _ := buildAgentPrompt(c, "fix the build", "", "fix build", "", 0)
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
	got, _ = buildAgentPrompt(c, "fix the build", "panda", "fix build", "/tmp/panda-tree", 0)
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
	// A degraded line is discovery, not a use: it must not bump use_count or
	// refresh LastUsed for a procedure whose steps never reached the agent.
	got, used := withSkills(c, "run the playbook", "", "playbook", 100)
	if len(used) != 0 {
		t.Fatalf("used = %d, want 0 (degraded index lines are not skill uses)", len(used))
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

// Chinese task titles reach curated bilingual triggers, and a device-scoped
// skill is injected only on the device that owns it. Both were invisible to
// the old title-only, global+project matcher.
func TestWithSkillsChineseTriggerAndDeviceScope(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	global := &skills.Skill{
		Name: "root-cause-debugging", Description: "Systematic root cause analysis",
		Scope: skills.ScopeGlobal, Status: skills.StatusActive,
		Triggers: []string{"debug", "排查", "根因"}, Body: "REPRODUCE-MARKER",
	}
	device := &skills.Skill{
		Name: "node-build", Description: "Device build procedure",
		Scope: skills.ScopeDevice, Device: "node-a", Status: skills.StatusActive,
		Triggers: []string{"build", "编译"}, Body: "DEVICE-BUILD-MARKER",
	}
	other := &skills.Skill{
		Name: "other-node-build", Description: "Another device build procedure",
		Scope: skills.ScopeDevice, Device: "node-b", Status: skills.StatusActive,
		Triggers: []string{"build", "编译"}, Body: "OTHER-DEVICE-MARKER",
	}
	for _, sk := range []*skills.Skill{global, device, other} {
		if err := store.Save(sk); err != nil {
			t.Fatalf("save %s: %v", sk.Name, err)
		}
	}
	c := &Core{logger: testLogger(), skills: store, nodeID: "node-a"}

	got, _ := withSkills(c, "排查登录失败的根因", "", "排查登录失败的根因", agentPromptBudget)
	if !strings.Contains(got, "REPRODUCE-MARKER") {
		t.Errorf("Chinese trigger did not load the debugging procedure: %q", got)
	}
	if strings.Contains(got, "DEVICE-BUILD-MARKER") {
		t.Errorf("unrelated device skill injected: %q", got)
	}

	got, _ = withSkills(c, "编译构建失败", "", "编译构建失败", agentPromptBudget)
	if !strings.Contains(got, "DEVICE-BUILD-MARKER") {
		t.Errorf("this device's skill was not injected: %q", got)
	}
	if strings.Contains(got, "OTHER-DEVICE-MARKER") {
		t.Errorf("another device's skill leaked: %q", got)
	}
	if strings.Contains(got, "REPRODUCE-MARKER") {
		t.Errorf("unrelated global skill injected: %q", got)
	}

	// The capability card's device label is accepted too: a user usually
	// types that name into --device, not the derived runtime node ID.
	labeled := &skills.Skill{
		Name: "labeled-device-build", Description: "Labelled device build",
		Scope: skills.ScopeDevice, Device: "device-label", Status: skills.StatusActive,
		Triggers: []string{"编译"}, Body: "LABELED-DEVICE-MARKER",
	}
	if err := store.Save(labeled); err != nil {
		t.Fatalf("save labeled: %v", err)
	}
	c2 := &Core{logger: testLogger(), skills: store, nodeID: "runtime-id"}
	c2.card.Device = "device-label"
	got, _ = withSkills(c2, "编译构建失败", "", "编译构建失败", agentPromptBudget)
	if !strings.Contains(got, "LABELED-DEVICE-MARKER") {
		t.Errorf("card device label did not reach the skill: %q", got)
	}
	if strings.Contains(got, "DEVICE-BUILD-MARKER") {
		t.Errorf("a different node's device skill leaked into this prompt: %q", got)
	}
}

// An always-on procedure rides regardless of the title, and still counts as a
// use because its full body reached the prompt.
func TestWithSkillsAlwaysOn(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	sk := &skills.Skill{
		Name: "standing-rule", Description: "Standing verification rule",
		Scope: skills.ScopeGlobal, Status: skills.StatusActive,
		Always: true, Body: "ALWAYS-MARKER",
	}
	if err := store.Save(sk); err != nil {
		t.Fatalf("save: %v", err)
	}
	c := &Core{logger: testLogger(), skills: store}
	got, used := withSkills(c, "ordinary unrelated title", "", "ordinary unrelated title", agentPromptBudget)
	if !strings.Contains(got, "ALWAYS-MARKER") {
		t.Errorf("always-on skill missing from prompt: %q", got)
	}
	if len(used) != 1 || used[0].Name != "standing-rule" {
		t.Errorf("used = %v, want the always-on skill", used)
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

// TestBuildAgentPromptBudgetAccommodatesLargeChineseIntent verifies that a multi-kilobyte
// Chinese intent does not prematurely degrade skill bodies to index lines under the 128KB budget.
func TestBuildAgentPromptBudgetAccommodatesLargeChineseIntent(t *testing.T) {
	root := t.TempDir()
	store := skills.NewStore(filepath.Join(root, "skills"))
	sk := &skills.Skill{
		Name:        "k8s-deploy",
		Description: "Kubernetes 部署规范",
		Scope:       skills.ScopeGlobal,
		Status:      skills.StatusActive,
		Body:        "## 详细部署指南\n1. 检查集群状态\n2. 应用 yaml 配置\n3. 验证 Pod 存活探针\n",
	}
	if err := store.Save(sk); err != nil {
		t.Fatalf("save: %v", err)
	}
	c := &Core{logger: testLogger(), skills: store}

	// Large Chinese intent: ~30,000 bytes
	chineseIntent := strings.Repeat("请按照生产环境发布规范严格执行部署流程，确保零停机时间与监控告警正常。", 300)
	prompt, used := buildAgentPrompt(c, chineseIntent, "", "k8s-deploy", "", 0)
	if len(used) != 1 {
		t.Fatalf("used = %d, want 1", len(used))
	}
	if !strings.Contains(prompt, "详细部署指南") {
		t.Fatalf("skill body was prematurely degraded into an index line under Chinese intent")
	}
	if !strings.Contains(prompt, "验证 Pod 存活探针") {
		t.Fatalf("skill body content missing: %s", prompt)
	}
}

func TestProjectTaskHistoryStaysOutsidePersonalDaily(t *testing.T) {
	root := t.TempDir()
	daily := memory.NewDaily(filepath.Join(root, "personal", "daily"))
	projectsRoot := filepath.Join(root, "projects")
	c := &Core{logger: testLogger(), daily: daily, projectsRoot: projectsRoot}
	c.logProjectTask("deploy panda", true, "alpha")
	if _, err := os.Stat(filepath.Join(root, "personal", "daily")); !os.IsNotExist(err) {
		t.Fatal("project task touched personal daily")
	}
	files, err := filepath.Glob(filepath.Join(projectsRoot, "alpha", "daily", "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("project history: %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(data), memory.ExternalMarker) || !strings.Contains(string(data), "deploy panda") {
		t.Fatalf("task history: %s %v", data, err)
	}
}
