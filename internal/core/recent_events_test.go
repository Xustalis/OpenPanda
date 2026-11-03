// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"testing"
)

// The live chat row asks for the tail of a long run, not its whole history.
// The rows must come back oldest-first so the timeline can append them
// directly, and the newest row must be the last one.
func TestRecentEventsReturnsTailChronologically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "recent events", "node")
	for i := 0; i < 10; i++ {
		if err := s.RecordEvent(ctx, tk.TaskID, "progress", map[string]any{"note": i}); err != nil {
			t.Fatalf("record event %d: %v", i, err)
		}
	}
	got, err := s.RecentEvents(ctx, tk.TaskID, 3)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("recent events = %d rows, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Fatalf("rows not chronological: %d then %d", got[i-1].ID, got[i].ID)
		}
	}
	var last struct {
		Note int `json:"note"`
	}
	if err := json.Unmarshal([]byte(got[len(got)-1].DataJSON), &last); err != nil {
		t.Fatalf("decode last row: %v", err)
	}
	if last.Note != 9 {
		t.Fatalf("last note = %d, want 9", last.Note)
	}
}
