package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestSuperviseTerminalRoutesIrreversibleToReview verifies that an accepted
// irreversible (tier-2) agent task parks in review even when the supervisor says
// done, so its side effects get human sign-off before being finalized.
func TestSuperviseTerminalRoutesIrreversibleToReview(t *testing.T) {
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
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (irreversible needs approval)", task.State)
	}
}