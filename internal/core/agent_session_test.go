// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	c := newCore(t, "sess-origin")
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
	c := newCore(t, "sess-adopter")

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

// TestAgentSessionResumesOnFallback: a persisted checkpoint makes the minting
// adapter the round's primary — session affinity promotes opencode over the
// card's claude_code primary BEFORE the first attempt, so the resume lands on
// the conversation that owns the transcript instead of burning a doomed
// primary attempt first (observed live: a timeout retry relaunched claude_code
// against an opencode session it could not consume — 13 minutes re-exploring
// what the live session already held).
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

	if len(adapters) != 1 || adapters[0] != "opencode.py" {
		t.Fatalf("attempt adapters = %v, want [opencode.py] — the minting adapter runs first under session affinity", adapters)
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
	c := newCore(t, "sess-origin-adapter")
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

// TestSessionAffinityPromotesMintingAdapter pins the checkpoint-resume
// contract at the routing boundary: Route scores each attempt from scratch
// and can name a primary whose adapter cannot read the persisted session —
// the affinity gate then drops the handle and the retry cold-starts,
// re-paying the whole orientation the checkpoint exists to save. When an
// alternate owns the minting adapter the plan must promote it, so a
// timed-out opencode run resumes on opencode instead of re-reading a repo
// from zero under a different harness.
func TestSessionAffinityPromotesMintingAdapter(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "affinity-node",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, Tier: 1},
			"opencode":    {Adapter: "opencode.py", Capabilities: []string{"code:modify"}, Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, "affinity-node", card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })

	tk := createTask(t, c.store, "", "affinity", c.nodeID)
	if err := c.store.Queue(ctx, tk.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.SetAgentSession(ctx, tk.TaskID, "ses_live", c.nodeID, "opencode.py"); err != nil {
		t.Fatalf("set session: %v", err)
	}

	// The tie routes to the alphabetical primary (claude_code) with opencode
	// as its alternate; the opencode session must flip that order.
	plan, _, _, err := c.resolveRunPlan(ctx, tk.TaskID, []string{"code:modify"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.Agent != "opencode" || plan.Adapter != "opencode.py" {
		t.Fatalf("plan = %+v, want session-affine opencode primary", plan)
	}
	if !slices.Contains(plan.Alternates, "claude_code") {
		t.Fatalf("demoted primary must remain a fallback: alternates=%v", plan.Alternates)
	}

	// A session minted by an adapter no candidate owns changes nothing —
	// a stale handle cannot resurrect a dead adapter.
	tk2 := createTask(t, c.store, "", "affinity-none", c.nodeID)
	if err := c.store.Queue(ctx, tk2.TaskID, c.nodeID); err != nil {
		t.Fatalf("queue2: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk2.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatalf("dispatch2: %v", err)
	}
	if err := c.store.SetAgentSession(ctx, tk2.TaskID, "ghost", c.nodeID, "gone.py"); err != nil {
		t.Fatalf("set session2: %v", err)
	}
	plan2, _, _, err := c.resolveRunPlan(ctx, tk2.TaskID, []string{"code:modify"})
	if err != nil {
		t.Fatalf("resolve2: %v", err)
	}
	if plan2.Agent != "claude_code" {
		t.Fatalf("plan2.Agent = %q, want scored primary kept (no candidate owns gone.py)", plan2.Agent)
	}
}

func TestAgentIncrementalContinuationAndRecovery(t *testing.T) {
	for _, mode := range []string{"delta", "changed_rules", "missing_session"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			c := newSuperviseCore(t, "incremental-"+mode, 1)
			dir := t.TempDir()
			c.SetWorkDir(dir)
			c.SetSuperviseRounds(2)
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("initial rules"), 0o600); err != nil {
				t.Fatal(err)
			}
			c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
				if call == 1 {
					return `{"status":"continue","reason":"continue","followup":"NEW_FOLLOWUP"}`
				}
				return `{"status":"done","reason":"done"}`
			}))
			var prompts, resumes []string
			c.router.SetAdapterRunner(func(runCtx context.Context, adapter, prompt, cwd string) commander.AgentResult {
				prompts = append(prompts, prompt)
				resumes = append(resumes, commander.ResumeID(runCtx))
				if len(prompts) == 1 && mode == "changed_rules" {
					if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("changed rules"), 0o600); err != nil {
						t.Error(err)
					}
				}
				if len(prompts) == 2 && mode == "missing_session" {
					return commander.AgentResult{OK: false, Result: "Session not found: checkpoint", ExitCode: 1}
				}
				return commander.AgentResult{OK: true, Result: "done", ExitCode: 0, SessionID: "checkpoint"}
			})
			task, _, err := c.SubmitLocal(ctx, TaskInput{Title: "incremental test", ContextType: "command", WorkDir: dir, Intent: "ORIGINAL_REQUIREMENT " + strings.Repeat("constraint ", 300), Requires: []string{"code:modify"}})
			if err != nil {
				t.Fatal(err)
			}
			if task.State != StateDone {
				t.Fatalf("state=%s", task.State)
			}
			if len(prompts) < 2 {
				t.Fatalf("rounds=%d", len(prompts))
			}
			if !strings.Contains(prompts[0], "ORIGINAL_REQUIREMENT") {
				t.Fatal("initial context lost")
			}
			if mode == "changed_rules" {
				if !strings.Contains(prompts[1], "ORIGINAL_REQUIREMENT") {
					t.Fatal("changed rules reused old context")
				}
				return
			}
			if strings.Contains(prompts[1], "ORIGINAL_REQUIREMENT") || !strings.Contains(prompts[1], "NEW_FOLLOWUP") {
				t.Fatalf("followup was not incremental: %q", prompts[1])
			}
			if mode == "missing_session" {
				if len(prompts) != 3 || !strings.Contains(prompts[2], "ORIGINAL_REQUIREMENT") || !strings.Contains(prompts[2], "NEW_FOLLOWUP") || resumes[2] != "" {
					t.Fatalf("bad full recovery: prompts=%v resumes=%v", prompts, resumes)
				}
			}
			events, _ := c.store.Events(ctx, task.TaskID)
			sawDelta := false
			for _, ev := range events {
				if ev.Type == EvPromptContext && strings.Contains(ev.DataJSON, `"mode":"delta"`) {
					sawDelta = true
				}
			}
			if !sawDelta {
				t.Fatal("delta delivery was invisible")
			}
		})
	}
}
