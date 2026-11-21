// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/util"
)

type proposalRevision struct {
	mu            sync.Mutex
	version       int64
	generation    int64
	body          string
	continuesGoal bool
}

// Render the reviewed graph for people. Dispatch reads the original version
// from the store, including all its routing constraints and acceptance checks.
func (e *Engine) proposalMarkdown(raw string) string {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return raw
	}
	var spec entry.PlanSpec
	if json.Unmarshal([]byte(raw), &spec) != nil || entry.ValidatePlanSpec(&spec) != nil {
		return raw
	}
	graph, err := e.compileClassifiedPlan(&spec, true, "")
	if err != nil {
		return raw
	}
	var doc strings.Builder
	fmt.Fprintf(&doc, "# %s\n", spec.Goal)
	for i, stage := range graph.Stages {
		fmt.Fprintf(&doc, "\n## %d. %s\n", i+1, stage.Title)
		if i < len(spec.Stages) {
			fmt.Fprintf(&doc, "\n%s\n", strings.TrimSpace(spec.Stages[i].Intent))
		}
		if len(stage.Needs) > 0 {
			fmt.Fprintf(&doc, "\n%s → %s\n", strings.Join(stage.Needs, ", "), stage.ID)
		}
		if len(stage.Requires) > 0 {
			fmt.Fprintf(&doc, "\n%s\n", strings.Join(stage.Requires, ", "))
		}
		if i < len(spec.Stages) && spec.Stages[i].SuccessDefinition != "" {
			fmt.Fprintf(&doc, "\n%s\n", spec.Stages[i].SuccessDefinition)
		}
	}
	// Indentation keeps the exact contract visible even if it contains fences.
	fmt.Fprintf(&doc, "\n    %s", strings.ReplaceAll(raw, "\n", "\n    "))
	return doc.String()
}

func (e *Engine) saveProposal(ctx context.Context, scope AskScope, text string) (int64, error) {
	if scope.planRevision == nil {
		return e.Conversations(nil).store.SaveProposal(ctx, scope.SessionID, text)
	}
	r := scope.planRevision
	r.mu.Lock()
	defer r.mu.Unlock()
	v, err := e.Conversations(nil).store.SaveProposal(ctx, scope.SessionID, text, r.version, r.generation)
	if err == nil {
		r.version = v
		r.body = e.proposalMarkdown(conversation.ProposalBody(text))
	}
	return v, err
}

func (e *Engine) registerProposalTool(reg *entry.Registry, scope AskScope) {
	reg.Register(entry.Tool{
		Name: "propose_plan", Tier: 1,
		Description: "Save the executable plan in Markdown. Include scope, steps, dependencies, risks and acceptance checks. The interface supplies the confirmation action. Put any message addressed to the user in the chat reply.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"implementation_markdown": map[string]any{"type": "string", "description": "The complete work instructions to show for review and send to the executor. Exclude requests for approval or follow-up choices. Ask outstanding questions in chat before saving."},
		}, "required": []string{"implementation_markdown"}},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			text, ok := args["implementation_markdown"].(string)
			if !ok { // Older tool-call transcripts used the proposal field.
				text, _ = args["proposal"].(string)
			}
			version, err := e.saveProposal(ctx, scope, text)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Proposal version %d saved. The client displays the proposal and its confirmation action.", version), nil
		},
	})
}

