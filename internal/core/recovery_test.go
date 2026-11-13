// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestRecoveryNeverAssumesCrossNodeReplaySafety(t *testing.T) {
	for _, tc := range []struct {
		r                  bus.TaskResultPayload
		category, strategy string
	}{
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "provider failure: Insufficient balance. Top up the account."}, "provider_billing_required", "stop"},
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "provider failure: rate limit exceeded"}, "execution_failure", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "test failed: provider failure: Insufficient balance"}, "execution_failure", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 1}, "execution_failure", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 127}, "execution_failure", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 124}, "timeout_without_checkpoint", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 124, AgentSessionID: "checkpoint"}, "checkpoint", "same_node_session_resume"},
		{bus.TaskResultPayload{ExitCode: 1, NoRetry: true}, "non_retryable", "stop"},
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "Not logged in · Please run /login"}, "authentication_required", "stop"},
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "Not logged in · Please run /login", Tokens: 100}, "execution_failure", "bounded_same_node_retry"},
		{bus.TaskResultPayload{ExitCode: 1, Stderr: "test failed: log contains Not logged in"}, "execution_failure", "bounded_same_node_retry"},
	} {
		a := assessRecovery(tc.r)
		if a.Category != tc.category || a.Strategy != tc.strategy || a.CrossNodeReplay {
			t.Fatalf("unsafe assessment: %+v", a)
		}
	}
}

func TestRecoveryAndSkippedInjectionAreObservable(t *testing.T) {
	c := newCoreWithAgent(t, "recovery-diagnostics")
	c.SetWorkDir(t.TempDir())
	c.sleep = func(time.Duration) {}
	c.SetRouterPolicy(config.InjectionConfig{Model: config.InjectionModelNever}, config.RoutingConfig{})
	c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		return commander.AgentResult{OK: false, Result: "execution failed", ExitCode: 1}
	})
	task, _, err := c.SubmitLocal(context.Background(), TaskInput{Title: "diagnostic task", ContextType: "command", Requires: []string{"code:modify"}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := c.store.Events(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	injection, recovery := 0, 0
	for _, e := range events {
		switch e.Type {
		case "model_injection_decision":
			injection++
			if !strings.Contains(e.DataJSON, "injection.model=never") || strings.Contains(e.DataJSON, "base_url") || strings.Contains(e.DataJSON, "api_key") {
				t.Fatalf("diagnostic incomplete/secret: %s", e.DataJSON)
			}
		case "recovery_assessment":
			recovery++
			var data struct {
				Assessment RecoveryAssessment `json:"assessment"`
			}
			if err := json.Unmarshal([]byte(e.DataJSON), &data); err != nil || data.Assessment.CrossNodeReplay {
				t.Fatal("unsafe replay diagnostic")
			}
		}
	}
	if injection == 0 || recovery == 0 {
		t.Fatalf("missing events injection=%d recovery=%d", injection, recovery)
	}
}
