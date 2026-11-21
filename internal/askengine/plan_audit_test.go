// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"strings"
	"testing"
)

func TestDetachedPlanPublishesAuditedDeliveryOnce(t *testing.T) {
	for _, withdrawn := range []bool{true, false} {
		t.Run(fmt.Sprintf("withdrawn=%t", withdrawn), func(t *testing.T) {
			e, store, ctx := newStageTestEngine(t)
			root := t.TempDir()
			st := sessions.NewStore(root)
			sess, err := st.Create("run plan")
			if err != nil {
				t.Fatal(err)
			}
			service := deepManualController(t, e, st)
			const round = "r-dag"
			if err := store.BeginAskRound(ctx, round, sess.ID, "en"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.db.Exec("INSERT INTO conversation_controls(session_id) VALUES(?)", sess.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.db.Exec("INSERT INTO conversation_plan_runs(session_id,version,proposal,status,round_id) VALUES(?,1,'implement','running',?)", sess.ID, round); err != nil {
				t.Fatal(err)
			}
			e.queueTasks = true
			res, err := e.startClassifiedPlan(core.WithAskRound(ctx, round), &entry.PlanSpec{Goal: "deliver implementation", Stages: []entry.PlanStageSpec{{ID: "inspect", Intent: "inspect"}, {ID: "extra", Intent: "extra check"}}}, AskScope{SessionID: sess.ID}, false, StreamCallbacks{})
			if err != nil || !res.OK {
				t.Fatalf("start: %+v, %v", res, err)
			}
			rows, err := store.PlanStages(ctx, res.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 {
				t.Fatalf("expected two branches and synthesis, got %d", len(rows))
			}
			var source, target string
			for _, row := range rows {
				switch row.StageID {
				case "extra":
					target = row.TaskID
				case "inspect":
					source = row.TaskID
				}
			}
			if _, err = st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Kind: "plan", Text: "implementation queued", RoundID: round, IdempotencyKey: "plan:assistant:" + round}); err != nil {
				t.Fatal(err)
			}
			if _, err = e.db.Exec("UPDATE tasks SET state='done',verification='verified',result_json=? WHERE plan_id=?", `{"stdout":"synthesized response"}`, res.PlanID); err != nil {
				t.Fatal(err)
			}
			// Ordinary cancellation leaves any old result in place. Only the origin's
			// cancellation event identifies work the reviewer intentionally withdrew.
			if _, err = e.db.Exec("UPDATE tasks SET state='cancelled',result_json=? WHERE task_id=?", `{"plan_withdrawn":true,"decision_id":"prior"}`, target); err != nil {
				t.Fatal(err)
			}
			cancelled := map[string]any{"from": "running"}
			if withdrawn {
				cancelled = map[string]any{"plan_withdrawn": true, "decision_id": "d-close"}
			}
			if err = store.RecordEvent(ctx, target, core.EvCancel, cancelled); err != nil {
				t.Fatal(err)
			}
			if err = store.RecordEvent(ctx, source, core.EvPlanIntervention, map[string]any{"decision_id": "d-first", "target_stage": "extra", "kind": "adjust", "outcome": "queued", "reason": "schema changed"}); err != nil {
				t.Fatal(err)
			}
			if err = store.RecordEvent(ctx, target, core.EvPlanAdjustmentRead, map[string]string{"decision_id": "d-first"}); err != nil {
				t.Fatal(err)
			}
			if withdrawn {
				if err = store.RecordEvent(ctx, source, core.EvPlanIntervention, map[string]any{"decision_id": "d-close", "target_stage": "extra", "kind": "close", "outcome": "closed", "reason": "redundant check"}); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.CloseAskRound(ctx, round, "closed"); err != nil {
				t.Fatal(err)
			}
			service.sweepPlans(ctx)
			// Simulate replay after the file write but before the settlement commit.
			if _, err = e.db.Exec("UPDATE conversation_plan_runs SET status='running' WHERE round_id=?", round); err != nil {
				t.Fatal(err)
			}
			service.sweepPlans(ctx)
			var status string
			if err = e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE round_id=?", round).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if withdrawn {
				want = "done"
			}
			if status != want {
				t.Fatalf("status=%s, want %s", status, want)
			}
			plans, err := store.ListPlans(ctx)
			if err != nil || len(plans) != 1 || (plans[0].Withdrawn == 1) != withdrawn || plans[0].Done != 2 || plans[0].Stages != 3 {
				t.Fatalf("plan progress=%+v err=%v", plans, err)
			}
			refreshed, err := sessions.NewStore(root).Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(refreshed.Turns) != 2 {
				t.Fatalf("persisted turns: %+v", refreshed.Turns)
			}
			delivery := refreshed.Turns[1]
			if delivery.IdempotencyKey != "plan:delivery:"+round+":"+res.PlanID || !strings.Contains(delivery.Text, "schema changed") || !strings.Contains(delivery.Text, "#") {
				t.Fatalf("delivery: %+v", delivery)
			}
			if withdrawn {
				if !strings.Contains(delivery.Text, "synthesized response") || !strings.Contains(delivery.Text, "redundant check") || delivery.Kind != "answer" {
					t.Fatalf("delivery: %+v", delivery)
				}
			} else if delivery.Kind != "error" || strings.Contains(delivery.Text, "synthesized response") {
				t.Fatalf("delivery: %+v", delivery)
			}
		})
	}
}
