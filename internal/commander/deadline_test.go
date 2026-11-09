// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestAgentFallbacksShareExecutionDeadline(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	var deadlines []time.Time
	r.SetAdapterRunner(func(ctx context.Context, _, _, _ string) AgentResult {
		d, ok := ctx.Deadline()
		if !ok {
			t.Fatal("attempt has no shared deadline")
		}
		deadlines = append(deadlines, d)
		if len(deadlines) == 1 {
			return AgentResult{ExitCode: 1, Result: "unauthorized: invalid api key"}
		}
		return AgentResult{OK: true, Result: "done"}
	})
	res := r.execAgent(WithAgentTimeout(context.Background(), time.Minute), Plan{Agent: "cheap", Alternates: []string{"expensive"}}, "task", "")
	if !res.OK || len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("result=%+v deadlines=%v", res, deadlines)
	}
}

func TestCancelledAgentDoesNotStartFallback(t *testing.T) {
	r := NewRouter(scoreCard(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	r.SetAdapterRunner(func(context.Context, string, string, string) AgentResult {
		calls++
		cancel()
		return AgentResult{ExitCode: 1, Result: "unauthorized: invalid api key"}
	})
	r.execAgent(ctx, Plan{Agent: "cheap", Alternates: []string{"expensive"}}, "task", "")
	if calls != 1 {
		t.Fatalf("started %d attempts after cancellation", calls)
	}
}
