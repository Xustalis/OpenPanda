// SPDX-License-Identifier: AGPL-3.0-or-later

package projects

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "test.db"))
	return NewStore(db)
}

func TestCreateGetList(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p, err := s.Create("panda-dev", dir, "the node itself")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.Name != "panda-dev" || p.Description != "the node itself" {
		t.Fatalf("created = %+v", p)
	}
	// The work dir is stored absolute so a task started from any cwd resolves to
	// the same tree.
	if !filepath.IsAbs(p.WorkDir) {
		t.Fatalf("work dir %q is not absolute", p.WorkDir)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", p)
	}
	if _, err := s.Create("panda-dev", "", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create err = %v, want ErrExists", err)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get err = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("other", "", ""); err != nil {
		t.Fatalf("create other: %v", err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v (%d), err %v", list, len(list), err)
	}
}

// TestCreateMakesWorkDir covers the promise `panda project new --dir` makes: the
// directory exists by the time the command returns. prepareRunDir would create
// it at first run anyway, but until then `project show` named a path that was
// not there and an isolate child degraded to a plain copy (gitRepoRootOf needs
// a real repository root) — a row and a filesystem that disagree.
func TestCreateMakesWorkDir(t *testing.T) {
	s := newTestStore(t)
	dir := filepath.Join(t.TempDir(), "nested", "feat-a")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("fixture directory already exists")
	}
	if _, err := s.Create("feat-a", dir, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("work dir not created: %v", err)
	}
	// An existing directory is adopted untouched: --dir routinely points at a
	// live checkout, and creating a project must not disturb its contents.
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("feat-b", dir, ""); err != nil {
		t.Fatalf("create on existing dir: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("existing content disturbed: %v", err)
	}
}

// TestActivePointer covers the reason this table exists: `panda ask` is one-shot,
// so "which project am I in" has to survive between two processes.
func TestActivePointer(t *testing.T) {
	s := newTestStore(t)
	if name, err := s.Active(); err != nil || name != "" {
		t.Fatalf("fresh active = %q, %v; want empty", name, err)
	}
	if err := s.SetActive("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("entering a project that does not exist: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if name, _ := s.Active(); name != "demo" {
		t.Fatalf("active = %q, want demo", name)
	}
	// Setting again must not conflict on the settings primary key.
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("re-set active: %v", err)
	}
	if err := s.ClearActive(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if name, _ := s.Active(); name != "" {
		t.Fatalf("active after clear = %q", name)
	}
	// Leaving when not in a project is already true, not an error.
	if err := s.ClearActive(); err != nil {
		t.Fatalf("clear twice: %v", err)
	}
}

// TestActiveForgetsADeletedProject: the pointer must not outlive its project, or
// every later "which project am I in" answers with one that is gone.
func TestActiveForgetsADeletedProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, "demo"); err != nil {
		t.Fatalf("raw delete: %v", err)
	}
	if name, err := s.Active(); err != nil || name != "" {
		t.Fatalf("active = %q, %v; want empty after the project vanished", name, err)
	}
}

// TestRenameCarriesActivePointer is the invariant that makes rename safe: a
// rename that moved the row but left the pointer behind would leave the user
// inside a project that no longer exists.
func TestRenameCarriesActivePointer(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("old", t.TempDir(), "d"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("old"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if _, err := s.Rename("old", "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if name, _ := s.Active(); name != "new" {
		t.Fatalf("active after rename = %q, want new", name)
	}
	if _, err := s.Get("old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still resolves: %v", err)
	}
	if _, err := s.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing = %v, want ErrNotFound", err)
	}
	if _, err := s.Create("taken", "", ""); err != nil {
		t.Fatalf("create taken: %v", err)
	}
	if _, err := s.Rename("new", "taken"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename onto existing = %v, want ErrExists", err)
	}
}

