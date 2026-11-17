// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

func TestIsolatedStaleAcceptanceCannotDeliverNewAttempt(t *testing.T) {
	// These attempts share a millisecond but have opposite random-tail order.
	// A receipt belongs to its exact attempt, regardless of lexical order or
	// the clock/generator used by the peer that minted it.
	const low = "019c0000-0000-7000-8000-000000000001"
	const high = "019c0000-0000-7000-8000-000000000002"
	for _, tc := range []struct {
		name, stored, receipt string
		isolate, accept       bool
	}{
		{"same-ms-receipt-sorts-before", high, low, true, false},
		{"same-ms-receipt-sorts-after", low, high, true, false},
		{"later-timestamp-receipt", low, "019c0000-0001-7000-8000-000000000001", true, false},
		{"opaque-receipt", low, "foreign-attempt", true, false},
		{"missing-receipt", low, "", true, false},
		{"current-attempt", low, low, true, true},
		{"legacy-both-empty-stays-parked", "", "", true, false},
		{"non-isolated-mirror", high, low, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			origin, worker := newCore(t, "audit-origin"), newCore(t, "audit-worker")
			const id = "audit-accept"
			seedAcceptWorkPair(t, ctx, origin, worker, id)
			if _, err := origin.db.ExecContext(ctx, "UPDATE tasks SET isolate=?,attempt_id=? WHERE task_id=?", tc.isolate, tc.stored, id); err != nil {
				t.Fatal(err)
			}
			env, err := bus.NewEnvelope(bus.MsgTaskResume, worker.nodeID, origin.nodeID, bus.TaskResumePayload{TaskID: id, AttemptID: tc.receipt, Accept: true})
			if err != nil {
				t.Fatal(err)
			}
			origin.handleResume(ctx, env)
			row, err := origin.store.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			want := StateReview
			if tc.accept {
				want = StateDone
			}
			if row.State != want || row.AttemptID != tc.stored || (!tc.accept && row.DeliveryAcceptAttempt != "") {
				t.Fatalf("receipt changed wrong attempt: state=%s want=%s attempt=%q accepted=%q", row.State, want, row.AttemptID, row.DeliveryAcceptAttempt)
			}
		})
	}
}

func TestIsolatedDirectoryReplacementPreservesConcurrentFiles(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "clean"
		if concurrent {
			name = "concurrent-file"
		}
		t.Run(name, func(t *testing.T) {
			c := newCore(t, "audit-replace")
			main := t.TempDir()
			writeFile(t, filepath.Join(main, "pkg", "nested", "old.txt"), "baseline")
			baseline, err := c.hashWorkTree(main)
			if err != nil {
				t.Fatal(err)
			}
			iso := t.TempDir()
			writeFile(t, filepath.Join(iso, "pkg"), "replacement")
			after, err := c.hashWorkTree(iso)
			if err != nil {
				t.Fatal(err)
			}
			if concurrent {
				writeFile(t, filepath.Join(main, "pkg", "keep.txt"), "user work")
			}
			_, conflicts, err := c.applyIsolatedChanges(iso, main, baseline, after)
			if err != nil {
				t.Fatal(err)
			}
			if concurrent {
				if len(conflicts) == 0 || readFile(t, filepath.Join(main, "pkg", "keep.txt")) != "user work" {
					t.Fatalf("concurrent file lost: conflicts=%v", conflicts)
				}
			} else {
				if len(conflicts) != 0 {
					t.Fatalf("unchanged directory blocked replacement: %v", conflicts)
				}
				if st, err := os.Stat(filepath.Join(main, "pkg")); err != nil || !st.Mode().IsRegular() {
					t.Fatalf("replacement missing: %v", err)
				}
				if got := readFile(t, filepath.Join(main, "pkg")); got != "replacement" {
					t.Fatal(got)
				}
			}
		})
	}
}
