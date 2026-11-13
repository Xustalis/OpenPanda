// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func TestTaskHandoffKeepsReportAndOpenItems(t *testing.T) {
	task := core.Task{State: core.StateDone, ResultJSON: `{"stdout":"## Changes\nFixed it.\n\n## Verification\nTests passed.\n\n## Remaining\nDeploy manually.","followup":"Deploy manually."}`}
	body, remaining := taskHandoff(task, i18n.English)
	if !strings.Contains(body, "## Changes") || !strings.Contains(body, "Tests passed.") || remaining != "Deploy manually." {
		t.Fatalf("handoff lost evidence: %q %q", body, remaining)
	}
	task.ResultJSON = `{"stdout":"done","verdict_reason":"all good"}`
	if _, remaining := taskHandoff(task, i18n.English); remaining != "" {
		t.Fatalf("success verdict is not an open item: %q", remaining)
	}
	for _, state := range []string{core.StateDone, core.StateFailed, core.StateReview} {
		body, _ := taskHandoff(core.Task{State: state}, i18n.English)
		if strings.TrimSpace(body) == "" {
			t.Fatalf("empty task %s was silent", state)
		}
	}
	if got := taskRemainingNote(i18n.English, core.StateDone, "partial"); !strings.Contains(got, "still required") {
		t.Fatal(got)
	}
}

func TestTaskHandoffStepsOnlyUsesRecordedActions(t *testing.T) {
	steps := taskHandoffSteps([]core.Event{
		{Type: "agent_event", DataJSON: `{"ev":"thinking","thinking":"reasoning"}`},
		{Type: "agent_event", DataJSON: `{"ev":"tool_use","name":"Bash","input":{"command":"go test ./..."}}`},
		{Type: "agent_event", DataJSON: `{"ev":"tool_use","name":"Bash","input":{"command":"go test ./..."}}`},
		{Type: "agent_event", DataJSON: `{"ev":"tool_use","name":"Read","input":{"file_path":"README.md"}}`},
		{Type: "agent_event", DataJSON: `invalid`},
	})
	if len(steps) != 2 || steps[0] != "Bash · go test ./..." || steps[1] != "Read · README.md" {
		t.Fatalf("unexpected execution evidence: %v", steps)
	}
}
