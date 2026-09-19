package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// newSuperviseCore builds a Core whose card advertises one agent ability at the
// given tier, with a fake always-available prober (no real CLI installed).
func newSuperviseCore(t *testing.T, id string, tier int) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}, Tier: tier},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, id, card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetSharedSecret(testSharedSecret)
	return c
}

// newFakeSupervisor returns an entry client pointed at a stub Anthropic endpoint
// whose text content is decided per call by fn (call counts from 1). This is the
// model that judges completeness without a real LLM.
func newFakeSupervisor(t *testing.T, fn func(call int) string) *entry.Client {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := fn(int(n.Add(1)))
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new supervisor client: %v", err)
	}
	return client
}

func agentRunner(calls *atomic.Int32) func(context.Context, string, string, string) commander.AgentResult {
	return func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		calls.Add(1)
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	}
}

// TestSuperviseLoopDelegatesUntilDone verifies the execute → judge → re-delegate
// loop: a "continue" verdict re-runs the agent with the follow-up instruction, and
// a later "done" verdict finishes the task into done.
func TestSuperviseLoopDelegatesUntilDone(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-loop", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		if call == 1 {
			return `{"status":"continue","reason":"只完成一半","followup":"补齐剩余改动"}`
		}
		return `{"status":"done","reason":"全部完成","followup":""}`
	}))

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "fix bugs",
		Project:     "proj",
		ContextType: "command",
		Intent:      "fix all bugs",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if calls.Load() != 2 {
		t.Fatalf("agent ran %d times, want 2 (one per round)", calls.Load())
	}
	if !result.OK {
		t.Fatalf("result OK = false, want true")
	}
}

