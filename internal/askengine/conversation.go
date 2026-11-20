// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// ConversationService is the application service for Web, CLI and TUI. Its
// controller lifetime belongs to the engine, independent of any browser stream.
type ConversationService struct {
	engine   *Engine
	store    *conversation.Store
	mu       sync.Mutex
	sessions *sessions.Store
	owner    string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	running  map[string]context.CancelFunc
}

func (e *Engine) Conversations(st *sessions.Store) *ConversationService {
	e.conversationMu.Lock()
	defer e.conversationMu.Unlock()
	if e.conversation == nil {
		ctx, cancel := context.WithCancel(context.Background())
		if st == nil && e.cfg != nil {
			st = sessions.NewStore(filepath.Join(filepath.Dir(e.cfg.Storage.DBPath), "sessions"))
		}
		s := &ConversationService{engine: e, store: &conversation.Store{DB: e.db}, sessions: st, owner: rand.Text(), ctx: ctx, cancel: cancel, running: map[string]context.CancelFunc{}}
		e.conversation = s
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.loop() }()
	} else if st != nil {
		e.conversation.mu.Lock()
		e.conversation.sessions = st
		e.conversation.mu.Unlock()
	}
	return e.conversation
}

func (s *ConversationService) Close() { s.cancel(); s.wg.Wait() }
func (s *ConversationService) sessionStore() *sessions.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}
func (s *ConversationService) State(ctx context.Context, sid string) (conversation.State, error) {
	return s.store.Get(ctx, sid)
}

func (s *ConversationService) SetPlanning(ctx context.Context, sid string, value bool) error {
	if _, err := s.sessionStore().Get(sid); err != nil {
		return err
	}
	return s.store.SetPlanning(ctx, sid, value)
}

func (s *ConversationService) Create(ctx context.Context, sid, text string, budget *int64) (*conversation.Goal, error) {
	if _, err := s.sessionStore().Get(sid); err != nil {
		return nil, err
	}
	// Replacement revokes the old generation before cancelling its work.
	old, err := s.State(ctx, sid)
	if err != nil {
		return nil, err
	}
	g, err := s.store.Create(ctx, sid, text, budget)
	if err == nil && old.Goal != nil {
		s.cancelRound(ctx, old.Goal)
		s.cancelRevokedPlans(ctx, sid)
	}
	return g, err
}

func (s *ConversationService) Control(ctx context.Context, sid, id, action, text string, budget *int64) error {
	state, err := s.State(ctx, sid)
	if err != nil {
		return err
	}
	if state.Goal == nil || state.Goal.ID != id {
		return conversation.ErrConflict
	}
	if err = s.store.Control(ctx, sid, id, action, text, budget); err != nil {
		return err
	}
	switch action {
	case "pause":
		// Pausing is an interruption, not just a status flag: a round that is
		// still waiting on the model would keep thinking (and spending) long
		// after the user asked it to hold, and its live operation would keep
		// the thread "busy" with no way to stop it from the goal bar. Stop the
		// round now but leave its dispatched tasks alone; the goal stays
		// paused, and the next resume claims a fresh round from the transcript.
		// Plan implementation rounds keep running — a paused-by-plan goal has
		// no round of its own to cancel.
		s.interruptRound(ctx, state.Goal)
	case "clear", "stop", "edit":
		s.cancelRound(ctx, state.Goal)
		s.cancelRevokedPlans(ctx, sid)
	}
	return nil
}

func (s *ConversationService) DeleteSession(ctx context.Context, sid string) error {
	state, err := s.State(ctx, sid)
	if err != nil {
		return err
	}
	if err = s.store.DeleteSession(ctx, sid); err != nil {
		return err
	}
	if state.Goal != nil {
		s.cancelRound(ctx, state.Goal)
	}
	s.cancelRevokedPlans(ctx, sid)
	return nil
}

