package panel

import (
	"errors"
	"net/http"
	"time"

	"github.com/Xustalis/OpenPanda/internal/security"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

// getVersion serves GET /api/version — the web equivalent of `panda version`.
func (h *handler) getVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"version": versionpkg.Version})
}

// metricJSON is the wire form of a core.DelegationMetric row.
type metricJSON struct {
	ID        int64   `json:"id"`
	TaskID    string  `json:"task_id"`
	Delegator string  `json:"delegator"`
	Executor  string  `json:"executor"`
	Abilities string  `json:"abilities"`
	Success   bool    `json:"success"`
	LatencyMs int64   `json:"latency_ms"`
	Tokens    *int64  `json:"tokens"`
	CreatedAt string  `json:"created_at"`
}

// listMetrics serves GET /api/metrics — delegation outcome rows, newest
// first, the web equivalent of `panda metrics` (without the CSV export).
func (h *handler) listMetrics(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListDelegationMetrics(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load metrics failed"))
		return
	}
	out := make([]metricJSON, 0, len(rows))
	for _, m := range rows {
		var tokens *int64
		if m.Tokens.Valid {
			tokens = &m.Tokens.Int64
		}
		out = append(out, metricJSON{
			ID:        m.ID,
			TaskID:    m.TaskID,
			Delegator: m.Delegator,
			Executor:  m.Executor,
			Abilities: m.AbilitiesJSON,
			Success:   m.Success,
			LatencyMs: m.LatencyMs,
			Tokens:    tokens,
			CreatedAt: time.Unix(m.CreatedAt, 0).Format(time.RFC3339),
		})
	}
	writeJSON(w, out)
}

// verifyAudit serves GET /api/audit — integrity verification of the tamper
// -evident hash chains, the web equivalent of `panda audit verify`. With
// ?task_id= it verifies that task's event chain; otherwise the global audit
// chain. Both return {ok, entries?, error?}.
func (h *handler) verifyAudit(w http.ResponseWriter, r *http.Request) {
	if taskID := r.URL.Query().Get("task_id"); taskID != "" {
		err := h.store.VerifyTaskEventChain(r.Context(), taskID)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "scope": "task:" + taskID, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "scope": "task:" + taskID})
		return
	}
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("audit store not configured"))
		return
	}
	entries, err := security.NewAudit(h.db).Entries(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load audit log failed"))
		return
	}
	if err := security.NewAudit(h.db).VerifyChain(r.Context()); err != nil {
		writeJSON(w, map[string]any{"ok": false, "scope": "global", "entries": len(entries), "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "scope": "global", "entries": len(entries)})
}

// auditEntries serves GET /api/audit/entries — the recent global audit rows
// for display alongside the verification result.
func (h *handler) auditEntries(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("audit store not configured"))
		return
	}
	rows, err := security.NewAudit(h.db).Entries(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load audit log failed"))
		return
	}
	type row struct {
		TS     string `json:"ts"`
		Who    string `json:"who"`
		What   string `json:"what"`
		Target string `json:"target"`
		Result string `json:"result"`
		Detail string `json:"detail"`
	}
	out := make([]row, 0, len(rows))
	for _, e := range rows {
		out = append(out, row{
			TS:     time.Unix(e.TS, 0).Format(time.RFC3339),
			Who:    e.Who,
			What:   e.What,
			Target: e.Target,
			Result: e.Result,
			Detail: e.Detail,
		})
	}
	writeJSON(w, out)
}
