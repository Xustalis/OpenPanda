// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestAgentSessionCheckpointResumesAcrossRounds exercises §5.2 breakpoint
// mooring inside one run: the adapter's session handle is captured per round,
// persisted to the task row, and threaded back through WithResume on the next
// round so the agent continues its conversation instead of cold-starting.
func TestAgentSessionCheckpointResumesAcrossRounds(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sess-checkpoint", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSuperviseRounds(2)
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"continue","reason":"还没完成","followup":"继续"}`
	}))

	var mu sync.Mutex
	var resumes []string
	c.router.SetAdapterRunner(func(runCtx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		mu.Lock()
		resumes = append(resumes, commander.ResumeID(runCtx))
		n := len(resumes)
		mu.Unlock()
		return commander.AgentResult{
			OK: true, Result: fmt.Sprintf("round-%d", n), ExitCode: 0,
			SessionID: fmt.Sprintf("sess-%d", n),
		}
	})

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "long work",
		Project:     "proj",
		ContextType: "command",
		Intent:      "do the thing",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (round budget spent on continue)", task.State)
	}
	if len(resumes) != 2 || resumes[0] != "" || resumes[1] != "sess-1" {
		t.Fatalf("resumes = %v, want [\"\" \"sess-1\"]", resumes)
	}
	row, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.AgentSessionID != "sess-2" || row.AgentSessionNode != "sess-checkpoint" {
		t.Fatalf("row session = %q@%q, want sess-2@sess-checkpoint",
			row.AgentSessionID, row.AgentSessionNode)
	}
}

// TestAgentSessionSeedOnRerun: a task row carrying a checkpoint — persisted by
// an interrupted earlier run or adopted from a ResumeSessionID hint — seeds
// the next run() with WithResume, and a clean finish clears the handle so a
// stale conversation is never dragged into unrelated work.
func TestAgentSessionSeedOnRerun(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sess-rerun", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var gotResume string
	c.router.SetAdapterRunner(func(runCtx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		gotResume = commander.ResumeID(runCtx)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0, SessionID: "sess-final"}
	})

	tk := createTask(t, c.store, "", "resume-me", c.nodeID)
	if err := c.store.SetAgentSession(ctx, tk.TaskID, "sess-xyz", c.nodeID, ""); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := c.run(ctx, tk.TaskID, "intent", []string{"code:modify"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotResume != "sess-xyz" {
		t.Fatalf("runner saw resume %q, want sess-xyz", gotResume)
	}
	row, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.State != StateDone {
		t.Fatalf("state = %s, want done", row.State)
	}
	if row.AgentSessionID != "" || row.AgentSessionNode != "" {
		t.Fatalf("finished task kept session %q@%q, want cleared",
			row.AgentSessionID, row.AgentSessionNode)
	}
}

// TestResultRecordsRemoteSession: when the executor's result carries
// AgentSessionID, the delegator stores it against the authenticated sender —
// so a later re-dispatch can offer the handle back to the one node whose
// adapter actually owns that conversation.
func TestResultRecordsRemoteSession(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "sess-origin", "127.0.0.1:18152")
	tk := createTask(t, c.store, "", "delegated", c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, "node-b"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	env, err := bus.NewEnvelope(bus.MsgTaskResult, "node-b", "r-sess", bus.TaskResultPayload{
		TaskID: tk.TaskID, AttemptID: tk.AttemptID, State: StateReview, OK: true,
		Chain:          []string{c.nodeID, "node-b"},
		AgentSessionID: "sess-remote-7",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, env)

	row, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.AgentSessionID != "sess-remote-7" || row.AgentSessionNode != "node-b" {
		t.Fatalf("recorded session = %q@%q, want sess-remote-7@node-b",
			row.AgentSessionID, row.AgentSessionNode)
	}
}

// TestDelegateAdoptsResumeHint: a ResumeSessionID hint arriving with a
// delegation is adopted as a session owned by this node — the hint names the
// conversation this node's adapter should continue, so its ownership row
// points at self, not at the delegator that merely carried it.
func TestDelegateAdoptsResumeHint(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "sess-adopter", "127.0.0.1:18151")

	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "parent", "d-resume", bus.TaskDelegatePayload{
		TaskID:          "t-resume-hint",
		Title:           "resumed work",
		Intent:          "x",
		Requires:        []string{"ability:nowhere"},
		Chain:           []string{"parent"},
		ResumeSessionID: "sess-hint-1",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	row, err := c.store.Get(ctx, "t-resume-hint")
	if err != nil {
		t.Fatalf("delegated task missing: %v", err)
	}
	if row.AgentSessionID != "sess-hint-1" || row.AgentSessionNode != "sess-adopter" {
		t.Fatalf("adopted session = %q@%q, want sess-hint-1@sess-adopter",
			row.AgentSessionID, row.AgentSessionNode)
	}
}

// TestAgentSessionAdapterAffinity: the §5.2 handle is adapter-affine, and the
// affinity gate is per ATTEMPT (buildAdapterRequest), not per round. A round
// whose plan adapter differs may still fail over onto the adapter that minted
// the session — that attempt has to receive the handle — so the core always
// attaches the handle together with its mint tag. The foreign id still never
// reaches a foreign adapter: the builder drops it there
// (TestResumeAdapterAffinity). A round-level drop here cannot make that call:
// it would also starve the fallback (TestAgentSessionResumesOnFallback).
func TestAgentSessionAdapterAffinity(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sess-affinity", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var gotResume, gotMint string
	c.router.SetAdapterRunner(func(runCtx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		gotResume, gotMint = commander.ResumeID(runCtx), commander.ResumeAdapter(runCtx)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0, SessionID: "sess-final"}
	})

	// Foreign-mint session: the round plans claude_code.py, but the handle
	// and its opencode.py tag still ride the context — only the request
	// builder may refuse it, and only for the adapter it would poison.
	tk := createTask(t, c.store, "", "affinity-drop", c.nodeID)
	if err := c.store.SetAgentSession(ctx, tk.TaskID, "bc47c3fd-29e9-4cbf-91b0-e4c84918f54b", c.nodeID, "opencode.py"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := c.run(ctx, tk.TaskID, "intent", []string{"code:modify"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotResume != "bc47c3fd-29e9-4cbf-91b0-e4c84918f54b" || gotMint != "opencode.py" {
		t.Fatalf("round offer = %q minted by %q, want the tagged handle",
			gotResume, gotMint)
	}

	// Same-adapter session: offered normally. The first run's fresh mint is
	// recorded against claude_code.py, so the row's adapter column moves on.
	gotResume, gotMint = "", ""
	tk2 := createTask(t, c.store, "", "affinity-match", c.nodeID)
	if err := c.store.SetAgentSession(ctx, tk2.TaskID, "sess-mine", c.nodeID, "claude_code.py"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := c.store.Queue(ctx, tk2.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk2.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := c.run(ctx, tk2.TaskID, "intent", []string{"code:modify"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotResume != "sess-mine" || gotMint != "claude_code.py" {
		t.Fatalf("runner saw resume %q minted by %q, want sess-mine@claude_code.py",
			gotResume, gotMint)
	}
	row, err := c.store.Get(ctx, tk2.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Done clears the handle — and the minting adapter with it, so nothing
	// downstream can resume a dead conversation.
	if row.AgentSessionID != "" || row.AgentSessionAdapter != "" {
		t.Fatalf("finished task kept session %q adapter %q, want cleared",
			row.AgentSessionID, row.AgentSessionAdapter)
	}
}

// TestAgentSessionResumesOnFallback: a checkpoint retry can fail over
// mid-round — the plan's adapter is unavailable, the chain runs the next
// candidate, and that candidate may be the adapter that minted the persisted
// session. Attaching the resume hint per round (gated on plan.Adapter) used
// to drop it before the fallback could see it, so every timeout slice of a
// long task re-ran from zero; the handle must reach the attempt that owns it.
func TestAgentSessionResumesOnFallback(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "sess-fallback",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, Tier: 1},
			"opencode":    {Adapter: "opencode.py", Capabilities: []string{"code:modify"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, "sess-fallback", card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var adapters, resumes []string
	c.router.SetAdapterRunner(func(runCtx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		adapters = append(adapters, adapter)
		if adapter != "opencode.py" {
			// The planned adapter's CLI is dead in this environment: fail
			// fast so the chain falls over to the next candidate.
			return commander.AgentResult{OK: false, ExitCode: 1, Result: "boom: cli exploded"}
		}
		resumes = append(resumes, commander.ResumeID(runCtx))
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0, SessionID: "sess-next"}
	})

	tk := createTask(t, c.store, "", "fallback-resume", c.nodeID)
	if err := c.store.SetAgentSession(ctx, tk.TaskID, "sess-mine", c.nodeID, "opencode.py"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := c.run(ctx, tk.TaskID, "intent", []string{"code:modify"}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(adapters) != 2 || adapters[0] != "claude_code.py" || adapters[1] != "opencode.py" {
		t.Fatalf("attempt adapters = %v, want claude_code.py then opencode.py", adapters)
	}
	if len(resumes) != 1 || resumes[0] != "sess-mine" {
		t.Fatalf("fallback attempt saw resume %v, want [sess-mine]", resumes)
	}
}

// TestResultRecordsRemoteSessionAdapter: the producer adapter rides the
// task_result payload so the delegator's stored copy carries the affinity a
// re-dispatch needs — not just the node it belongs to.
func TestResultRecordsRemoteSessionAdapter(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "sess-origin-adapter", "127.0.0.1:18153")
	tk := createTask(t, c.store, "", "delegated", c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, "node-b"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	env, err := bus.NewEnvelope(bus.MsgTaskResult, "node-b", "r-sess-ad", bus.TaskResultPayload{
		TaskID: tk.TaskID, AttemptID: tk.AttemptID, State: StateReview, OK: true,
		Chain:               []string{c.nodeID, "node-b"},
		AgentSessionID:      "ses_remotexyz",
		AgentSessionAdapter: "opencode.py",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleResult(ctx, env)

	row, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.AgentSessionID != "ses_remotexyz" || row.AgentSessionNode != "node-b" ||
		row.AgentSessionAdapter != "opencode.py" {
		t.Fatalf("recorded session = %q@%q adapter %q, want ses_remotexyz@node-b opencode.py",
			row.AgentSessionID, row.AgentSessionNode, row.AgentSessionAdapter)
	}
}
