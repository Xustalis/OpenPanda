// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustAppend(t *testing.T, store *Store, id string, turn Turn) {
	t.Helper()
	if _, err := store.AppendTurn(id, turn); err != nil {
		t.Fatalf("append turn: %v", err)
	}
}

func mustWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckpointAndRewindRestoresFiles is the whole promise in one run: each
// finished answer leaves a snapshot, and a rewind puts both the transcript and
// the tree back to one — including deleting the files the agent created after
// it.
func TestCheckpointAndRewindRestoresFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess, err := store.Create("rewind")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Ensure(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, wt, Branch(sess.ID)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Turn 1: one new file.
	mustWrite(t, wt, "a.txt", "one")
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "写 a"})
	mustAppend(t, store, sess.ID, Turn{Role: "assistant", Text: "好了"})

	// Turn 2: change it, and add another the rewind must take away.
	mustWrite(t, wt, "a.txt", "two")
	mustWrite(t, wt, "b.txt", "new")
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "再改"})
	mustAppend(t, store, sess.ID, Turn{Role: "assistant", Text: "改好了"})

	// Only finished answers snapshot: two, one per turn.
	cps, err := store.Checkpoints(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 2 || cps[0].Turn != 2 || cps[1].Turn != 4 {
		t.Fatalf("checkpoints = %+v", cps)
	}

	res, err := store.Rewind(ctx, sess.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Files {
		t.Error("rewind should have restored the worktree")
	}
	if res.Turn != 2 {
		t.Errorf("result turn = %d, want 2", res.Turn)
	}

	body, err := os.ReadFile(filepath.Join(wt, "a.txt"))
	if err != nil {
		t.Fatalf("a.txt is gone: %v", err)
	}
	if string(body) != "one" {
		t.Errorf("a.txt = %q, want the first turn's content", body)
	}
	if _, err := os.Stat(filepath.Join(wt, "b.txt")); !os.IsNotExist(err) {
		t.Error("b.txt should have been removed by the rewind")
	}
	var sawB bool
	for _, p := range res.Removed {
		if p == "b.txt" {
			sawB = true
		}
	}
	if !sawB {
		t.Errorf("removed = %v, want it to name b.txt", res.Removed)
	}

	stored, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Turns) != 2 {
		t.Errorf("turns = %d, want 2", len(stored.Turns))
	}
}

// TestRewindWithoutWorktreeTruncatesOnly is the non-git thread: the
// conversation moves back and the interface has to say the files did not move.
func TestRewindWithoutWorktreeTruncatesOnly(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("plain")
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "a"})
	mustAppend(t, store, sess.ID, Turn{Role: "assistant", Text: "b"})
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "c"})

	res, err := store.Rewind(context.Background(), sess.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files {
		t.Error("a thread without a worktree has no files to restore")
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 {
		t.Errorf("turns = %d, want 2", len(got.Turns))
	}
}

// TestDeleteDropsCheckpoints keeps deleted threads from leaving reachable
// commits behind in the repository forever.
func TestDeleteDropsCheckpoints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess, err := store.Create("gone")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Ensure(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, wt, Branch(sess.ID)); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, wt, "a.txt", "one")
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "a"})
	mustAppend(t, store, sess.ID, Turn{Role: "assistant", Text: "b"})

	if cps, _ := store.Checkpoints(context.Background(), sess.ID); len(cps) == 0 {
		t.Fatal("no checkpoint to clean up")
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := gitOut(context.Background(), wt, nil, "for-each-ref", checkpointPrefix(sess.ID))
	if strings.TrimSpace(out) != "" {
		t.Errorf("checkpoint refs survived the delete: %s", out)
	}
}

// TestRewindNeedsACheckpoint: a turn that never finished (a user prompt with no
// answer yet) has no snapshot, and the rewind must say so instead of truncating
// the transcript against files that stay where they are.
func TestRewindNeedsACheckpoint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess, err := store.Create("half")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Ensure(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, wt, Branch(sess.ID)); err != nil {
		t.Fatal(err)
	}
	// A user turn with no answer: no snapshot for it.
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "还没答"})

	if _, err := store.Rewind(context.Background(), sess.ID, 1); !errors.Is(err, ErrNoCheckpoint) {
		t.Errorf("err = %v, want ErrNoCheckpoint", err)
	}
	if got, _ := store.Get(sess.ID); len(got.Turns) != 1 {
		t.Errorf("a refused rewind must not truncate: %d turns", len(got.Turns))
	}
}

// TestRewindRejectsBadTurn keeps the transcript from being truncated to a
// length it never had.
func TestRewindRejectsBadTurn(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("bad")
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "a"})
	if _, err := store.Rewind(context.Background(), sess.ID, 9); !errors.Is(err, ErrBadTurn) {
		t.Errorf("err = %v, want ErrBadTurn", err)
	}
}

// TestRemovingTheWorktreeClearsCheckpoints walks the order every caller uses:
// remove the worktree, then delete the session row. The snapshots have to be
// cleared at removal time — once the directory is gone, `git -C <dir>` cannot
// run and the refs stay pinned forever.
func TestRemovingTheWorktreeClearsCheckpoints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newGitRepo(t)
	w, err := OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess, err := store.Create("gone")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Ensure(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, wt, Branch(sess.ID)); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, wt, "a.txt", "one")
	mustAppend(t, store, sess.ID, Turn{Role: "user", Text: "a"})
	mustAppend(t, store, sess.ID, Turn{Role: "assistant", Text: "b"})
	if cps, _ := store.Checkpoints(context.Background(), sess.ID); len(cps) == 0 {
		t.Fatal("no checkpoint was recorded to clean up")
	}

	if err := w.Remove(context.Background(), sess.ID); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	out, _ := gitOut(context.Background(), repo, nil, "for-each-ref", checkpointPrefix(sess.ID))
	if strings.TrimSpace(out) != "" {
		t.Errorf("checkpoint refs survived the worktree removal: %s", out)
	}
}
