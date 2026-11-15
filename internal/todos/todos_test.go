// SPDX-License-Identifier: AGPL-3.0-or-later

package todos

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "todos.db"))
	return NewStore(db)
}

func TestCreateDefaultsToPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	got, err := s.Create(ctx, CreateInput{Title: "  写周报  ", Tags: []string{"工作", " 工作 ", ""}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Title != "写周报" {
		t.Fatalf("title = %q, want trimmed", got.Title)
	}
	if got.Status != StatusPending || got.Priority != PriorityNormal {
		t.Fatalf("status/priority = %s/%s, want pending/normal", got.Status, got.Priority)
	}
	// Duplicate tags collapse, blanks drop.
	if len(got.Tags) != 1 || got.Tags[0] != "工作" {
		t.Fatalf("tags = %v, want one 工作", got.Tags)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatal("timestamps not stamped")
	}
}

func TestBoardOrderPriorityThenDue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Created out of order on purpose: the board sorts, the writer does not.
	low, _ := s.Create(ctx, CreateInput{Title: "low", Priority: PriorityLow})
	high, _ := s.Create(ctx, CreateInput{Title: "high", Priority: PriorityHigh})
	dated, _ := s.Create(ctx, CreateInput{Title: "dated", DueAt: 1_700_000_000})
	normal, _ := s.Create(ctx, CreateInput{Title: "normal"})

	list, err := s.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []int64{high.ID, dated.ID, normal.ID, low.ID}
	if len(list) != len(want) {
		t.Fatalf("got %d rows, want %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Fatalf("position %d = %d, want %d (order: high, dated, normal, low)", i, list[i].ID, id)
		}
	}
}

func TestStatusTransitionsStampTimes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	todo, _ := s.Create(ctx, CreateInput{Title: "发货"})

	running := StatusRunning
	got, err := s.Patch(ctx, todo.ID, Patch{Status: &running})
	if err != nil {
		t.Fatalf("patch running: %v", err)
	}
	if got.StartedAt == 0 {
		t.Fatal("entering running must stamp started_at")
	}
	started := got.StartedAt

	// Pausing and resuming keeps the original start — "how long has this been
	// on my plate" must not reset every time it is parked.
	paused := StatusPaused
	if got, err = s.Patch(ctx, todo.ID, Patch{Status: &paused}); err != nil {
		t.Fatalf("patch paused: %v", err)
	}
	if got.StartedAt != started {
		t.Fatalf("paused started_at = %d, want %d", got.StartedAt, started)
	}
	if got, err = s.Patch(ctx, todo.ID, Patch{Status: &running}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got.StartedAt != started {
		t.Fatalf("resumed started_at = %d, want %d", got.StartedAt, started)
	}

	done := StatusDone
	if got, err = s.Patch(ctx, todo.ID, Patch{Status: &done}); err != nil {
		t.Fatalf("patch done: %v", err)
	}
	if got.DoneAt == 0 {
		t.Fatal("done must stamp done_at")
	}
	// Re-opening clears the completion stamp.
	pending := StatusPending
	if got, err = s.Patch(ctx, todo.ID, Patch{Status: &pending}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got.DoneAt != 0 {
		t.Fatalf("reopened done_at = %d, want 0", got.DoneAt)
	}
}

func TestPatchRejectsEmptyTitle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	todo, _ := s.Create(ctx, CreateInput{Title: "keep"})
	blank := "   "
	if _, err := s.Patch(ctx, todo.ID, Patch{Title: &blank}); err == nil {
		t.Fatal("empty title must be rejected")
	}
	if _, err := s.Patch(ctx, todo.ID+99, Patch{Title: &blank}); !errors.Is(err, ErrNotFound) && err == nil {
		t.Fatal("missing id must not report success")
	}
}

func TestReorderWritesSeq(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, CreateInput{Title: "a"})
	b, _ := s.Create(ctx, CreateInput{Title: "b"})
	c, _ := s.Create(ctx, CreateInput{Title: "c"})

	if err := s.Reorder(ctx, []int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	list, _ := s.List(ctx, Filter{})
	got := []int64{list[0].ID, list[1].ID, list[2].ID}
	want := []int64{c.ID, a.ID, b.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order after drag = %v, want %v", got, want)
		}
	}
}

func TestFiltersAndDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	one, _ := s.Create(ctx, CreateInput{Title: "写方案", Project: "panda", Tags: []string{"文档"}})
	_, _ = s.Create(ctx, CreateInput{Title: "买牛奶", Project: "home"})

	byProject, _ := s.List(ctx, Filter{Project: "panda"})
	if len(byProject) != 1 || byProject[0].ID != one.ID {
		t.Fatalf("project filter returned %d rows", len(byProject))
	}
	byQuery, _ := s.List(ctx, Filter{Query: "文档"})
	if len(byQuery) != 1 || byQuery[0].ID != one.ID {
		t.Fatalf("tag query returned %d rows", len(byQuery))
	}
	byStatus, _ := s.List(ctx, Filter{Status: StatusDone})
	if len(byStatus) != 0 {
		t.Fatalf("done filter returned %d rows, want 0", len(byStatus))
	}

	ok, err := s.Delete(ctx, one.ID)
	if err != nil || !ok {
		t.Fatalf("delete = %v/%v", ok, err)
	}
	if _, err := s.Get(ctx, one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
	if ok, _ := s.Delete(ctx, one.ID); ok {
		t.Fatal("second delete reported success")
	}
}

func TestFingerprintTracksEdits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	before, err := s.Fingerprint(ctx)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	todo, _ := s.Create(ctx, CreateInput{Title: "动一下"})
	created, _ := s.Fingerprint(ctx)
	if created == before {
		t.Fatal("creating a todo must change the fingerprint")
	}
	done := StatusDone
	if _, err := s.Patch(ctx, todo.ID, Patch{Status: &done}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	after, _ := s.Fingerprint(ctx)
	if after == created {
		t.Fatal("a status change must change the fingerprint")
	}
}
