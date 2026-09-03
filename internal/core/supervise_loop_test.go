package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
// was consented to at submit (--authorize, or approving the refusal's review,
// which re-queues carrying consent): that consent is the single approval, so
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
// refusal — approving that review re-queues the run with consent.
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

