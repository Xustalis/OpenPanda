// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

type deliveryEvidence struct {
	status, judge, decision     string
	executionMS, approvalWaitMS int64
}

func sessionDeliveryExcerpt(state, result string) string {
	switch state {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
	default:
		return ""
	}
	const limit = 1800
	text := extractResultText(result)
	n := 0
	for i := range text {
		if n == limit {
			return text[:i]
		}
		n++
	}
	return text
}

func (h *handler) taskDeliveryEvidence(ctx context.Context, task core.Task) deliveryEvidence {
	events, err := h.taskAttemptEvents(ctx, task.TaskID)
	if err != nil {
		return deliveryEvidence{}
	}
	return summarizeDelivery(task, events)
}

func summarizeDelivery(task core.Task, events []core.Event) deliveryEvidence {
	out := deliveryEvidence{status: "unverified", judge: attemptVerdict(events)}
	var start, parkedAt, workWait, end int64
	for _, ev := range events {
		if ev.TS > task.UpdatedAt {
			continue
		}
		switch ev.Type {
		case core.EvAccept, core.EvExecAgentStart:
			if start == 0 {
				start = ev.TS
			}
		case core.EvReview:
			var review struct {
				Reason    string  `json:"reason"`
				Approved  bool    `json:"approved"`
				Resumed   bool    `json:"resumed"`
				Continued bool    `json:"continued"`
				Rejected  *string `json:"rejected"`
			}
			if json.Unmarshal([]byte(ev.DataJSON), &review) != nil {
				continue
			}
			if review.Approved || review.Continued || review.Rejected != nil {
				if parkedAt > 0 {
					out.approvalWaitMS += max(0, ev.TS-parkedAt) * 1000
					if start > 0 {
						workWait += max(0, ev.TS-max(start, parkedAt))
					}
				}
				if review.Approved && !review.Resumed {
					out.decision = "approved"
					end = ev.TS
				} else if review.Rejected != nil {
					out.decision = "rejected"
					end = ev.TS
				}
				parkedAt = 0
			} else if parkedAt == 0 && review.Reason != "" {
				parkedAt = ev.TS
				out.decision = ""
			}
		}
	}
	if end == 0 {
		end = task.UpdatedAt
	}
	if parkedAt > 0 && end > parkedAt {
		out.approvalWaitMS += (end - parkedAt) * 1000
		if start > 0 {
			workWait += max(0, end-max(start, parkedAt))
		}
	}
	if start > 0 {
		out.executionMS = max(0, end-start-workWait) * 1000
	}
	if task.State == core.StateDone {
		if task.Verification == contract.VerdictVerified {
			out.status = "verified"
		} else if out.decision == "approved" {
			out.status = "manual"
		}
		if out.decision == "approved" {
			// The historical judge reason may be an approval error. It is not
			// the verdict of the later human decision; keep it in events only.
			out.judge = ""
		}
	}
	return out
}

// Correct legacy receipts only in the API's display copy. A prior attempt's
// report is historical and must never be re-labelled from the current row.
func (h *handler) projectSessionDeliveries(ctx context.Context, sess *sessions.Session) {
	if h.store == nil {
		return
	}
	for i := range sess.Turns {
		turn := &sess.Turns[i]
		if turn.Kind != "answer" || turn.Ref == "" || turn.DeliveryStatus != "" || !strings.HasPrefix(turn.Text, "[") {
			continue
		}
		task, err := h.store.Get(ctx, turn.Ref)
		if err != nil || task.SessionID != sess.ID || (turn.DeliveryAttemptID != "" && turn.DeliveryAttemptID != task.AttemptID) ||
			(turn.DeliveryAttemptID == "" && (turn.TS == 0 || turn.TS < task.UpdatedAt)) {
			continue
		}
		switch task.State {
		case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
		default:
			continue
		}
		evidence := h.taskDeliveryEvidence(ctx, task)
		if turn.Verdict != evidence.judge && turn.Verdict != "" {
			// Preserve agent attribution and the report, replacing only the
			// display digest generated from stale review evidence.
			turn.Text = h.taskSummary(ctx, task, evidence.judge)
		}
		turn.Verdict, turn.DeliveryStatus = evidence.judge, evidence.status
		turn.DurationMS, turn.ApprovalWaitMS = evidence.executionMS, evidence.approvalWaitMS
	}
}
