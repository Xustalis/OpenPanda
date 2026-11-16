// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestAskScopeRosterRidesTaskSpec pins the hand-off that makes the composer's
// team pick real: the roster arrives on AskScope and must land in the
// classified task's spec_json, because that is what the executor routes from
// (and what a delegated task carries to another node). A roster that only
// lived in the request would be advice, not a restriction.
func TestAskScopeRosterRidesTaskSpec(t *testing.T) {
	taskJSON := `{"kind":"task","task":{"title":"改一处代码","context_type":"file","requires":{"abilities":["code:modify"]},"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"delivery","weight":1,"priority":"NORMAL","requirement":"Complete the fixture task","reason":"The fixture requests a task","criteria":["Observe the expected fixture result"]}]},"scope":"internal","target":"改一处代码","constraints":[],"success_definition":"改完"},"complexity":0.3,"risk":"low","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short"}}}`
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(taskJSON))
		_, _ = w.Write(b)
	})

	roster := []string{"claude_code", "opencode"}
	res, err := e.AskTurnsScoped(context.Background(), nil, "改一下这个函数", AskScope{
		WorkDir: t.TempDir(),
		Agents:  roster,
	}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want a queued task", res)
	}

	task, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var spec entry.TaskSpecDetail
	if err := json.Unmarshal([]byte(task.SpecJSON), &spec); err != nil {
		t.Fatalf("spec json %q: %v", task.SpecJSON, err)
	}
	if len(spec.AllowAgents) != len(roster) {
		t.Fatalf("allow_agents = %v, want %v", spec.AllowAgents, roster)
	}
	for i, name := range roster {
		if spec.AllowAgents[i] != name {
			t.Fatalf("allow_agents = %v, want %v (order preserved)", spec.AllowAgents, roster)
		}
	}
}
