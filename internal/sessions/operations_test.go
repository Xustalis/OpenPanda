// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"testing"
)

// TestOperationGenerations pins the durable compare-and-set contract: a
// SetOperation carrying a stale id must never overwrite a newer generation,
// which is what makes a late completion from an older task harmless.
func TestOperationGenerations(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("ops")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.StartOperation(sess.ID, Operation{ID: "gen-1", Status: "running", TaskID: "t1"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation == nil || got.Operation.ID != "gen-1" || got.Operation.UpdatedAt.IsZero() {
		t.Fatalf("operation after start = %+v", got.Operation)
	}

	// Same generation updates fine.
	ok, err := store.SetOperation(sess.ID, Operation{ID: "gen-1", Status: "done"})
	if err != nil || !ok {
		t.Fatalf("same-gen SetOperation = %v, %v", ok, err)
	}
	// A stale generation is refused — silently, not with an error, because a
	// late completion racing a newer op is normal.
	ok, err = store.SetOperation(sess.ID, Operation{ID: "gen-0", Status: "hijack"})
	if err != nil || ok {
		t.Fatalf("stale-gen SetOperation = %v, %v", ok, err)
	}
	got, _ = store.Get(sess.ID)
	if got.Operation.Status != "done" || got.Operation.ID != "gen-1" {
		t.Errorf("stale gen overwrote operation: %+v", got.Operation)
	}

	// A new generation via StartOperation wins unconditionally.
	if err := store.StartOperation(sess.ID, Operation{ID: "gen-2", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(sess.ID)
	if got.Operation.ID != "gen-2" {
		t.Errorf("new generation not installed: %+v", got.Operation)
	}

	// SetOperation on an operation-free session installs unconditionally —
	// legacy sessions never started an op.
	s2, err := store.Create("no-op")
	if err != nil {
		t.Fatal(err)
	}
	ok, err = store.SetOperation(s2.ID, Operation{ID: "any", Status: "x"})
	if err != nil || !ok {
		t.Fatalf("first SetOperation = %v, %v", ok, err)
	}
}

// TestMetadataSetters covers the small field writers the panel and TUI both
// reach for: worktree path, agent session ids, title trimming, pinned flag.
func TestMetadataSetters(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("meta")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SetWorktree(sess.ID, "/tmp/wt", "feat/x"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAgentSession(sess.ID, "claude", "as-123"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAgentSession(sess.ID, "codex", "cx-9"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTitle(sess.ID, "  padded title  "); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPinned(sess.ID, true); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Worktree != "/tmp/wt" || got.Branch != "feat/x" {
		t.Errorf("worktree = %q@%q", got.Worktree, got.Branch)
	}
	if got.AgentSessions["claude"] != "as-123" || got.AgentSessions["codex"] != "cx-9" {
		t.Errorf("agent sessions = %v", got.AgentSessions)
	}
	if got.Title != "padded title" {
		t.Errorf("title = %q — trim contract broken", got.Title)
	}
	if !got.Pinned {
		t.Error("pinned flag not set")
	}

	// Setters on a missing session must error, not silently create.
	if err := store.SetTitle("missing", "x"); err == nil {
		t.Error("SetTitle on missing session succeeded")
	}
}
