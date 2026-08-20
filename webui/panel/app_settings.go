package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// appSettingsJSON is the wire form of the four "app policy" config groups
// (C1): model injection, routing preferences, memory caps, and the approval
// gate. Sandbox is GET-only — it describes the confinement every agent
// subprocess already runs under (security.Sandbox), it is not a switch.
type appSettingsJSON struct {
	InjectionModel  string           `json:"injection_model"` // auto | always | never
	PreferredAgents []string         `json:"preferred_agents"`
	MemoryLimits    memoryLimitsJSON `json:"memory_limits"`
	ApprovalMode    string           `json:"approval_mode"` // always | on-request | never
	Sandbox         *sandboxJSON     `json:"sandbox,omitempty"`
}

type memoryLimitsJSON struct {
	User    int `json:"user"`
	Memory  int `json:"memory"`
	Project int `json:"project"`
}

type sandboxJSON struct {
	WorkPath string `json:"work_path"`
}

// getAppSettings serves GET /api/settings/app — the live values of the four
// policy groups plus the read-only sandbox description.
func (h *handler) getAppSettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	writeJSON(w, appSettingsJSON{
		InjectionModel:  h.cfg.Injection.NormalizedModel(),
		PreferredAgents: append([]string{}, h.cfg.Routing.PreferredAgents...),
		MemoryLimits: memoryLimitsJSON{
			User:    h.cfg.Memory.Limits.User,
			Memory:  h.cfg.Memory.Limits.Memory,
			Project: h.cfg.Memory.Limits.Project,
		},
		ApprovalMode: h.cfg.Approval.NormalizedMode(),
		Sandbox:      &sandboxJSON{WorkPath: h.cfg.Storage.WorkPath},
	})
}

// putAppSettings serves PUT /api/settings/app — validate the four policy
// groups, persist them to config.yaml with comments preserved (one
// comment-preserving config.UpdateSection* write per field), then refresh the
// in-memory config the engine shares so the next task already
// routes/injects/approves with the new policy. No restart needed.
func (h *handler) putAppSettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var req appSettingsJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}

	injection := strings.TrimSpace(req.InjectionModel)
	switch injection {
	case config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever:
	default:
		writeErr(w, http.StatusBadRequest, errors.New("injection_model must be auto, always, or never"))
		return
	}
	approval := strings.TrimSpace(req.ApprovalMode)
	switch approval {
	case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
	default:
		writeErr(w, http.StatusBadRequest, errors.New("approval_mode must be always, on-request, or never"))
		return
	}
	limits := req.MemoryLimits
	if limits.User <= 0 || limits.Memory <= 0 || limits.Project <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("memory_limits values must be positive"))
		return
	}
	agents := make([]string, 0, len(req.PreferredAgents))
	seen := map[string]bool{}
	for _, name := range req.PreferredAgents {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if err := memory.ValidateName(name); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("preferred_agents contains an invalid agent name"))
			return
		}
		seen[name] = true
		agents = append(agents, name)
	}
	if len(agents) > 16 {
		writeErr(w, http.StatusBadRequest, errors.New("preferred_agents holds at most 16 entries"))
		return
	}

	// Persist field by field; the first failure aborts the rest, and the
	// in-memory config only moves once the file is written.
	if h.configPath != "" {
		if err := config.UpdateSectionField(h.configPath, []string{"injection"}, "model", injection); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := config.UpdateSectionList(h.configPath, []string{"routing"}, "preferred_agents", agents); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, lim := range []struct {
			key   string
			value int
		}{
			{"user", limits.User},
			{"memory", limits.Memory},
			{"project", limits.Project},
		} {
			if err := config.UpdateSectionFieldInt(h.configPath, []string{"memory", "limits"}, lim.key, lim.value); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		if err := config.UpdateSectionField(h.configPath, []string{"approval"}, "mode", approval); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	h.cfg.Injection.Model = injection
	h.cfg.Routing.PreferredAgents = agents
	h.cfg.Memory.Limits.User = limits.User
	h.cfg.Memory.Limits.Memory = limits.Memory
	h.cfg.Memory.Limits.Project = limits.Project
	h.cfg.Approval.Mode = approval

	writeJSON(w, appSettingsJSON{
		InjectionModel:  injection,
		PreferredAgents: agents,
		MemoryLimits:    limits,
		ApprovalMode:    approval,
		Sandbox:         &sandboxJSON{WorkPath: h.cfg.Storage.WorkPath},
	})
}
