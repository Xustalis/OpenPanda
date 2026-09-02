package panel

// The plan plane for the console.
//
// A plan is the flagship scenario — develop where the coding agent lives, train
// where the GPU lives, summarize where the user is — and the console could start
// one (POST /api/ask classifies "plan" and returns the plan id) but never look at
// one afterwards. Following a running pipeline meant `panda plan show <id>` in a
// terminal, which is a strange thing to need from a device whose whole point is
// that it is not the terminal.
//
// Two endpoints, both read-only: the board (which plans exist, how far along) and
// one plan's stages with the artifact wiring between them. Starting a plan stays
// where it already worked, on /api/ask.

import (
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// planStageJSON is one stage of a plan: an ordinary task plus its place in the
// pipeline — what it waits for, what it consumed, what it handed on. The artifact
// fields are the ones that answer "did the training stage actually get the
// script?", which is the question a pipeline view exists to answer.
type planStageJSON struct {
	Stage    string             `json:"stage"`
	TaskID   string             `json:"task_id"`
	Title    string             `json:"title,omitempty"`
	State    string             `json:"state"`
	Owner    string             `json:"owner,omitempty"`
	Needs    []string           `json:"needs,omitempty"`
	Inputs   []planArtifactJSON `json:"inputs,omitempty"`
	Output   string             `json:"output_artifact,omitempty"`
	Created  int64              `json:"created_at"`
	Updated  int64              `json:"updated_at"`
	Priority string             `json:"priority,omitempty"`
}

// planArtifactJSON names one tree a stage started from and the node it came from.
// A hash alone cannot be fetched — content addressing says what the bytes must be,
// not who holds them.
type planArtifactJSON struct {
	Stage  string `json:"stage,omitempty"`
	Hash   string `json:"hash"`
	Source string `json:"source,omitempty"`
}

// listPlans serves GET /api/plans — the plan board, most recently active first.
func (h *handler) listPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.store.ListPlans(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load plans failed"))
		return
	}
	if plans == nil {
		plans = []core.PlanSummary{}
	}
	writeJSON(w, plans)
}

// getPlan serves GET /api/plans/{id} — one plan's stages in stage order.
func (h *handler) getPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stages, err := h.store.PlanStages(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load plan failed"))
		return
	}
	if len(stages) == 0 {
		writeErr(w, http.StatusNotFound, errors.New("no such plan"))
		return
	}
	out := make([]planStageJSON, 0, len(stages))
	for _, t := range stages {
		st := planStageJSON{
			Stage: t.StageID, TaskID: t.TaskID, Title: t.Title, State: t.State,
			Owner: t.OwnerNode, Needs: t.Needs, Output: t.OutputArtifact,
			Created: t.CreatedAt, Updated: t.UpdatedAt, Priority: priorityLabel(t.Priority),
		}
		for _, in := range t.Inputs {
			st.Inputs = append(st.Inputs, planArtifactJSON{Stage: in.Stage, Hash: in.Hash, Source: in.Source})
		}
		out = append(out, st)
	}
	writeJSON(w, map[string]any{"plan_id": id, "stages": out})
}
