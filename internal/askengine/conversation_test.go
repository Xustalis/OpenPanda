// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func waitConversation(t *testing.T, s *ConversationService, sid string, check func(conversation.State) bool) conversation.State {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var last conversation.State
	for time.Now().Before(deadline) {
		state, err := s.State(context.Background(), sid)
		if err != nil {
			t.Fatal(err)
		}
		last = state
		if check(state) {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("conversation did not converge: %+v goal=%+v", last, last.Goal)
	return last
}

// lastUserMessage is the prompt the entry model was actually handed: the last
// user turn of the request, with block content flattened to its text.
func lastUserMessage(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		t.Fatalf("request body: %v (%s)", err, body)
	}
	raw := req.Messages[len(req.Messages)-1].Content
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("content: %v (%s)", err, raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

func TestGoalSharedRunnerMultipleRoundsAuditAndAccounting(t *testing.T) {
	var calls atomic.Int32
	var firstPrompt atomic.Value
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			firstPrompt.Store(lastUserMessage(t, r))
		}
		w.Header().Set("Content-Type", "application/json")
		content := []map[string]any{{"type": "text", "text": "More work remains; this answer alone does not complete the objective."}}
		if n == 3 {
			content = []map[string]any{{"type": "tool_use", "id": "audit", "name": "update_goal", "input": map[string]any{"status": "complete", "reason": "Verified all original requirements using the local fixture evidence."}}}
		}
		if n > 3 {
			content = []map[string]any{{"type": "text", "text": "Full objective verified."}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": content, "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}})
	})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("autonomous fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	_, err = service.Create(context.Background(), sess.ID, "Verify all original fixture requirements, including final audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConversation(t, service, sess.ID, func(st conversation.State) bool { return st.Goal != nil && st.Goal.Status == "complete" })
	if state.Goal.Rounds != 3 || calls.Load() != 4 || state.Goal.Tokens != 40 {
		t.Fatalf("rounds/calls/tokens: %+v calls=%d", state.Goal, calls.Load())
	}
	persisted, _ := st.Get(sess.ID)
	if len(persisted.Turns) != 4 {
		t.Fatalf("lost or duplicated autonomous turns: %d (%+v)", len(persisted.Turns), persisted.Turns)
	}
	// The goal was published, not asked: the first round must post the
	// objective as the user's own message and hand it to the model as the
	// prompt, while later rounds continue without repeating it.
	const objective = "Verify all original fixture requirements, including final audit"
	if first := persisted.Turns[0]; first.Role != "user" || first.Text != objective || first.RoundID == "" {
		t.Fatalf("first round did not file the objective as the user's message: %+v", first)
	}
	// The provider dialect folds consecutive user turns into one message, so
	// the objective is the tail the model reads last — not the runtime's own
	// "continue the goal" wording.
	if got, _ := firstPrompt.Load().(string); !strings.HasSuffix(strings.TrimSpace(got), objective) {
		t.Fatalf("first round prompt = %q, want it to end with the objective", got)
	}
	for _, turn := range persisted.Turns[1:] {
		if turn.Role == "user" {
			t.Fatalf("objective repeated on a later round: %+v", turn)
		}
	}
	service.sweep()
	state, _ = service.State(context.Background(), sess.ID)
	if state.Goal.Tokens != 40 || calls.Load() != 4 {
		t.Fatal("completion or accounting replayed")
	}
}

