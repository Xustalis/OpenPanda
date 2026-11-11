// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"github.com/Xustalis/OpenPanda/internal/core"
	"testing"
)

func TestWatcherWaitsForCheckpointRecovery(t *testing.T) {
	start := core.Event{Type: core.EvExecAgentStart}
	failed := core.Event{Type: core.EvResult}
	checkpoint := core.Event{Type: core.EvRecoveryAssessment, DataJSON: "{\"assessment\":{\"strategy\":\"same_node_session_resume\"}}"}
	stop := core.Event{Type: core.EvRecoveryAssessment, DataJSON: "{\"assessment\":{\"strategy\":\"stop\"}}"}
	for _, tc := range []struct {
		name   string
		events []core.Event
		want   bool
	}{
		{"assessment write race", []core.Event{start, failed}, true},
		{"checkpoint backoff", []core.Event{start, failed, checkpoint}, true},
		{"nonretryable", []core.Event{start, failed, stop}, false},
		{"review exhausted", []core.Event{start, failed, checkpoint, {Type: core.EvReview}}, false},
		{"routing failed", []core.Event{{Type: core.EvSubmit}, failed}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := awaitingRecovery(tc.events); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
