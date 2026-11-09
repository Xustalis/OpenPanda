// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleSkill() *Skill {
	return &Skill{
		Name:        "deploy-panda",
		Description: "Deploy PANDA binary to Orange Pi and wire systemd",
		Scope:       ScopeProject,
		Project:     "panda",
		Status:      StatusActive,
		UseCount:    3,
		Body:        "## Steps\n1. scp the binary\n2. restart systemd",
	}
}

func TestParseSkillBytesRoundTrip(t *testing.T) {
	sk := sampleSkill()
	data, err := sk.Bytes()
	if err != nil {
		t.Fatalf("bytes: %v", err)
	}
	got, err := ParseSkill(data)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, data)
	}
	if got.Name != sk.Name || got.Description != sk.Description ||
		got.Scope != sk.Scope || got.Project != sk.Project ||
		got.Status != sk.Status || got.UseCount != sk.UseCount {
		t.Errorf("frontmatter mismatch: %+v", got)
	}
	if got.Body != sk.Body {
		t.Errorf("body mismatch: %q", got.Body)
	}
}

func TestParseSkillValidation(t *testing.T) {
	if _, err := ParseSkill([]byte("no frontmatter here")); err == nil {
		t.Errorf("missing delimiter should error")
	}
	if _, err := ParseSkill([]byte("---\nname: x\n---\nbody")); err == nil {
		t.Errorf("missing description should error")
	}
}

func TestScopePaths(t *testing.T) {
	store := NewStore(t.TempDir())
	cases := []struct {
		sk   *Skill
		want string
	}{
		{&Skill{Name: "g", Scope: ScopeGlobal}, "global/g/SKILL.md"},
		{&Skill{Name: "p", Scope: ScopeProject, Project: "x"}, "project/x/p/SKILL.md"},
		{&Skill{Name: "d", Scope: ScopeDevice, Device: "pi"}, "device/pi/d/SKILL.md"},
	}
	for _, c := range cases {
		path, err := store.Path(c.sk)
		if err != nil {
			t.Fatalf("path: %v", err)
		}
		if !strings.HasSuffix(filepath.ToSlash(path), c.want) {
			t.Errorf("path = %q, want suffix %q", path, c.want)
		}
	}
}

func TestStoreSaveLoad(t *testing.T) {
	store := NewStore(t.TempDir())
	sk := sampleSkill()
	if err := store.Save(sk); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load(ScopeProject, "panda", "deploy-panda")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.Name != "deploy-panda" || got.Body != sk.Body {
		t.Fatalf("load mismatch: %+v", got)
	}
	// A missing skill is nil, not an error.
	missing, err := store.Load(ScopeProject, "panda", "nope")
	if err != nil || missing != nil {
		t.Errorf("missing skill: got %v err=%v, want nil nil", missing, err)
	}
}

// TestSaveEmptyRoot pins the cwd-pollution fix: NewStore("") used to turn
// filepath.Join into a relative path and write global/<name>/SKILL.md into
// whatever directory the process ran from.
func TestSaveEmptyRoot(t *testing.T) {
	store := NewStore("")
	if err := store.Save(sampleSkill()); err == nil {
		t.Fatal("Save on an empty root must error instead of writing relative to cwd")
	}
	if err := store.EnsureBuiltins(); err == nil {
		t.Fatal("EnsureBuiltins on an empty root must error")
	}
	if _, err := os.Stat("global"); err == nil {
		t.Fatal("a refused write must not leave a global/ directory behind")
	}
}

func TestStoreIndexAndMatch(t *testing.T) {
	store := NewStore(t.TempDir())
	global := &Skill{Name: "git-workflow", Description: "Commit and PR workflow", Scope: ScopeGlobal, Status: StatusActive}
	proj := &Skill{Name: "deploy-panda", Description: "Deploy to Orange Pi", Scope: ScopeProject, Project: "panda", Status: StatusActive}
	other := &Skill{Name: "deploy-other", Description: "Deploy to other host", Scope: ScopeProject, Project: "other", Status: StatusActive}

	for _, sk := range []*Skill{global, proj, other} {
		if err := store.Save(sk); err != nil {
			t.Fatalf("save %s: %v", sk.Name, err)
		}
	}

	index, err := store.Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 3 {
		t.Fatalf("index has %d entries, want 3", len(index))
	}

	// Project-scoped matching must not leak other projects' skills.
	matched := Match(index, ScopeProject, "panda", "deploy")
	if len(matched) != 1 || matched[0].Name != "deploy-panda" {
		t.Errorf("project match = %v, want only deploy-panda", names(matched))
	}
	// Global skills match regardless of key.
	globalMatched := Match(index, ScopeGlobal, "", "workflow")
	if len(globalMatched) != 1 || globalMatched[0].Name != "git-workflow" {
		t.Errorf("global match = %v", names(globalMatched))
	}
}