// StopOperation routes the ordinary Stop button to the durable controller
// when that controller owns the operation, including confirmed implementation.
func (s *ConversationService) StopOperation(ctx context.Context, sid, round string) (bool, error) {
	state, err := s.State(ctx, sid)
	if err != nil {
		return false, err
	}
	if state.Goal != nil && state.Goal.RoundID == round {
		return true, s.Control(ctx, sid, state.Goal.ID, "stop", "", nil)
	}
	var linked string
	err = s.store.DB.QueryRowContext(ctx, "SELECT goal_id FROM conversation_plan_runs WHERE session_id=? AND round_id=? AND status='running'", sid, round).Scan(&linked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.Goal != nil && state.Goal.ID == linked {
		return true, s.Control(ctx, sid, linked, "stop", "", nil)
	}
	result, err := s.store.DB.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='cancelled' WHERE session_id=? AND round_id=? AND status='running'", sid, round)
	if err != nil {
		return true, err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		s.cancelRound(ctx, &conversation.Goal{SessionID: sid, RoundID: round})
	}
	return true, nil
}

func (s *ConversationService) cancelRevokedPlans(ctx context.Context, sid string) {
	rows, err := s.store.DB.QueryContext(ctx, "SELECT round_id FROM conversation_plan_runs WHERE session_id=? AND status='cancelled' AND round_id!=''", sid)
	if err != nil {
		return
	}
	var rounds []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			rounds = append(rounds, id)
		}
	}
	rows.Close()
	for _, id := range rounds {
		s.cancelRound(ctx, &conversation.Goal{SessionID: sid, RoundID: id})
	}
}

// interruptRound stops the round's own runner — its model wait above all —
// without touching work it already dispatched. A pause holds the controller;
// it does not revoke jobs the user already set in motion, so those keep
// running and report into the transcript as usual.
func (s *ConversationService) interruptRound(ctx context.Context, g *conversation.Goal) {
	if g.RoundID == "" {
		return
	}
	closeErr := s.engine.TaskStore().CloseAskRound(ctx, g.RoundID, "stopped")
	s.mu.Lock()
	cancel := s.running[g.RoundID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	round, err := s.engine.TaskStore().AskRoundProgress(ctx, g.SessionID, g.RoundID)
	if err != nil {
		return
	}
	if closeErr == nil && round.Status == "stopped" {
		if st := s.sessionStore(); st != nil {
			_, _ = st.FinishRunningOperation(g.SessionID, sessions.Operation{ID: g.RoundID, Status: "cancelled"})
		}
	}
}

func (s *ConversationService) cancelRound(ctx context.Context, g *conversation.Goal) {
	if g.RoundID == "" {
		return
	}
	s.interruptRound(ctx, g)
	round, err := s.engine.TaskStore().AskRoundProgress(ctx, g.SessionID, g.RoundID)
	if err != nil {
		return
	}
	for _, task := range round.Tasks {
		if !core.Terminal(task.State) {
			_, _ = s.engine.CancelTask(ctx, task.TaskID)
		}
	}
}

// ConfirmPlan validates the displayed proposal. The returned prompt is then
// sent through the same ordinary ask path, preserving the session's permissions.
func (s *ConversationService) ConfirmPlan(ctx context.Context, sid string, version int64) (string, error) {
	proposal, err := s.store.Confirm(ctx, sid, version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Implement the user-confirmed proposal version %d in this session. Preserve all requirements and verify the result.\n\n%s", version, proposal), nil
}

func goalTaskSettled(state string) bool {
	return state == core.StateDone || state == core.StateFailed || state == core.StateCancelled || state == core.StateExpired
}

func (s *ConversationService) loop() {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	retention := time.NewTicker(time.Minute)
	defer retention.Stop()
	s.pruneEvents()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			s.sweep()
		case <-retention.C:
			s.pruneEvents()
		}
	}
}

func (s *ConversationService) pruneEvents() {
	if s.store.DB != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		if err := s.store.PruneEvents(ctx); err != nil && ctx.Err() == nil && s.engine.logger != nil {
			s.engine.logger.Warn("prune conversation events", "err", err)
		}
	}
}

