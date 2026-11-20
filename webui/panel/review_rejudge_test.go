// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import "testing"

// TestNeedsObjectiveRejudge pins the flag the card uses to replace a doomed
// approve button: only a review row parked as accept_work whose stored result
// cannot satisfy its MAX-objective contract needs a re-review.
func TestNeedsObjectiveRejudge(t *testing.T) {
	spec := `{"objectives":{"version":1,"revision":1,"dimensions":[` +
		`{"dimension":"visual","weight":0.6,"priority":"MAX","requirement":"cinematic scene",` +
		`"reason":"user asked for a finished render","criteria":["reads as a real tree"]}]}}`
	withEvidence := `{"ok":true,"objective_assessments":[{"dimension":"visual","criterion":0,"status":"passed","evidence":"reviewed render"}]}`

	tests := []struct {
		name string
		row  taskJSON
		want bool
	}{
		{
			name: "max objectives without evidence",
			row:  taskJSON{State: "review", ApprovalDisposition: "accept_work", Spec: spec, Result: `{"ok":true}`},
			want: true,
		},
		{
			name: "max objectives with evidence",
			row:  taskJSON{State: "review", ApprovalDisposition: "accept_work", Spec: spec, Result: withEvidence},
			want: false,
		},
		{
			name: "no objective contract",
			row:  taskJSON{State: "review", ApprovalDisposition: "accept_work", Spec: `{}`, Result: `{"ok":true}`},
			want: false,
		},
		{
			name: "question park is not an evidence problem",
			row:  taskJSON{State: "review", ApprovalDisposition: "resume_execution", Spec: spec, Result: `{"ok":true}`},
			want: false,
		},
		{
			name: "running task",
			row:  taskJSON{State: "running", ApprovalDisposition: "accept_work", Spec: spec, Result: `{"ok":true}`},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsObjectiveRejudge(tt.row); got != tt.want {
				t.Fatalf("needsObjectiveRejudge = %v, want %v", got, tt.want)
			}
		})
	}
}