func TestMatchWordBoundary(t *testing.T) {
	index := []IndexEntry{{Name: "deploy", Description: "Deploy workflow", Scope: ScopeGlobal, Status: StatusActive}}
	// "dep" is a substring of "deploy" but not a word — must not match.
	if got := Match(index, ScopeGlobal, "", "dep"); len(got) != 0 {
		t.Errorf("substring should not match: %v", names(got))
	}
	if got := Match(index, ScopeGlobal, "", "deploy"); len(got) != 1 {
		t.Errorf("whole word should match: %v", names(got))
	}
}

func TestMatchCJKOverlap(t *testing.T) {
	index := []IndexEntry{
		{Name: "deploy-panda", Description: "部署到香橙派", Scope: ScopeGlobal, Status: StatusActive},
		{Name: "git-workflow", Description: "Commit and PR workflow", Scope: ScopeGlobal, Status: StatusActive},
	}
	// Single-ideograph CJK overlap: "部署" matches the deploy skill even though
	// its description is a longer Chinese phrase (the old whole-word matcher
	// would treat "部署到香橙派" as one opaque token and miss it).
	got := Match(index, ScopeGlobal, "", "部署")
	if len(got) != 1 || got[0].Name != "deploy-panda" {
		t.Errorf("CJK match = %v, want only deploy-panda", names(got))
	}
}

func TestMatchRanksByRelevance(t *testing.T) {
	index := []IndexEntry{
		{Name: "git-workflow", Description: "Commit and PR workflow", Scope: ScopeGlobal, Status: StatusActive},
		{Name: "deploy-panda", Description: "Deploy to Orange Pi", Scope: ScopeGlobal, Status: StatusActive},
	}
	got := Match(index, ScopeGlobal, "", "deploy")
	if len(got) != 1 || got[0].Name != "deploy-panda" {
		t.Errorf("ranked match = %v, want deploy-panda", names(got))
	}
}

func TestScopeKeyRejectsTraversal(t *testing.T) {
	store := NewStore(t.TempDir())
	sk := &Skill{Name: "x", Description: "d", Scope: ScopeProject, Project: "../evil"}
	if err := store.Save(sk); err == nil {
		t.Errorf("traversal scope key should error on save")
	}
	if _, err := store.Load(ScopeProject, "../evil", "x"); err == nil {
		t.Errorf("traversal scope key should error on load")
	}
}

func TestMatchIgnoresStopwords(t *testing.T) {
	index := []IndexEntry{
		{Name: "deploy-panda", Description: "Deploy to Orange Pi", Scope: ScopeGlobal, Status: StatusActive},
	}
	// A query made only of Latin stopwords must not match a skill whose only
	// overlap is those stopwords.
	if got := Match(index, ScopeGlobal, "", "the and to"); len(got) != 0 {
		t.Errorf("stopword-only query matched: %v", names(got))
	}
	// The content token still matches once present.
	if got := Match(index, ScopeGlobal, "", "deploy the app"); len(got) != 1 || got[0].Name != "deploy-panda" {
		t.Errorf("content match = %v, want deploy-panda", names(got))
	}
}

func TestParseSkillTriggersAndAlwaysRoundTrip(t *testing.T) {
	sk := sampleSkill()
	sk.Triggers = []string{"deploy", "发布", "code review"}
	sk.Always = true
	data, err := sk.Bytes()
	if err != nil {
		t.Fatalf("bytes: %v", err)
	}
	got, err := ParseSkill(data)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, data)
	}
	if len(got.Triggers) != 3 || got.Triggers[1] != "发布" || !got.Always {
		t.Fatalf("trigger metadata lost: %+v", got)
	}
}

// A built-in SKILL.md seeded by an older binary has no triggers on disk.
// Index() must merge the curated table at read time so a reseed (which would
// discard the user's counters and edits) is never required.
func TestIndexMergesCuratedTriggers(t *testing.T) {
	store := NewStore(t.TempDir())
	sk := &Skill{
		Name:        "git-workflow",
		Description: "Standard Git branching, commit conventions, rebase workflow and pull request verification",
		Scope:       ScopeGlobal,
		Status:      StatusActive,
		Builtin:     true,
		UseCount:    7,
		Body:        "## Steps\n1. branch",
	}
	if err := store.Save(sk); err != nil {
		t.Fatalf("save: %v", err)
	}
	index, err := store.Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(index) != 1 || !index[0].Builtin || index[0].UseCount != 7 {
		t.Fatalf("index lost disk state: %+v", index)
	}
	if len(index[0].Triggers) == 0 {
		t.Fatal("built-in entry did not inherit curated triggers")
	}
	if got := Match(index, ScopeGlobal, "", "提交代码到分支"); len(got) != 1 || got[0].Name != "git-workflow" {
		t.Errorf("Chinese title cannot reach the built-in skill: %v", names(got))
	}
}

