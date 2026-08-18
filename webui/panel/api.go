package panel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/xenith/openpanda/internal/ledger"
	"github.com/xenith/openpanda/internal/memory"
)

// askRequest is the body of POST /api/ask.
type askRequest struct {
	Prompt    string `json:"prompt"`
	Authorize bool   `json:"authorize"`
}

// askResult is the wire form of askengine.Result: an answer carries text; a
// task carries its id/state plus execution output.
type askResult struct {
	Kind      string `json:"kind"` // "answer" | "task"
	Answer    string `json:"answer,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	TaskState string `json:"task_state,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
}

// ask serves POST /api/ask — the unified entry: one prompt in, answer or task
// out, exactly the pipeline `panda ask` runs (shared askengine). This is the
// panel's task-creation path: the web equivalent of typing at the CLI. Input
// is validated before the engine check so malformed requests get 400s even
// from an answers-only panel.
func (h *handler) ask(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, errors.New("prompt must not be empty"))
		return
	}
	if h.engine == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured (set model.base_url to enable /api/ask)"))
		return
	}

	out, err := h.engine.Ask(r.Context(), req.Prompt, req.Authorize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, askResult{
		Kind:      out.Kind,
		Answer:    out.Answer,
		TaskID:    out.TaskID,
		TaskState: out.TaskState,
		OK:        out.OK,
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
	})
}

// cancelTask serves POST /api/tasks/{id}/cancel — cancels a task and its
// subtree, the web equivalent of `panda cancel`.
func (h *handler) cancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ids, err := h.store.CancelCascade(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, errors.New("no such task"))
			return
		}
		writeErr(w, http.StatusInternalServerError, errors.New("cancel failed"))
		return
	}
	writeJSON(w, map[string]any{"id": id, "cancelled": len(ids)})
}

// taskLogs serves GET /api/tasks/{id}/logs — the event timeline only, the web
// equivalent of `panda logs`.
func (h *handler) taskLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := h.store.Events(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load events failed"))
		return
	}
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, eventJSON{TS: e.TS, Type: e.Type, Data: e.DataJSON})
	}
	writeJSON(w, map[string]any{"id": id, "events": out})
}

// nodeJSON is the wire form of a capability-directory node.
type nodeJSON struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Chip      string   `json:"chip"`
	LastSeen  string   `json:"last_seen"`
	Abilities []string `json:"abilities"`
}

// listNodes serves GET /api/nodes — the local capability directory (every node
// this one knows about: itself via the daemon's heartbeat, plus remote peers).
func (h *handler) listNodes(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("node directory not configured"))
		return
	}
	nodes, err := ledger.Query(h.db, "", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("query nodes failed"))
		return
	}
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		seen := "never"
		if n.LastSeen != 0 {
			seen = time.Unix(n.LastSeen, 0).Format(time.RFC3339)
		}
		out = append(out, nodeJSON{
			ID:        n.ID,
			Status:    n.Status,
			Chip:      n.Chip,
			LastSeen:  seen,
			Abilities: n.Abilities(),
		})
	}
	writeJSON(w, out)
}

// listProjects serves GET /api/projects — the project names known to the
// memory layer.
func (h *handler) listProjects(w http.ResponseWriter, r *http.Request) {
	if h.projects == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("projects not configured"))
		return
	}
	names, err := h.projects.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("list projects failed"))
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, map[string]any{"projects": names})
}

// createProjectRequest is the body of POST /api/projects.
type createProjectRequest struct {
	Name string `json:"name"`
}

// createProject serves POST /api/projects — creates a project by seeding its
// memory file (the memory layer's notion of "a project exists").
func (h *handler) createProject(w http.ResponseWriter, r *http.Request) {
	if h.projects == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("projects not configured"))
		return
	}
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := memory.ValidateName(req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid project name"))
		return
	}
	// Idempotent create: saving the empty seed marks the project as existing
	// (List lists directories); re-creating an existing project is a no-op.
	if err := h.projects.Save(req.Name, memory.MemFile{Limit: memory.ProjectCharLimit}); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("create project failed"))
		return
	}
	writeJSON(w, map[string]string{"name": req.Name, "status": "created"})
}