// TestDeleteLeavesTheTree pins the one thing this package must not do: removing a
// project is bookkeeping, and deleting the directory the user pointed at would be
// the only irreversible operation here.
func TestDeleteLeavesTheTree(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, err := s.Create("demo", dir, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive("demo"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if err := s.Delete("demo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("delete removed the work tree: %v", err)
	}
	if name, _ := s.Active(); name != "" {
		t.Fatalf("active still %q after deleting it", name)
	}
	if err := s.Delete("demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v, want ErrNotFound", err)
	}
}

// TestEnsureFromName covers adoption: projects existed as bare memory files
// before this table, so a name from a task or from projects/<name>/ must still
// resolve to a project.
func TestEnsureFromName(t *testing.T) {
	s := newTestStore(t)
	p, err := s.EnsureFromName("legacy")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if p.Name != "legacy" || p.WorkDir != "" {
		t.Fatalf("adopted = %+v, want name legacy and no work dir", p)
	}
	again, err := s.EnsureFromName("legacy")
	if err != nil || !again.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("ensure is not idempotent: %+v vs %+v (err %v)", again, p, err)
	}
}

func TestUpdateWorkDirAndDescription(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("demo", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := t.TempDir()
	p, err := s.Update("demo", dir, "now with a tree")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !filepath.IsAbs(p.WorkDir) || p.Description != "now with a tree" {
		t.Fatalf("updated = %+v", p)
	}
	// An empty work dir clears it: that is how a project gives up its tree.
	if p, err = s.Update("demo", "", ""); err != nil || p.WorkDir != "" {
		t.Fatalf("cleared = %+v, err %v", p, err)
	}
	if _, err := s.Update("nope", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}
}

func TestValidateName(t *testing.T) {
	bad := []string{"", "   ", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", "a\"b", "a<b", "a>b", "a|b",
		"x\x00y", string(make([]byte, 65))}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
	for _, n := range []string{"panda-dev", "项目 一", "a.b", "A_B-1"} {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
}

func TestFindByWorkDir(t *testing.T) {
	s := newTestStore(t)
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	if _, err := s.Create("proj1", dir1, "desc 1"); err != nil {
		t.Fatalf("create proj1: %v", err)
	}
	if _, err := s.Create("proj2", dir2, "desc 2"); err != nil {
		t.Fatalf("create proj2: %v", err)
	}

	p1, err := s.FindByWorkDir(dir1)
	if err != nil || p1.Name != "proj1" {
		t.Fatalf("FindByWorkDir(dir1) = %+v, err %v", p1, err)
	}

	p2, err := s.FindByWorkDir(dir2)
	if err != nil || p2.Name != "proj2" {
		t.Fatalf("FindByWorkDir(dir2) = %+v, err %v", p2, err)
	}

	if _, err := s.FindByWorkDir(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByWorkDir unknown = %v, want ErrNotFound", err)
	}
}

// TestApprovalPolicyFields covers the per-project tier-2 policy the gate
// resolves: mode/scope/decision round-trip through the row, validation rejects
// typos instead of silently degrading them, and an unset scope normalizes to
// session rather than defaulting a remember onto the project row itself.
func TestApprovalPolicyFields(t *testing.T) {
	s := newTestStore(t)
	p, err := s.Create("gate", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ApprovalMode != "" || p.ApprovalScope != "" || p.ApprovalDecision != "" {
		t.Fatalf("fresh project carries approval state: %+v", p)
	}
	if p.NormalizedScope() != ScopeSession {
		t.Fatalf("unset scope normalized to %q, want session", p.NormalizedScope())
	}

	if err := s.SetApprovalPolicy("gate", "bogus", ScopeSession); err == nil {
		t.Fatal("SetApprovalPolicy accepted an invalid mode")
	}
	if err := s.SetApprovalPolicy("gate", "on-request", "bogus"); err == nil {
		t.Fatal("SetApprovalPolicy accepted an invalid scope")
	}
	if err := s.SetApprovalPolicy("gate", "always", ScopeProject); err != nil {
		t.Fatalf("SetApprovalPolicy: %v", err)
	}
	p, err = s.Get("gate")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.ApprovalMode != "always" || p.ApprovalScope != ScopeProject {
		t.Fatalf("policy = mode %q scope %q, want always/project", p.ApprovalMode, p.ApprovalScope)
	}

	if err := s.SetApprovalDecision("gate", "bogus"); err == nil {
		t.Fatal("SetApprovalDecision accepted an invalid decision")
	}
	if err := s.SetApprovalDecision("gate", DecisionApprove); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}
	p, _ = s.Get("gate")
	if p.ApprovalDecision != DecisionApprove {
		t.Fatalf("decision = %q, want approve", p.ApprovalDecision)
	}
	if err := s.SetApprovalDecision("gate", ""); err != nil {
		t.Fatalf("clear decision: %v", err)
	}
	p, _ = s.Get("gate")
	if p.ApprovalDecision != "" {
		t.Fatalf("cleared decision = %q", p.ApprovalDecision)
	}

	// A rename must carry the policy — the decision follows the project
	// identity, not a stale name.
	if _, err := s.Rename("gate", "gate2"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	p, err = s.Get("gate2")
	if err != nil {
		t.Fatalf("get renamed: %v", err)
	}
	if p.ApprovalMode != "always" || p.ApprovalScope != ScopeProject {
		t.Fatalf("renamed project lost policy: %+v", p)
	}
}

// TestApprovalPolicyAdoptsMemoryOnlyProject covers the pre-migration shape: a
// project that exists only as a memory file gets a row the first time its
// policy is touched, so remembering never fails for want of a Create call.
func TestApprovalPolicyAdoptsMemoryOnlyProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetApprovalPolicy("legacy", "never", ScopeOnce); err != nil {
		t.Fatalf("policy on memory-only project: %v", err)
	}
	p, err := s.Get("legacy")
	if err != nil {
		t.Fatalf("adopted project missing: %v", err)
	}
	if p.ApprovalMode != "never" || p.NormalizedScope() != ScopeOnce {
		t.Fatalf("adopted policy = %+v", p)
	}
}

// TestValidateWorkDir pins the breadth rule the web console relies on: a work
// dir widens /api/fs/read's allow-list, so "the whole machine" must not be
// expressible as one — while an ordinary project tree still is.
func TestValidateWorkDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to test against")
	}
	rejected := []string{
		// The volume root: "/" on unix, "C:\" on Windows. The literal "/" is
		// refused there as "not absolute", which tests the wrong branch.
		filepath.VolumeName(home) + string(filepath.Separator),
		home,
		filepath.Dir(home),
	}
	if runtime.GOOS != "windows" {
		// System trees are a unix list; Windows has no equivalent here.
		rejected = append(rejected, "/etc", "/usr")
	}
	for _, dir := range rejected {
		if err := ValidateWorkDir(dir); !errors.Is(err, ErrWorkDirTooBroad) {
			t.Errorf("ValidateWorkDir(%q) = %v, want ErrWorkDirTooBroad", dir, err)
		}
	}
	// A normal project tree passes, and so does one that does not exist yet:
	// a project may be created before its directory.
	if err := ValidateWorkDir(t.TempDir()); err != nil {
		t.Errorf("temp project dir rejected: %v", err)
	}
	if err := ValidateWorkDir(filepath.Join(t.TempDir(), "not-created-yet")); err != nil {
		t.Errorf("not-yet-created dir rejected: %v", err)
	}
	if err := ValidateWorkDir("relative/dir"); err == nil {
		t.Error("a relative work dir must be refused")
	}
	if err := ValidateWorkDir(""); err != nil {
		t.Errorf("empty work dir means 'no tree of its own': %v", err)
	}
}

