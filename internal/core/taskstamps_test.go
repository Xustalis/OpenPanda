// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
)

// TaskStamps is the SSE digest's read path — it must see every task's
// id/state/updated_at tuple without touching payload columns.
func TestTaskStamps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if stamps, err := s.TaskStamps(ctx); err != nil || len(stamps) != 0 {
		t.Fatalf("empty store stamps = %v, %v", stamps, err)
	}
	tk, err := s.Create(ctx, "", "", "probe task", "n1", []string{"n1"})
	if err != nil {
		t.Fatal(err)
	}
	// A payload column the digest must survive ignoring — the point of the
	// light projection is that this blob never crosses the wire per poll.
	big := ""
	for i := 0; i < 4096; i++ {
		big += "x"
	}
	if _, err := s.db.Exec(`UPDATE tasks SET spec_json=? WHERE task_id=?`, big, tk.TaskID); err != nil {
		t.Fatal(err)
	}
	stamps, err := s.TaskStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps) != 1 || stamps[0].ID != tk.TaskID {
		t.Fatalf("stamps = %+v", stamps)
	}
	if stamps[0].State != tk.State || stamps[0].UpdatedAt != tk.UpdatedAt {
		t.Fatalf("stamp mismatch: %+v vs task %q/%d", stamps[0], tk.State, tk.UpdatedAt)
	}
	// A mutation moves the stamp — that is what the digest watches.
	if err := s.ForceFail(ctx, tk.TaskID, "test"); err != nil {
		t.Fatal(err)
	}
	stamps2, err := s.TaskStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps2) != 1 || stamps2[0].State == stamps[0].State {
		t.Fatalf("state flip invisible: %+v", stamps2)
	}
}

// ExistingTaskIDs is the dir-sweep's batched existence check. It must answer
// exactly for the ids that have rows, and keep answering past the 500-id
// batch boundary the IN clause chunks at.
func TestExistingTaskIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if live, err := s.ExistingTaskIDs(ctx, nil); err != nil || len(live) != 0 {
		t.Fatalf("nil input: %v, %v", live, err)
	}

	// Seed past the batch size so the second IN chunk is exercised: 600
	// known ids, of which only the first 300 get rows.
	var all, have []string
	for i := 0; i < 600; i++ {
		id := "task-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "-" + strings.Repeat("x", i%7)
		all = append(all, id)
		if i < 300 {
			have = append(have, id)
		}
	}
	for _, id := range have {
		if _, err := s.CreateWithID(ctx, id, "", "", "t", "n1", nil, false); err != nil {
			t.Fatal(err)
		}
	}

	live, err := s.ExistingTaskIDs(ctx, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != len(have) {
		t.Fatalf("live set = %d ids, want %d", len(live), len(have))
	}
	for _, id := range have {
		if !live[id] {
			t.Fatalf("live id %s missed", id)
		}
	}
	for _, id := range all[300:] {
		if live[id] {
			t.Fatalf("ghost id %s reported live", id)
		}
	}
}
