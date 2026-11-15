// SPDX-License-Identifier: AGPL-3.0-or-later
package panel

import (
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/taskflow"
)

func (h *handler) listConnections(w http.ResponseWriter, r *http.Request) {
	v, e := h.taskFlow.List(r.Context())
	if e != nil {
		writeWorkError(w, e)
		return
	}
	writeJSON(w, v)
}
func (h *handler) getConnection(w http.ResponseWriter, r *http.Request) {
	v, e := h.taskFlow.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		writeWorkError(w, e)
		return
	}
	writeJSON(w, v)
}
func (h *handler) connectionMembers(w http.ResponseWriter, r *http.Request) {
	v, err := h.taskFlow.Members(r.Context(), r.PathValue("id"), r.PathValue("space"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, v)
}
func (h *handler) connectTasks(w http.ResponseWriter, r *http.Request) {
	var opts taskflow.ConnectOptions
	if e := decodeWorkJSON(w, r, &opts, false); e != nil {
		writeWorkError(w, e)
		return
	}
	v, e := h.taskFlow.Connect(r.Context(), opts)
	if e != nil {
		writeWorkError(w, e)
		return
	}
	writeJSON(w, v)
}
func (h *handler) exchangeTasks(w http.ResponseWriter, r *http.Request) {
	v, e := h.taskFlow.FinishConnect(r.Context(), r.PathValue("id"))
	if e != nil {
		writeWorkError(w, e)
		return
	}
	writeJSON(w, v)
}
func (h *handler) syncTasks(w http.ResponseWriter, r *http.Request) {
	if e := h.taskFlow.RequestSync(r.Context(), r.PathValue("id")); e != nil {
		writeWorkError(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"queued":true}`))
}
func (h *handler) disconnectTasks(w http.ResponseWriter, r *http.Request) {
	if e := h.taskFlow.Disconnect(r.Context(), r.PathValue("id")); e != nil {
		writeWorkError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"disconnected": true})
}
