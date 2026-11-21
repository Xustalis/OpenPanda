// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSynthesisGetsScopedBoundedAuditWithAcknowledgement(t *testing.T) {
	c, source, target, final := activeReviewFixture(t)
	ctx := context.Background()
	// Another plan can reuse the same stage names and can hold unrelated acknowledgements.
	_, err := c.store.db.ExecContext(ctx, "INSERT INTO tasks(task_id,plan_id,stage_id,state,owner_node,attempt_id,created_at,updated_at) VALUES('other-target','other-plan','target','done','other','attempt',1,1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.RecordEvent(ctx, "other-target", EvPlanIntervention, map[string]string{"reason": "not in this graph"}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.RecordEvent(ctx, "other-target", EvPlanAdjustmentRead, map[string]string{"decision_id": "d-32"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 34; i++ {
		d := map[string]any{"decision_id": fmt.Sprintf("d-%d", i), "round": i + 1, "target_stage": target.StageID, "kind": "adjust", "outcome": "queued", "reason": "ANTHROPIC_API_KEY=sk-test-123 " + strings.Repeat("a", 400)}
		if err := c.store.RecordEvent(ctx, source.TaskID, EvPlanIntervention, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.store.RecordEvent(ctx, target.TaskID, EvPlanAdjustmentRead, map[string]string{"decision_id": "d-33"}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.releasePlanStage(ctx, final, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.store.Get(ctx, final.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(loaded.Intent, "\n")
	var audit PlanAudit
	if err = json.Unmarshal([]byte(lines[len(lines)-1]), &audit); err != nil {
		t.Fatalf("synthesis prompt lost audit: %s", loaded.Intent)
	}
	if audit.Omitted != 2 || len(audit.Decisions) != 32 || audit.Decisions[0].DecisionID != "d-2" {
		t.Fatalf("unexpected page: %+v", audit)
	}
	before, last := audit.Decisions[30], audit.Decisions[31]
	if before.ReadEventID != 0 || last.ReadEventID <= last.EventID || last.Round != 34 || last.State != StateRunning || last.Target != target.StageID || last.Source != source.StageID {
		t.Fatalf("source/target evidence: %+v, %+v", before, last)
	}
	if strings.Contains(loaded.Intent, "sk-test-123") || strings.Contains(loaded.Intent, "not in this graph") || !strings.Contains(last.Reason, "[truncated]") {
		t.Fatal("unbounded, unrelated, or unredacted synthesis evidence")
	}
}