// TestSuperviseLoopParksOnBudgetExhausted verifies that a supervisor that keeps
// rejecting the work parks the task in review after the round budget, instead of
// looping forever or silently accepting it.
func TestSuperviseLoopParksOnBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-park", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSuperviseRounds(2)
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"continue","reason":"还没完成","followup":"继续"}`
	}))

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "never done",
		Project:     "proj",
		ContextType: "command",
		Intent:      "large refactor",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (parked for human help)", task.State)
	}
	if calls.Load() != 2 {
		t.Fatalf("agent ran %d times, want 2 (round budget)", calls.Load())
	}
}

// TestSuperviseTerminalCompletesAuthorizedIrreversible verifies that an
// accepted irreversible (tier-2) agent task completes directly when its run
// was consented to at submit (--authorize, or by approving and resuming the
// refusal's review): that consent is the single approval, so
// the finished result is not parked for a second sign-off.
func TestSuperviseTerminalCompletesAuthorizedIrreversible(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-irrev", 2)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "push changes",
		Project:     "proj",
		ContextType: "command",
		Intent:      "push to remote",
		Requires:    []string{"code:modify"},
		Authorized:  true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done (authorized tier-2 completes without a second approval)", task.State)
	}
	if calls.Load() != 1 {
		t.Fatalf("agent ran %d times, want 1", calls.Load())
	}
}

// TestSuperviseTerminalRefusesUnauthorizedIrreversible keeps the consent gate
// the direct-completion rule relaxes: a tier-2 agent task submitted without
// --authorize never executes, and parks in review carrying the actionable
// refusal — approving that review resumes the original run with consent.
func TestSuperviseTerminalRefusesUnauthorizedIrreversible(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-irrev-unauth", 2)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "push changes",
		Project:     "proj",
		ContextType: "command",
		Intent:      "push to remote",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (tier-2 without consent parks with the refusal)", task.State)
	}
	if calls.Load() != 0 {
		t.Fatalf("agent ran %d times, want 0 (the gate refuses before the adapter spawns)", calls.Load())
	}
	if !commander.IsAuthorizationRefusal(result.Stderr) {
		t.Fatalf("result stderr = %q, want the tier-2 authorization refusal", result.Stderr)
	}
}

// TestResumeApprovedRunsInPlace pins the inline approval closure (the ask/repl
// path): a tier-2 task parked in review by an authorization refusal, once the
// user consents, re-runs to completion in the same process — no background
// scheduler — and the agent spawns exactly once (the refusal was raised before
// the first spawn, so no work is duplicated).
func TestResumeApprovedRunsInPlace(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-resume", 2)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "push changes",
		Project:     "proj",
		ContextType: "command",
		Intent:      "push to remote",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("pre-approval state = %s, want review", task.State)
	}
	if calls.Load() != 0 {
		t.Fatalf("agent ran %d times before approval, want 0", calls.Load())
	}
	if !commander.IsAuthorizationRefusal(result.Stderr) {
		t.Fatalf("pre-approval stderr = %q, want the tier-2 refusal", result.Stderr)
	}

	final, res, err := c.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("post-approval state = %s, want done", final.State)
	}
	if !final.Authorized {
		t.Fatal("resumed task must carry the tier-2 consent")
	}
	if !res.OK {
		t.Fatalf("resumed result OK = false, want true (stderr=%q)", res.Stderr)
	}
	if calls.Load() != 1 {
		t.Fatalf("agent ran %d times, want 1 (the resume is the sole spawn)", calls.Load())
	}
}

func TestResumeApprovedAcceptWorkWithEmptyLegacyResult(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-accept-empty", 1)
	task, err := c.store.Create(ctx, "", "proj", "accept empty legacy work", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.PauseWithResult(ctx, task.TaskID, c.nodeID, nil) },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}
	if _, err := c.store.db.ExecContext(ctx, `UPDATE tasks SET result_json=NULL, approval_disposition=NULL WHERE task_id=?`, task.TaskID); err != nil {
		t.Fatalf("make legacy review: %v", err)
	}

	final, result, err := c.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("accept reviewed work: %v", err)
	}
	if final.State != StateDone || result.State != StateDone || !result.OK {
		t.Fatalf("final/result = state %s/%s ok=%v", final.State, result.State, result.OK)
	}
}

func TestResumeApprovedNeedsChangedInputDoesNotRun(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-changed-input", 1)
	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))
	task, err := c.store.Create(ctx, "", "proj", "fix scope", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Pause(ctx, task.TaskID, c.nodeID, "scope drift") },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}

	final, _, err := c.ResumeApproved(ctx, task.TaskID)
	if !errors.Is(err, ErrApprovalNeedsChangedInput) {
		t.Fatalf("resume error = %v, want %v", err, ErrApprovalNeedsChangedInput)
	}
	if final.State != StateReview || final.ApprovalDisposition != ApprovalNeedsChangedInput {
		t.Fatalf("final = state %s disposition %q, want unchanged review", final.State, final.ApprovalDisposition)
	}
	if calls.Load() != 0 {
		t.Fatalf("agent ran %d times, want 0", calls.Load())
	}
}

func TestResumeApprovedAcceptWorkPreservesStoredResult(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-accept-result", 1)
	task, err := c.store.Create(ctx, "", "proj", "accept completed work", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive task: %v", err)
		}
	}
	want := map[string]any{
		"task_id": task.TaskID, "attempt_id": task.AttemptID, "state": StateReview,
		"ok": true, "exit_code": 7, "stdout": "kept output", "stderr": "kept warning",
		"executor": "worker", "agent": "codex", "model": "model-x", "injected": true,
	}
	if err := c.store.PauseWithResult(ctx, task.TaskID, c.nodeID, want); err != nil {
		t.Fatalf("pause with result: %v", err)
	}

	final, result, err := c.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("accept reviewed work: %v", err)
	}
	if final.State != StateDone || result.State != StateDone || !result.OK {
		t.Fatalf("final/result = state %s/%s ok=%v", final.State, result.State, result.OK)
	}
	if result.Stdout != "kept output" || result.Stderr != "kept warning" || result.ExitCode != 7 ||
		result.Executor != "worker" || result.Agent != "codex" || result.Model != "model-x" || !result.Injected {
		t.Fatalf("stored result attribution lost: %+v", result)
	}
}

func TestResumeApprovedUsesPersistedWorkDir(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-resume-workdir", 2)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"done","reason":"done","followup":""}`
	}))

	var gotCWD string
	c.router.SetAdapterRunner(func(_ context.Context, _, _, cwd string) commander.AgentResult {
		gotCWD = cwd
		return commander.AgentResult{OK: true, Result: "done"}
	})
	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "persist cwd", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	origin := t.TempDir()
	if err := c.store.SetWorkDir(ctx, task.TaskID, origin); err != nil {
		t.Fatalf("set workdir: %v", err)
	}

	final, _, err := c.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone || gotCWD != origin {
		t.Fatalf("resume state/cwd = %s, %q; want done, %q", final.State, gotCWD, origin)
	}
}