func (s *ConversationService) sweep() {
	if s.store.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	s.sweepPlans(ctx)
	// A clarification, cancellation or restart can detach an implementation
	// round from its run row. The immutable ledger still owns its usage.
	rows, ledgerErr := s.store.DB.QueryContext(ctx, `SELECT r.round_id,g.id,g.session_id FROM conversation_goal_rounds r JOIN conversation_goals g ON g.id=r.goal_id JOIN ask_rounds a ON a.round_id=r.round_id AND a.session_id=g.session_id WHERE r.settled=0 AND r.round_id!=g.round_id`)
	if ledgerErr == nil {
		var detached []*conversation.Goal
		for rows.Next() {
			g := new(conversation.Goal)
			if rows.Scan(&g.RoundID, &g.ID, &g.SessionID) == nil {
				detached = append(detached, g)
			}
		}
		rows.Close()
		for _, g := range detached {
			s.reconcile(ctx, g)
		}
	}
	goals, err := s.store.Runnable(ctx)
	if err != nil {
		return
	}
	for _, g := range goals {
		if ctx.Err() != nil {
			return
		}
		if g.RoundID != "" {
			s.reconcile(ctx, g)
			continue
		}
		if g.Status != "active" {
			continue
		}
		st := s.sessionStore()
		if st == nil {
			continue
		}
		sess, err := st.Get(g.SessionID)
		if err != nil {
			_ = s.store.DeleteSession(ctx, g.SessionID)
			continue
		}
		// A pending user turn, approval, question or another ask owns the session.
		if sess.Operation != nil && (sess.Operation.Status == "running" || sess.Operation.Status == "awaiting_answer" || sess.Operation.Status == "awaiting_approval") {
			continue
		}
		var live bool
		if err = s.store.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM ask_rounds WHERE session_id=? AND status='open' AND lease_until>=?)", g.SessionID, time.Now().Unix()).Scan(&live); err != nil || live {
			continue
		}
		round, err := s.store.Claim(ctx, g, s.owner, string(s.engine.Locale()))
		if err != nil {
			continue
		}
		g.RoundID = round
		s.wg.Add(1)
		go func(g *conversation.Goal) { defer s.wg.Done(); s.run(g) }(g)
	}
}

func (s *ConversationService) reconcile(ctx context.Context, g *conversation.Goal) {
	s.mu.Lock()
	_, local := s.running[g.RoundID]
	s.mu.Unlock()
	if local {
		return
	}
	round, err := s.engine.TaskStore().AskRoundProgress(ctx, g.SessionID, g.RoundID)
	if err != nil {
		return
	}
	if round.Status == "open" {
		var lease int64
		if err = s.store.DB.QueryRowContext(ctx, "SELECT lease_until FROM ask_rounds WHERE round_id=?", g.RoundID).Scan(&lease); err != nil {
			return
		}
		if lease >= time.Now().Unix() {
			return
		}
		// A dead dispatcher is sealed; existing task execution is observed, never replayed.
		result, err := s.store.DB.ExecContext(ctx, "UPDATE ask_rounds SET status='interrupted',closed_at=? WHERE round_id=? AND status='open' AND lease_until<?", time.Now().Unix(), g.RoundID, time.Now().Unix())
		if err != nil {
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return
		}
		round.Status = "interrupted"
	}
	for _, task := range round.Tasks {
		if !goalTaskSettled(task.State) || round.PendingRecovery[task.TaskID] {
			return
		}
	}
	// Settlement reads task usage atomically with sealing its ledger.
	if s.store.Settle(ctx, g, 0) == nil {
		if st := s.sessionStore(); st != nil {
			status := "done"
			if round.Status == "stopped" {
				status = "cancelled"
			}
			_, _ = st.FinishRunningOperation(g.SessionID, sessions.Operation{ID: g.RoundID, Status: status})
		}
	}
}

// An explicit stop seals the durable round before signalling its local runner.
// Check that seal too, since another process may own the running invocation.
func (s *ConversationService) finishCancelledRound(ctx, finishCtx context.Context, st *sessions.Store, sid string, op sessions.Operation) bool {
	var stopped bool
	_ = s.store.DB.QueryRowContext(finishCtx, "SELECT status='stopped' FROM ask_rounds WHERE round_id=? AND session_id=?", op.ID, sid).Scan(&stopped)
	if ctx.Err() == nil && !stopped {
		return false
	}
	status := "interrupted"
	op.Status = "done"
	if stopped {
		status = "stopped"
		op.Status = "cancelled"
	}
	_, _ = st.FinishRunningOperation(sid, op)
	_ = s.engine.TaskStore().CloseAskRound(finishCtx, op.ID, status)
	return true
}

