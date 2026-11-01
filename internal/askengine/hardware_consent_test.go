// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

func TestHardwarePlanConsentPersistsForAllStages(t *testing.T) {
	for _, tc := range []struct {
		name                string
		marked, grant, want bool
		mode                string
	}{
		{"hardware owner grant", true, true, true, config.ApprovalModeOnRequest},
		{"ordinary explicit grant unchanged", false, true, false, config.ApprovalModeOnRequest},
		{"hardware marker without grant", true, false, false, config.ApprovalModeOnRequest},
		{"always mode still withholds", true, true, false, config.ApprovalModeAlways},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store, ctx := newStageTestEngine(t)
			e.cfg.Approval.Mode = tc.mode
			e.queueTasks = true
			if tc.marked {
				ctx = WithHardwarePlanConsent(ctx)
			}
			spec := &entry.PlanSpec{Goal: "two-stage fixture", Stages: []entry.PlanStageSpec{
				{ID: "first", Intent: "produce fixture"},
				{ID: "later", Intent: "consume fixture", Needs: []string{"first"}},
			}}
			out, err := e.startClassifiedPlan(ctx, spec, AskScope{SessionID: "hardware-fixture"}, tc.grant, StreamCallbacks{})
			if err != nil || out == nil || out.PlanID == "" {
				t.Fatalf("start = %+v, %v", out, err)
			}
			rows, err := store.PlanStages(ctx, out.PlanID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("stages = %+v, %v", rows, err)
			}
			for _, row := range rows {
				if row.Authorized != tc.want {
					t.Fatalf("stage %s authorized=%v want %v", row.StageID, row.Authorized, tc.want)
				}
				if row.StageID == "later" && row.State != core.StateSubmitted {
					t.Fatalf("dependent stage prematurely released: %s", row.State)
				}
			}
			// The per-request marker must not mutate a shared engine or its next ask.
			if hardwarePlanConsent(context.Background()) {
				t.Fatal("consent leaked to a different request")
			}
		})
	}
}

func TestHardwarePlanConsentRespectsRememberedDeny(t *testing.T) {
	e, _, ctx := newStageTestEngine(t)
	e.queueTasks = true
	e.cfg.Approval.Mode = config.ApprovalModeOnRequest
	e.decisions = map[string]string{decisionKey("hardware-denied", ""): projectstore.DecisionDeny}
	out, err := e.startClassifiedPlan(WithHardwarePlanConsent(ctx), &entry.PlanSpec{
		Goal: "denied fixture", Stages: []entry.PlanStageSpec{{ID: "first", Intent: "must not start"}},
	}, AskScope{SessionID: "hardware-denied"}, true, StreamCallbacks{})
	if err != nil || out == nil || !out.Denied || out.PlanID != "" {
		t.Fatalf("deny = %+v, %v", out, err)
	}
	var count int
	if err := e.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied plan created %d rows: %v", count, err)
	}
}
