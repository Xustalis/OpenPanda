// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

func TestLocalProviderRefusalHandsOffToPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	origin := newSuperviseCore(t, "origin", 1)
	worker := newSuperviseCore(t, "worker", 1)
	var local, remote atomic.Int32
	origin.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		local.Add(1)
		return commander.AgentResult{ExitCode: 1, Result: "authentication failed"}
	})
	worker.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
		remote.Add(1)
		return commander.AgentResult{OK: true, Result: "remote execution succeeded"}
	})
	startPair(t, ctx, origin, worker)
	task, result, err := origin.Submit(ctx, TaskInput{Title: "analyze", Intent: "report findings", ContextType: "command", Requires: []string{"agent:claude_code"}, PreferredNode: origin.nodeID, Authorized: true})
	if err != nil || task.State != StateDone || !result.OK || result.Executor != worker.nodeID || local.Load() != 1 || remote.Load() != 1 {
		t.Fatalf("state=%s result=%+v local=%d remote=%d err=%v", task.State, result, local.Load(), remote.Load(), err)
	}
}
