// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

func TestLocalProviderRefusalHandsOffToPeer(t *testing.T) {
	// The peer advertises only installed, configured agents during hello.
	// Execution itself uses the adapter runners below.
	bin := t.TempDir()
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	if err := os.WriteFile(filepath.Join(bin, name), nil, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "native-test-key")
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
