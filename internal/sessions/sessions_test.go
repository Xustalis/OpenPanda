// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionProjectAssociation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create session without project
	s1, err := store.Create("Global Chat")
	if err != nil {
		t.Fatalf("Create s1: %v", err)
	}
	if s1.Project != "" {
		t.Errorf("s1.Project = %q, want empty", s1.Project)
	}

	// Create session with project
	s2, err := store.Create("Project Alpha Chat", "alpha")
	if err != nil {
		t.Fatalf("Create s2: %v", err)
	}
	if s2.Project != "alpha" {
		t.Errorf("s2.Project = %q, want alpha", s2.Project)
	}

	s3, err := store.Create("Project Beta Chat", "beta")
	if err != nil {
		t.Fatalf("Create s3: %v", err)
	}
	if s3.Project != "beta" {
		t.Errorf("s3.Project = %q, want beta", s3.Project)
	}

	_, err = store.Create("Another Alpha Chat", "alpha")
	if err != nil {
		t.Fatalf("Create s4: %v", err)
	}

	// List all
	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("List() len = %d, want 4", len(all))
	}

	// ListByProject "alpha"
	alphaList, err := store.ListByProject("alpha")
	if err != nil {
		t.Fatalf("ListByProject alpha: %v", err)
	}
	if len(alphaList) != 2 {
		t.Errorf("ListByProject(alpha) len = %d, want 2", len(alphaList))
	}

	// ListByProject unassigned ("")
	unassigned, err := store.ListByProject("")
	if err != nil {
		t.Fatalf("ListByProject empty: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].ID != s1.ID {
		t.Errorf("ListByProject() unassigned = %+v, want s1", unassigned)
	}

	// SetProject: move s1 to alpha
	if err := store.SetProject(s1.ID, "alpha"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	s1Loaded, err := store.Get(s1.ID)
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if s1Loaded.Project != "alpha" {
		t.Errorf("s1Loaded.Project = %q, want alpha", s1Loaded.Project)
	}

	// SetProject: disassociate s2
	if err := store.SetProject(s2.ID, ""); err != nil {
		t.Fatalf("SetProject empty: %v", err)
	}
	s2Loaded, err := store.Get(s2.ID)
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if s2Loaded.Project != "" {
		t.Errorf("s2Loaded.Project = %q, want empty", s2Loaded.Project)
	}
}

func TestLegacySessionWithoutProject(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write a legacy session JSON without project field
	legacyJSON := `{
  "id": "legacy1234567890",
  "title": "Legacy Session",
  "created_at": "2026-08-01T12:00:00Z",
  "updated_at": "2026-08-01T12:00:00Z",
  "turns": [
    {
      "role": "user",
      "text": "hello"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "legacy1234567890.json"), []byte(legacyJSON), 0o644); err != nil {
		t.Fatalf("Write legacy file: %v", err)
	}

	sess, err := store.Get("legacy1234567890")
	if err != nil {
		t.Fatalf("Get legacy session: %v", err)
	}
	if sess.Project != "" {
		t.Errorf("sess.Project = %q, want empty string for legacy file", sess.Project)
	}
	if sess.Title != "Legacy Session" {
		t.Errorf("sess.Title = %q, want Legacy Session", sess.Title)
	}

	// Make sure ListByProject("") includes it
	unassigned, err := store.ListByProject("")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].ID != "legacy1234567890" {
		t.Fatalf("unassigned = %+v, want legacy session", unassigned)
	}

	// Now associate it with a project
	if err := store.SetProject("legacy1234567890", "migrated-project"); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	migrated, err := store.Get("legacy1234567890")
	if err != nil {
		t.Fatalf("Get migrated: %v", err)
	}
	if migrated.Project != "migrated-project" {
		t.Errorf("migrated.Project = %q, want migrated-project", migrated.Project)
	}
}

func TestRenameProject(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	s1, err := store.Create("Session 1", "old-p")
	if err != nil {
		t.Fatalf("Create s1: %v", err)
	}
	s2, err := store.Create("Session 2", "old-p")
	if err != nil {
		t.Fatalf("Create s2: %v", err)
	}
	s3, err := store.Create("Session 3", "other-p")
	if err != nil {
		t.Fatalf("Create s3: %v", err)
	}

	n, err := store.RenameProject("old-p", "new-p")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if n != 2 {
		t.Errorf("RenameProject count = %d, want 2", n)
	}

	newList, err := store.ListByProject("new-p")
	if err != nil || len(newList) != 2 {
		t.Errorf("ListByProject(new-p) = %d, want 2", len(newList))
	}

	s1Loaded, err := store.Get(s1.ID)
	if err != nil || s1Loaded.Project != "new-p" {
		t.Errorf("s1.Project = %q, want new-p", s1Loaded.Project)
	}
	s2Loaded, err := store.Get(s2.ID)
	if err != nil || s2Loaded.Project != "new-p" {
		t.Errorf("s2.Project = %q, want new-p", s2Loaded.Project)
	}

	oldList, err := store.ListByProject("old-p")
	if err != nil || len(oldList) != 0 {
		t.Errorf("ListByProject(old-p) = %d, want 0", len(oldList))
	}

	s3Loaded, err := store.Get(s3.ID)
	if err != nil || s3Loaded.Project != "other-p" {
		t.Errorf("s3.Project = %q, want other-p", s3Loaded.Project)
	}
}

func TestForkCopiesPrefixAndTree(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	parent, err := store.Create("root thread", "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i, txt := range []string{"q1", "q2", "q3"} {
		if _, err := store.AppendTurn(parent.ID, Turn{Role: "user", Text: txt}); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
		if _, err := store.AppendTurn(parent.ID, Turn{Role: "assistant", Text: "a" + txt}); err != nil {
			t.Fatalf("AppendTurn a%d: %v", i, err)
		}
	}

	// Fork at turn 2: only the first exchange.
	child, err := store.Fork(parent.ID, 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child.ParentID != parent.ID || child.ForkIndex != 2 {
		t.Errorf("child ParentID/ForkIndex = %q/%d, want %q/2", child.ParentID, child.ForkIndex, parent.ID)
	}
	if len(child.Turns) != 2 {
		t.Fatalf("child turns = %d, want 2", len(child.Turns))
	}
	if child.Project != "proj" || child.Title != "root thread" {
		t.Errorf("child inherited project/title = %q/%q", child.Project, child.Title)
	}

	// Fork copies: appending to the child must not touch the parent.
	if _, err := store.AppendTurn(child.ID, Turn{Role: "user", Text: "child-only"}); err != nil {
		t.Fatalf("child AppendTurn: %v", err)
	}
	p2, err := store.Get(parent.ID)
	if err != nil || len(p2.Turns) != 6 {
		t.Errorf("parent turns after child append = %d, want 6", len(p2.Turns))
	}

	// at <= 0 or beyond length copies everything.
	full, err := store.Fork(parent.ID, 0)
	if err != nil || len(full.Turns) != 6 {
		t.Errorf("full fork turns = %d, want 6", len(full.Turns))
	}

	// Children lists direct forks only, oldest first.
	kids, err := store.Children(parent.ID)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("Children = %d, want 2", len(kids))
	}
	if kids[0].ID != child.ID || kids[1].ID != full.ID {
		t.Errorf("Children order = %q,%q want %q,%q", kids[0].ID, kids[1].ID, child.ID, full.ID)
	}

	// A grandchild is not a direct child of the root.
	gc, err := store.Fork(child.ID, 0)
	if err != nil {
		t.Fatalf("grandchild fork: %v", err)
	}
	kids, _ = store.Children(parent.ID)
	if len(kids) != 2 {
		t.Errorf("Children after grandchild = %d, want 2", len(kids))
	}
	if gc.ParentID != child.ID {
		t.Errorf("grandchild parent = %q, want %q", gc.ParentID, child.ID)
	}
}

func TestCompactHistoryFoldsIntoSummary(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("long thread")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 10 exchanges × ~200 chars each — far over a small test budget.
	for i := 0; i < 10; i++ {
		body := string(rune('a'+i)) + strings.Repeat("x", 200)
		if _, err := store.AppendTurn(sess.ID, Turn{Role: "user", Text: "q" + body}); err != nil {
			t.Fatalf("AppendTurn u%d: %v", i, err)
		}
		if _, err := store.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "a" + body}); err != nil {
			t.Fatalf("AppendTurn a%d: %v", i, err)
		}
	}

	var got [][]Turn
	summarize := func(ctx context.Context, evicted []Turn) (string, error) {
		got = append(got, evicted)
		return "digest: " + evicted[0].Text[:3], nil
	}
	updated, err := store.CompactHistory(context.Background(), sess.ID, 500, summarize)
	if err != nil {
		t.Fatalf("CompactHistory: %v", err)
	}
	if updated.Summary == "" {
		t.Fatal("Summary empty after compaction")
	}
	if len(updated.Turns) >= 20 {
		t.Fatalf("turns not trimmed: %d", len(updated.Turns))
	}
	// Pair-aligned: kept thread must start on a user turn.
	if len(updated.Turns) > 0 && updated.Turns[0].Role != "user" {
		t.Errorf("kept thread starts on %q, want user", updated.Turns[0].Role)
	}
	if len(got) != 1 || len(got[0]) == 0 {
		t.Fatalf("summarizer saw %d evicted batches", len(got))
	}

	// Second compaction folds the prior digest forward: it must reach the
	// summarizer as the first evicted turn.
	for i := 0; i < 10; i++ {
		body := strings.Repeat("y", 200)
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "user", Text: "p" + body})
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "r" + body})
	}
	updated, err = store.CompactHistory(context.Background(), sess.ID, 500, summarize)
	if err != nil {
		t.Fatalf("CompactHistory 2: %v", err)
	}
	last := got[len(got)-1]
	if !strings.HasPrefix(last[0].Text, SummaryMarker) {
		t.Errorf("prior digest not folded forward; first evicted turn starts %q", last[0].Text[:min(30, len(last[0].Text))])
	}

	// Under budget: summarizer not called, session untouched.
	small, _ := store.Create("small")
	_, _ = store.AppendTurn(small.ID, Turn{Role: "user", Text: "hi"})
	before := len(got)
	if _, err := store.CompactHistory(context.Background(), small.ID, 500, summarize); err != nil {
		t.Fatalf("CompactHistory small: %v", err)
	}
	if len(got) != before {
		t.Error("summarizer called on an under-budget thread")
	}

	// A failing summarizer leaves the session intact.
	fail := func(ctx context.Context, evicted []Turn) (string, error) { return "", errors.New("no model") }
	untouched, err := store.CompactHistory(context.Background(), sess.ID, 10, fail)
	if err != nil || untouched == nil {
		t.Fatalf("CompactHistory fail: %v", err)
	}
}

// Turns appended while the summarizer is running must not be dropped: the
// digest only covers the turns it was shown, so the appended tail stays in
// the thread even when that leaves it over budget until the next pass.
func TestCompactHistoryKeepsAppendedTurns(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("concurrent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 6; i++ {
		body := strings.Repeat("x", 200)
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "user", Text: "q" + body})
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "a" + body})
	}

	entered := make(chan struct{})
	appended := make(chan struct{})
	summarize := func(ctx context.Context, evicted []Turn) (string, error) {
		close(entered) // signal: summarizer is mid-flight
		<-appended     // wait for the sneaky AppendTurn before returning
		return "digest", nil
	}
	// Run the compaction in a goroutine; once the summarizer is entered,
	// append a fresh turn — it must survive the write phase.
	done := make(chan *Session, 1)
	go func() {
		updated, _ := store.CompactHistory(context.Background(), sess.ID, 500, summarize)
		done <- updated
	}()
	<-entered
	newTurn := Turn{Role: "user", Text: "arrived mid-summarize"}
	if _, err := store.AppendTurn(sess.ID, newTurn); err != nil {
		t.Fatalf("AppendTurn mid-summarize: %v", err)
	}
	close(appended)
	updated := <-done
	if updated == nil {
		t.Fatal("CompactHistory returned nil")
	}
	last := updated.Turns[len(updated.Turns)-1]
	if last.Text != newTurn.Text {
		t.Errorf("appended turn lost during compaction; tail is %q", last.Text[:min(30, len(last.Text))])
	}
	if updated.Summary != "digest" {
		t.Errorf("Summary = %q, want digest", updated.Summary)
	}
}

// A second compaction racing the first must not be overwritten by a stale
// digest: whichever write lands second sees the other's Summary and yields.
func TestCompactHistoryCAS(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("race")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 6; i++ {
		body := strings.Repeat("x", 200)
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "user", Text: "q" + body})
		_, _ = store.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "a" + body})
	}
	entered := make(chan struct{})
	proceed := make(chan struct{})
	summarize := func(ctx context.Context, evicted []Turn) (string, error) {
		close(entered)
		<-proceed
		return "slow digest", nil
	}
	done := make(chan *Session, 1)
	go func() {
		updated, _ := store.CompactHistory(context.Background(), sess.ID, 500, summarize)
		done <- updated
	}()
	<-entered
	// A competing compaction finishes first and publishes its summary.
	winner, err := store.CompactHistory(context.Background(), sess.ID, 500,
		func(ctx context.Context, evicted []Turn) (string, error) { return "winner digest", nil })
	if err != nil || winner.Summary != "winner digest" {
		t.Fatalf("winner compaction: %v summary=%q", err, winner.Summary)
	}
	close(proceed) // let the slow summarizer return; its write must now yield
	loser := <-done
	if loser == nil {
		t.Fatal("loser compaction returned nil")
	}
	if loser.Summary != "winner digest" {
		t.Errorf("stale digest overwrote the winner: %q", loser.Summary)
	}
}

func TestPinnedSortsFirst(t *testing.T) {
	store := NewStore(t.TempDir())
	old, err := store.Create("old")
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	mid, err := store.Create("mid")
	if err != nil {
		t.Fatalf("create mid: %v", err)
	}
	fresh, err := store.Create("fresh")
	if err != nil {
		t.Fatalf("create fresh: %v", err)
	}
	if err := store.SetPinned(old.ID, true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len = %d, want 3", len(list))
	}
	// Pinned leads; the unpinned pair still sorts newest first.
	if list[0].ID != old.ID || !list[0].Pinned {
		t.Fatalf("list[0] = %s pinned=%v, want pinned %s", list[0].ID, list[0].Pinned, old.ID)
	}
	if list[1].ID != fresh.ID || list[2].ID != mid.ID {
		t.Fatalf("unpinned order = %s,%s, want %s,%s", list[1].ID, list[2].ID, fresh.ID, mid.ID)
	}

	// Unpinning drops it back into recency order.
	if err := store.SetPinned(old.ID, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	got, err := store.Get(old.ID)
	if err != nil || got.Pinned {
		t.Fatalf("after unpin pinned=%v err=%v", got.Pinned, err)
	}
	if err := store.SetPinned("nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPinned unknown id err = %v, want ErrNotFound", err)
	}
}

func TestStampsTrackFileChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	st0, err := store.Stamps()
	if err != nil || len(st0) != 0 {
		t.Fatalf("empty Stamps = %v err %v", st0, err)
	}
	sess, err := store.Create("x")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st1, err := store.Stamps()
	if err != nil || len(st1) != 1 || st1[0].ID != sess.ID {
		t.Fatalf("Stamps after create = %+v err %v", st1, err)
	}
	if _, err := store.AppendTurn(sess.ID, Turn{Role: "user", Text: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	st2, err := store.Stamps()
	if err != nil || len(st2) != 1 {
		t.Fatalf("Stamps after append = %+v err %v", st2, err)
	}
	if st1[0] == st2[0] {
		t.Fatal("append did not change the stamp — SSE would miss the write")
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	st3, err := store.Stamps()
	if err != nil || len(st3) != 0 {
		t.Fatalf("Stamps after delete = %+v err %v", st3, err)
	}
}

func TestInvalidIDsRejected(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"", "..", "../x", "a/b", `a\b`, "x/../../y"} {
		if err := store.Delete(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) err = %v, want ErrNotFound", id, err)
		}
		if _, err := store.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) err = %v, want ErrNotFound", id, err)
		}
	}
	// The traversal probe must never have touched the filesystem: plant a
	// .json sibling next to the store root and confirm it survives.
	outside := filepath.Join(filepath.Dir(store.root), "sibling.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("../sibling"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(../sibling) err = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("traversal delete removed a file outside the store root: %v", err)
	}
}
