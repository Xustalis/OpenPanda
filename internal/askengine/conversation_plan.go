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

// planInstruction is the prompt an implementation round hands the model. The
// same text is stored on the confirmation turn, so every later turn replays
// the requirements instead of a receipt.
func planInstruction(version int64, proposal string) string {
	return fmt.Sprintf("Implement user-confirmed proposal version %d in the current session. Inspect the saved transcript and existing task results/checkpoints first; continue remaining work without repeating completed side effects. Preserve the original requirements and verify the result.\n\n%s", version, proposal)
}

// HistoryText is the text a stored turn contributes to the model's history.
// A confirmation written by an earlier build carries only "/plan confirm N";
// the proposal is still in conversation_plan_runs, so the instruction can be
// rebuilt from (session, version, round) without rewriting the session file.
func (e *Engine) HistoryText(ctx context.Context, sid string, t sessions.Turn) string {
	version, ok := t.PlanConfirmationReceiptVersion()
	if !ok || e.db == nil {
		return t.Text
	}
	var proposal string
	// version is monotonic within a session, so it identifies the run without
	// depending on round_id being written yet (Confirm inserts the row before
	// the round exists).
	if err := e.db.QueryRowContext(ctx,
		`SELECT proposal FROM conversation_plan_runs WHERE session_id=? AND version=? ORDER BY rowid DESC LIMIT 1`,
		sid, version).Scan(&proposal); err != nil || strings.TrimSpace(proposal) == "" {
		return t.Text
	}
	return planInstruction(version, proposal)
}

