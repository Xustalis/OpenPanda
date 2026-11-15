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
	ctx := context.Background()
	origin, worker := newCore(t, "audit-origin"), newCore(t, "audit-worker")
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	seedAcceptWorkPair(t, ctx, origin, worker, id)
	row, err := origin.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stale := row.AttemptID
	newer, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := origin.db.ExecContext(ctx, "UPDATE tasks SET isolate=1,attempt_id=? WHERE task_id=?", newer, id); err != nil {
		t.Fatal(err)
	}
	env, err := bus.NewEnvelope(bus.MsgTaskResume, worker.nodeID, origin.nodeID, bus.TaskResumePayload{TaskID: id, AttemptID: stale, Accept: true})
	if err != nil {
		t.Fatal(err)
	}
	origin.handleResume(ctx, env)
	row, err = origin.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateReview || row.DeliveryAcceptAttempt != "" {
		t.Fatalf("stale delivery changed newer attempt: state=%s accepted=%q", row.State, row.DeliveryAcceptAttempt)
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
