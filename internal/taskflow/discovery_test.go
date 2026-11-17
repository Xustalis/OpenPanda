// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskhub"
)

func TestDiscoveryContinuesAfterConnectionFailure(t *testing.T) {
	for _, kind := range []string{"capacity", "storage_failure"} {
		t.Run(kind, func(t *testing.T) {
			s, e, c, p, wire := automationTest(t, true)
			ctx := context.Background()
			p.MaxConcurrent = 1
			if err := s.SetPolicy(ctx, c.ID, "s", p); err != nil {
				t.Fatal(err)
			}
			_ = discoverTestFlow(t, s, e)
			wire.Key.TaskID = "u"
			snapshot, err := wire.Snapshot(1, "Space")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES(?,?,?,?,?,0)`, c.ID, c.AccountID, snapshot.Key.String(), string(raw), c.AuthEpoch); err != nil {
				t.Fatal(err)
			}
			columns := strings.TrimPrefix(connectionColumns, "connection_id,")
			if _, err = s.db.Exec(`INSERT INTO task_connections(connection_id,`+columns+`,updated_at) SELECT 'z',`+columns+`,updated_at FROM task_connections WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO task_policies SELECT 'z',account_id,space_id,policy_json,updated_at FROM task_policies WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO task_snapshots SELECT 'z',account_id,task_key,snapshot_json,auth_epoch,updated_at FROM task_snapshots WHERE connection_id=? AND task_key!=?`, c.ID, snapshot.Key.String()); err != nil {
				t.Fatal(err)
			}
			if kind == "storage_failure" {
				if _, err = s.db.Exec(`UPDATE task_flow_runs SET state='succeeded'`); err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec(`CREATE TRIGGER reject_c_run BEFORE INSERT ON task_flow_runs WHEN NEW.connection_id='c' BEGIN SELECT RAISE(ABORT,'injected run failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			tasks, err := taskhub.New(s.db).List(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			blocked, independent := -1, -1
			for i, task := range tasks {
				if task.ConnectionID == c.ID && task.Snapshot.Key == snapshot.Key {
					blocked = i
				}
				if task.ConnectionID == "z" {
					independent = i
				}
			}
			if blocked < 0 || independent <= blocked {
				t.Fatal("fixture must encounter the blocked connection first")
			}
			err = e.discover(ctx, "owner")
			if kind == "capacity" && err != nil {
				t.Errorf("capacity wait aborted discovery: %v", err)
			}
			if kind == "storage_failure" && err == nil {
				t.Error("storage failure silently discarded")
			}
			if n := scalar(t, s.db, `SELECT COUNT(*) FROM task_flow_runs WHERE connection_id='z'`); n != "1" {
				t.Fatalf("blocked connection starved independent work: %s", n)
			}
			if n := scalar(t, s.db, `SELECT COUNT(*) FROM task_flow_runs WHERE connection_id='c'`); n != "1" {
				t.Fatalf("capacity or failed write bypassed: %s", n)
			}
		})
	}
}
