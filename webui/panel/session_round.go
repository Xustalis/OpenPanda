// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func (h *handler) readSessionRound(w http.ResponseWriter, req *http.Request) (core.AskRound, bool) {
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task store unavailable"))
		return core.AskRound{}, false
	}
	round, err := h.store.AskRoundProgress(req.Context(), req.PathValue("id"), req.PathValue("round"))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, errors.New("no such round in this session"))
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("could not read round"))
	}
	return round, err == nil
}

func (h *handler) sessionRound(w http.ResponseWriter, req *http.Request) {
	round, ok := h.readSessionRound(w, req)
	if !ok {
		return
	}
	writeJSON(w, map[string]string{"state": roundPhase(round)})
}

func roundPhase(round core.AskRound) string {
	if round.Status == "open" {
		return "running"
	}
	needsReview, interrupted := false, round.Status != "closed"
	for _, task := range round.Tasks {
		switch {
		case task.State == core.StateReview:
			needsReview = true
		case task.State == core.StateFailed && !round.PendingRecovery[task.TaskID]:
			interrupted = true
		case !core.Terminal(task.State):
			return "running"
		case task.State != core.StateDone:
			interrupted = true
		}
	}
	if needsReview {
		return "review"
	}
	if interrupted {
		return "interrupted"
	}
	return "finished"
}

// Seal this operation before stopping its work. The normal session Stop can
// detach from a dispatch, whereas the goal controls must stop the task tree too.
func (h *handler) stopSessionRound(w http.ResponseWriter, req *http.Request) {
	round, ok := h.readSessionRound(w, req)
	if !ok {
		return
	}
	ctx := req.Context()
	if err := h.store.CloseAskRound(ctx, round.ID, "stopped"); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("could not stop round"))
		return
	}
	h.cancelSessionAsk(round.SessionID, round.ID)
	// Dispatch could have committed after the first read. Enumerate again
	// after sealing, and use the same cascade and delivery as task Cancel.
	round, ok = h.readSessionRound(w, req)
	if !ok {
		return
	}
	eng := h.currentEngine()
	for _, task := range round.Tasks {
		if core.Terminal(task.State) {
			continue
		}
		var err error
		if eng != nil {
			_, err = eng.CancelTask(ctx, task.TaskID)
		} else {
			_, err = h.store.CancelCascade(ctx, task.TaskID)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusInternalServerError, errors.New("could not stop round tasks"))
			return
		}
	}
	writeJSON(w, map[string]bool{"stopped": true})
}
