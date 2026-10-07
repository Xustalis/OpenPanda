// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
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