func (s *ConversationService) sweepPlans(ctx context.Context) {
	rows, err := s.store.DB.QueryContext(ctx, "SELECT session_id,version,proposal,round_id,status,goal_id,goal_generation FROM conversation_plan_runs WHERE status IN ('pending','running')")
	if err != nil {
		return
	}
	type run struct {
		sid, text, round, status, goal string
		version, generation            int64
	}
	var runs []run
	for rows.Next() {
		var r run
		if rows.Scan(&r.sid, &r.version, &r.text, &r.round, &r.status, &r.goal, &r.generation) != nil {
			rows.Close()
			return
		}
		runs = append(runs, r)
	}
	rows.Close()
	for _, r := range runs {
		var deleted, planning bool
		if s.store.DB.QueryRowContext(ctx, "SELECT deleted,planning FROM conversation_controls WHERE session_id=?", r.sid).Scan(&deleted, &planning) != nil {
			continue
		}
		if deleted {
			_, _ = s.store.DB.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='cancelled' WHERE session_id=? AND version=?", r.sid, r.version)
			continue
		}
		if r.round != "" {
			s.mu.Lock()
			_, local := s.running[r.round]
			s.mu.Unlock()
			if local {
				continue
			}
			round, err := s.engine.TaskStore().AskRoundProgress(ctx, r.sid, r.round)
			if err != nil {
				continue
			}
			if round.Status == "open" {
				var lease int64
				if s.store.DB.QueryRowContext(ctx, "SELECT lease_until FROM ask_rounds WHERE round_id=?", r.round).Scan(&lease) != nil || lease >= time.Now().Unix() {
					continue
				}
				result, sealErr := s.store.DB.ExecContext(ctx, "UPDATE ask_rounds SET status='interrupted',closed_at=? WHERE round_id=? AND status='open' AND lease_until<?", time.Now().Unix(), r.round, time.Now().Unix())
				if sealErr != nil {
					continue
				}
				if n, _ := result.RowsAffected(); n != 1 {
					continue
				}
				round.Status = "interrupted"
			}
			settled := true
			success := round.Status == "closed"
			for _, t := range round.Tasks {
				if !goalTaskSettled(t.State) || round.PendingRecovery[t.TaskID] {
					settled = false
				}
				if t.State != core.StateDone && !round.Withdrawn[t.TaskID] {
					success = false
				}
			}
			if !settled {
				continue
			}
			if r.goal != "" {
				s.reconcile(ctx, &conversation.Goal{ID: r.goal, SessionID: r.sid, RoundID: r.round})
			}
			if round.Status == "interrupted" {
				// Reconcile the previous tree before continuing from durable
				// checkpoints in a new round; never replay the old dispatch.
				_, _ = s.store.DB.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='pending',round_id='' WHERE session_id=? AND version=? AND status='running' AND round_id=?", r.sid, r.version, r.round)
				if st := s.sessionStore(); st != nil {
					_, _ = st.SetOperation(r.sid, sessions.Operation{ID: r.round, Status: "done"})
				}
				continue
			}
			turns, err := s.planDeliveryTurns(ctx, r.sid, r.round)
			if err != nil {
				continue
			}
			tx, err := s.store.DB.BeginTx(ctx, nil)
			if err != nil {
				continue
			}
			runStatus := "done"
			if !success {
				runStatus = "failed"
			}
			result, err := tx.ExecContext(ctx, "UPDATE conversation_plan_runs SET status=? WHERE session_id=? AND version=? AND round_id=? AND status='running'", runStatus, r.sid, r.version, r.round)
			if err != nil {
				tx.Rollback()
				continue
			}
			n, err := result.RowsAffected()
			if err != nil || n != 1 {
				tx.Rollback()
				continue
			}
			if success && r.goal != "" {
				_, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET status=CASE WHEN token_budget IS NOT NULL AND tokens>=token_budget THEN 'budget_limited' ELSE 'active' END,paused_by_plan=0,blocked_streak=0,blocker='' WHERE id=? AND generation=? AND status='paused' AND paused_by_plan=1 AND NOT EXISTS(SELECT 1 FROM conversation_controls WHERE session_id=? AND planning=1)", r.goal, r.generation, r.sid)
				if err != nil {
					tx.Rollback()
					continue
				}
			}
			// The conditional update holds the writer lock against concurrent stop.
			// A crash before commit replays the same transcript keys.
			st := s.sessionStore()
			appended := false
			if len(turns) != 0 {
				_, appended, err = st.PersistTurns(r.sid, turns...)
				if err != nil {
					tx.Rollback()
					continue
				}
			}
			if tx.Commit() != nil {
				continue
			}
			if st != nil {
				if appended {
					_ = st.Checkpoint(ctx, r.sid)
				}
				status := "done"
				if !success {
					status = "error"
				}
				_, _ = st.SetOperation(r.sid, sessions.Operation{ID: r.round, Status: status})
			}
			continue
		}
		if planning {
			continue
		}
		// Wait for work that was already running when the user entered Plan.
		var busy bool
		if s.store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ask_rounds WHERE session_id=? AND status='open' AND lease_until>=?) OR EXISTS(SELECT 1 FROM conversation_goals WHERE session_id=? AND round_id!='')`, r.sid, time.Now().Unix(), r.sid).Scan(&busy) != nil || busy {
			continue
		}
		round, err := util.UUIDv7()
		if err != nil {
			continue
		}
		tx, err := s.store.DB.BeginTx(ctx, nil)
		if err != nil {
			continue
		}
		result, err := tx.ExecContext(ctx, "UPDATE conversation_plan_runs SET status='running',round_id=? WHERE session_id=? AND version=? AND status='pending' AND EXISTS(SELECT 1 FROM conversation_controls c WHERE c.session_id=conversation_plan_runs.session_id AND c.deleted=0 AND c.planning=0) AND NOT EXISTS(SELECT 1 FROM conversation_plan_runs p WHERE p.session_id=conversation_plan_runs.session_id AND p.status='running') AND NOT EXISTS(SELECT 1 FROM ask_rounds a WHERE a.session_id=conversation_plan_runs.session_id AND a.status='open' AND a.lease_until>=CAST(strftime('%s','now') AS INTEGER)) AND NOT EXISTS(SELECT 1 FROM tasks t WHERE t.session_id=conversation_plan_runs.session_id AND t.state NOT IN ('done','failed','cancelled','expired'))", round, r.sid, r.version)
		if err != nil {
			tx.Rollback()
			continue
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			tx.Rollback()
			continue
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO ask_rounds(round_id,session_id,locale,lease_until,created_at) VALUES(?,?,?,?,?)", round, r.sid, string(s.engine.Locale()), time.Now().Unix()+60, time.Now().Unix())
		if err != nil {
			tx.Rollback()
			continue
		}
		if r.goal != "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO conversation_goal_rounds(round_id,goal_id,generation) VALUES(?,?,?)", round, r.goal, r.generation)
			if err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE conversation_goals SET rounds=rounds+1 WHERE id=?", r.goal)
			}
			if err != nil {
				tx.Rollback()
				continue
			}
		}
		if tx.Commit() != nil {
			continue
		}
		s.wg.Add(1)
		go func(sid, round, text string, version int64) { defer s.wg.Done(); s.runPlan(sid, round, text, version) }(r.sid, round, r.text, r.version)
	}
}

func (s *ConversationService) planEligible(ctx context.Context, sid, round string, version int64) bool {
	var eligible bool
	err := s.store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_plan_runs p JOIN ask_rounds a ON a.round_id=p.round_id JOIN conversation_controls c ON c.session_id=p.session_id WHERE p.session_id=? AND p.version=? AND p.round_id=? AND p.status='running' AND a.status='open' AND c.deleted=0 AND (p.goal_id='' OR EXISTS(SELECT 1 FROM conversation_goals g WHERE g.id=p.goal_id AND g.generation=p.goal_generation AND g.status!='cleared')))`, sid, version, round).Scan(&eligible)
	return err == nil && eligible
}