func (s *ConversationService) run(g *conversation.Goal) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.running[g.RoundID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.running, g.RoundID); s.mu.Unlock() }()
	// Recheck the generation after claim, before accessing the model.
	if err := s.store.Renew(ctx, g, s.owner); err != nil {
		return
	}
	leaseDone := make(chan struct{})
	defer close(leaseDone)
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-leaseDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.store.Renew(ctx, g, s.owner) != nil {
					cancel()
					return
				}
				_ = s.engine.TaskStore().TouchAskRound(ctx, g.RoundID)
			}
		}
	}()
	st := s.sessionStore()
	sess, err := st.Get(g.SessionID)
	if err != nil {
		return
	}
	op := sessions.Operation{ID: g.RoundID, Status: "running", StartedAt: time.Now()}
	if err = st.StartOperation(sess.ID, op); err != nil {
		_ = s.engine.TaskStore().CloseAskRound(context.WithoutCancel(ctx), g.RoundID, "interrupted")
		return
	}
	history := make([]entry.Turn, 0, len(sess.Turns)+2)
	if sess.Summary != "" {
		history = append(history, entry.Turn{Role: "user", Content: sessions.SummaryMarker + "\n" + sess.Summary})
	}
	for i, t := range sess.Turns {
		if i >= sess.SummaryTurns && !t.DisplayArchive {
			history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
		}
	}
	history = append(history, entry.Turn{Role: "user", Content: "[Autonomous goal runtime] Original full objective: " + g.Objective + "\nContinue until the COMPLETE objective is verified. A task result or answer does not complete the goal. Call update_goal status=complete with a concrete audit only when every requirement is met. Report the same concrete blocker in three consecutive rounds before blocking. Never change the objective, budget or paused state. Ask for missing input instead of inventing it."})
	workDir := sess.Worktree
	if workDir == "" {
		workDir = s.engine.WorkPath()
	}
	prompt := "Continue the active goal. Inspect previous results and checkpoints, perform remaining authorized work, and verify the complete objective."
	// Publishing a goal is not an ask: the composer files no user turn, so the
	// thread used to open with a reply to a message nobody could see. The first
	// round is the user's own task — file it as that bubble and hand it to the
	// model as the prompt, the way a normal send does. Later rounds continue the
	// goal and stay assistant-only; a goal published by the model's create_goal
	// tool already has the user's ask in the transcript and must not repeat it.
	if g.Rounds == 0 && !goalFiled(sess, g.Objective) {
		prompt = g.Objective
		_, _ = st.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: g.Objective, RoundID: g.RoundID, IdempotencyKey: "goal:user:" + g.RoundID})
	}
	out, askErr := s.engine.AskTurnsScoped(ctx, history, prompt, AskScope{SessionID: sess.ID, RoundID: g.RoundID, WorkDir: workDir, Project: sess.Project, Agents: sess.Team, PermissionMode: sess.PermissionMode, PersonalMemory: true}, false, StreamCallbacks{})
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if s.finishCancelledRound(ctx, finishCtx, st, sess.ID, op) {
		return
	}
	turn := sessions.Turn{Role: "assistant", RoundID: g.RoundID, OperationID: g.RoundID, IdempotencyKey: "goal:" + g.RoundID}
	if out != nil {
		turn.Text = out.Answer
		turn.Kind = out.Kind
		turn.Ref = out.TaskID
		turn.Tokens = out.Tokens()
		turn.Model = out.EntryModel
		_ = s.store.Finish(finishCtx, g.RoundID, out.Tokens())
	}
	if askErr != nil && !strings.Contains(askErr.Error(), "empty model output") {
		turn.Kind = "error"
		turn.Text = askErr.Error()
		state := "paused"
		if kind, _ := entry.APIErrorKind(askErr); kind == entry.ErrKindRateLimited {
			state = "usage_limited"
		}
		_ = s.store.Hold(finishCtx, g, state, askErr.Error(), false)
	} else if out == nil || out.EmptyResponse || (strings.TrimSpace(out.Answer) == "" && out.TaskID == "" && out.PlanID == "" && len(out.Dispatched) == 0) {
		state := "active"
		if g.EmptyStreak >= 1 {
			state = "paused"
		}
		_ = s.store.Hold(finishCtx, g, state, "empty model response", true)
	} else if out.NeedsClarification {
		_, _ = s.store.DB.ExecContext(finishCtx, "UPDATE conversation_goals SET waiting_for='input',reason='waiting for user input' WHERE id=? AND generation=? AND round_id=?", g.ID, g.Generation, g.RoundID)
	} else if out.NeedsApproval {
		// The review task keeps this round un-settled until the user decides.

	} else {
		_, _ = s.store.DB.ExecContext(finishCtx, "UPDATE conversation_goals SET empty_streak=0 WHERE id=? AND generation=? AND round_id=?", g.ID, g.Generation, g.RoundID)
	}
	op.Status = "done"
	op.UpdatedAt = time.Now()
	if askErr != nil {
		op.Status = "error"
	}
	_, _ = st.AppendTurn(sess.ID, turn)
	_, _ = st.FinishRunningOperation(sess.ID, op)
	// Publish closure only after input/error state and transcript are durable.
	// Another process may settle and claim the next round immediately.
	_ = s.engine.TaskStore().CloseAskRound(finishCtx, g.RoundID, "closed")
}