func TestGoalQuestionWaitsForExplicitAnswer(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		text := "User answer recorded; remaining work can continue."
		if n == 1 {
			text = clarifyJSON
		}
		_ = json.NewEncoder(w).Encode(anthropicText(text))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("question fixture")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	if _, err := service.Create(context.Background(), sess.ID, "Wait for the user's fixture directory, then verify it", nil); err != nil {
		t.Fatal(err)
	}
	state := waitConversation(t, service, sess.ID, func(st conversation.State) bool { return st.Goal.WaitingFor == "input" && st.Goal.RoundID == "" })
	for i := 0; i < 4; i++ {
		service.sweep()
	}
	if calls.Load() != 1 || state.Goal.Status != "active" {
		t.Fatalf("question spun or paused: calls=%d goal=%+v", calls.Load(), state.Goal)
	}
	_, err := e.AskTurnsScoped(context.Background(), nil, "Use the isolated fixture directory.", AskScope{SessionID: sess.ID}, false, StreamCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = service.State(context.Background(), sess.ID)
	if state.Goal.WaitingFor != "" {
		t.Fatalf("answer failed to release wait: %+v", state.Goal)
	}
	if err := service.Control(context.Background(), sess.ID, state.Goal.ID, "pause", "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmedImplementationChargesGoalBudgetOnce(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "Confirmed implementation verified."}}, "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}})
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("implementation accounting")
	s := e.Conversations(st)
	t.Cleanup(s.Close)
	ctx := context.Background()
	if err := s.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	budget := int64(10)
	if _, err := s.Create(ctx, sess.ID, "Verify every original fixture requirement", &budget); err != nil {
		t.Fatal(err)
	}
	v, err := s.store.SaveProposal(ctx, sess.ID, "Implement the complete fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmPlan(ctx, sess.ID, v); err != nil {
		t.Fatal(err)
	}
	state := waitConversation(t, s, sess.ID, func(st conversation.State) bool { return st.Goal.Status == "budget_limited" })
	if state.Goal.Tokens != 10 || state.Goal.Rounds != 1 || calls.Load() != 1 {
		t.Fatalf("goal=%+v calls=%d", state.Goal, calls.Load())
	}
	for i := 0; i < 3; i++ {
		s.sweep()
	}
	state, _ = s.State(ctx, sess.ID)
	if state.Goal.Tokens != 10 || calls.Load() != 1 {
		t.Fatal("implementation replayed or charged twice")
	}
}

func TestConfirmedImplementationQuestionWaitsForAnswer(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		text := "Continued the confirmed implementation using the supplied fixture input."
		if calls.Add(1) == 1 {
			text = clarifyJSON
		}
		_ = json.NewEncoder(w).Encode(anthropicText(text))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("implementation question")
	s := e.Conversations(st)
	t.Cleanup(s.Close)
	ctx := context.Background()
	if err := s.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	v, err := s.store.SaveProposal(ctx, sess.ID, "Implement with the user's directory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmPlan(ctx, sess.ID, v); err != nil {
		t.Fatal(err)
	}
	waitRun := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var status string
			_ = e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&status)
			if status == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("implementation failed to reach %s", want)
	}
	waitRun("waiting_input")
	for i := 0; i < 3; i++ {
		s.sweep()
	}
	if calls.Load() != 1 {
		t.Fatalf("question repeated %d times", calls.Load())
	}
	if _, err := e.AskTurnsScoped(ctx, nil, "Use the isolated fixture directory.", AskScope{SessionID: sess.ID}, false, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	waitRun("done")
	if calls.Load() != 3 {
		t.Fatalf("lost answer or replayed implementation: %d", calls.Load())
	}
}

