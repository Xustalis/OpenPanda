// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

const EvPlanAdjustmentRead = "plan_adjustment_read"

const planSenderPrefix = "panda:plan:"

// A verdict is a proposal against a snapshot. Recheck its authority, the live
// target and the final delivery barrier under the same lock as the mutation.
func (c *Core) applyPlanAction(ctx context.Context, snapshot Task, round, index int, a entry.PlanAction) error {
	if snapshot.PlanID == "" || snapshot.StageID == "" || strings.TrimSpace(a.Reason) == "" {
		return fmt.Errorf("plan adjustment requires a source and a reason")
	}
	decision := fmt.Sprintf("%s:%d:%d", snapshot.AttemptID, round, index)
	var interrupt, cancel string
	err := c.store.withTx(ctx, func(tx *sql.Tx) error {
		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_events WHERE task_id=? AND type=? AND json_extract(data_json,'$.decision_id')=?)`, snapshot.TaskID, EvPlanIntervention, decision).Scan(&applied); err != nil || applied {
			return err
		}
		if err := checkConversationTaskTx(ctx, tx, snapshot.TaskID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE plan_id=? AND parent_id='' AND remote=0 ORDER BY stage_id`, snapshot.PlanID)
		if err != nil {
			return err
		}
		var stages []Task
		for rows.Next() {
			t, err := c.store.scanTask(rows)
			if err != nil {
				rows.Close()
				return err
			}
			stages = append(stages, t)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		var source, target, final Task
		for _, t := range stages {
			if t.TaskID == snapshot.TaskID {
				source = t
			}
			if t.StageID == a.TargetStage {
				target = t
			}
			if !objectiveIntermediate(t) {
				if final.TaskID != "" {
					return fmt.Errorf("ambiguous final delivery stage")
				}
				final = t
			}
		}
		if source.State != StateRunning || source.AttemptID != snapshot.AttemptID || source.StageID != snapshot.StageID || !objectiveIntermediate(source) || !c.orchestratesAny([]Task{source}) || !scheduler.SameRuntimeIdentity(source.OwnerNode, c.nodeID) {
			return fmt.Errorf("reviewing attempt is no longer active at the plan origin")
		}
		if final.State != StateSubmitted {
			return fmt.Errorf("final delivery is no longer waiting for changes")
		}
		if a.Kind != "open" && (target.StageID == "" || source.TaskID == target.TaskID || final.TaskID == target.TaskID) {
			return fmt.Errorf("adjustment must name another intermediate stage in this plan")
		}
		if a.Kind != "open" && len(a.Needs) != 0 {
			return fmt.Errorf("only a new stage may declare inputs")
		}
		outcome := ""
		// An already-dispatched peer cannot consume a note from this store.
		// Schedule the correction against its actual output, like a sibling
		// that finished while the supervisor was deciding.
		followOn := target.State == StateDone || target.State == StateDispatched || target.State == StateWaitingCtx || target.State == StateRunning && !scheduler.SameRuntimeIdentity(target.OwnerNode, c.nodeID)
		if a.Kind == "open" || a.Kind == "adjust" && followOn {
			if a.Kind == "open" && a.TargetStage != "" || strings.TrimSpace(a.Instruction) == "" {
				return fmt.Errorf("invalid follow-up assignment")
			}
			if len(stages) >= plan.MaxStages {
				return fmt.Errorf("plan already contains %d stages", len(stages))
			}
			// A sibling may finish while the supervisor is deciding. Open a separate
			// session with its output so the correction can still run.
			needs := []string{source.StageID}
			if target.StageID != "" {
				needs = append(needs, target.StageID)
			}
			for _, id := range a.Needs {
				i := slices.IndexFunc(stages, func(t Task) bool { return t.StageID == id })
				if i < 0 || id == final.StageID || stages[i].State == StateCancelled || stages[i].State == StateExpired {
					return fmt.Errorf("invalid follow-up dependency %q", id)
				}
				if !slices.Contains(needs, id) {
					needs = append(needs, id)
				}
			}
			template := source
			if a.Kind == "adjust" {
				template = target // preserve the executor constraints of the branch being corrected
			}
			target, err = c.openPlanStageTx(ctx, tx, source, template, needs, a.Instruction)
			if err != nil {
				return err
			}
			final.Needs = append(final.Needs, target.StageID)
			outcome = "opened"
		} else {
			if terminalState(target.State) || target.State == StateReview {
				return fmt.Errorf("stage %s is %s", target.StageID, target.State)
			}
			switch a.Kind {
			case "adjust":
				if strings.TrimSpace(a.Instruction) == "" {
					return fmt.Errorf("adjustment requires an instruction")
				}
				if target.State == StateSubmitted || target.State == StateQueued {
					// The future executor may be on another node. Persist the amendment in
					// the assignment that routing transmits, before it can claim this row.
					_, err := tx.ExecContext(ctx, `UPDATE tasks SET intent=?,state_version=state_version+1,updated_at=? WHERE task_id=?`, target.Intent+"\n\n[Panda plan adjustment]\n"+a.Instruction, c.store.now(), target.TaskID)
					if err != nil {
						return err
					}
					outcome = "amended"
					break
				}
				if target.State != StateRunning || !scheduler.SameRuntimeIdentity(target.OwnerNode, c.nodeID) {
					return fmt.Errorf("stage %s has been dispatched without an active local session", target.StageID)
				}
				now := time.Now().Unix()
				_, err = tx.ExecContext(ctx, `INSERT INTO task_messages(scope,sender,rcpt,sender_seq,sender_label,grade,body,sent_at,recv_at,expires_at) VALUES(?,?,?,1,'Panda',?,?,?,?,?)`, "task:"+target.TaskID, planSenderPrefix+decision, target.TaskID, MsgGradeSteer, a.Instruction, now, now, int64(math.MaxInt64))
				if err != nil {
					return err
				}
				interrupt = target.TaskID
				outcome = "queued"
			case "close":
				if !scheduler.SameRuntimeIdentity(target.OwnerNode, c.nodeID) {
					return fmt.Errorf("the remote executor has no branch-scoped cancellation for %s", target.StageID)
				}
				for _, t := range stages {
					if t.TaskID != final.TaskID && t.State != StateCancelled && slices.Contains(t.Needs, target.StageID) {
						return fmt.Errorf("stage %s still needs the output of %s", t.StageID, target.StageID)
					}
				}
				var children bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE parent_id=? AND state NOT IN ('cancelled','expired','done','failed'))`, target.TaskID).Scan(&children); err != nil {
					return err
				}
				if children {
					return fmt.Errorf("stage %s has active child tasks", target.StageID)
				}
				result := map[string]any{"plan_withdrawn": true, "decision_id": decision, "source_stage": source.StageID, "reason": a.Reason}
				raw, _ := json.Marshal(result)
				_, err = tx.ExecContext(ctx, `UPDATE tasks SET state=?, state_version=state_version+1, result_json=?,updated_at=? WHERE task_id=?`, StateCancelled, string(raw), c.store.now(), target.TaskID)
				if err != nil {
					return err
				}
				if err = c.store.recordEventTx(ctx, tx, target.TaskID, EvCancel, result); err != nil {
					return err
				}
				final.Needs = slices.DeleteFunc(final.Needs, func(id string) bool { return id == target.StageID })
				for _, need := range append(slices.Clone(target.Needs), source.StageID) {
					if !slices.Contains(final.Needs, need) {
						final.Needs = append(final.Needs, need)
					}
				}
				cancel = target.TaskID
				outcome = "closed"
			default:
				return fmt.Errorf("unknown plan action %q", a.Kind)
			}
		}
		if outcome == "opened" || outcome == "closed" {
			raw, _ := json.Marshal(final.Needs)
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET needs_json=?,state_version=state_version+1,updated_at=? WHERE task_id=?`, string(raw), c.store.now(), final.TaskID); err != nil {
				return err
			}
		}
		return c.store.recordEventTx(ctx, tx, source.TaskID, EvPlanIntervention, map[string]any{"decision_id": decision, "round": round, "plan_id": source.PlanID, "source_stage": source.StageID, "target_stage": target.StageID, "requested_target": a.TargetStage, "needs": a.Needs, "kind": a.Kind, "instruction": a.Instruction, "reason": a.Reason, "outcome": outcome})
	})
	if err != nil {
		return err
	}
	if interrupt != "" {
		c.interrupt(interrupt)
	}
	if cancel != "" {
		c.finishCancel(context.WithoutCancel(ctx), []string{cancel})
	}
	return nil
}

