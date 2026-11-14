// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestNormalizeScopePaths(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain", []string{"pkg/api", "web/src"}, []string{"pkg/api", "web/src"}},
		{"backslashes fold", []string{`pkg\api`}, []string{"pkg/api"}},
		{"leading ./ stripped", []string{"./pkg/api/"}, []string{"pkg/api"}},
		{"dot dot clamps to root", []string{"../../etc"}, []string{"etc"}},
		{"absolute clamps", []string{"/etc/passwd"}, []string{"etc/passwd"}},
		{"empty is root", []string{"."}, []string{""}},
		{"nested leaf dropped", []string{"pkg", "pkg/api"}, []string{"pkg"}},
		{"nested first still wins", []string{"pkg/api", "pkg"}, []string{"pkg"}},
		{"duplicates collapse", []string{"a", "a", "./a"}, []string{"a"}},
		{"blank entries dropped", []string{"  ", "b"}, []string{"b"}},
	}
	for _, tc := range cases {
		got := normalizeScopePaths(tc.in)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: normalizeScopePaths(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
	// The cap bounds the lock set; the shallowest declarations survive.
	many := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	if got := normalizeScopePaths(many); len(got) != maxScopePaths {
		t.Fatalf("cap: got %d paths, want %d", len(got), maxScopePaths)
	}
}

func TestChildResourceKeys(t *testing.T) {
	// A scoped child holds leaf keys only — no project key, no scope root:
	// the leaves are what let disjoint siblings run in parallel.
	got := childResourceKeys("c1", TaskInput{Project: "p", Paths: []string{"pkg/api", "pkg/util"}})
	want := []string{"scope:p/pkg/api", "scope:p/pkg/util"}
	if !slices.Equal(got, want) {
		t.Fatalf("scoped child keys = %v, want %v", got, want)
	}
	// Actuator keys merge on top of the scope, exactly as they do on the
	// project key: a scoped child driving a servo still owns the device.
	got = childResourceKeys("c1", TaskInput{Project: "p", Paths: []string{"pkg"}, Requires: []string{"hardware:servo"}})
	if !slices.Contains(got, "scope:p/pkg") || !slices.Contains(got, "hardware:servo") {
		t.Fatalf("scoped child with actuator = %v", got)
	}
	// An isolated child works in a private copy: it locks nothing shared —
	// just itself — and the actuator merge still applies.
	got = childResourceKeys("c-iso", TaskInput{Project: "p", Isolate: true, Paths: []string{"pkg"}, Requires: []string{"hardware:servo"}})
	if !slices.Equal(got, []string{"task:c-iso", "hardware:servo"}) {
		t.Fatalf("isolated child keys = %v, want [task:c-iso hardware:servo]", got)
	}
	// No declaration → the monolithic project serialization stays.
	got = childResourceKeys("c1", TaskInput{Project: "p"})
	if !slices.Contains(got, "project:p") || !slices.Contains(got, "scope:p") {
		t.Fatalf("unscoped child keys = %v, want project+scope root", got)
	}
	// Paths without a project cannot scope anything: fall back to the
	// anonymous default rather than minting keys nobody else holds.
	got = childResourceKeys("c1", TaskInput{Paths: []string{"pkg"}})
	if slices.Contains(got, "scope:p/pkg") {
		t.Fatalf("projectless paths must not mint scope keys: %v", got)
	}
	// A pinned work directory is a tree of its own and outranks the project
	// key: holding project:p as well would make the child queue behind every
	// sibling editing a tree it never touches — the exact serialization
	// one-business-domain-per-directory exists to avoid.
	wd := t.TempDir()
	got = childResourceKeys("c-wd", TaskInput{Project: "p", WorkDir: wd})
	if want := []string{"dir:" + queue.WorkDirKey(wd)}; !slices.Equal(got, want) {
		t.Fatalf("pinned-workdir child keys = %v, want %v", got, want)
	}
	// A pin INSIDE the project's tree arrives as both (delegateChild fills the
	// lanes): the directory decides where to run, the lane decides what the
	// child excludes — a dir: key there would be invisible to the project root.
	got = childResourceKeys("c-wd", TaskInput{Project: "p", WorkDir: wd, Paths: []string{"feat-a"}})
	if want := []string{"scope:p/feat-a"}; !slices.Equal(got, want) {
		t.Fatalf("in-project pinned child keys = %v, want %v", got, want)
	}
}

// TestEnqueueAdoptsScopeAndIsolate locks the queue path's metadata honesty:
// an input carrying paths or isolate lands its narrowed keys AND the row
// flags — without them a queued child would run with a task-key claim but
// the unscoped behavior, or vice versa.
func TestEnqueueAdoptsScopeAndIsolate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := newCore(t, "enqueue-scope")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	queued, err := c.Enqueue(ctx, TaskInput{
		Title: "scoped-queue", Intent: "work", Project: "proj",
		Paths: []string{"pkg/api"},
	}, QueueSpec{Priority: PriorityNormal})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := c.store.Get(ctx, queued.TaskID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Equal(got.ResourceKeys, []string{"scope:proj/pkg/api"}) {
		t.Fatalf("queued scope keys = %v", got.ResourceKeys)
	}
	if len(got.ScopePaths) != 1 || got.ScopePaths[0] != "pkg/api" {
		t.Fatalf("queued scope paths = %v", got.ScopePaths)
	}
	iso, err := c.Enqueue(ctx, TaskInput{
		Title: "iso-queue", Intent: "work", Project: "proj", Isolate: true,
	}, QueueSpec{Priority: PriorityNormal})
	if err != nil {
		t.Fatalf("enqueue isolate: %v", err)
	}
	gotIso, err := c.store.Get(ctx, iso.TaskID)
	if err != nil {
		t.Fatalf("reload isolate: %v", err)
	}
	if !gotIso.Isolate {
		t.Fatal("queued isolate flag lost")
	}
	if !slices.Contains(gotIso.ResourceKeys, "task:"+iso.TaskID) || slices.Contains(gotIso.ResourceKeys, "project:proj") {
		t.Fatalf("queued isolate keys = %v", gotIso.ResourceKeys)
	}
}

// TestScopedProjectChildrenRunInParallel is the end-to-end proof of the
// feature: two children of one project, each declaring a disjoint path
// scope, run SIMULTANEOUSLY in the shared worktree — while a third claim
// overlapping a running sibling's scope, and an unscoped whole-project
// task, both wait their turn. This is the same-node parallelism the
// monolithic project lock used to forbid.
func TestScopedProjectChildrenRunInParallel(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "scope-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:slow", Command: "sleep", Args: []string{"1"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "scope-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	enqueue := func(title string, paths []string) string {
		t.Helper()
		in := TaskInput{Title: title, Intent: "x", Requires: []string{"sys:slow"}, Authorized: true, Project: "proj", Paths: paths}
		tk, _, _, err := c.createTask(ctx, in)
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if err := c.store.SetQueueMeta(ctx, tk.TaskID, PriorityNormal, "", "", childResourceKeys(tk.TaskID, in)); err != nil {
			t.Fatalf("queue meta %s: %v", title, err)
		}
		if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
			t.Fatalf("queue %s: %v", title, err)
		}
		return tk.TaskID
	}
	state := func(id string) string {
		t.Helper()
		got, err := c.store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return got.State
	}

	a := enqueue("scoped-a", []string{"pkg/api"})
	b := enqueue("scoped-b", []string{"pkg/ui"})
	// Overlapping sibling: same leaf as a — must serialize behind it.
	overlap := enqueue("scoped-overlap", []string{"pkg/api"})
	// Whole-project task: unscoped claim holds the scope root — excluded by
	// every running scoped child.
	whole := enqueue("whole-project", nil)

	deadline := time.Now().Add(5 * time.Second)
	overlapped := false
	for time.Now().Before(deadline) {
		sa, sb := state(a), state(b)
		if sa == StateRunning && sb == StateRunning {
			overlapped = true
			// While both scoped children run, the conflicting and the
			// whole-project tasks must NOT be running.
			if st := state(overlap); st == StateRunning {
				t.Fatal("overlapping scope ran concurrently with its sibling")
			}
			if st := state(whole); st == StateRunning {
				t.Fatal("whole-project task ran concurrently with scoped children")
			}
			break
		}
		if (sa == StateDone || sa == StateFailed) && (sb == StateDone || sb == StateFailed) {
			t.Fatal("scoped children finished but never overlapped — still serialized")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !overlapped {
		t.Fatal("scoped children never overlapped within deadline")
	}
	waitLocalTasksSettled(t, c)
	// Everything eventually completes: the conflicts serialized, not failed.
	for id, want := range map[string]string{a: StateDone, b: StateDone, overlap: StateDone, whole: StateDone} {
		if got := state(id); got != want {
			t.Fatalf("task %s final state = %s, want %s", id, got, want)
		}
	}
}

// TestExecLocksScopePrefixConflict pins the exec_locks prefix semantics: a
// held root blocks leaves, a held leaf blocks the root and its ancestors,
// disjoint leaves coexist, and stale rows are stealable exactly like the
// exact-match path.
func TestExecLocksScopePrefixConflict(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scope.db")
	db := testdb.OpenFile(t, path)
	s := NewTaskStore(db, testLogger())

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	live := func(title string) Task {
		t.Helper()
		tk := createTask(t, s, "", title, "n1")
		must(s.Queue(ctx, tk.TaskID, "n1"))
		must(s.Dispatch(ctx, tk.TaskID, "n1", "n1"))
		must(s.Accept(ctx, tk.TaskID, "n1"))
		must(s.SetLease(ctx, tk.TaskID, 600_000))
		return tk
	}

	// Child A holds scope:proj/pkg/api.
	a := live("scope a")
	ok, err := s.TryAcquireExecLocks(ctx, a.TaskID, "n1", "run-a", []string{queue.ScopeLeafKey("proj", "pkg/api")})
	if err != nil || !ok {
		t.Fatalf("leaf acquire: ok=%v err=%v", ok, err)
	}

	// A disjoint sibling runs in parallel.
	b := live("scope b")
	if ok, err = s.TryAcquireExecLocks(ctx, b.TaskID, "n1", "run-b", []string{queue.ScopeLeafKey("proj", "pkg/ui")}); err != nil || !ok {
		t.Fatalf("disjoint leaf must parallelize: ok=%v err=%v", ok, err)
	}

	// The same leaf conflicts.
	c := live("scope c")
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("proj", "pkg/api")}); err != nil || ok {
		t.Fatalf("same leaf must conflict: ok=%v err=%v", ok, err)
	}
	// A descendant of the held leaf conflicts.
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("proj", "pkg/api/deep")}); err != nil || ok {
		t.Fatalf("descendant must conflict: ok=%v err=%v", ok, err)
	}
	// An ancestor of the held leaf conflicts.
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("proj", "pkg")}); err != nil || ok {
		t.Fatalf("ancestor must conflict: ok=%v err=%v", ok, err)
	}
	// The whole-project root conflicts with any held leaf.
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeRootKey("proj")}); err != nil || ok {
		t.Fatalf("root must conflict with a held leaf: ok=%v err=%v", ok, err)
	}
	// Another project is untouched.
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("other", "pkg/api")}); err != nil || !ok {
		t.Fatalf("another project must not conflict: ok=%v err=%v", ok, err)
	}
	must(s.ReleaseExecLocks(ctx, c.TaskID, "run-c"))

	// While a and b still hold leaves, the root is blocked (covered above);
	// release them, then the reverse direction: with a root held, a leaf
	// and the root itself are blocked.
	must(s.ReleaseExecLocks(ctx, a.TaskID, "run-a"))
	must(s.ReleaseExecLocks(ctx, b.TaskID, "run-b"))
	d := live("whole project")
	if ok, err = s.TryAcquireExecLocks(ctx, d.TaskID, "n1", "run-d", []string{queue.ScopeRootKey("proj")}); err != nil || !ok {
		t.Fatalf("root acquire: ok=%v err=%v", ok, err)
	}
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("proj", "x")}); err != nil || ok {
		t.Fatalf("leaf must conflict with a held root: ok=%v err=%v", ok, err)
	}

	// A stale holder (lapsed lease) is stolen exactly like exact-match keys.
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET lease_expires_at=1 WHERE task_id=?`, d.TaskID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if ok, err = s.TryAcquireExecLocks(ctx, c.TaskID, "n1", "run-c", []string{queue.ScopeLeafKey("proj", "x")}); err != nil || !ok {
		t.Fatalf("stale root must be stealable: ok=%v err=%v", ok, err)
	}
}

// TestRelUnderDirResolvesSymlinks locks the containment rule against the one
// thing path math cannot see: a link makes "inside the tree" a property of the
// target, not of the spelling. Answering it lexically is not a near miss — it
// hands the project's lock to a task writing a tree the project never sees, or
// hides an in-tree task behind a dir: key nothing else collides with.
//
// The paths here carry mixed case on purpose: t.TempDir() on Linux names the
// tree after the test, and a case-folded lock identity of that tree resolves to
// nothing on a case-sensitive filesystem. Passing identities instead of paths
// would make every case below answer "lexically" while looking correct on NTFS.
func TestRelUnderDirResolvesSymlinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Project")
	outside := filepath.Join(base, "Elsewhere")
	for _, d := range []string{filepath.Join(root, "Sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// A path inside the tree that resolves out of it, and the mirror image.
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "Sub"), filepath.Join(base, "link-in")); err != nil {
		t.Fatalf("symlink link-in: %v", err)
	}

	cases := []struct {
		name    string
		p       string
		wantRel string
		wantOK  bool
	}{
		{"plain subdirectory", filepath.Join(root, "Sub"), "Sub", true},
		{"the tree itself", root, "", false},
		{"link out of the tree", filepath.Join(root, "link-out"), "", false},
		{"link into the tree", filepath.Join(base, "link-in"), "Sub", true},
		// A pin may name a directory that does not exist yet: --workdir creates
		// it, and a project may precede its tree. The link in its prefix still
		// decides which tree it is.
		{"missing tail under a link out", filepath.Join(root, "link-out", "later"), "", false},
		{"missing tail in the tree", filepath.Join(root, "later"), "later", true},
	}
	for _, tc := range cases {
		rel, ok := relUnderDir(root, tc.p)
		if ok != tc.wantOK || rel != tc.wantRel {
			t.Fatalf("%s: relUnderDir = %q,%v want %q,%v", tc.name, rel, ok, tc.wantRel, tc.wantOK)
		}
		// The lane folds case, so the tree's own spelling still lands on one
		// key: "Sub" here and "sub" from a declared path are the same lane.
		if ok {
			if a, b := queue.ScopeLeafKey("p", rel), queue.ScopeLeafKey("p", strings.ToLower(rel)); a != b {
				t.Fatalf("%s: lane %q does not fold to %q", tc.name, a, b)
			}
		}
	}
}