func TestClaimApprovedTransfersOwnerWithExactCAS(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "entry-11111111", 2)
	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "transfer approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := c.store.ClaimApproved(ctx, task.TaskID, "entry-11111111", "entry-22222222", "entry-22222222"); err != nil {
		t.Fatalf("claim with exact previous owner: %v", err)
	}
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDispatched || got.OwnerNode != "entry-22222222" || !got.Authorized || got.Scheduled {
		t.Fatalf("transferred task = state %s owner %s authorized=%v scheduled=%v", got.State, got.OwnerNode, got.Authorized, got.Scheduled)
	}
}

func TestClaimApprovedRejectsWrongExpectedOwner(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "entry-11111111", 2)
	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "guard approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := c.store.ClaimApproved(ctx, task.TaskID, "entry-99999999", "entry-22222222", "entry-22222222"); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim error = %v, want conflict", err)
	}
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateReview || got.OwnerNode != "entry-11111111" || got.Authorized {
		t.Fatalf("task changed after rejected claim: %+v", got)
	}
}

func TestResumeApprovedAfterEphemeralRestart(t *testing.T) {
	ctx := context.Background()
	first := newSuperviseCore(t, "entry-11111111", 2)
	var calls atomic.Int32
	first.router.SetAdapterRunner(agentRunner(&calls))
	task, _, err := first.SubmitLocal(ctx, TaskInput{
		Title: "restart approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	second := NewCore(first.db, "entry-22222222", first.card, 5, testLogger(), config.ModelConfig{})
	second.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	second.router.SetAdapterRunner(agentRunner(&calls))
	second.SetSharedSecret(testSharedSecret)
	final, _, err := second.ResumeApproved(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("resume from replacement core: %v", err)
	}
	if final.State != StateDone || final.OwnerNode != "entry-22222222" || calls.Load() != 1 {
		t.Fatalf("final = state %s owner %s calls %d", final.State, final.OwnerNode, calls.Load())
	}
}

func TestClaimApprovedAtomicallyDispatches(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-claim-approved", 2)
	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "claim approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := c.store.ClaimApproved(ctx, task.TaskID, c.nodeID, c.nodeID, "worker"); err != nil {
		t.Fatalf("claim approved: %v", err)
	}
	got, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get claimed task: %v", err)
	}
	if got.State != StateDispatched || !got.Authorized || got.Scheduled {
		t.Fatalf("claimed task = state %s authorized=%v scheduled=%v", got.State, got.Authorized, got.Scheduled)
	}
	target, err := c.store.DispatchTarget(ctx, task.TaskID)
	if err != nil || target != "worker" {
		t.Fatalf("dispatch target = %q, %v; want worker", target, err)
	}
	if err := c.store.ClaimApproved(ctx, task.TaskID, c.nodeID, c.nodeID, "worker"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim = %v, want conflict", err)
	}
}

func TestResumeApprovedConcurrentRunsOnce(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-resume-race", 2)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"done","reason":"done","followup":""}`
	}))

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c.router.SetAdapterRunner(func(_ context.Context, _, _, _ string) commander.AgentResult {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return commander.AgentResult{OK: true, Result: "done"}
	})
	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "race approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := c.ResumeApproved(ctx, task.TaskID)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first approval did not start the agent")
	}
	if _, _, err := c.ResumeApproved(ctx, task.TaskID); err == nil {
		t.Fatal("concurrent approval must conflict")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("agent ran %d times, want exactly once", got)
	}
}

