// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"
)

func TestDerivedAgentBudget(t *testing.T) {
	cases := []struct {
		name    string
		task    Task
		turns   int
		timeout time.Duration
	}{
		{
			name: "ordinary task keeps adapter defaults",
			task: Task{Complexity: 0.2, ResourceJSON: `{"cpu":1,"ram_gb":1,"duration_hint":"short"}`},
		},
		{
			name:    "medium task gets a wider loop",
			task:    Task{Complexity: 0.5},
			turns:   derivedMediumTurns,
			timeout: derivedMediumTimeout,
		},
		{
			name:    "hard task gets a longer budget",
			task:    Task{Complexity: 0.8},
			turns:   derivedHardTurns,
			timeout: derivedHardTimeout,
		},
		{
			name:    "cli coding task gets the medium floor",
			task:    Task{Requires: []string{"coding"}},
			turns:   derivedMediumTurns,
			timeout: derivedMediumTimeout,
		},
		{
			name:    "explicit agent requirement is execution work",
			task:    Task{Requires: []string{"agent:claude_code"}},
			turns:   derivedMediumTurns,
			timeout: derivedMediumTimeout,
		},
		{
			name: "a hardware lookup keeps the default budget",
			task: Task{Requires: []string{"sys:info"}},
		},
		{
			name:    "declared long wins over complexity",
			task:    Task{Complexity: 0.9, ResourceJSON: `{"duration_hint":"long"}`},
			turns:   derivedLongTurns,
			timeout: derivedLongTimeout,
		},
		{
			name:  "malformed resource json degrades to complexity",
			task:  Task{Complexity: 0.5, ResourceJSON: `{`},
			turns: derivedMediumTurns, timeout: derivedMediumTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turns, timeout := derivedAgentBudget(tc.task)
			if turns != tc.turns || timeout != tc.timeout {
				t.Fatalf("derivedAgentBudget = (%d, %v), want (%d, %v)", turns, timeout, tc.turns, tc.timeout)
			}
		})
	}
}

func TestTaskDurationHint(t *testing.T) {
	if got := taskDurationHint(`{"duration_hint":"LONG"}`); got != "long" {
		t.Fatalf("duration hint = %q, want long", got)
	}
	if got := taskDurationHint(`not json`); got != "" {
		t.Fatalf("malformed hint = %q, want empty", got)
	}
}
