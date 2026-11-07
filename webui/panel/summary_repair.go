// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

type summaryAuditRow struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	Status    string `json:"status"` // present | compacted | ambiguous | orphan | repaired | failed
}

// auditSessionSummaries is an explicit per-session recovery tool, not an
// unbounded background history rewrite. Legacy unkeyed answer turns cannot
// prove their attempt identity: leave those ambiguous rows untouched rather
// than append a second conclusion. Only a positive current/legacy marker
// without any matching persisted answer is safe to restore.
func (h *handler) auditSessionSummaries(ctx context.Context, id string, repair bool) ([]summaryAuditRow, error) {
	sess, err := h.sessions.Get(id)
	if err != nil {
		return nil, err
	}
	tasks, err := h.store.ListBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []summaryAuditRow{}
	for _, task := range tasks {
		switch task.State {
		case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
		default:
			continue
		}
		events, err := h.store.Events(ctx, task.TaskID)
		if err != nil {
			return nil, err
		}
		marked := false
		for _, ev := range events {
			if ev.Type != evSessionSummary {
				continue
			}
			var data struct {
				AttemptID string `json:"attempt_id"`
			}
			if json.Unmarshal([]byte(ev.DataJSON), &data) == nil && (data.AttemptID == "" || data.AttemptID == task.AttemptID) {
				marked = true
			}
		}
		if !marked {
			continue
		} // normal retryable finalizer owns this task
		row := summaryAuditRow{TaskID: task.TaskID, AttemptID: task.AttemptID, Status: "orphan"}
		for _, turn := range sess.Turns {
			if turn.IdempotencyKey == taskSummaryKey(task) {
				row.Status = "present"
				break
			}
			if turn.IdempotencyKey == "" && turn.Role == "assistant" && turn.Ref == task.TaskID && turn.Kind == "answer" {
				row.Status = "ambiguous"
			}
		}
		if row.Status == "orphan" {
			for _, key := range sess.CompactedTurnKeys {
				if key == taskSummaryKey(task) {
					row.Status = "compacted"
					break
				}
			}
			// Old compactions have no archived delivery keys. Absence of a turn
			// is not proof of a lost write in a summarized session.
			if row.Status == "orphan" && sess.Summary != "" {
				row.Status = "ambiguous"
			}
		}
		if repair && row.Status == "orphan" {
			// Same durable key as the normal finalizer; concurrent repair requests
			// and a process crash after append remain safe.
			if _, err := h.sessions.AppendTurn(id, sessions.Turn{Role: "assistant", Kind: "answer", Ref: task.TaskID, Text: h.taskSummary(ctx, task), IdempotencyKey: taskSummaryKey(task)}); err != nil {
				row.Status = "failed"
			} else {
				row.Status = "repaired"
				_ = h.store.RecordEvent(ctx, task.TaskID, "session_summary_repaired", map[string]string{"session_id": id, "attempt_id": task.AttemptID})
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (h *handler) sessionSummaryAudit(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task store unavailable"))
		return
	}
	if _, err := h.sessions.Get(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	rows, err := h.auditSessionSummaries(r.Context(), r.PathValue("id"), r.Method == http.MethodPost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"summaries": rows})
}