func TestResumeApprovedCancellationStopsTask(t *testing.T) {
	c := newSuperviseCore(t, "sup-resume-cancel", 2)
	c.SetWorkDir(t.TempDir())

	started := make(chan struct{})
	var once sync.Once
	c.router.SetAdapterRunner(func(ctx context.Context, _, _, _ string) commander.AgentResult {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return commander.AgentResult{OK: false, Stderr: ctx.Err().Error(), ExitCode: 1}
	})
	task, _, err := c.SubmitLocal(context.Background(), TaskInput{
		Title: "cancel approval", Intent: "edit files", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := c.ResumeApproved(ctx, task.TaskID)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("approved agent did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resume error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled approval did not return")
	}
	got, err := c.store.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("get cancelled task: %v", err)
	}
	if got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
}

// TestResumeApprovedRejectsNonReview guards the precondition: ResumeApproved is
// only valid on a review-parked task, never a running or done one.
func TestResumeApprovedRejectsNonReview(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-resume-guard", 1)
	c.SetWorkDir(t.TempDir())
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		return `{"status":"done","reason":"完成","followup":""}`
	}))
	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "build",
		Project:     "proj",
		ContextType: "command",
		Intent:      "build the project",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done (tier-1 runs without consent)", task.State)
	}
	if _, _, err := c.ResumeApproved(ctx, task.TaskID); err == nil {
		t.Fatal("resume of a done task must error, not re-run it")
	}
}

// TestSuperviseRecordsEntryUsage verifies the commander model's own token
// consumption is billed into the delegation metrics: a supervisor whose
// provider reports usage produces an "entry:<model>" row the web panel's
// tokens column picks up alongside adapter delegations.
func TestSuperviseRecordsEntryUsage(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-usage", 1)
	c.SetWorkDir(t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": `{"status":"done","reason":"全部完成","followup":""}`}},
			"usage":   map[string]int{"input_tokens": 100, "output_tokens": 20},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new supervisor client: %v", err)
	}
	c.SetSupervisor(client)

	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))

	task, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "fix bugs",
		Project:     "proj",
		ContextType: "command",
		Intent:      "fix all bugs",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}

	metrics, err := c.store.ListDelegationMetrics(ctx)
	if err != nil {
		t.Fatalf("list metrics: %v", err)
	}
	found := false
	for _, m := range metrics {
		if m.Executor == "entry:deepseek-chat" {
			found = true
			if m.TaskID != task.TaskID {
				t.Fatalf("entry usage row task = %s, want %s", m.TaskID, task.TaskID)
			}
			if !m.Tokens.Valid || m.Tokens.Int64 != 120 {
				t.Fatalf("entry usage tokens = %+v, want 120 (100 in + 20 out)", m.Tokens)
			}
			if !m.Success {
				t.Fatal("entry usage row should record the done verdict as success")
			}
		}
	}
	if !found {
		t.Fatalf("no entry:<model> metric row recorded; metrics = %+v", metrics)
	}
}

