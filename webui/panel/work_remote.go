// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func (h *handler) createRemoteWork(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title       string                   `json:"title"`
		Description string                   `json:"description"`
		Priority    *int                     `json:"priority"`
		ProjectID   string                   `json:"projectId"`
		StartAt     *taskproviders.TypedTime `json:"startAt"`
		DueAt       *taskproviders.TypedTime `json:"dueAt"`
	}
	if err := decodeWorkJSON(w, r, &input, false); err != nil {
		writeWorkError(w, err)
		return
	}
	priority := 2
	if input.Priority != nil {
		priority = *input.Priority
	}
	connection := r.PathValue("id")
	result, err := h.taskFlow.CreateTask(r.Context(), connection, r.PathValue("space"), taskproviders.Snapshot{Title: input.Title, Description: input.Description, Priority: priority, ProjectID: input.ProjectID, StartAt: input.StartAt, DueAt: input.DueAt}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+result.TaskRevision+`"`)
	writeJSON(w, taskhub.Task{ConnectionID: connection, Snapshot: result})
}
func (h *handler) patchRemoteWork(w http.ResponseWriter, r *http.Request, k taskproviders.Key, revision string) {
	var raw json.RawMessage
	if err := decodeWorkJSON(w, r, &raw, false); err != nil {
		writeWorkError(w, err)
		return
	}
	connection := r.URL.Query().Get("connection")
	if connection == "" || connection == "local" || h.taskFlow == nil {
		writeWorkError(w, taskproviders.ErrUnsupported)
		return
	}
	result, err := h.taskFlow.PatchTask(r.Context(), connection, k, revision, raw, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+result.TaskRevision+`"`)
	writeJSON(w, taskhub.Task{ConnectionID: connection, Snapshot: result})
}
