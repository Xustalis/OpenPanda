// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"errors"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/plan"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

func TestWorkPlanOperationApproval(t *testing.T) {
	e, _, ctx := newStageTestEngine(t)
	t.Cleanup(e.Close)
	e.cfg.Approval.Mode = config.ApprovalModeOnRequest
	e.decisions = map[string]string{}
	p := plan.Plan{Goal: "prepare business work", Stages: []plan.Stage{{ID: "prepare", Intent: "prepare"}, {ID: "verify", Intent: "verify", Needs: []string{"prepare"}}}}
	req := core.ExecutionRequest{RequestID: "business-1", TaskKey: "local/local/local/1", TriggerKey: "business-1", Purpose: "manual", ApprovalMode: "after_execution"}
	q := core.DefaultQueueSpec()
	q.SessionID = "session-1"
	if err := e.RememberApproval(q.SessionID, "", projectstore.ScopeSession, projectstore.DecisionDeny); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.StartWorkPlan(ctx, req, p, q); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("remembered denial: %v", err)
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM execution_requests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("denial published %d requests: %v", n, err)
	}
	if err := e.ClearApproval(q.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	x, _, err := e.StartWorkPlan(ctx, req, p, q)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.db.Query(`SELECT authorized FROM tasks WHERE plan_id=?`, x.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n = 0
	for rows.Next() {
		var authorized bool
		if err := rows.Scan(&authorized); err != nil {
			t.Fatal(err)
		}
		if authorized {
			t.Fatal("business approval mode granted operation authorization")
		}
		n++
	}
	if err := rows.Err(); err != nil || n != 2 {
		t.Fatalf("stages=%d err=%v", n, err)
	}
}