// TestSuperviseLoopPreservesOriginalIntentAndStderr verifies that when a supervisor
// returns continue with a followup, the followup is concatenated to the original intent
// (rather than overwriting it), and any stderr produced by the agent is supplied to the
// supervisor for cross-verification.
func TestSuperviseLoopPreservesOriginalIntentAndStderr(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-preserve", 1)
	c.SetWorkDir(t.TempDir())

	var supervisorInputs []string
	var supMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		supMu.Lock()
		if len(req.Messages) > 0 {
			supervisorInputs = append(supervisorInputs, req.Messages[0].Content)
		}
		call := len(supervisorInputs)
		supMu.Unlock()

		w.Header().Set("content-type", "application/json")
		text := `{"status":"done","reason":"全部完成","followup":""}`
		if call == 1 {
			text = `{"status":"continue","reason":"需要补充单测","followup":"请在 tests 目录下补充测试"}`
		}
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.SetSupervisor(client)

	var prompts []string
	var promptMu sync.Mutex
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		promptMu.Lock()
		prompts = append(prompts, prompt)
		promptMu.Unlock()
		return commander.AgentResult{OK: true, Result: "agent output done", Stderr: "warning: test missing", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "important feature",
		Project:     "proj",
		ContextType: "command",
		Intent:      "实现核心业务逻辑，成功标准：单元测试通过",
		Requires:    []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if len(prompts) != 2 {
		t.Fatalf("agent ran %d times, want 2", len(prompts))
	}
	// Verify round 2 prompt contains the original intent AND the followup
	if !strings.Contains(prompts[1], "实现核心业务逻辑，成功标准：单元测试通过") {
		t.Fatalf("round 2 prompt lost original intent: %s", prompts[1])
	}
	if !strings.Contains(prompts[1], "请在 tests 目录下补充测试") {
		t.Fatalf("round 2 prompt missing followup: %s", prompts[1])
	}
	if !strings.Contains(prompts[1], "[上级补充指令]") {
		t.Fatalf("round 2 prompt missing [上级补充指令] section: %s", prompts[1])
	}

	// Verify supervisor in round 1 received stderr
	supMu.Lock()
	inputs := append([]string{}, supervisorInputs...)
	supMu.Unlock()

	if len(inputs) < 1 || !strings.Contains(inputs[0], "warning: test missing") {
		t.Fatalf("supervisor input did not include stderr: %v", inputs)
	}
	// Verify supervisor in round 2 also sees original intent + followup
	if len(inputs) < 2 || !strings.Contains(inputs[1], "实现核心业务逻辑，成功标准：单元测试通过") {
		t.Fatalf("supervisor round 2 did not receive original intent: %v", inputs)
	}
	if !result.OK {
		t.Fatalf("result OK = false, want true")
	}
}

func TestSuperviseStagnationFailoverToAlternate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "node-1",
		ResourceClass: "Standard",
		Agents: map[string]ledger.Agent{
			"opencode":    {Adapter: "opencode.py", Capabilities: []string{"coding"}, CostTier: "low", Tier: 1},
			"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"coding"}, CostTier: "medium", Tier: 1},
		},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	c := NewCore(db, "node-1", card, 5, testLogger(), config.ModelConfig{})
	c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
	c.SetSharedSecret(testSharedSecret)

	// Supervisor: calls 1 and 2 return continue (since opencode didn't follow instructions), call 3 returns done.
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		if call <= 2 {
			return `{"status":"continue","reason":"missing file content","followup":"read the file"}`
		}
		return `{"status":"done","reason":"all complete"}`
	}))

	var ranAdapters []string
	var ranMu sync.Mutex
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		ranMu.Lock()
		ranAdapters = append(ranAdapters, adapter)
		ranMu.Unlock()
		if adapter == "opencode.py" {
			// Repeated identical output across rounds -> stagnation
			return commander.AgentResult{OK: true, Result: "cannot write to /tmp", ExitCode: 0}
		}
		// claude_code succeeds
		return commander.AgentResult{OK: true, Result: "file created and read successfully", ExitCode: 0}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "write and read file",
		ContextType: "command",
		Intent:      "create file and read it",
		Requires:    []string{"coding"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done (reason=%s)", task.State, task.ResultJSON)
	}
	ranMu.Lock()
	defer ranMu.Unlock()
	// Round 0: opencode.py ran.
	// Round 1: opencode.py ran with identical output -> stagnation detected -> failover to claude_code.py!
	// Round 2: claude_code.py ran -> supervisor judged done!
	if len(ranAdapters) < 3 {
		t.Fatalf("expected at least 3 agent invocations (opencode x2, claude_code x1), got: %v", ranAdapters)
	}
	if ranAdapters[0] != "opencode.py" || ranAdapters[1] != "opencode.py" {
		t.Fatalf("expected initial runs by opencode.py, got: %v", ranAdapters)
	}
	if ranAdapters[2] != "claude_code.py" {
		t.Fatalf("expected failover to claude_code.py, got: %v", ranAdapters)
	}
	if !result.OK {
		t.Fatalf("expected result.OK = true, got false")
	}
}