func (s *ConversationService) runPlan(sid, round, proposal string, version int64) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	s.mu.Lock()
	s.running[round] = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.running, round); s.mu.Unlock() }()
	if !s.planEligible(ctx, sid, round, version) {
		return
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if !s.planEligible(ctx, sid, round, version) {
					cancel()
					return
				}
				_ = s.engine.TaskStore().TouchAskRound(ctx, round)
			}
		}
	}()
	st := s.sessionStore()
	sess, err := st.Get(sid)
	if err != nil {
		return
	}
	op := sessions.Operation{ID: round, Status: "running", StartedAt: time.Now()}
	if st.StartOperation(sid, op) != nil {
		_ = s.engine.TaskStore().CloseAskRound(context.WithoutCancel(ctx), round, "interrupted")
		return
	}
	// The reviewed version is self-contained. Only a continuation needs
	// conversation from a previous attempt (including clarification answers).
	// Planning replies and confirmation prompts are display-only here.
	proposal = conversation.ProposalBody(proposal)
	history, resuming := confirmedPlanHistory(sess, version, s.engine.ContextWindow())
	prompt := fmt.Sprintf("Implement user-confirmed proposal version %d in the current session. Continue from existing task results and checkpoints, preserve the requirements and verify the result.\n\n%s", version, proposal)
	_, _ = st.AppendTurn(sid, sessions.Turn{Role: "user", Text: prompt, RoundID: round, IdempotencyKey: "plan:user:" + round})
	workDir := s.engine.SessionWorkDir(sess)
	scope := AskScope{confirmedImplementation: true, SessionID: sid, RoundID: round, WorkDir: workDir, Project: sess.Project, Agents: sess.Team, PermissionMode: sess.PermissionMode, PersonalMemory: true}
	if !resuming {
		var spec entry.PlanSpec
		if json.Unmarshal([]byte(proposal), &spec) == nil && entry.ValidatePlanSpec(&spec) == nil {
			scope.reviewedPlan = &spec
		}
	}
	out, err := s.engine.AskTurnsScoped(ctx, history, prompt, scope, false, StreamCallbacks{})
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if s.finishCancelledRound(ctx, finishCtx, st, sid, op) {
		return
	}
	turn := sessions.Turn{Role: "assistant", RoundID: round, OperationID: round, IdempotencyKey: "plan:assistant:" + round}
	status := "closed"
	if out != nil {
		turn.Text = out.Answer
		turn.Kind = out.Kind
		turn.Ref = out.TaskID
		turn.Warning = out.Warning
		if out.Kind == "plan" {
			turn.Ref = out.PlanID
			if turn.Text == "" && out.OK && out.PlanID != "" {
				turn.Text = i18n.Tf(s.engine.Locale(), "cli.plan.started", "id", out.PlanID, "n", strconv.Itoa(len(out.PlanStages)), "goal", out.PlanGoal)
			}
		}
		turn.Tokens = out.Tokens()
		if out.ExitCode != 0 || out.Denied {
			status = "stopped"
			turn.Kind = "error"
			if turn.Text == "" {
				turn.Text = out.Stderr
			}
		}
		if out.NeedsClarification {
			status = "stopped"
		}
		if out.NeedsApproval {
			status = "interrupted"
		}
	}
	if err != nil {
		turn.Kind = "error"
		turn.Text = err.Error()
		status = "stopped"
	}
	if out != nil && out.NeedsClarification && err == nil {
		_, _ = s.store.DB.ExecContext(finishCtx, "UPDATE conversation_plan_runs SET status='waiting_input' WHERE session_id=? AND version=? AND round_id=? AND status='running'", sid, version, round)
	}
	_, _ = st.AppendTurn(sid, turn)
	// A dispatch receipt closes this ask, while the durable plan operation
	// remains running until sweepPlans observes all member tasks settling.
	if status == "stopped" {
		op.Status = "error"
		if out != nil && out.NeedsClarification && err == nil {
			op.Status = "done"
		}
		_, _ = st.FinishRunningOperation(sid, op)
	}
	_ = s.engine.TaskStore().CloseAskRound(finishCtx, round, status)
}

// The plan:user turn is the durable boundary between planning discussion and
// the implementation. Older sessions persisted the same version prefix.
func confirmedPlanHistory(sess *sessions.Session, version int64, window int) ([]entry.Turn, bool) {
	prefix := fmt.Sprintf("Implement user-confirmed proposal version %d in the current session.", version)
	for i, t := range sess.Turns {
		if !strings.HasPrefix(t.IdempotencyKey, "plan:user:") || !strings.HasPrefix(t.Text, prefix) {
			continue
		}
		_, tail := sessions.SplitForBudget(sess.Turns[i+1:], sessions.ReplayBudget(window))
		var history []entry.Turn
		for _, t := range tail {
			if !t.DisplayArchive && !strings.HasPrefix(t.IdempotencyKey, "plan:user:") && t.Text != "" {
				history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
			}
		}
		return history, true
	}
	return nil, false
}
