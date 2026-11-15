// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func TestPrivateWorkReadsRequireCurrentTaskGrant(t *testing.T) {
	for _, mutate := range []string{
		`DELETE FROM task_snapshots`,
		`UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.permissions.read',json('false'))`,
		`UPDATE task_connections SET spaces_json='[]'`,
		`UPDATE task_connections SET scopes_json='[]'`,
		`UPDATE task_connections SET state='revoked'`,
		`UPDATE task_connections SET auth_epoch='changed'`,
		`UPDATE task_connections SET account_id='other'`,
	} {
		t.Run(mutate, func(t *testing.T) {
			x := completionTest(t, taskproviders.BeforeSubmit)
			ctx := context.Background()
			if w, err := x.s.Work(ctx, x.f.ID); err != nil || w.Prepared == "" {
				t.Fatalf("initial work=%+v err=%v", w, err)
			}
			if _, err := x.s.db.Exec(mutate); err != nil {
				t.Fatal(err)
			}
			if w, err := x.s.Work(ctx, x.f.ID); err == nil {
				t.Fatalf("private work leaked: %+v", w)
			}
			if runs, err := x.s.Runs(ctx, "", ""); err != nil || len(runs) != 0 {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
		})
	}
}

func TestWorkRefreshAndExportIncludeFactsWithoutProofs(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	f, err := x.s.flow(context.Background(), x.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ensureDelivery(context.Background(), f, x.c); err != nil {
		t.Fatal(err)
	}
	// A new service simulates refreshing the UI or reopening a CLI. It reads
	// only persisted content and never needs an action/model client.
	s := New(x.s.db, "")
	w, err := s.Work(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "signature") || strings.Contains(string(raw), "proof") || strings.Contains(string(raw), "body_json") {
		t.Fatalf("private protocol material: %s", raw)
	}
	if len(w.Actions) != 1 || len(w.Deliveries) != 1 || len(w.Sync) != 1 {
		t.Fatalf("refresh=%+v", w)
	}
	for _, want := range []string{"Prepared file content", "Verification:", "File:", "Action:", "Sync:"} {
		if !strings.Contains(w.Export(), want) {
			t.Fatalf("export missing %q", want)
		}
	}
}

func TestResearchReadIsPrivateAndVersionBound(t *testing.T) {
	s, _, c, _, wire := automationTest(t, false)
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO research_briefs(cache_key,subject,connection_id,account_id,task_key,input_revision,auth_epoch,memory_hash,model_version,state,brief,reserved_tokens,day,created_at,expires_at) VALUES('brief','subject',?,?,?,?,?,'hash','model','done','private brief',2300,'day',?,?)`, c.ID, c.AccountID, wire.Key.String(), "i1", c.AuthEpoch, time.Now().UnixMilli(), time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	briefs, err := s.Research(ctx, c.ID, wire.Key.String())
	if err != nil || len(briefs) != 1 || briefs[0].Brief != "private brief" {
		t.Fatalf("briefs=%+v err=%v", briefs, err)
	}
	if _, err = s.db.Exec(`UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.inputRevision','i2')`); err != nil {
		t.Fatal(err)
	}
	briefs, err = s.Research(ctx, c.ID, wire.Key.String())
	if err != nil || len(briefs) != 0 {
		t.Fatalf("stale brief=%+v err=%v", briefs, err)
	}
	if _, err = s.db.Exec(`UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.assigneeAccountIds',json('["other"]'))`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Research(ctx, c.ID, wire.Key.String()); err == nil {
		t.Fatal("assigned task exposed private brief")
	}
}