func (c *Core) openPlanStageTx(ctx context.Context, tx *sql.Tx, source, template Task, needs []string, instruction string) (Task, error) {
	t := template
	t.OwnerNode = c.nodeID
	id, err := newUUID()
	if err != nil {
		return Task{}, err
	}
	attempt, err := newUUID()
	if err != nil {
		return Task{}, err
	}
	t.TaskID, t.StageID, t.AttemptID = id, "followup-"+id, attempt
	t.Title, t.Intent = boundedEvidenceText(instruction, 96), instruction
	t.Needs, t.Chain = needs, []string{c.nodeID}
	t.ResourceKeys = slices.Clone(template.ResourceKeys)
	for i, key := range t.ResourceKeys {
		if key == "plan:"+t.PlanID+":"+template.StageID {
			t.ResourceKeys[i] = "plan:" + t.PlanID + ":" + t.StageID
		}
	}
	t.Inputs = nil
	for _, in := range template.Inputs {
		if in.Stage == worktreeArtifactStage {
			t.Inputs = append(t.Inputs, in)
		}
	}
	if err := c.store.insertPlanStageTx(ctx, tx, &t); err != nil {
		return Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ask_round_tasks(task_id,round_id) SELECT ?,round_id FROM ask_round_tasks WHERE task_id=?`, t.TaskID, source.TaskID); err != nil {
		return Task{}, err
	}
	if err := c.store.recordEventTx(ctx, tx, t.TaskID, EvSubmit, map[string]any{"owner": c.nodeID, "source_stage": source.StageID}); err != nil {
		return Task{}, err
	}
	var goal string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT json_extract(data_json,'$.plan_goal') FROM task_events WHERE task_id=? AND type=? AND json_type(data_json,'$.plan_goal')='text' ORDER BY id LIMIT 1),'')`, source.TaskID, EvClassifyResult).Scan(&goal); err != nil {
		return Task{}, err
	}
	return t, c.store.recordEventTx(ctx, tx, t.TaskID, EvClassifyResult, map[string]any{"kind": "plan", "plan_goal": goal, "stage_id": t.StageID})
}

