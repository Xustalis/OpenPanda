package panel

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/skills"
)

// skillJSON is the wire form of a skills.IndexEntry.
type skillJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Key         string `json:"key,omitempty"`
	Status      string `json:"status"`
	UseCount    int    `json:"use_count"`
}

// listSkills serves GET /api/skills — every skill with its approval status,
// the web equivalent of `panda skill list`. Pending entries are the ones
// awaiting the human sign-off that activates them.
func (h *handler) listSkills(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	index, err := h.skillStore.Index()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("index skills failed"))
		return
	}
	out := make([]skillJSON, 0, len(index))
	for _, e := range index {
		out = append(out, skillJSON{
			Name:        e.Name,
			Description: e.Description,
			Scope:       string(e.Scope),
			Key:         e.Key,
			Status:      string(e.Status),
			UseCount:    e.UseCount,
		})
	}
	writeJSON(w, out)
}

// skillActionRequest is the body of POST /api/skills/approve|reject.
type skillActionRequest struct {
	Name string `json:"name"`
}

// approveSkill and rejectSkill serve the skill approval flow — the web
// equivalent of `panda skill approve|reject`. A skill is resolved by its
// (unique) name; ambiguous names are refused rather than guessed.
func (h *handler) skillAction(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.skillStore == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
			return
		}
		var req skillActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
		if req.Name == "" {
			writeErr(w, http.StatusBadRequest, errors.New("name must not be empty"))
			return
		}
		index, err := h.skillStore.Index()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("index skills failed"))
			return
		}
		var entry *skills.IndexEntry
		for i := range index {
			if index[i].Name == req.Name {
				if entry != nil {
					writeErr(w, http.StatusConflict, errors.New("multiple skills with that name; rename to make it unique"))
					return
				}
				entry = &index[i]
			}
		}
		if entry == nil {
			writeErr(w, http.StatusNotFound, errors.New("no such skill"))
			return
		}
		if approve {
			err = h.skillStore.Approve(entry.Scope, entry.Key, entry.Name)
		} else {
			err = h.skillStore.Reject(entry.Scope, entry.Key, entry.Name)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("update skill failed"))
			return
		}
		action := "approved"
		if !approve {
			action = "rejected"
		}
		writeJSON(w, map[string]string{"name": req.Name, "status": action})
	}
}
