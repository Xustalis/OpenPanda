// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
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
			queueFixtureAbility(t, "sys:slow", "sleep", "1s"),
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

func TestQueuedSessionWorkspaceConcurrency(t *testing.T) {
	cases := []struct {
		name     string
		pathsA   []string
		pathsB   []string
		sessionB string
		private  bool
		isolate  bool
		parallel bool
	}{
		{"same shared path", []string{"src"}, []string{"src"}, "session-b", false, false, false},
		{"disjoint shared paths", []string{"src"}, []string{"docs"}, "session-b", false, false, true},
		{"sessionless shared path", []string{"src"}, []string{"src"}, "", false, false, false},
		{"separate pinned trees same project", nil, nil, "session-b", true, false, true},
		{"separate scoped trees same project", []string{"src"}, []string{"src"}, "session-b", true, false, true},
		{"isolated execution after snapshots", nil, nil, "session-b", false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCore(openTestDB(t), "workspace-queue", ledger.Card{
				Device: "workspace-queue", Capacity: ledger.Capacity{MaxConcurrent: 3},
				Native: []ledger.NativeAbility{queueFixtureAbility(t, "sys:pause", "sleep", "300ms")},
			}, 5, testLogger(), config.ModelConfig{})
			c.SetWorkDir(t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c.StartQueueScheduler(ctx)
			enqueue := func(session string, paths []string) Task {
				t.Helper()
				q := QueueSpec{Priority: PriorityNormal, SessionID: session}
				if tc.private {
					q.WorkDir = t.TempDir()
				}
				task, err := c.Enqueue(ctx, TaskInput{Title: "workspace concurrency", Intent: "pause",
					Project: "same-project", Paths: paths, Isolate: tc.isolate, Requires: []string{"sys:pause"}, Authorized: true}, q)
				if err != nil {
					t.Fatal(err)
				}
				return task
			}
			a, b := enqueue("session-a", tc.pathsA), enqueue(tc.sessionB, tc.pathsB)
			overlapped := false
			for {
				first, err := c.store.Get(ctx, a.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				second, err := c.store.Get(ctx, b.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if first.State == StateRunning && second.State == StateRunning && first.ExecWorkDir != "" && second.ExecWorkDir != "" {
					overlapped = true
					if !tc.parallel {
						t.Fatal("writers of the same shared path ran together")
					}
				}
				if first.State == StateDone && second.State == StateDone {
					break
				}
				if first.State == StateFailed || second.State == StateFailed || ctx.Err() != nil {
					t.Fatalf("tasks did not complete: %s / %s; context %v", first.State, second.State, ctx.Err())
				}
				time.Sleep(5 * time.Millisecond)
			}
			waitLocalTasksSettled(t, c)
			if tc.parallel && !overlapped {
				t.Fatal("independent writable paths never ran together")
			}
		})
	}
}

func TestPhysicalWorkspaceExecLocksAndChildYield(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newCore(t, "physical-child-yield")
	dir := t.TempDir()
	live := func(title string) Task {
		t.Helper()
		task, _, _, err := c.createTask(ctx, TaskInput{Title: title, WorkDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		for _, move := range []func() error{
			func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
			func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
			func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		} {
			if err := move(); err != nil {
				t.Fatal(err)
			}
		}
		task, err = c.store.Get(ctx, task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		// Custom scheduling keys cannot waive physical workspace exclusion.
		task.ResourceKeys = []string{"custom:" + title}
		return task
	}
	parent, child := live("parent"), live("child")
	parent.ResourceKeys = append(parent.ResourceKeys, "hardware:servo")
	keys, wait, err := c.executionLockKeys(parent, "hardware:servo")
	if err != nil {
		t.Fatal(err)
	}
	childKeys, _, err := c.executionLockKeys(child, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.execLockAcquire(ctx, parent.TaskID, "parent-run", keys, wait, true); err != nil {
		t.Fatal(err)
	}
	defer c.execLockUnlock(parent.TaskID)
	if ok, err := c.store.TryAcquireExecLocks(ctx, child.TaskID, c.nodeID, "child-run", childKeys); err != nil || ok {
		t.Fatalf("custom keys hid a shared tree: acquired=%v err=%v", ok, err)
	}
	resume, err := c.yieldForChildren(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, child.TaskID, c.nodeID, "child-run", childKeys); err != nil || !ok {
		t.Fatalf("child could not use the yielded physical workspace: acquired=%v err=%v", ok, err)
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, "foreign-actuator", c.nodeID, "foreign-run", []string{"hardware:servo"}); err != nil || ok {
		t.Fatalf("physical actuator was released along with workspace: acquired=%v err=%v", ok, err)
	}
	resumed := make(chan struct{})
	go func() { resume(); close(resumed) }()
	select {
	case <-resumed:
		t.Fatal("parent resumed while child still held its physical workspace")
	case <-time.After(30 * time.Millisecond):
	}
	if err := c.store.ReleaseExecLocks(ctx, child.TaskID, "child-run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resumed:
	case <-ctx.Done():
		t.Fatal("parent could not reacquire workspace after child released it")
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, child.TaskID, c.nodeID, "child-again", childKeys); err != nil || ok {
		t.Fatalf("parent resumed without reacquiring physical workspace: acquired=%v err=%v", ok, err)
	}
}

func TestLegacySessionExecLockRowsBlockNewClaims(t *testing.T) {
	ctx := context.Background()
	s := NewTaskStore(openTestDB(t), testLogger())
	holder := createTask(t, s, "", "old executor", "n")
	for _, err := range []error{s.Queue(ctx, holder.TaskID, "n"), s.Dispatch(ctx, holder.TaskID, "n", "n"), s.Accept(ctx, holder.TaskID, "n")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Seed the exact old on-disk representation, bypassing today's canonical
	// acquisition path, to cover an old process that is still draining a run.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO exec_locks(key, task_id, node_id, acquired_at, run_id) VALUES(?,?,?,?,?)`,
		"scope:proj/@session/old/src", holder.TaskID, "n", s.now(), "old-run"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scope:proj/src", "scope:proj/src/deep", "scope:proj", "scope:proj/@session/new/src"} {
		if ok, err := s.TryAcquireExecLocks(ctx, "new-task", "n", "new-run", []string{key}); err != nil || ok {
			t.Fatalf("old lane did not block %s: acquired=%v err=%v", key, ok, err)
		}
	}
	if ok, err := s.TryAcquireExecLocks(ctx, "new-task", "n", "new-run", []string{"scope:proj/docs"}); err != nil || !ok {
		t.Fatalf("old scoped row blocked a disjoint path: acquired=%v err=%v", ok, err)
	}
}

// Input landing writes an entire source tree even when execution only owns a
// leaf or will take place in a private copy. It must wait for live leaf writers,
// then relinquish the source root before the long execution starts.
func TestWorkspacePreparationWaitsForSharedWriter(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		name := "scoped input landing"
		if isolated {
			name = "isolated input landing and snapshot"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := newCore(t, "preparation")
			withArtifactPool(t, c)
			source, err := c.projectWorkDir("project")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(source, "src", "file.txt"), "live writer")
			incoming := t.TempDir()
			writeFile(t, filepath.Join(incoming, "src", "file.txt"), "incoming artifact")
			pack, err := c.artifacts.PackDir(incoming)
			if err != nil {
				t.Fatal(err)
			}
			live := func(in TaskInput, nonce string) (Task, []string) {
				t.Helper()
				task, _, _, err := c.createTask(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				for _, move := range []func() error{
					func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
					func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
					func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
				} {
					if err := move(); err != nil {
						t.Fatal(err)
					}
				}
				task, err = c.store.Get(ctx, task.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				keys, wait, err := c.executionLockKeys(task, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.execLockAcquire(ctx, task.TaskID, nonce, keys, wait, true); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.execLockUnlock(task.TaskID) })
				return task, keys
			}
			writer, writerKeys := live(TaskInput{Title: "writer", Project: "project", Paths: []string{"src"}}, "writer")
			preparer, _ := live(TaskInput{Title: "preparer", Project: "project", Paths: []string{"docs"}, Isolate: isolated}, "preparer")
			preparer.Inputs = []bus.ArtifactRef{{Stage: worktreeArtifactStage, Hash: pack.Hash, Source: c.nodeID}}
			type outcome struct {
				prep runPrep
				err  error
			}
			finished := make(chan outcome, 1)
			go func() {
				prep, err := c.prepareRunDir(ctx, ctx, preparer, preparer.TaskID, commander.Plan{Kind: "native"})
				finished <- outcome{prep, err}
			}()
			select {
			case out := <-finished:
				t.Fatalf("input preparation passed a live shared writer: %v", out.err)
			case <-time.After(30 * time.Millisecond):
			}
			if got := readFile(t, filepath.Join(source, "src", "file.txt")); got != "live writer" {
				t.Fatalf("artifact overwrote a path while its writer was running: %q", got)
			}
			c.execLockUnlock(writer.TaskID)
			select {
			case out := <-finished:
				if out.err != nil {
					t.Fatal(out.err)
				}
				if got := readFile(t, filepath.Join(out.prep.workDir, "src", "file.txt")); got != "incoming artifact" {
					t.Fatalf("prepared workspace missed its artifact: %q", got)
				}
			case <-ctx.Done():
				t.Fatal("preparer did not progress after writer released its leaf")
			}
			if ok, err := c.store.TryAcquireExecLocks(ctx, writer.TaskID, c.nodeID, "writer-next", writerKeys); err != nil || !ok {
				t.Fatalf("source root stayed locked during independent execution: acquired=%v err=%v", ok, err)
			}
			if err := c.store.ReleaseExecLocks(ctx, writer.TaskID, "writer-next"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspacePreparationCancellationReleasesRoot(t *testing.T) {
	c := newCore(t, "cancel-preparation")
	ctx := context.Background()
	task := createTask(t, c.store, "", "cancel preparation", c.nodeID)
	if err := c.store.Queue(ctx, task.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Accept(ctx, task.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	task.Isolate = true
	keys, wait, err := c.executionLockKeys(task, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.execLockAcquire(ctx, task.TaskID, "cancel-run", keys, wait, true); err != nil {
		t.Fatal(err)
	}
	defer c.execLockUnlock(task.TaskID)
	preparing, cancel := context.WithCancel(ctx)
	finish, cleanup, err := c.prepareWorkspaceLocks(preparing, task)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := finish(); err == nil {
		t.Fatal("canceled preparation unexpectedly resumed execution")
	}
	cleanup()
	source, err := c.projectWorkDir(task.Project)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, "after-cancel", c.nodeID, "after-cancel", []string{queue.WorkspaceScopeKey(source, "")}); err != nil || !ok {
		t.Fatalf("canceled preparation retained the source root: acquired=%v err=%v", ok, err)
	}
}

func TestDelegateSnapshotWaitsForSharedWriterAndRestoresParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newCore(t, "snapshot-lease")
	source, err := c.projectWorkDir("proj")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "docs", "file.txt"), "writer unfinished")
	live := func(title, scope string) (Task, []string) {
		t.Helper()
		task := createTask(t, c.store, "", title, c.nodeID)
		for _, move := range []func() error{
			func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
			func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
			func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		} {
			if err := move(); err != nil {
				t.Fatal(err)
			}
		}
		task.ScopePaths = []string{scope}
		keys, wait, err := c.executionLockKeys(task, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.execLockAcquire(ctx, task.TaskID, title, keys, wait, true); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.execLockUnlock(task.TaskID) })
		return task, keys
	}
	parent, parentKeys := live("parent", "src")
	writer, writerKeys := live("writer", "docs")
	type outcome struct {
		ctx     context.Context
		cleanup func()
		err     error
	}
	finished := make(chan outcome, 1)
	go func() {
		snapshotCtx, cleanup, err := c.snapshotDelegateBatch(ctx, parent, []delegateRequest{{Isolate: true}, {Isolate: true}})
		finished <- outcome{snapshotCtx, cleanup, err}
	}()
	select {
	case out := <-finished:
		if out.cleanup != nil {
			out.cleanup()
		}
		t.Fatalf("sibling snapshot passed a live disjoint-path writer: %v", out.err)
	case <-time.After(30 * time.Millisecond):
	}
	writeFile(t, filepath.Join(source, "docs", "file.txt"), "writer completed")
	c.execLockUnlock(writer.TaskID)
	select {
	case out := <-finished:
		if out.err != nil {
			t.Fatal(out.err)
		}
		defer out.cleanup()
		snapshot, _ := out.ctx.Value(delegateSnapshotKey{}).(*delegateSnapshot)
		if snapshot == nil || readFile(t, filepath.Join(snapshot.dir, "docs", "file.txt")) != "writer completed" {
			t.Fatal("sibling snapshot missed the completed shared-tree state")
		}
	case <-ctx.Done():
		t.Fatal("sibling snapshot did not finish after the writer released")
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, "foreign-src", c.nodeID, "foreign-src", parentKeys); err != nil || ok {
		t.Fatalf("snapshot did not restore the parent's original leaf: acquired=%v err=%v", ok, err)
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, writer.TaskID, c.nodeID, "writer-next", writerKeys); err != nil || !ok {
		t.Fatalf("snapshot retained the source root after restoring parent: acquired=%v err=%v", ok, err)
	}
	if err := c.store.ReleaseExecLocks(ctx, writer.TaskID, "writer-next"); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateSnapshotFailureRestoresParentClaim(t *testing.T) {
	c := newCore(t, "snapshot-failed-copy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent := createTask(t, c.store, "", "parent", c.nodeID)
	for _, move := range []func() error{
		func() error { return c.store.Queue(ctx, parent.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, parent.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, parent.TaskID, c.nodeID) },
	} {
		if err := move(); err != nil {
			t.Fatal(err)
		}
	}
	parent.ScopePaths = []string{"src"}
	keys, wait, err := c.executionLockKeys(parent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.execLockAcquire(ctx, parent.TaskID, "parent-run", keys, wait, true); err != nil {
		t.Fatal(err)
	}
	defer c.execLockUnlock(parent.TaskID)
	if _, cleanup, err := c.snapshotDelegateBatch(ctx, parent, []delegateRequest{{Isolate: true}, {Isolate: true}}); err == nil {
		cleanup()
		t.Fatal("missing source tree unexpectedly produced a sibling snapshot")
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, "foreign-parent", c.nodeID, "foreign-run", keys); err != nil || ok {
		t.Fatalf("failed snapshot left the running parent without its leaf: acquired=%v err=%v", ok, err)
	}
	source, err := c.projectWorkDir(parent.Project)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.store.TryAcquireExecLocks(ctx, "foreign-docs", c.nodeID, "foreign-docs", []string{queue.WorkspaceScopeKey(source, "docs")}); err != nil || !ok {
		t.Fatalf("failed snapshot retained its temporary source root: acquired=%v err=%v", ok, err)
	}
}

func TestDelegateSnapshotCancellationPreservesWorkspaceSafety(t *testing.T) {
	for _, afterYield := range []bool{false, true} {
		name := "before_source_resolution"
		if afterYield {
			name = "after_workspace_yield"
		}
		t.Run(name, func(t *testing.T) {
			c := newCore(t, "snapshot-canceled")
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			live := func(title, scope string) (Task, []string) {
				t.Helper()
				task := createTask(t, c.store, "", title, c.nodeID)
				for _, move := range []func() error{
					func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
					func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
					func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
				} {
					if err := move(); err != nil {
						t.Fatal(err)
					}
				}
				task.ScopePaths = []string{scope}
				keys, wait, err := c.executionLockKeys(task, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.execLockAcquire(ctx, task.TaskID, title, keys, wait, true); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.execLockUnlock(task.TaskID) })
				return task, keys
			}
			parent, parentKeys := live("parent", "src")
			// A disjoint writer makes source-root acquisition wait after the
			// parent yields its leaf, so cancellation exercises the unlocked path.
			writer, _ := live("writer", "docs")
			runCtx, stopRun := context.WithCancel(ctx)
			defer stopRun()
			defer c.registerRunning(parent.TaskID, stopRun)()
			preparing, cancel := context.WithCancel(ctx)
			defer cancel()
			if !afterYield {
				cancel()
			}
			finished := make(chan error, 1)
			go func() {
				_, cleanup, err := c.snapshotDelegateBatch(preparing, parent, []delegateRequest{{Isolate: true}, {Isolate: true}})
				if cleanup != nil {
					cleanup()
				}
				finished <- err
			}()
			if afterYield {
				for {
					var held int
					if err := c.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_locks WHERE task_id=?", parent.TaskID).Scan(&held); err != nil {
						t.Fatal(err)
					}
					if held == 0 {
						break
					}
					select {
					case err := <-finished:
						t.Fatalf("snapshot returned before yielding: %v", err)
					case <-ctx.Done():
						t.Fatal("parent did not yield its workspace")
					case <-time.After(time.Millisecond):
					}
				}
				cancel()
			}
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled snapshot: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("canceled snapshot did not return")
			}
			if afterYield {
				if runCtx.Err() == nil {
					t.Fatal("parent continued after source acquisition failed with its workspace yielded")
				}
			} else {
				if runCtx.Err() != nil {
					t.Fatal("source lookup failure canceled a parent that still owns its workspace")
				}
				if ok, err := c.store.TryAcquireExecLocks(ctx, "foreign-parent", c.nodeID, "foreign", parentKeys); err != nil || ok {
					t.Fatalf("early cancellation lost parent locks: acquired=%v err=%v", ok, err)
				}
			}
			c.execLockUnlock(writer.TaskID)
			c.execLockUnlock(parent.TaskID)
			source, err := c.projectWorkDir(parent.Project)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := c.store.TryAcquireExecLocks(ctx, "after-snapshot-cancel", c.nodeID, "foreign", []string{queue.WorkspaceScopeKey(source, "")}); err != nil || !ok {
				t.Fatalf("canceled snapshot retained source locks: acquired=%v err=%v", ok, err)
			}
		})
	}
}
