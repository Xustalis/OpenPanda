// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// RenderPlanAudit keeps event IDs alongside the executor's synthesis. A delivery
// to a mailbox and an acknowledgement by an executor are different evidence.
func RenderPlanAudit(ctx context.Context, loc i18n.Locale, store *core.TaskStore, planID string) (string, error) {
	if planID == "" {
		return "", nil
	}
	audit, err := store.PlanAudit(ctx, planID)
	if err != nil || len(audit.Decisions) == 0 {
		return "", err
	}
	var b strings.Builder
	b.WriteString(i18n.T(loc, "ask.plan.auditTitle"))
	b.WriteByte('\n')
	for _, d := range audit.Decisions {
		fmt.Fprintf(&b, "\n- #%d · %s", d.EventID, d.Source)
		if d.RequestedTarget != "" && d.RequestedTarget != d.Target {
			fmt.Fprintf(&b, " → %s", d.RequestedTarget)
		}
		if d.Target != "" {
			fmt.Fprintf(&b, " → %s", d.Target)
		}
		fmt.Fprintf(&b, " · %s (%s)", d.Kind, d.Outcome)
		if d.ReadEventID > 0 {
			fmt.Fprintf(&b, " · %s #%d", i18n.T(loc, "ask.plan.auditRead"), d.ReadEventID)
		}
		if d.State != "" {
			fmt.Fprintf(&b, " · %s", d.State)
		}
		if d.Verification != "" {
			fmt.Fprintf(&b, " · %s", d.Verification)
		}
		for _, text := range []string{d.Reason, d.Instruction, d.Detail} {
			if text != "" {
				b.WriteString(" · ")
				b.WriteString(strings.Join(strings.Fields(text), " "))
			}
		}
	}
	if audit.Omitted > 0 {
		b.WriteString("\n\n")
		b.WriteString(i18n.Tf(loc, "ask.plan.auditEarlier", "n", fmt.Sprint(audit.Omitted)))
	}
	return b.String(), nil
}

// Prepare the final transcript before taking the run settlement's write lock.
func (s *ConversationService) planDeliveryTurns(ctx context.Context, sid, round string) ([]sessions.Turn, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT DISTINCT t.plan_id FROM ask_round_tasks a JOIN tasks t ON t.task_id=a.task_id
 WHERE a.round_id=? AND t.session_id=? AND t.plan_id!='' AND t.parent_id='' ORDER BY t.plan_id`, round, sid)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	st := s.sessionStore()
	sess, err := st.Get(sid)
	if err != nil {
		return nil, err
	}
	var turns []sessions.Turn
	store := s.engine.TaskStore()
	for _, id := range ids {
		stages, err := store.PlanStages(ctx, id)
		if err != nil {
			return nil, err
		}
		outcome := &Result{PlanID: id, OK: true}
		if err := s.engine.planBoardVerdict(ctx, store, outcome, stages); err != nil {
			return nil, err
		}
		text := outcome.Answer
		kind := "answer"
		if !outcome.OK {
			kind = "error"
			if text == "" {
				text = outcome.Stderr
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		seen := false
		for _, turn := range sess.Turns {
			if turn.IdempotencyKey == "plan:assistant:"+round && turn.Text == text {
				seen = true
				break
			}
		}
		if !seen {
			turns = append(turns, sessions.Turn{Role: "assistant", Kind: kind, Text: text, OperationID: round, RoundID: round, IdempotencyKey: "plan:delivery:" + round + ":" + id})
		}
	}
	return turns, nil
}