func TestConfirmedProposalRecoversInterruptedRound(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(anthropicText("Resumed remaining implementation and verified the original proposal."))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("plan recovery fixture")
	service := e.Conversations(st)
	service.Close()
	ctx := context.Background()
	_ = service.store.SetPlanning(ctx, sess.ID, true)
	v, err := service.store.SaveProposal(ctx, sess.ID, "preserve completed fixture changes; finish remaining work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.store.Confirm(ctx, sess.ID, v); err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().BeginAskRound(ctx, "old-plan-round", sess.ID, "en"); err != nil {
		t.Fatal(err)
	}
	_, _ = e.db.Exec("UPDATE ask_rounds SET lease_until=0 WHERE round_id='old-plan-round'")
	_, err = e.db.Exec("UPDATE conversation_plan_runs SET status='running',round_id='old-plan-round' WHERE session_id=?", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.conversationMu.Lock()
	e.conversation = nil
	e.conversationMu.Unlock()
	restarted := e.Conversations(st)
	t.Cleanup(restarted.Close)
	deadline := time.Now().Add(8 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		_ = e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&status)
		if status == "done" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "done" || calls.Load() != 1 {
		t.Fatalf("recovery status=%s calls=%d", status, calls.Load())
	}
	var oldStatus string
	_ = e.db.QueryRow("SELECT status FROM ask_rounds WHERE round_id='old-plan-round'").Scan(&oldStatus)
	if oldStatus != "interrupted" {
		t.Fatalf("old round replayed: %s", oldStatus)
	}
}

func TestGoalGracefulServiceRestartContinuesWithoutPausing(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		if n == 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "restart-audit", "name": "update_goal", "input": map[string]any{"status": "complete", "reason": "Verified the complete original fixture after reconciling the interrupted round."}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(anthropicText("All original requirements verified."))
	})
	st := sessions.NewStore(t.TempDir())
	t.Cleanup(func() { close(release) })
	sess, _ := st.Create("graceful restart fixture")
	service := e.Conversations(st)
	_, err := service.Create(context.Background(), sess.ID, "Verify complete original restart objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("round never started")
	}
	service.Close()
	before, _ := service.State(context.Background(), sess.ID)
	if before.Goal.Status != "active" {
		t.Fatalf("shutdown paused goal: %+v", before.Goal)
	}
	e.conversationMu.Lock()
	e.conversation = nil
	e.conversationMu.Unlock()
	restarted := e.Conversations(st)
	t.Cleanup(restarted.Close)
	after := waitConversation(t, restarted, sess.ID, func(s conversation.State) bool { return s.Goal.Status == "complete" })
	if after.Goal.Rounds != 2 || calls.Load() != 3 {
		t.Fatalf("restart replayed: rounds=%d calls=%d", after.Goal.Rounds, calls.Load())
	}
}

func TestGoalConsecutiveEmptyResponsesStopContinuation(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(anthropicText(""))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("empty fixture")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	if _, err := service.Create(context.Background(), sess.ID, "preserve complete objective despite empty model", nil); err != nil {
		t.Fatal(err)
	}
	state := waitConversation(t, service, sess.ID, func(s conversation.State) bool { return s.Goal.Status == "paused" && s.Goal.RoundID == "" })
	if state.Goal.Rounds != 2 || state.Goal.EmptyStreak != 2 {
		t.Fatalf("empty guard failed: %+v calls=%d", state.Goal, calls.Load())
	}
	before := calls.Load()
	service.sweep()
	if calls.Load() != before {
		t.Fatal("empty loop continued")
	}
}

