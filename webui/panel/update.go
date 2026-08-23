package panel

import (
	"net/http"
)

// getUpdate serves GET /api/update — the self-update pipeline's point-in-time
// status (stage, current/latest version, and whether the task queue is idle).
// The console polls it to track progress across check, download, and apply.
func (h *handler) getUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.updater.Status())
}

// checkUpdate runs a version check against GitHub and returns the fresh
// status. A lookup failure is recorded in the manager's status (stage "error")
// rather than failing the request, so the console can render the message
// inline and offer a retry.
func (h *handler) checkUpdate(w http.ResponseWriter, r *http.Request) {
	_ = h.updater.Check(r.Context())
	writeJSON(w, h.updater.Status())
}

// downloadUpdate fetches and verifies the latest release, staging it for
// apply. It returns the fresh status on success (stage "staged"); on a network
// or checksum failure it returns 500 with the reason, and the manager discards
// any partial download so no residue is left behind.
func (h *handler) downloadUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.updater.Download(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, h.updater.Status())
}

// applyUpdate installs the staged release and restarts the process. It refuses
// while the task queue is busy (409), so the console can keep polling until
// the queue drains and the update never interrupts live work.
func (h *handler) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.updater.Apply(r.Context()); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, h.updater.Status())
}

// cancelUpdate discards a staged (or in-flight) update. This is the "delete"
// path: the downloaded archive and its extracted files are removed from disk,
// leaving no residue.
func (h *handler) cancelUpdate(w http.ResponseWriter, r *http.Request) {
	h.updater.Cancel()
	writeJSON(w, h.updater.Status())
}
