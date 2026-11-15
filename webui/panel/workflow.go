// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/taskflow"
)

func (h *handler) workPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var p taskflow.Policy
		if err := decodeWorkJSON(w, r, &p, false); err != nil {
			writeWorkError(w, err)
			return
		}
		if err := h.taskFlow.SetPolicy(r.Context(), r.PathValue("id"), r.PathValue("space"), p); err != nil {
			writeWorkError(w, err)
			return
		}
	}
	p, err := h.taskFlow.Policy(r.Context(), r.PathValue("id"), r.PathValue("space"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, p)
}

func (h *handler) workRuns(w http.ResponseWriter, r *http.Request) {
	x, err := h.taskFlow.Runs(r.Context(), r.URL.Query().Get("connection"), r.URL.Query().Get("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, x)
}

func (h *handler) workRunResult(w http.ResponseWriter, r *http.Request) {
	if format := r.PathValue("format"); format != "" && format != "export" {
		http.NotFound(w, r)
		return
	}
	x, err := h.taskFlow.Work(r.Context(), r.PathValue("id"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	if r.PathValue("format") == "export" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="work-result.txt"`)
		_, _ = w.Write([]byte(x.Export()))
		return
	}
	writeJSON(w, x)
}

func (h *handler) workResearch(w http.ResponseWriter, r *http.Request) {
	x, err := h.taskFlow.Research(r.Context(), r.URL.Query().Get("connection"), r.PathValue("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, x)
}

func (h *handler) workActionDecision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fingerprint string `json:"fingerprint"`
		Decision    string `json:"decision"`
	}
	if err := decodeWorkJSON(w, r, &in, false); err != nil {
		writeWorkError(w, err)
		return
	}
	var err error
	switch in.Decision {
	case "approve":
		err = h.taskFlow.ApproveAction(r.Context(), r.PathValue("id"), in.Fingerprint)
	case "reject":
		err = h.taskFlow.RejectAction(r.Context(), r.PathValue("id"), in.Fingerprint)
	default:
		writeErr(w, 400, errWorkDecision)
		return
	}
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"recorded": true})
}

func (h *handler) workDeliveryDecision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision uint64 `json:"revision"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := decodeWorkJSON(w, r, &in, false); err != nil {
		writeWorkError(w, err)
		return
	}
	if err := h.taskFlow.DecideDelivery(r.Context(), r.PathValue("id"), in.Revision, in.Decision, in.Reason); err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"recorded": true})
}

func (h *handler) pauseWorkRun(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if err := decodeWorkJSON(w, r, &in, true); err != nil {
		writeWorkError(w, err)
		return
	}
	if err := h.taskFlow.PauseWork(r.Context(), r.PathValue("id")); err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"recorded": true})
}