func TestMatchBilingualTriggers(t *testing.T) {
	index := []IndexEntry{
		{Name: "root-cause-debugging", Description: "Systematic root cause analysis", Scope: ScopeGlobal, Status: StatusActive,
			Triggers: []string{"debug", "排查", "复现", "根因"}},
		{Name: "docker-compose", Description: "Manage multi-container stacks", Scope: ScopeGlobal, Status: StatusActive,
			Triggers: []string{"docker", "容器"}},
	}
	got := Match(index, ScopeGlobal, "", "排查登录失败的根因")
	if len(got) != 1 || got[0].Name != "root-cause-debugging" {
		t.Errorf("bilingual trigger match = %v, want only root-cause-debugging", names(got))
	}
}

// Latin triggers must match whole words: "api" inside "capitalize" is not a
// retrieval signal.
func TestMatchTriggerWordBoundary(t *testing.T) {
	index := []IndexEntry{
		{Name: "api-debug", Description: "REST endpoints", Scope: ScopeGlobal, Status: StatusActive,
			Triggers: []string{"api", "endpoint"}},
	}
	if got := Match(index, ScopeGlobal, "", "capitalize the word"); len(got) != 0 {
		t.Errorf("substring fired a Latin trigger: %v", names(got))
	}
	if got := Match(index, ScopeGlobal, "", "call the api endpoint"); len(got) != 1 {
		t.Errorf("whole-word trigger did not fire: %v", names(got))
	}
}

// Device skills used to be visible to skill_list/skill_show but were never
// matched for injection. Scope keys are also case-insensitive here, matching
// the boundary used by the ask-engine tools.
func TestMatchScopesRanksAcrossScopesAndIncludesDevice(t *testing.T) {
	index := []IndexEntry{
		{Name: "global-generic", Description: "Generic notes", Scope: ScopeGlobal, Status: StatusActive,
			Triggers: []string{"build"}},
		{Name: "device-build", Description: "Device-specific build", Scope: ScopeDevice, Key: "node-a", Status: StatusActive,
			Triggers: []string{"build", "编译"}},
		{Name: "other-device-build", Description: "Another device build", Scope: ScopeDevice, Key: "node-b", Status: StatusActive,
			Triggers: []string{"build", "编译"}},
		{Name: "project-build", Description: "Project build", Scope: ScopeProject, Key: "Panda", Status: StatusActive,
			Triggers: []string{"build", "编译"}},
	}
	got := MatchScopes(index, "panda", []string{"node-a", "node-a@vm-1a2b3c4d"}, "编译构建", "")
	if len(got) != 2 {
		t.Fatalf("match = %v, want device + project only", names(got))
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.Name] = true
	}
	if !seen["device-build"] || !seen["project-build"] {
		t.Errorf("scope isolation/ranking broken: %v", names(got))
	}
	if seen["other-device-build"] || seen["global-generic"] {
		t.Errorf("other device's or unrelated global skill leaked: %v", names(got))
	}
}

func TestMatchScopesAcceptsAnyDeviceAlias(t *testing.T) {
	index := []IndexEntry{
		{Name: "vm-skill", Description: "VM build procedure", Scope: ScopeDevice,
			Key: "build-node@vm-1a2b3c4d", Status: StatusActive, Triggers: []string{"编译"}},
	}
	if got := MatchScopes(index, "", []string{"build-node", "build-node@vm-1a2b3c4d"}, "编译", ""); len(got) != 1 {
		t.Errorf("neither node alias reached the device skill: %v", names(got))
	}
	if got := MatchScopes(index, "", []string{"other-node"}, "编译", ""); len(got) != 0 {
		t.Errorf("another node's device skill leaked: %v", names(got))
	}
	index[0].Key = ""
	if got := MatchScopes(index, "", []string{"build-node"}, "编译", ""); len(got) != 0 {
		t.Errorf("a device skill with no label became globally visible: %v", names(got))
	}
}

func TestMatchDormantDeprioritized(t *testing.T) {
	index := []IndexEntry{
		{Name: "active-skill", Description: "build workflow", Scope: ScopeGlobal, Status: StatusActive},
		{Name: "dormant-skill", Description: "build workflow", Scope: ScopeGlobal, Status: StatusDormant, UseCount: 99},
	}
	got := Match(index, ScopeGlobal, "", "build")
	if len(got) != 2 || got[0].Name != "active-skill" {
		t.Errorf("dormant skill not deprioritized despite higher use count: %v", names(got))
	}
}

func TestMatchCapsResults(t *testing.T) {
	index := make([]IndexEntry, 0, 8)
	for i := 0; i < 8; i++ {
		index = append(index, IndexEntry{
			Name:        "skill-" + string(rune('a'+i)),
			Description: "Build procedure",
			Scope:       ScopeGlobal,
			Status:      StatusActive,
			Triggers:    []string{"build"},
		})
	}
	if got := Match(index, ScopeGlobal, "", "build"); len(got) != MaxSkillMatches {
		t.Errorf("match count = %d, want cap %d", len(got), MaxSkillMatches)
	}
}

func names(entries []IndexEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}