// Complete or return the claimed stage to the queue under the same database
// lock used by a live adjustment. A correction can arrive while acceptance
// checks and artifact packing are running, after the last round's steer check.
func (s *TaskStore) completeReviewedStage(ctx context.Context, task Task, owner, attempt string, result any) (bool, error) {
	if err := objectiveResultEvidence(task, result); err != nil {
		return false, err
	}
	var requeued bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_messages WHERE scope=? AND rcpt=? AND sender GLOB ? AND grade=? AND consumed_at=0)`, "task:"+task.TaskID, task.TaskID, planSenderPrefix+"*", MsgGradeSteer).Scan(&requeued); err != nil {
			return err
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		state, next, event := StateDone, attempt, EvResult
		var data any = payload
		if requeued {
			if err := checkConversationTaskTx(ctx, tx, task.TaskID); err != nil {
				return err
			}
			state, event = StateQueued, EvQueue
			next, err = newUUID()
			if err != nil {
				return err
			}
			payload = nil
			data = map[string]any{"reason": "pending plan adjustment", "previous_attempt": attempt}
		}
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?,attempt_id=?,state_version=state_version+1,result_json=?,updated_at=? WHERE task_id=? AND state=? AND owner_node=? AND attempt_id=?`, state, next, string(payload), s.now(), task.TaskID, StateRunning, owner, attempt)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrConflict
		}
		return s.recordEventTx(ctx, tx, task.TaskID, event, data)
	})
	return requeued, err
}
