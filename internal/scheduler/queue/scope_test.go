// SPDX-License-Identifier: AGPL-3.0-or-later

package queue

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScopeKeysConflict(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"scope:p", "scope:p", true},          // same root
		{"scope:p", "scope:p/a", true},        // root vs leaf
		{"scope:p/a", "scope:p", true},        // reversed
		{"scope:p/a", "scope:p/a/b", true},    // ancestor vs descendant
		{"scope:p/a/b", "scope:p/a", true},    // reversed
		{"scope:p/a", "scope:p/b", false},     // disjoint siblings
		{"scope:p/a", "scope:q/a", false},     // same path, different project
		{"scope:p", "scope:pp", false},        // project-name bleed guard
		{"scope:p/a", "scope:p/ab", false},    // "…/a" must not eat "…/ab"
		{"scope:p/ab", "scope:p/a", false},    // reversed
		{"scope:p/a/b", "scope:p/a/c", false}, // disjoint deep siblings
	}
	for _, tc := range cases {
		if got := ScopeKeysConflict(tc.a, tc.b); got != tc.want {
			t.Errorf("ScopeKeysConflict(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := ScopeKeysConflict(tc.b, tc.a); got != tc.want {
			t.Errorf("ScopeKeysConflict(%q, %q) = %v, want %v (symmetry)", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestScopeAncestors(t *testing.T) {
	if got := ScopeAncestors("scope:p"); got != nil {
		t.Fatalf("root has no ancestors: %v", got)
	}
	want := []string{"scope:p", "scope:p/a"}
	if got := ScopeAncestors("scope:p/a/b"); !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
	}
	if got := ScopeAncestors("project:p"); got != nil {
		t.Fatalf("non-scope keys have no scope ancestors: %v", got)
	}
}

func TestRegistryScopePrefixConflict(t *testing.T) {
	r := NewResourceRegistry()
	// A scoped child claims scope:p/a.
	if !r.TryAcquire([]string{ScopeLeafKey("p", "a")}, "child-a") {
		t.Fatal("first scoped claim must succeed")
	}
	// A disjoint sibling runs in parallel.
	if !r.TryAcquire([]string{ScopeLeafKey("p", "b")}, "child-b") {
		t.Fatal("disjoint scope must not conflict")
	}
	// An overlapping sibling (same leaf) is refused.
	if r.TryAcquire([]string{ScopeLeafKey("p", "a")}, "child-c") {
		t.Fatal("same leaf must conflict")
	}
	// A descendant of the held leaf is refused.
	if r.TryAcquire([]string{ScopeLeafKey("p", "a/deep")}, "child-d") {
		t.Fatal("descendant of a held scope must conflict")
	}
	// The whole-project root is refused while any scoped child runs.
	if r.TryAcquire([]string{ScopeRootKey("p")}, "whole") {
		t.Fatal("root must conflict with a held leaf")
	}
	// A different project's root is fine.
	if !r.TryAcquire([]string{ScopeRootKey("q")}, "other") {
		t.Fatal("another project must not conflict")
	}
	// Reverse direction: with the root held, a leaf is refused.
	r2 := NewResourceRegistry()
	if !r2.TryAcquire([]string{ScopeRootKey("p")}, "whole") {
		t.Fatal("root claim must succeed on empty registry")
	}
	if r2.TryAcquire([]string{ScopeLeafKey("p", "x")}, "leaf") {
		t.Fatal("leaf must conflict with a held root")
	}
	// Non-scope keys keep exact-match semantics.
	r3 := NewResourceRegistry()
	if !r3.TryAcquire([]string{"project:p"}, "t1") {
		t.Fatal("exact key claim must succeed")
	}
	if !r3.TryAcquire([]string{"scope:p"}, "t2") {
		t.Fatal("scope root must not conflict with the legacy project key alone")
	}
}

// TestScopeKeysFoldCase pins the case-insensitive filesystem contract: on
// APFS/NTFS "Docs" and "docs" are the same directory, so the lock keys must
// collapse to one identity or two same-tree writers claim disjoint locks.
// Folding is conservative — on a case-sensitive volume a colliding pair
// merely serializes where it could have run in parallel.
func TestScopeKeysFoldCase(t *testing.T) {
	if got := ScopeLeafKey("Proj", "Docs/Sub"); got != "scope:proj/docs/sub" {
		t.Fatalf("leaf key = %q, want folded scope:proj/docs/sub", got)
	}
	if got := ScopeRootKey("Proj"); got != "scope:proj" {
		t.Fatalf("root key = %q, want folded scope:proj", got)
	}
	// A leaf declared in one case blocks the same leaf in another — the
	// whole point of folding.
	r := NewResourceRegistry()
	if !r.TryAcquire([]string{ScopeLeafKey("p", "Docs")}, "child-upper") {
		t.Fatal("first claim must succeed")
	}
	if r.TryAcquire([]string{ScopeLeafKey("p", "docs")}, "child-lower") {
		t.Fatal("same directory in a different case must conflict")
	}
	// And the folded identity composes with ancestor rules both ways.
	if r.TryAcquire([]string{ScopeLeafKey("p", "DOCS/deep")}, "child-deep") {
		t.Fatal("descendant of a held folded leaf must conflict")
	}
	// Pinned work dirs fold the same way: two spellings of one directory
	// must land on one key.
	a := WorkDirKey(filepath.Join(string(filepath.Separator), "NonExist", "CaseDir"))
	b := WorkDirKey(filepath.Join(string(filepath.Separator), "nonexist", "casedir"))
	if a != b {
		t.Fatalf("workdir keys differ by case: %q vs %q", a, b)
	}
}

// TestProjectKeyFoldsCase: the project lock must collide across casings —
// spec.project arrives in whatever casing the classifier inferred from the
// directory ("Needlen") while --project is verbatim ("needlen"). Live
// evidence: two whole-project tasks on one tree landed project:Needlen and
// project:needlen in the same queue, parallel writers the lock exists to
// exclude.
func TestProjectKeyFoldsCase(t *testing.T) {
	if ProjectKey("Needlen") != ProjectKey("needlen") {
		t.Fatalf("ProjectKey casing differs: %q vs %q", ProjectKey("Needlen"), ProjectKey("needlen"))
	}
	a := DefaultKeys(nil, "Needlen", "", "t1")
	b := DefaultKeys(nil, "needlen", "", "t2")
	if a[0] != b[0] {
		t.Fatalf("default project keys diverged: %q vs %q", a[0], b[0])
	}
}

// TestSessionLanesParallelize pins the conversation-lane contract: two
// conversations on ONE checkout run side by side, each stays serial against
// itself, and a claim with no conversation keeps excluding everyone.
func TestSessionLanesParallelize(t *testing.T) {
	a := SessionScopeKey("proj", "sess-a")
	b := SessionScopeKey("proj", "sess-b")
	if !strings.HasSuffix(a, "/@session/sess-a") {
		t.Fatalf("lane key = %q, want the @session level under the project root", a)
	}
	if ScopeKeysConflict(a, b) {
		t.Fatalf("two conversations must not conflict: %q vs %q", a, b)
	}
	if !ScopeKeysConflict(ScopeRootKey("proj"), a) {
		t.Fatal("a whole-project claim (no conversation) must still exclude a lane")
	}
	if !ScopeKeysConflict(SessionScopeKey("proj", "sess-a"), SessionScopeLeafKey("proj", "sess-a", "src")) {
		t.Fatal("a conversation's own scoped child must stay inside its lane")
	}
	if ScopeKeysConflict(SessionScopeLeafKey("proj", "sess-a", "src"), SessionScopeLeafKey("proj", "sess-b", "src")) {
		t.Fatal("the same path in two conversations must stay disjoint")
	}
	if SessionScopeKey("proj", "") != ScopeRootKey("proj") {
		t.Fatalf("empty session must fall back to the root, got %q", SessionScopeKey("proj", ""))
	}
	if SessionScopeLeafKey("proj", "", "src") != ScopeLeafKey("proj", "src") {
		t.Fatal("empty session must keep the legacy leaf")
	}
	if SessionProjectKey("proj", "") != ProjectKey("proj") {
		t.Fatal("empty session must keep the legacy project key")
	}
	if SessionProjectKey("proj", "sess-a") == SessionProjectKey("proj", "sess-b") {
		t.Fatal("project keys of two conversations must differ")
	}

	r := NewResourceRegistry()
	if !r.TryAcquire([]string{a}, "t1") {
		t.Fatal("first lane must acquire")
	}
	if !r.TryAcquire([]string{b}, "t2") {
		t.Fatal("a second conversation must acquire while the first holds its lane")
	}
	if r.TryAcquire([]string{SessionScopeKey("proj", "sess-a")}, "t3") {
		t.Fatal("the same lane must conflict with itself")
	}
	if r.TryAcquire([]string{ScopeRootKey("proj")}, "t4") {
		t.Fatal("a whole-project claim must wait for every lane")
	}
}
