package panel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
)

// askRequest is the body of POST /api/ask.
type askRequest struct {
	Prompt    string `json:"prompt"`
	Authorize bool   `json:"authorize"`
}

// askResult is the wire form of askengine.Result: an answer carries text; a
// task carries its id/state plus execution output.
type askResult struct {
	Kind      string `json:"kind"` // "answer" | "task" | "plan"
	Answer    string `json:"answer,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	TaskState string `json:"task_state,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	// Plan fields (kind == "plan"): a multi-stage pipeline has no single task id
	// and no result yet — its stages are queued and run on other machines — so the
	// client follows it by plan id and shows the stage ids it was decomposed into.
	PlanID     string   `json:"plan_id,omitempty"`
	PlanGoal   string   `json:"plan_goal,omitempty"`
	PlanStages []string `json:"plan_stages,omitempty"`
}

// planResultOf maps one ask outcome into the panel wire form, so the ask and
// session-ask handlers cannot drift on which fields a plan carries.
func planResultOf(out *askengine.Result) askResult {
	res := askResult{
		Kind:      out.Kind,
		Answer:    out.Answer,
		TaskID:    out.TaskID,
		TaskState: out.TaskState,
		OK:        out.OK,
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
		PlanID:    out.PlanID,
		PlanGoal:  out.PlanGoal,
	}
	for _, t := range out.PlanStages {
		res.PlanStages = append(res.PlanStages, t.StageID)
	}
	return res
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
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured (set model.base_url to enable /api/ask)"))
		return
	}

	out, err := eng.Ask(r.Context(), req.Prompt, req.Authorize)
	if err != nil {
		// A missing API key is a configuration gap, not a server fault: 503
		// keeps it in the same family as "engine not configured" so clients
		// treat it as "finish setup and retry" rather than a crash.
		if errors.Is(err, entry.ErrNoKey) {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, planResultOf(out))
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

// listNodes serves GET /api/nodes — the local capability directory (every node
// this one knows about: itself via the daemon's heartbeat, plus remote peers).
// Each row carries the full card breakdown (hardware capacity, resource
// profile, native ids, per-agent capabilities) so the console can expand a
// node's detail without a second round-trip (C3).
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
	out := make([]nodeRow, 0, len(nodes))
	for _, n := range nodes {
		row := toNodeRow(n)
		if h.cfg != nil && n.ID == localNodeID(h.cfg) {
			row.IsLocal = true
			held, err := nodeidentity.Held(h.cfg.Node.Kind, h.cfg.Node.EffectiveIdentity())
			row.Running = err == nil && held && n.Status == "online"
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// getProjectMemory serves GET /api/projects/{name}/memory — one project's
// MEMORY.md content (the read half of PUT /api/projects/{name}/memory),
// loaded through the Projects store so name validation and the configured
// cap apply. A project without a memory file reads as empty, not an error.
func (h *handler) getProjectMemory(w http.ResponseWriter, r *http.Request) {
	if h.projects == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("projects not configured"))
		return
	}
	name := r.PathValue("name")
	if err := memory.ValidateName(name); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid project name"))
		return
	}
	mf, err := h.projects.Load(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load project memory failed"))
		return
	}
	writeJSON(w, map[string]any{
		"project": name,
		"content": string(mf.Bytes()),
		"limit":   h.projects.Limit(),
	})
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
	if err := h.projects.Save(req.Name, memory.MemFile{Limit: h.projects.Limit()}); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("create project failed"))
		return
	}
	writeJSON(w, map[string]string{"name": req.Name, "status": "created"})
}
