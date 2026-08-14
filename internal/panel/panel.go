// Package panel serves the PWA control panel: the static web app under web/pwa
// plus the JSON API that backs it — task queue, task detail, and the human
// approval of reviewed tasks (design §14.2 Layer 4). It is the HTTP face of the
// daemon, distinct from the node-to-node WebSocket transport in internal/bus.
package panel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/xenith/panda/internal/core"
)

// New builds the panel HTTP handler. staticDir is the directory holding the PWA
// static files (web/pwa); JSON endpoints are served under /api/.
func New(store *core.TaskStore, staticDir string) http.Handler {
	h := &handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/approve", h.approveTask)
	mux.HandleFunc("POST /api/tasks/{id}/reject", h.rejectTask)
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))
	return mux
}

type handler struct {
	store *core.TaskStore
}

// taskJSON is the wire form of a task row, with stable snake_case names so the
// PWA does not depend on Go field casing.
type taskJSON struct {
	ID        string      `json:"id"`
	ParentID  string      `json:"parent_id"`
	Project   string      `json:"project"`
	Title     string      `json:"title"`
	State     string      `json:"state"`
	Owner     string      `json:"owner"`
	AttemptID string      `json:"attempt_id"`
	Intent    string      `json:"intent,omitempty"`
	Spec      string      `json:"spec,omitempty"`
	Result    string      `json:"result,omitempty"`
	Risk      string      `json:"risk,omitempty"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
	Events    []eventJSON `json:"events,omitempty"`
}

type eventJSON struct {
	TS   int64  `json:"ts"`
	Type string `json:"type"`
	Data string `json:"data"`
}

func toTaskJSON(t core.Task) taskJSON {
	return taskJSON{
		ID:        t.TaskID,
		ParentID:  t.ParentID,
		Project:   t.Project,
		Title:     t.Title,
		State:     t.State,
		Owner:     t.OwnerNode,
		AttemptID: t.AttemptID,
		Intent:    t.Intent,
		Spec:      t.SpecJSON,
		Result:    t.ResultJSON,
		Risk:      t.Risk,
		CreatedAt: ts(t.CreatedAt),
		UpdatedAt: ts(t.UpdatedAt),
	}
}

// listTasks serves the queue, optionally filtered by state and project.
func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	project := r.URL.Query().Get("project")

	tasks, err := h.store.ListByState(r.Context(), state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var filtered []core.Task
	for _, t := range tasks {
		if project == "" || t.Project == project {
			filtered = append(filtered, t)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].UpdatedAt > filtered[j].UpdatedAt })

	out := make([]taskJSON, 0, len(filtered))
	for _, t := range filtered {
		out = append(out, toTaskJSON(t))
	}
	writeJSON(w, out)
}

// getTask serves one task's full row plus its event timeline.
func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, errors.New("no such task"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := toTaskJSON(t)
	if events, err := h.store.Events(r.Context(), id); err == nil {
		for _, e := range events {
			out.Events = append(out.Events, eventJSON{TS: e.TS, Type: e.Type, Data: e.DataJSON})
		}
	}
	writeJSON(w, out)
}

// approveTask accepts a reviewed task (review -> done).
func (h *handler) approveTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Approve(r.Context(), id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id, "status": "approved"})
}

// rejectTask rejects a reviewed task (review -> failed).
func (h *handler) rejectTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Reject(r.Context(), id, r.URL.Query().Get("reason")); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]string{"id": id, "status": "rejected"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func ts(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}
