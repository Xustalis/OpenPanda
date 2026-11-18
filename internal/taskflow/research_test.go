// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskhub"
)

func TestResearchContinuesAfterConnectionFailure(t *testing.T) {
	for _, kind := range []string{"budget", "storage_failure"} {
		t.Run(kind, func(t *testing.T) {
			s, e, c, p, wire := automationTest(t, false)
			ctx := context.Background()
			p.DailyTokens = 2300
			if err := s.SetPolicy(ctx, c.ID, "s", p); err != nil {
				t.Fatal(err)
			}
			snapshot, err := wire.Snapshot(1, "Space")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "budget" {
				if claimed, err := e.reserveBudget(ctx, "owner", c, p, snapshot, "previous-budget-reservation", "memory", "model", 2300, nil); err != nil || !claimed {
					t.Fatalf("initial budget reservation: %v, %v", claimed, err)
				}
			} else if _, err = s.db.Exec(`CREATE TRIGGER reject_c_research BEFORE INSERT ON research_briefs WHEN NEW.connection_id='c' BEGIN SELECT RAISE(ABORT,'injected reservation failure'); END`); err != nil {
				t.Fatal(err)
			}
			columns := strings.TrimPrefix(connectionColumns, "connection_id,")
			if _, err = s.db.Exec(`INSERT INTO task_connections(connection_id,`+columns+`,updated_at) SELECT 'z',`+columns+`,updated_at FROM task_connections WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO task_policies SELECT 'z',account_id,space_id,policy_json,updated_at FROM task_policies WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO task_snapshots SELECT 'z',account_id,task_key,snapshot_json,auth_epoch,updated_at FROM task_snapshots WHERE connection_id=?`, c.ID); err != nil {
				t.Fatal(err)
			}
			tasks, err := taskhub.New(s.db).List(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 2 || tasks[0].ConnectionID != c.ID || tasks[1].ConnectionID != "z" {
				t.Fatal("fixture must encounter blocked research first")
			}
			automationServer(t, s, c, wire, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
			model := &auxiliaryTestModel{}
			e.options = AutomationOptions{
				Model:         func() AuxiliaryModel { return model },
				PrivateMemory: func() ([]string, error) { return []string{"solar energy research experience"}, nil },
			}
			for poll := 0; poll < 2; poll++ {
				err = e.researchOnce(ctx, "owner")
				if kind == "budget" && err != nil {
					t.Errorf("budget wait aborted research: %v", err)
				}
				if kind == "storage_failure" && (err == nil || !strings.Contains(err.Error(), "injected reservation failure")) {
					t.Errorf("storage failure silently discarded: %v", err)
				}
			}
			if n := scalar(t, s.db, `SELECT COUNT(*) FROM research_briefs WHERE connection_id='z' AND state='done'`); n != "1" || model.calls.Load() != 1 {
				t.Fatalf("independent research starved or replayed across polls: briefs=%s calls=%d", n, model.calls.Load())
			}
			reserved := scalar(t, s.db, `SELECT COALESCE(SUM(reserved_tokens),0) FROM research_briefs WHERE connection_id='c'`)
			if kind == "budget" && reserved != "2300" || kind == "storage_failure" && reserved != "0" {
				t.Fatalf("budget was bypassed or failed reservation committed: %s", reserved)
			}
		})
	}
}
