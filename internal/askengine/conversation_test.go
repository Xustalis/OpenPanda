// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
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
			content = []map[string]any{{"type": "tool_use", "id": "proposal", "name": "propose_plan", "input": map[string]any{"implementation_markdown": "## Complete proposal\n\nInspect the fixture, implement the change, then verify all requirements.", "confirmation": "Never deliver this chat-only instruction."}}}
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
		if out.ProposalVersion != int64(i+1) || !strings.HasPrefix(out.ProposalBody, "## Complete proposal") || strings.Contains(out.ProposalBody, "chat-only") {
			t.Fatalf("incorrect terminal plan preview: version=%d body=%q", out.ProposalVersion, out.ProposalBody)
		}
	}
	state, _ := service.State(context.Background(), sess.ID)
	if writes.Load() != 0 || state.ProposalVersion != 2 || !strings.Contains(state.Proposal, "Complete proposal") {
		t.Fatalf("planning escaped: writes=%d %+v", writes.Load(), state)
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
		if strings.Contains(string(body), "CHAT-ONLY-APPROVAL") {
			t.Error("confirmation prompt from planning was replayed to the executor")
		}
		if strings.Contains(string(body), "EXACT-CONFIRMED-PROPOSAL") {
			seen.Store(true)
		}
		_ = json.NewEncoder(w).Encode(anthropicText("Implementation verified."))
	})
	st := sessions.NewStore(t.TempDir())
	sess, _ := st.Create("implementation fixture")
	_ = st.SetPermissionMode(sess.ID, "manual")
	_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "CHAT-ONLY-APPROVAL"})
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	ctx := context.Background()
	if err := service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	version, err := service.store.SaveProposal(ctx, sess.ID, "EXACT-CONFIRMED-PROPOSAL\n\n---\n\n以上只是提案，**在你确认版本之前我不会改动任何文件**。CHAT-ONLY-APPROVAL")
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
}

func TestConfirmedPlanWaitsForMembersAndStoresFailure(t *testing.T) {
	e, tasks, ctx := newStageTestEngine(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("plan failure")
	if err != nil {
		t.Fatal(err)
	}
	s := &ConversationService{engine: e, store: &conversation.Store{DB: e.db}, sessions: st}
	if err := s.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	version, err := s.store.SaveProposal(ctx, sess.ID, "Audit, then verify")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	const round = "confirmed-round"
	if err := tasks.BeginAskRound(ctx, round, sess.ID, "en"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec("UPDATE conversation_plan_runs SET status='running',round_id=? WHERE session_id=?", round, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.StartOperation(sess.ID, sessions.Operation{ID: round, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Create(core.WithAskRound(ctx, round), "", "", "verify", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.CloseAskRound(ctx, round, "closed"); err != nil {
		t.Fatal(err)
	}
	check := func(run, op string) {
		t.Helper()
		s.sweepPlans(ctx)
		var status string
		if err := e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&status); err != nil || status != run {
			t.Fatalf("run=%q want=%q err=%v", status, run, err)
		}
		snapshot, err := st.Get(sess.ID)
		if err != nil || snapshot.Operation == nil || snapshot.Operation.Status != op {
			t.Fatalf("session=%+v want operation %s err=%v", snapshot, op, err)
		}
	}
	check("running", "running")
	if err := tasks.ForceFail(ctx, task.TaskID, "unavailable"); err != nil {
		t.Fatal(err)
	}
	check("failed", "error")
}

func TestConfirmedStructuredProposalDispatchesReviewedGraphWithoutReclassification(t *testing.T) {
	e, store, ctx := newStageTestEngine(t) // No entry model is installed.
	e.queueTasks = true
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("reviewed graph")
	if err != nil {
		t.Fatal(err)
	}
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	if err = service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	version, err := service.store.SaveProposal(ctx, sess.ID, "{\"goal\":\"ship\",\"stages\":[{\"id\":\"inspect\",\"intent\":\"inspect source\"},{\"id\":\"ship\",\"intent\":\"verify result\",\"needs\":[\"inspect\"]}]}")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.State(ctx, sess.ID)
	if err != nil || !strings.HasPrefix(preview.Proposal, "# ship") || !strings.Contains(preview.Proposal, "inspect → ship") || !strings.Contains(preview.Proposal, "ship → panda-final") {
		t.Fatalf("missing reviewed dependency in preview: %q, %v", preview.Proposal, err)
	}
	if _, err = service.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var round string
		if err = e.db.QueryRow("SELECT round_id FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&round); err == nil && round != "" {
			progress, err := store.AskRoundProgress(ctx, sess.ID, round)
			if err == nil && len(progress.Tasks) == 3 {
				for _, member := range progress.Tasks {
					task, err := store.Get(ctx, member.TaskID)
					if err != nil {
						t.Fatal(err)
					}
					if task.StageID == "ship" && task.State == core.StateSubmitted && len(task.Needs) == 1 && task.Needs[0] == "inspect" {
						persisted, err := st.Get(sess.ID)
						if err != nil {
							t.Fatal(err)
						}
						for _, turn := range persisted.Turns {
							if turn.Kind == "plan" && turn.Ref == task.PlanID && strings.Contains(turn.Text, task.PlanID) {
								return
							}
						}
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("confirmed graph did not dispatch into its session round with the reviewed edge")
}

func TestInvalidConfirmedGraphFailsWithoutCreatingTasks(t *testing.T) {
	e, _, ctx := newStageTestEngine(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("invalid legacy graph")
	if err != nil {
		t.Fatal(err)
	}
	service := e.Conversations(st)
	t.Cleanup(service.Close)
	spec := entry.PlanSpec{Goal: "check", FinalStage: "first", Stages: []entry.PlanStageSpec{{ID: "first", Intent: "inspect"}, {ID: "second", Intent: "verify", Needs: []string{"first"}}}}
	draft, err := e.startClassifiedPlan(ctx, &spec, AskScope{Mode: "plan"}, false, StreamCallbacks{})
	if err != nil || draft.PlanProposal || draft.ExitCode == 0 {
		t.Fatalf("invalid draft offered for confirmation: %+v, %v", draft, err)
	}
	if err = service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	version, err := service.store.SaveProposal(ctx, sess.ID, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err = e.db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			var count int
			if err = e.db.QueryRow("SELECT count(*) FROM tasks").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid graph created %d tasks, %v", count, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("invalid legacy proposal did not settle as failed")
}