// historyTurns is the transcript slice the model replays, from index `from`.
func (e *Engine) historyTurns(ctx context.Context, sess *sessions.Session, from int) []entry.Turn {
	out := make([]entry.Turn, 0, len(sess.Turns))
	for i, t := range sess.Turns {
		if i < from || t.DisplayArchive {
			continue
		}
		out = append(out, entry.Turn{Role: t.Role, Content: e.HistoryText(ctx, sess.ID, t)})
	}
	return out
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
	// The instruction is stored with the turn, so a later turn replays the
	// requirements rather than a receipt, and both sides of this file agree on
	// one wording (planInstruction).
	prompt := planInstruction(version, proposal)
	_, _ = st.AppendTurn(sid, sessions.PlanConfirmationTurn(round, version, prompt))
	workDir := s.engine.SessionWorkDir(sess)
	scope := AskScope{confirmedImplementation: true, SessionID: sid, RoundID: round, WorkDir: workDir, Project: sess.Project, Agents: sess.Team, PermissionMode: sess.PermissionMode, PersonalMemory: true}
	if !resuming {
		var spec entry.PlanSpec
		if json.Unmarshal([]byte(proposal), &spec) == nil && entry.ValidatePlanSpec(&spec) == nil {
			scope.reviewedPlan = &spec
		}
	}
	// This round has no SSE response to type into. Confirming used to start it
	// with an empty callback set, so the console saw a spinner and nothing else
	// for as long as the work ran — indistinguishable from a hang, and the
	// first thing it ever heard was the error at the end. Report the phase on
	// the session operation instead: the console already polls conversation
	// state once a second, so one written field is a live progress line.
	progress := &planPhaseWriter{st: st, sid: sid, op: op, minGap: planPhaseInterval}
	progress.report("dispatch", "")
	phaseDone := make(chan struct{})
	defer close(phaseDone)
	go func() {
		tick := time.NewTicker(planPhaseInterval)
		defer tick.Stop()
		for {
			select {
			case <-phaseDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				progress.flushPending()
			}
		}
	}()
	out, err := s.engine.AskTurnsScoped(ctx, history, prompt, scope, false, StreamCallbacks{
		// Only the display half of the callback set is filled, and OnDelta is
		// deliberately left nil: setting it switches the engine to a streaming
		// request, which this round has no consumer for — and a provider that
		// answers non-streamed then yields "empty model output", turning a
		// working implementation into a failure just because someone asked to
		// watch it. OnApproval and OnQuestion stay nil for the same class of
		// reason: a non-nil handler changes engine semantics (it would block
		// this goroutine waiting for an answer that has no UI), and this
		// round's contract is to hand those cases back as
		// NeedsApproval/NeedsClarification results, which the transcript and
		// the approval endpoint already handle.
		OnProgress: func(p Progress) {
			if p.Kind == ProgressUsage {
				return
			}
			detail := p.Detail
			if detail == "" && p.Kind == ProgressTool {
				detail = p.Name
			}
			progress.report(planPhaseOf(p), detail)
		},
		OnTaskRow: func(taskID string) { progress.task(taskID) },
	})
	progress.stop()
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

// planPhaseInterval bounds how often a plan round rewrites its session file.
// The store rewrites the whole session JSON under a lock, and a busy tool loop
// can emit several progress events a second; two seconds is more than the
// console's one-second poll can distinguish and cheap enough to hold for a
// twenty-minute run.
const planPhaseInterval = 2 * time.Second

// planPhaseOf names the phase a progress event belongs to, in tokens the
// console localizes. It deliberately collapses the engine's richer kinds into
// the handful a reader waiting on a plan can act on: waiting on the model,
// dispatching, running, verifying, and answering.
func planPhaseOf(p Progress) string {
	switch p.Kind {
	case ProgressTask:
		return "dispatch"
	case ProgressPlan:
		return "plan"
	case ProgressRoute:
		return "route"
	case ProgressExec, ProgressSpawn:
		return "exec"
	case ProgressTool:
		return "tool"
	case ProgressJudge:
		return "judge"
	case ProgressWait:
		return "wait"
	case ProgressRetry:
		return "retry"
	case ProgressOffline:
		return "offline"
	case ProgressClarify:
		return "clarify"
	case ProgressAgentText:
		return "answer"
	default:
		return "model"
	}
}

// planPhaseWriter keeps one session operation's Plan field current while a
// confirmed proposal executes.
//
// Every write goes through the same mutex that stop() takes, so a finished
// operation can never be dragged back to "running" by a progress event that
// arrived after the round settled. Writes are throttled: the phase token alone
// would change a handful of times in a run, but tool progress repeats, and
// each repeat would otherwise rewrite the session file.
type planPhaseWriter struct {
	mu      sync.Mutex
	st      *sessions.Store
	sid     string
	op      sessions.Operation
	cur     sessions.PlanProgress
	last    time.Time
	minGap  time.Duration
	dirty   bool
	stopped bool
}

func (w *planPhaseWriter) report(phase, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || (w.cur.Phase == phase && w.cur.Detail == detail) {
		return
	}
	w.cur.Round, w.cur.Phase, w.cur.Detail = w.op.ID, phase, detail
	w.dirty = true
	if !w.last.IsZero() && time.Since(w.last) < w.minGap {
		return
	}
	w.flushLocked()
}

// flushPending writes a change the throttle held back. Without it the last
// event of a busy phase could be dropped and the console would keep reading a
// stale line for as long as the next quiet phase lasts. It deliberately does
// not re-check the gap: its only caller is a ticker that *is* the gap.
func (w *planPhaseWriter) flushPending() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || !w.dirty {
		return
	}
	w.flushLocked()
}

// task records the row the round dispatched, so the console can open the work
// and not only describe it. It writes immediately: the dispatch is the one
// milestone worth a file rewrite out of band.
func (w *planPhaseWriter) task(taskID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || taskID == "" {
		return
	}
	w.cur.Round, w.cur.TaskID = w.op.ID, taskID
	w.dirty = true
	w.flushLocked()
}

// stop freezes the writer and drops the progress field, so a settled round
// leaves no "implementing…" line behind.
func (w *planPhaseWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.stopped = true
	_, _ = w.st.UpdateRunningPlan(w.sid, w.op.ID, nil)
}

func (w *planPhaseWriter) flushLocked() {
	w.last = time.Now()
	w.dirty = false
	_, _ = w.st.UpdateRunningPlan(w.sid, w.op.ID, &w.cur)
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
