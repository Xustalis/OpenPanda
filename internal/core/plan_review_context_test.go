// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestPlanReviewContextBindsOriginGraph(t *testing.T) {
	c, source, final := resumePlanFixture(t)
	ctx := context.Background()
	options := func(stage Task) string { return c.planReviewOptions(ctx, stage, entry.SuperviseOptions{}).PlanContext }
	if got := options(source); got != "" {
		t.Fatalf("unmanaged plan exposed graph: %s", got)
	}
	final.SpecJSON = `{"plan_final":true}`
	if _, err := c.db.Exec("UPDATE tasks SET spec_json=? WHERE task_id=?", final.SpecJSON, final.TaskID); err != nil {
		t.Fatal(err)
	}
	snapshot := options(source)
	for _, field := range []string{`"source_stage":"work"`, `"goal":"resume"`, `"id":"check"`, `"state":"submitted"`, `"needs":["work"]`, `"final":true`} {
		if !strings.Contains(snapshot, field) {
			t.Errorf("missing %s in %s", field, snapshot)
		}
	}
	if got := options(final); got != "" {
		t.Fatalf("final stage requested changes to the graph: %s", got)
	}
	source.Remote = true
	if got := options(source); got != "" {
		t.Fatalf("remote stage exposed local graph: %s", got)
	}
}