func TestGoalPauseDoesNotCancelCurrentRoundAndExitPlanDoesNotResume(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(anthropicText("current work finished"))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("pause fixture")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	g, err := service.Create(context.Background(), sess.ID, "keep original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("round did not start")
	}
	if err = service.SetPlanning(context.Background(), sess.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = service.SetPlanning(context.Background(), sess.ID, false); err != nil {
		t.Fatal(err)
	}
	state, _ := service.State(context.Background(), sess.ID)
	if state.Goal.ID != g.ID || state.Goal.Status != "paused" {
		t.Fatalf("%+v", state.Goal)
	}
	close(release)
	state = waitConversation(t, service, sess.ID, func(st conversation.State) bool { return st.Goal.RoundID == "" })
	if state.Goal.Rounds != 1 || state.Goal.Status != "paused" {
		t.Fatalf("pause resumed goal: %+v", state.Goal)
	}
}

func TestGoalAuditRecoversAfterRestartWithoutReplay(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(anthropicText("unused")) })
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("restart fixture")
	service := e.Conversations(st)
	// Freeze only the controller; persist an in-flight round as another process would.
	service.Close()
	g, err := service.store.Create(context.Background(), sess.ID, "complete the full objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	round, err := service.store.Claim(context.Background(), g, "dead-process", "en")
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID = round
	_ = service.store.Report(context.Background(), g, "complete", "full audit recorded before crash")
	if _, err = e.db.Exec("UPDATE ask_rounds SET lease_until=0 WHERE round_id=?", round); err != nil {
		t.Fatal(err)
	}
	// A real second engine/service uses the same persisted data and no frontend.
	next := &Engine{cfg: e.cfg, db: e.db, registry: e.registry, injector: e.injector, logger: e.logger}
	next.client.Store(e.client.Load())
	resumed := next.Conversations(st)
	t.Cleanup(resumed.Close)
	state := waitConversation(t, resumed, sess.ID, func(st conversation.State) bool { return st.Goal.Status == "complete" })
	if state.Goal.Rounds != 1 {
		t.Fatalf("replayed existing round: %+v", state.Goal)
	}
}

func TestPlanningBlocksMutationAndDispatchAndPersistsProposal(t *testing.T) {
	var calls atomic.Int32
	var writes atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var content any
		if n == 1 {
			content = []map[string]any{{"type": "tool_use", "id": "write", "name": "mutate_fixture", "input": map[string]any{}}}
		} else if n == 2 || n == 4 {
			content = []map[string]any{{"type": "tool_use", "id": "proposal", "name": "propose_plan", "input": map[string]any{"proposal": "Complete proposal: inspect, implement after confirmation, then verify all requirements."}}}
		} else {
			content = []map[string]any{{"type": "text", "text": "Proposal saved for user confirmation."}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": content, "usage": map[string]int{"input_tokens": 5, "output_tokens": 5}})
	})
	e.registry.Register(entry.Tool{Name: "mutate_fixture", Tier: 1, Run: func(context.Context, map[string]any) (string, error) { writes.Add(1); return "wrote", nil }})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("planning fixture")
	_ = st.SetPermissionMode(sess.ID, "bypass")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	if err := service.SetPlanning(context.Background(), sess.ID, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := e.AskTurnsScoped(context.Background(), nil, "Prepare the fixture proposal", AskScope{SessionID: sess.ID, PermissionMode: "bypass"}, true, StreamCallbacks{})
		if err != nil || out == nil {
			t.Fatalf("%+v %v", out, err)
		}
	}
	state, _ := service.State(context.Background(), sess.ID)
	if writes.Load() != 0 || state.ProposalVersion != 2 || !strings.Contains(state.Proposal, "Complete proposal") {
		t.Fatalf("planning escaped: writes=%d %+v", writes.Load(), state)
	}
	staleVersion := state.ProposalVersion
	if _, err := e.AskTurnsScoped(context.Background(), nil, "Clarify the revised scope", AskScope{SessionID: sess.ID}, false, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background(), sess.ID)
	if err != nil || !state.Planning || state.Proposal != "" {
		t.Fatalf("planning without a new proposal retained the previous proposal: %+v %v", state, err)
	}
	if _, err = service.ConfirmPlan(context.Background(), sess.ID, staleVersion); !errors.Is(err, conversation.ErrConflict) {
		t.Fatalf("superseded proposal confirm: %v", err)
	}
	persisted, _ := st.Get(sess.ID)
	if persisted.PermissionMode != "bypass" {
		t.Fatal("original permission was overwritten")
	}
	// Direct dispatch and structured stage paths use the same deterministic boundary.
	registry := entry.NewRegistry()
	registry.Register(e.dispatchTaskTool("write", AskScope{Mode: "plan"}, true, StreamCallbacks{}, nil))
	result, _ := executeToolChecked(withPlanningTools(context.Background()), registry, &entry.ToolCall{Tool: entry.ToolNameTaskSubmit}, true, false, "")
	if !strings.Contains(result, "read-only") {
		t.Fatalf("task dispatch allowed: %s", result)
	}
}

