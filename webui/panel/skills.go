package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	Builtin     bool   `json:"builtin"`
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
			Builtin:     e.Builtin,
		})
	}
	writeJSON(w, out)
}

// resetSkill restores a built-in skill to its factory default definition.
func (h *handler) resetSkill(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	var req skillActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("missing skill name"))
		return
	}
	sk, err := h.skillStore.ResetBuiltin(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": sk.Name})
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

// hubSkillJSON is the wire form of a Skills Hub skill.
type hubSkillJSON struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	URL         string   `json:"url,omitempty"`
	DocURL      string   `json:"doc_url,omitempty"`
	Recommended bool     `json:"recommended"`
	Alias       string   `json:"alias,omitempty"`
	Installed   bool     `json:"installed"`
}

// listHubSkills serves GET /api/skills/hub?q=... — browse and search available Skills Hub packages.
func (h *handler) listHubSkills(w http.ResponseWriter, r *http.Request) {
	hubURL := ""
	if h.cfg != nil {
		hubURL = h.cfg.Skills.HubURL
	}
	q := r.URL.Query().Get("q")
	idx, err := skills.FetchHubIndex(r.Context(), hubURL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("failed to fetch hub index"))
		return
	}
	filtered := skills.SearchHub(idx, q)

	installedMap := make(map[string]bool)
	if h.skillStore != nil {
		if localIndex, err := h.skillStore.Index(); err == nil {
			for _, sk := range localIndex {
				installedMap[sk.Name] = true
			}
		}
	}

	out := make([]hubSkillJSON, 0, len(filtered))
	for _, s := range filtered {
		out = append(out, hubSkillJSON{
			Name:        s.Name,
			Description: s.Description,
			Scope:       string(s.Scope),
			Author:      s.Author,
			Version:     s.Version,
			Tags:        s.Tags,
			URL:         s.URL,
			DocURL:      s.DocURL,
			Recommended: s.Recommended,
			Alias:       s.Alias,
			Installed:   installedMap[s.Name],
		})
	}
	writeJSON(w, out)
}

// installHubSkillRequest is the payload of POST /api/skills/hub/install.
type installHubSkillRequest struct {
	Name    string `json:"name"`
	Scope   string `json:"scope,omitempty"`
	Project string `json:"project,omitempty"`
	Device  string `json:"device,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// installHubSkill serves POST /api/skills/hub/install.
func (h *handler) installHubSkill(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	var req installHubSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name must not be empty"))
		return
	}
	hubURL := ""
	if h.cfg != nil {
		hubURL = h.cfg.Skills.HubURL
	}
	opts := skills.ImportOptions{
		Scope:   skills.Scope(req.Scope),
		Project: req.Project,
		Device:  req.Device,
		Force:   req.Force,
		Status:  skills.StatusActive,
	}
	sk, err := skills.InstallFromHub(r.Context(), h.skillStore, hubURL, req.Name, opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"name": sk.Name, "status": "installed"})
}

// installRecommendedSkills serves POST /api/skills/hub/install-recommended.
func (h *handler) installRecommendedSkills(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	hubURL := ""
	if h.cfg != nil {
		hubURL = h.cfg.Skills.HubURL
	}
	opts := skills.ImportOptions{
		Status: skills.StatusActive,
	}
	installed, err := skills.InstallAllRecommended(r.Context(), h.skillStore, hubURL, opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var names []string
	for _, s := range installed {
		names = append(names, s.Name)
	}
	writeJSON(w, map[string]any{"count": len(installed), "names": names, "status": "installed"})
}

// importSkillRequest is the payload of POST /api/skills/import.
type importSkillRequest struct {
	Source  string `json:"source,omitempty"`
	Content string `json:"content,omitempty"`
	Name    string `json:"name,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Project string `json:"project,omitempty"`
	Device  string `json:"device,omitempty"`
	Pending bool   `json:"pending,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// importSkill serves POST /api/skills/import — import skill from URL, path, or content.
func (h *handler) importSkill(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	var req importSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if req.Source == "" && req.Content == "" {
		writeErr(w, http.StatusBadRequest, errors.New("either source or content is required"))
		return
	}
	status := skills.StatusActive
	if req.Pending {
		status = skills.StatusPending
	}
	opts := skills.ImportOptions{
		Scope:   skills.Scope(req.Scope),
		Project: req.Project,
		Device:  req.Device,
		Name:    req.Name,
		Status:  status,
		Force:   req.Force,
	}
	if req.Content != "" {
		sk, err := h.skillStore.ImportBytes([]byte(req.Content), opts)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"name": sk.Name, "count": 1, "status": "imported"})
		return
	}
	imported, err := h.skillStore.ImportSource(r.Context(), req.Source, opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var names []string
	for _, s := range imported {
		names = append(names, s.Name)
	}
	writeJSON(w, map[string]any{"names": names, "count": len(imported), "status": "imported"})
}

// discoverSkillRequest is the payload of POST /api/skills/discover.
type discoverSkillRequest struct {
	Query string `json:"query"`
}

// discoverSkill serves POST /api/skills/discover — autonomously finds and installs the best matching skill.
func (h *handler) discoverSkill(w http.ResponseWriter, r *http.Request) {
	if h.skillStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("skill store not configured"))
		return
	}
	var req discoverSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeErr(w, http.StatusBadRequest, errors.New("query must not be empty"))
		return
	}
	hubURL := ""
	if h.cfg != nil {
		hubURL = h.cfg.Skills.HubURL
	}
	// The discover button is the user's own click — that click is the approval,
	// so the skill lands active like every other user-initiated install here.
	sk, isNew, err := h.skillStore.DiscoverAndInstall(r.Context(), hubURL, query, skills.ImportOptions{Status: skills.StatusActive})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{
		"name":        sk.Name,
		"description": sk.Description,
		"status":      sk.Status,
		"is_new":      isNew,
	})
}