// goalFiled reports whether the thread already carries this objective as the
// user's own message. The model's create_goal tool publishes a goal from an ask
// that filed the same words, so the first round must not file them again — a
// second bubble would read as if the user had said it twice. Only the newest
// user turn is examined: an objective that merely repeats something said much
// earlier is still a new task.
func goalFiled(sess *sessions.Session, objective string) bool {
	for i := len(sess.Turns) - 1; i >= 0; i-- {
		turn := sess.Turns[i]
		if turn.Role != "user" {
			continue
		}
		text := strings.TrimSpace(turn.Text)
		if text == objective {
			return true
		}
		// The ask that authorized the tool keeps the command token in its text.
		for _, prefix := range []string{"/goal ", "create goal: ", "创建目标："} {
			if strings.HasPrefix(strings.ToLower(text), prefix) && strings.TrimSpace(text[len(prefix):]) == objective {
				return true
			}
		}
		return false
	}
	return false
}

func (e *Engine) registerGoalTools(reg *entry.Registry, scope AskScope) {
	if scope.SessionID == "" || scope.Side || e.db == nil {
		return
	}
	service := e.Conversations(nil)
	state, err := service.State(context.Background(), scope.SessionID)
	if err != nil {
		return
	}
	schema := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	reg.Register(entry.Tool{Name: "get_goal", Description: "Read the current server-owned goal, status and usage.", Tier: 1, ReadOnly: true, Schema: schema(map[string]any{}), Run: func(ctx context.Context, _ map[string]any) (string, error) {
		st, err := service.State(ctx, scope.SessionID)
		b, _ := json.Marshal(st.Goal)
		return string(b), err
	}})
	reg.Register(entry.Tool{Name: "create_goal", Description: "Create exactly the objective explicitly authorized by this user request, or return the matching current goal. Never invent or narrow the objective or raise its budget.", Tier: 1, Schema: schema(map[string]any{"objective": str()}, "objective"), Run: func(ctx context.Context, args map[string]any) (string, error) {
		st, err := service.State(ctx, scope.SessionID)
		if err != nil {
			return "", err
		}
		objective, _ := args["objective"].(string)
		if scope.goalObjective != "" && objective == scope.goalObjective && (st.Goal == nil || st.Goal.Objective != objective) {
			g, createErr := service.Create(ctx, scope.SessionID, objective, nil)
			b, _ := json.Marshal(g)
			return string(b), createErr
		}
		if st.Goal == nil || st.Goal.Objective != objective {
			return "", errors.New("explicit user goal instruction required; use /goal")
		}
		b, _ := json.Marshal(st.Goal)
		return string(b), nil
	}})
	reg.Register(entry.Tool{Name: "update_goal", Description: "Audit completion of the entire goal, or report one concrete blocker. Completion is checked after associated work settles. Block only after the same real blocker in three consecutive rounds. Cannot change objective, resume, pause, or raise budgets.", Tier: 1, Schema: schema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"complete", "blocked"}}, "reason": str()}, "status", "reason"), Run: func(ctx context.Context, args map[string]any) (string, error) {
		if state.Goal == nil || state.Goal.RoundID != scope.RoundID {
			return "", conversation.ErrConflict
		}
		status, _ := args["status"].(string)
		reason, _ := args["reason"].(string)
		if err := service.store.Report(ctx, state.Goal, status, reason); err != nil {
			return "", err
		}
		return "Goal audit recorded; the controller will verify associated work has settled.", nil
	}})
}
