package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// TestProgressForEvent pins the mapping from scheduler-core trace events to the
// CLI's live progress phases (P0 §1.4): the bridge in submitTask forwards these
// while a synchronous Submit blocks, so a delegated agent run shows routing →
// executing → judging instead of a frozen spinner. Events with no live-progress
// meaning must report ok=false so the caller stays silent.
func TestProgressForEvent(t *testing.T) {
	cases := []struct {
		name   string
		typ    string
		data   any
		want   Progress
		wantOK bool
	}{
		{"route with target", core.EvRouteDecision, map[string]any{"target_node": "pi-3b"}, Progress{Kind: ProgressRoute, Name: "pi-3b"}, true},
		{"route local falls back to action", core.EvRouteDecision, map[string]any{"action": "local"}, Progress{Kind: ProgressRoute, Name: "local"}, true},
		// The real route event stores the action as scheduler.Action — a named
		// string type. A plain .(string) assertion misses it, and the progress
		// line degrades to "routing to …" with nothing after the preposition.
		{"route local as named string type", core.EvRouteDecision, map[string]any{"action": scheduler.ActionLocal}, Progress{Kind: ProgressRoute, Name: "local"}, true},
		{"exec names the agent", core.EvExecAgentStart, map[string]any{"agent": "claude_code"}, Progress{Kind: ProgressExec, Name: "claude_code"}, true},
		{"exec falls back to adapter", core.EvExecAgentStart, map[string]any{"adapter": "codex"}, Progress{Kind: ProgressExec, Name: "codex"}, true},
		// Supervision rounds carry their position in the loop; renderers use it
		// to say "round 2/5" once a task goes multi-round.
		{"exec carries round and budget", core.EvExecAgentStart, map[string]any{"agent": "claude_code", "round": 2, "budget": 5}, Progress{Kind: ProgressExec, Name: "claude_code", Round: 2, Budget: 5}, true},
		{"exec carries model", core.EvExecAgentStart, map[string]any{"agent": "claude_code", "model": "deepseek-v4-flash"}, Progress{Kind: ProgressExec, Name: "claude_code", Model: "deepseek-v4-flash"}, true},
		{"model injection carries model and agent", core.EvModelInjection, map[string]any{"agent": "claude_code", "model": "deepseek-v4-flash"}, Progress{Kind: ProgressExec, Name: "claude_code", Model: "deepseek-v4-flash"}, true},
		// The round result records its verdict under verdict_status; reading
		// the wrong key leaves the status line "reviewing result ()…".
		{"judge carries verdict", core.EvSupervisionRound, map[string]any{"verdict_status": "done", "round": 1, "budget": 5}, Progress{Kind: ProgressJudge, Name: "done", Round: 1, Budget: 5}, true},
		// judge_start marks the moment the reviewing stage begins — the stage's
		// on-screen duration runs until the next event, so without it the
		// judge's runtime is billed to the executing stage.
		{"judge start opens the reviewing stage", core.EvJudgeStart, map[string]any{"round": 1, "budget": 3}, Progress{Kind: ProgressJudge, Round: 1, Budget: 3}, true},
		// The state-transition path marshals event data to JSON first, where
		// numbers decode as float64 — extraction must survive that too.
		{"agent progress notes tool action", core.EvProgress, map[string]any{"note": `Bash: curl -s "https://news.com"`}, Progress{Kind: ProgressTool, Name: `Bash: curl -s "https://news.com"`}, true},
		{"subagent event notes action", core.EvSubagentEvent, map[string]any{"note": "Task: analyze files"}, Progress{Kind: ProgressTool, Name: "Task: analyze files"}, true},
		{"unrelated event is silent", core.EvSubmit, map[string]any{}, Progress{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := progressForEvent(tc.typ, tc.data)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("progressForEvent(%q) = (%+v,%v), want (%+v,%v)",
					tc.typ, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestProgressStatusFallback guards the English OnStatus prose a caller gets
// when it supplies no structured sink: the verdict must ride along (the panel's
// SSE status line used to read "reviewing result ()…"), and multi-round runs
// name the round.
func TestProgressStatusFallback(t *testing.T) {
	var got string
	cb := StreamCallbacks{OnStatus: func(s string) { got = s }}

	cb.progress(Progress{Kind: ProgressJudge, Name: "done"})
	if !strings.Contains(got, "done") || strings.Contains(got, "()") {
		t.Fatalf("judge status lost the verdict: %q", got)
	}
	cb.progress(Progress{Kind: ProgressExec, Name: "claude_code", Round: 2, Budget: 5})
	if !strings.Contains(got, "2/5") {
		t.Fatalf("exec status lost the round: %q", got)
	}
	cb.progress(Progress{Kind: ProgressExec, Name: "claude_code", Model: "deepseek-v4-flash"})
	if !strings.Contains(got, "deepseek-v4-flash") {
		t.Fatalf("exec status lost the model: %q", got)
	}
	cb.progress(Progress{Kind: ProgressExec, Name: "claude_code", Round: 1, Budget: 1})
	if strings.Contains(got, "1/1") {
		t.Fatalf("single-round run must not narrate its round: %q", got)
	}
}

// TestEventFieldReadsBothForms guards that eventField extracts a field whether
// the event data arrives as a map (direct RecordEvent) or as JSON bytes (the
// state-transition path marshals before recording).
func TestEventFieldReadsBothForms(t *testing.T) {
	if got := eventField(map[string]any{"agent": "claude_code"}, "agent"); got != "claude_code" {
		t.Fatalf("map form: got %q, want claude_code", got)
	}
	if got := eventField([]byte(`{"agent":"claude_code"}`), "agent"); got != "claude_code" {
		t.Fatalf("bytes form: got %q, want claude_code", got)
	}
	if got := eventField([]byte(`not json`), "agent"); got != "" {
		t.Fatalf("bad json: got %q, want empty", got)
	}
	if got := eventField(map[string]any{"agent": 42}, "agent"); got != "" {
		t.Fatalf("non-string field: got %q, want empty", got)
	}
	// Named string types ride in the map as their own type (scheduler.Action,
	// verdict kinds, …); they are still strings for extraction purposes.
	if got := eventField(map[string]any{"action": scheduler.ActionLocal}, "action"); got != "local" {
		t.Fatalf("named string type: got %q, want local", got)
	}
}

// TestIntFieldReadsBothForms: round/budget counters arrive as Go ints in the
// direct path and as float64 after JSON marshaling; both must extract.
func TestIntFieldReadsBothForms(t *testing.T) {
	if got := intField(map[string]any{"round": 2}, "round"); got != 2 {
		t.Fatalf("int: got %d, want 2", got)
	}
	if got := intField(map[string]any{"round": float64(3)}, "round"); got != 3 {
		t.Fatalf("float64: got %d, want 3", got)
	}
	if got := intField([]byte(`{"budget":5}`), "budget"); got != 5 {
		t.Fatalf("json: got %d, want 5", got)
	}
	if got := intField(map[string]any{"round": "two"}, "round"); got != 0 {
		t.Fatalf("non-number: got %d, want 0", got)
	}
}
