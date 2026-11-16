// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/objectives"
)

func TestTaskInputPreservesModelObjectives(t *testing.T) {
	p := &objectives.Policy{Version: 1, Revision: 3, Dimensions: []objectives.Dimension{{Dimension: "quality", Weight: 1, Priority: objectives.Max, Requirement: "User correction requires full validation", Reason: "Latest explicit requirement", Criteria: []string{"Regression evidence"}}}}
	in := toTaskInput(&entry.TaskSpec{Title: "deliver", Spec: entry.TaskSpecDetail{Objectives: p}})
	got, err := objectives.FromSpecJSON(in.SpecJSON)
	if err != nil || got == nil || got.Revision != 3 || !objectives.HasMax(got) {
		t.Fatalf("lost objectives: %s %v", in.SpecJSON, err)
	}
	in.SpecJSON = appendUserAcceptance(restrictAcceptanceToProject(in.SpecJSON, t.TempDir()), nil)
	if got, err = objectives.FromSpecJSON(in.SpecJSON); err != nil || got == nil || got.Revision != 3 {
		t.Fatal("acceptance filtering discarded objectives")
	}
}

func TestTaskSubmitRejectsMissingOrMalformedObjectivesBeforeDispatch(t *testing.T) {
	e := &Engine{}
	tool := e.dispatchTaskTool("deliver", AskScope{}, false, StreamCallbacks{}, nil)
	for _, policy := range []any{nil, map[string]any{"version": 1, "revision": 1, "dimensions": []any{}}, "not a policy"} {
		_, err := tool.Run(context.Background(), map[string]any{"title": "deliver", "target": "run checks", "abilities": []any{"agent:codex"}, "objectives": policy})
		if err == nil || !strings.Contains(err.Error(), "objective") {
			t.Fatalf("invalid generated policy entered dispatch: %v", err)
		}
	}
}

// Protocol-complete fake provider contract. NORMAL does not fake acceptance.
func fixtureTaskObjectives() *objectives.Policy {
	return &objectives.Policy{Version: 1, Revision: 1, Dimensions: []objectives.Dimension{{Dimension: "delivery", Weight: 1, Priority: objectives.Normal, Requirement: "Complete the fixture task", Reason: "Fixture user request", Criteria: []string{"Observe the expected result"}}}}
}
