package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// listReminders serves GET /api/reminders — pending first, then recently
// fired ones, so the console shows both what's coming and what just fired.
func (h *handler) listReminders(w http.ResponseWriter, r *http.Request) {
	list, err := h.reminders.List(r.Context(), true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, list)
}

// createReminder serves POST /api/reminders — the console's add form.
// Body: {"message": "...", "after_minutes": 10} or {"message": "...", "due_at": "2026-08-18T15:00:00+08:00"}.
func (h *handler) createReminder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message      string  `json:"message"`
		AfterMinutes float64 `json:"after_minutes"`
		DueAt        string  `json:"due_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if req.Message == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}

	var due time.Time
	switch {
	case req.AfterMinutes > 0:
		due = time.Now().Add(time.Duration(req.AfterMinutes * float64(time.Minute)))
	case req.DueAt != "":
		var err error
		due, err = time.Parse(time.RFC3339, req.DueAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("due_at must be RFC3339, e.g. 2026-08-18T15:00:00+08:00"))
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("after_minutes or due_at is required"))
		return
	}

	rem, err := h.reminders.Add(r.Context(), req.Message, due, "web")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, rem)
}

// deleteReminder serves DELETE /api/reminders/{id}.
func (h *handler) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid reminder id"))
		return
	}
	ok, err := h.reminders.Delete(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no such reminder"))
		return
	}
	writeJSON(w, map[string]any{"deleted": id})
}

// reminderFingerprint digests the reminder set so the SSE feed can signal a
// change (a reminder added, deleted, or fired) and the console re-fetches.
func (h *handler) reminderFingerprint() string {
	if h.reminders == nil {
		return ""
	}
	list, err := h.reminders.List(context.Background(), false)
	if err != nil {
		return ""
	}
	// Small set — a simple inline digest is enough.
	var sum uint64 = 1469598103934665603
	for _, r := range list {
		for _, b := range []byte(strconv.FormatInt(r.ID, 10) + ":" + strconv.FormatInt(r.DueAt, 10) + ";") {
			sum = (sum ^ uint64(b)) * 1099511628211
		}
	}
	return strconv.FormatUint(sum, 16)
}
