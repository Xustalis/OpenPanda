// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// needsObjectiveRejudge reports whether approving this parked row can only be
// refused by the MAX-objective evidence gate. The assessments that gate demands
// come from the reviewing model, so a row parked without them is a dead end for
// a human: the card must offer a re-review instead of an approve button that
// cannot succeed. Cheap enough for the list path — review rows are rare and the
// spec/result are already on the projection.
func needsObjectiveRejudge(t taskJSON) bool {
	if t.State != string(core.StateReview) || t.ApprovalDisposition != string(core.ApprovalAcceptWork) {
		return false
	}
	row := core.Task{ParentID: t.ParentID, SpecJSON: t.Spec, ResultJSON: t.Result}
	if t.PlanMeta != nil {
		row.PlanID = t.PlanMeta.PlanID
	}
	return core.ObjectiveEvidenceError(row) != nil
}

// rejudgeTask asks the reviewing model again about work already stored on a
// review-parked task. It is the operator's way out of a park whose verdict was
// unusable: the agent is not re-run, only the judgment is.
func (h *handler) rejudgeTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	if cur.State != core.StateReview {
		writeErr(w, http.StatusConflict, errors.New("task is not awaiting review"))
		return
	}
	if disposition, derr := h.store.ApprovalDisposition(r.Context(), id); derr != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("re-review failed"))
		return
	} else if disposition != core.ApprovalAcceptWork {
		writeErr(w, http.StatusConflict, errors.New("only a reviewed result can be re-reviewed"))
		return
	}
	if core.ObjectiveEvidenceError(cur) == nil {
		// Nothing is blocking the approval: re-judging would spend a model call
		// to answer a question the operator can already settle with Approve.
		writeErr(w, http.StatusConflict, errors.New("task needs no re-review — approve it instead"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task execution engine is unavailable"))
		return
	}
	h.startTaskOperation(w, r, eng, id, "", operationRejudge)
}
