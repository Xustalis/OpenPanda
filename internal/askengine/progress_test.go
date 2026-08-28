package askengine

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// TestProgressForEvent pins the mapping from scheduler-core trace events to the
// CLI's live progress phases (P0 §1.4): the bridge in submitTask forwards these
// while a synchronous Submit blocks, so a delegated agent run shows routing →
// executing → judging instead of a frozen spinner. Events with no live-progress
// meaning must report ok=false so the caller stays silent.
func TestProgressForEvent(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		data     any
		wantKind ProgressKind
		wantName string
		wantOK   bool
	}{
		{"route with target", core.EvRouteDecision, map[string]any{"target_node": "pi-3b"}, ProgressRoute, "pi-3b", true},
		{"route local falls back to action", core.EvRouteDecision, map[string]any{"action": "local"}, ProgressRoute, "local", true},
		{"exec names the agent", core.EvExecAgentStart, map[string]any{"agent": "claude_code"}, ProgressExec, "claude_code", true},
		{"exec falls back to adapter", core.EvExecAgentStart, map[string]any{"adapter": "codex"}, ProgressExec, "codex", true},
		{"judge carries verdict", core.EvSupervisionRound, map[string]any{"verdict": "pass"}, ProgressJudge, "pass", true},
		{"unrelated event is silent", core.EvSubmit, map[string]any{}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, name, ok := progressForEvent(tc.typ, tc.data)
			if ok != tc.wantOK || kind != tc.wantKind || name != tc.wantName {
				t.Fatalf("progressForEvent(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.typ, kind, name, ok, tc.wantKind, tc.wantName, tc.wantOK)
			}
		})
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
}