func TestGoalNonretryableErrorStopsAutonomousLoop(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid local fixture request"}}`)
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("error fixture")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	if _, err := service.Create(context.Background(), sess.ID, "complete original fixture", nil); err != nil {
		t.Fatal(err)
	}
	state := waitConversation(t, service, sess.ID, func(st conversation.State) bool { return st.Goal.Status == "paused" && st.Goal.RoundID == "" })
	if state.Goal.Rounds != 1 {
		t.Fatalf("irretryable error spun: %+v", state.Goal)
	}
}

func TestGoalAuditRejectsUnsettledTasksAndExpiredLeaseDoesNotReplay(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not call model while prior tasks are live")
		_ = json.NewEncoder(w).Encode(anthropicText("unexpected"))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("live recovery fixture")
	service := e.Conversations(st)
	service.Close()
	ctx := context.Background()
	g, err := service.store.Create(ctx, sess.ID, "full objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	round, err := service.store.Claim(ctx, g, "dead-process", "en")
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID = round
	task, err := e.TaskStore().Create(core.WithAskRound(ctx, round), "", "", "associated running work", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.store.Report(ctx, g, "complete", "premature audit"); err != conversation.ErrWaiting {
		t.Fatalf("premature completion accepted: %v", err)
	}
	_, err = e.db.Exec("UPDATE ask_rounds SET lease_until=0 WHERE round_id=?", round)
	if err != nil {
		t.Fatal(err)
	}
	service.reconcile(ctx, g)
	state, _ := service.State(ctx, sess.ID)
	if state.Goal.RoundID != round || state.Goal.Rounds != 1 {
		t.Fatalf("replayed live work: %+v", state.Goal)
	}
	if err = e.TaskStore().Cancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = service.store.Report(ctx, g, "complete", "remaining requirements verified after cancellation of unnecessary fixture task"); err != nil {
		t.Fatal(err)
	}
	service.reconcile(ctx, g)
	state, _ = service.State(ctx, sess.ID)
	if state.Goal.Status != "complete" || state.Goal.Rounds != 1 {
		t.Fatalf("settlement: %+v", state.Goal)
	}
}

func TestGoalStopRevokesRoundAndCancelsTaskTree(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(anthropicText("unused")) })
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("stop tree fixture")
	service := e.Conversations(st)
	service.Close()
	ctx := context.Background()
	g, err := service.store.Create(ctx, sess.ID, "original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	round, err := service.store.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID = round
	root, err := e.TaskStore().Create(core.WithAskRound(ctx, round), "", "", "root", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.TaskStore().Create(ctx, root.TaskID, "", "child", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Control(ctx, sess.ID, g.ID, "pause", "", nil); err != nil {
		t.Fatal(err)
	}
	current, _ := e.TaskStore().Get(ctx, root.TaskID)
	if current.State == core.StateCancelled {
		t.Fatal("pause cancelled task")
	}
	if err = service.Control(ctx, sess.ID, g.ID, "stop", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.TaskID, child.TaskID} {
		current, err := e.TaskStore().Get(ctx, id)
		if err != nil || current.State != core.StateCancelled {
			t.Fatalf("stop tree %s: %+v %v", id, current, err)
		}
	}
	if _, err = e.TaskStore().Create(core.WithAskRound(ctx, round), "", "", "late callback", "fixture", nil); err == nil {
		t.Fatal("stale callback dispatched after stop")
	}
	if err = service.Control(ctx, sess.ID, g.ID, "clear", "", nil); err != nil {
		t.Fatal(err)
	}
	service.reconcile(ctx, g)
	state, _ := service.State(ctx, sess.ID)
	if state.Goal != nil {
		t.Fatal("old callback restored cleared goal")
	}
}

func TestStoppedConversationDoesNotPersistAnErrorTurn(t *testing.T) {
	for _, mode := range []string{"goal", "plan"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			})
			t.Cleanup(func() { close(release) })
			st := sessions.NewStore(t.TempDir())
			sess, err := st.Create("stop fixture")
			if err != nil {
				t.Fatal(err)
			}
			s := e.Conversations(st)
			ctx := context.Background()
			if mode == "goal" {
				_, err = s.Create(ctx, sess.ID, "fixture objective", nil)
			} else {
				if err = s.SetPlanning(ctx, sess.ID, true); err != nil {
					t.Fatal(err)
				}
				version, saveErr := s.store.SaveProposal(ctx, sess.ID, "fixture implementation")
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				_, err = s.ConfirmPlan(ctx, sess.ID, version)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("round did not enter the model")
			}
			live, err := st.Get(sess.ID)
			if err != nil || live.Operation == nil || live.Operation.Status != "running" {
				t.Fatalf("missing live operation: %+v, %v", live, err)
			}
			round := live.Operation.ID
			stopped, err := s.StopOperation(ctx, sess.ID, round)
			if err != nil || !stopped {
				t.Fatalf("stop operation = %t, %v", stopped, err)
			}
			if mode == "goal" {
				waitConversation(t, s, sess.ID, func(state conversation.State) bool {
					return state.Goal != nil && state.Goal.Status == "paused" && state.Goal.RoundID == ""
				})
			} else {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					s.mu.Lock()
					_, running := s.running[round]
					s.mu.Unlock()
					if !running {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			persisted, err := st.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, turn := range persisted.Turns {
				if turn.Role == "assistant" && turn.RoundID == round {
					t.Errorf("stopped round wrote an assistant turn: %+v", turn)
				}
			}
			if op := persisted.Operation; op == nil || op.ID != round || op.Status != "cancelled" {
				t.Errorf("operation after stop and settlement: %+v", op)
			}
		})
	}
}

func TestGoalRevocationRejectsRecoveryAndLateChildren(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected model call") })
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("revocation race")
	s := e.Conversations(st)
	s.Close()
	ctx := context.Background()
	g, err := s.store.Create(ctx, sess.ID, "full objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	round, err := s.store.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	root, err := e.TaskStore().Create(core.WithAskRound(ctx, round), "", "", "root", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.TaskStore().Create(ctx, root.TaskID, "", "child", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.db.Exec("UPDATE tasks SET state='failed' WHERE task_id=?", child.TaskID); err != nil {
		t.Fatal(err)
	}
	// Revoke in SQLite without running the cancellation walk yet.
	if err = s.store.Control(ctx, sess.ID, g.ID, "stop", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = e.TaskStore().RequeueForRetry(ctx, child.TaskID, "fixture", false); err == nil {
		t.Fatal("revoked descendant retried")
	}
	if _, err = e.TaskStore().Create(ctx, root.TaskID, "", "late child", "fixture", nil); err == nil {
		t.Fatal("revoked parent created child")
	}
}

func TestGoalAccountingIncludesSupervisionWithoutDoubleChargingEntry(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected model call") })
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("usage source fixture")
	s := e.Conversations(st)
	s.Close()
	ctx := context.Background()
	g, err := s.store.Create(ctx, sess.ID, "original full objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	round, err := s.store.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID = round
	root, err := e.TaskStore().Create(core.WithAskRound(ctx, round), "", "", "root", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.TaskStore().Create(ctx, root.TaskID, "", "child", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.db.Exec("UPDATE tasks SET state='done' WHERE task_id IN (?,?)", root.TaskID, child.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []struct {
		id, executor string
		tokens       int
	}{{root.TaskID, "fixture-agent", 20}, {root.TaskID, "entry:supervisor", 5}, {child.TaskID, "fixture-agent", 7}} {
		if err = e.TaskStore().RecordDelegationMetric(ctx, metric.id, "fixture", metric.executor, nil, true, 1, metric.tokens); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.TaskStore().RecordAskMetric(ctx, root.TaskID, "fixture", "entry:commander", true, 1, 10, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.store.Finish(ctx, round, 10); err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().CloseAskRound(ctx, round, "closed"); err != nil {
		t.Fatal(err)
	}
	s.reconcile(ctx, g)
	s.reconcile(ctx, g)
	state, err := s.State(ctx, sess.ID)
	if err != nil || state.Goal.Tokens != 42 {
		t.Fatalf("goal=%+v err=%v", state.Goal, err)
	}
}

func TestConfirmedProposalRunsExactVersionWithoutCreatingGoal(t *testing.T) {
	var seen atomic.Bool
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "EXACT-CONFIRMED-PROPOSAL") {
			seen.Store(true)
		}
		_ = json.NewEncoder(w).Encode(anthropicText("Implementation verified."))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("implementation fixture")
	_ = st.SetPermissionMode(sess.ID, "manual")
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	ctx := context.Background()
	if err := service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	version, err := service.store.SaveProposal(ctx, sess.ID, "EXACT-CONFIRMED-PROPOSAL")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&status)
		if status == "done" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	state, _ := service.State(ctx, sess.ID)
	persisted, _ := st.Get(sess.ID)
	if !seen.Load() || state.Goal != nil || state.Planning || persisted.PermissionMode != "manual" {
		t.Fatalf("implementation mismatch: seen=%v state=%+v session=%+v", seen.Load(), state, persisted)
	}
	if len(persisted.Turns) != 2 || persisted.Turns[0].Kind != "plan_confirmation" ||
		!strings.Contains(persisted.Turns[0].Text, "EXACT-CONFIRMED-PROPOSAL") ||
		!strings.Contains(persisted.Turns[0].Text, fmt.Sprintf("version %d", version)) {
		t.Fatalf("expected a confirmation in the transcript: %+v", persisted.Turns)
	}
}

// A goal round waiting on the model is what the thread draws as "thinking".
// Pausing the goal must end that wait at once: leaving the request running
// keeps the bubble spinning and the composer busy behind a paused goal bar.
func TestGoalPauseCancelsRunningRound(t *testing.T) {
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		// Drain the body first: Go only notices a client disconnect while a
		// read is outstanding, and an unread body hides the cancellation.
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		_ = json.NewEncoder(w).Encode(anthropicText("resumed and verified"))
	})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("pause cancels thinking")
	if err != nil {
		t.Fatal(err)
	}
	s := deepManualController(t, e, st)
	ctx := context.Background()
	g, err := s.Create(ctx, sess.ID, "verify every requirement", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.sweep()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("goal round never reached the model")
	}
	if err = s.Control(ctx, sess.ID, g.ID, "pause", "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("pause left the model request running")
	}
	s.wg.Wait()
	s.sweep()
	state, err := s.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Status != "paused" || state.Goal.RoundID != "" {
		t.Fatalf("pause did not settle the round: %+v", state.Goal)
	}
	persisted, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Operation != nil && persisted.Operation.Status == "running" {
		t.Fatalf("operation still running after pause: %+v", persisted.Operation)
	}
	// Resuming claims a fresh round instead of leaving the goal parked.
	if err = s.Control(ctx, sess.ID, g.ID, "resume", "", nil); err != nil {
		t.Fatal(err)
	}
	s.sweep()
	s.wg.Wait()
	s.sweep()
	state, err = s.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Rounds < 2 || state.Goal.RoundID != "" || state.Goal.Status != "active" {
		t.Fatalf("resume did not run a fresh round: %+v", state.Goal)
	}
	if calls.Load() != 2 {
		t.Fatalf("resume did not run a fresh round: calls=%d", calls.Load())
	}
}