// TestValidateWorkDirSymlinkToRoot pins the resolved-path half of the rule: a
// symlink is an ordinary directory by its own path, so the breadth check on the
// path as written passes it; reads through it still reach the whole disk.
func TestValidateWorkDirSymlinkToRoot(t *testing.T) {
	link := filepath.Join(t.TempDir(), "rootlink")
	if err := os.Symlink("/", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ValidateWorkDir(link); !errors.Is(err, ErrWorkDirTooBroad) {
		t.Fatalf("ValidateWorkDir(symlink to /) = %v, want ErrWorkDirTooBroad", err)
	}
}

// TestCreateRefusesTooBroadWorkDir pins the enforcement point: the store is the
// writer every caller (CLI, panel, bus) goes through, so the rule has to hold
// here, not only in the web handler that happened to be reported.
func TestCreateRefusesTooBroadWorkDir(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("wide", "/", ""); !errors.Is(err, ErrWorkDirTooBroad) {
		t.Fatalf("create with / = %v, want ErrWorkDirTooBroad", err)
	}
	if _, err := s.Create("ok", t.TempDir(), ""); err != nil {
		t.Fatalf("create with a project dir: %v", err)
	}
	if _, err := s.Update("ok", "/", ""); !errors.Is(err, ErrWorkDirTooBroad) {
		t.Fatalf("update to / = %v, want ErrWorkDirTooBroad", err)
	}
}
