// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The dev-server surface: what a project's .panda/dev.json declares, whether
// its server is up, and the tokenized preview link the console frames. The
// reverse proxy itself lives in internal/devserver; the panel only exposes a
// status/start/stop API, so a project's own command is never inferred from a
// request.

import (
	"errors"
	"net/http"
)

// devStatus serves GET /api/dev/{project} — the current state, the declared
// target, a log tail, and (when ready) the preview link for this request host.
func (h *handler) devStatus(w http.ResponseWriter, r *http.Request) {
	if h.dev == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("dev preview is not configured"))
		return
	}
	writeJSON(w, h.dev.Status(r.PathValue("project"), r.Host))
}

// startDev serves POST /api/dev/{project}/start. It returns promptly; the
// server becomes ready asynchronously, and the console polls the status.
func (h *handler) startDev(w http.ResponseWriter, r *http.Request) {
	if h.dev == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("dev preview is not configured"))
		return
	}
	name := r.PathValue("project")
	if _, err := h.dev.StartProject(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, h.dev.Status(name, r.Host))
}

// stopDev serves POST /api/dev/{project}/stop.
func (h *handler) stopDev(w http.ResponseWriter, r *http.Request) {
	if h.dev == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("dev preview is not configured"))
		return
	}
	name := r.PathValue("project")
	h.dev.StopProject(name)
	writeJSON(w, h.dev.Status(name, r.Host))
}
