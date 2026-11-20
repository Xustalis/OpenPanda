// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"errors"
	"net/http"
)

// retryTask repeats failed execution without accepting work or remembering an
// authorization grant. Eligibility is rechecked atomically by the core claim.
func (h *handler) retryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := h.store.ReviewFailure(r.Context(), id)
	if err != nil || !info.RetryAvailable {
		writeErr(w, http.StatusConflict, errors.New("task is not a reviewed execution failure"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task execution engine is unavailable"))
		return
	}
	h.startTaskOperation(w, r, eng, id, "", operationRetry)
}
