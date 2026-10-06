package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/security"
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
	// ToolsPolicy grades the tool face agent adapters run with: minimal keeps each
	// adapter's safe whitelist, extended reaches the agent's own skills, sub-agent
	// tooling and MCP servers. `panda config routing set tools_policy` has edited
	// it since v0.0.7; it was the one policy the console could not reach.
	ToolsPolicy string       `json:"tools_policy"` // minimal | extended
	Sandbox     *sandboxJSON `json:"sandbox,omitempty"`
}

type memoryLimitsJSON struct {
	User    int `json:"user"`
	Memory  int `json:"memory"`
	Project int `json:"project"`
}

// sandboxJSON reports the confinement actually in effect — read-only by
// design: the mode comes from config.yaml, the backend from whatever the
// platform provides (seatbelt/bwrap/""), and the UI must not pretend the
// sandbox is stronger than it is.
type sandboxJSON struct {
	WorkPath string `json:"work_path"`
	Mode     string `json:"mode"`
	Backend  string `json:"backend"`
	// Active is true only when a non-off mode meets a real platform backend.
	// mode=standard with Backend()=="" is configured-but-unenforced — the UI
	// must render that as "not in effect", not as a weaker kind of on.
	Active bool `json:"active"`
}

// getAppSettings serves GET /api/settings/app — the live values of the four
// policy groups plus the read-only sandbox description.
func (h *handler) getAppSettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var out appSettingsJSON
	h.readCfg(func(c *config.Config) {
		out = appSettingsJSON{
			InjectionModel:  c.Injection.NormalizedModel(),
			PreferredAgents: append([]string{}, c.Routing.PreferredAgents...),
			MemoryLimits: memoryLimitsJSON{
				User:    c.Memory.Limits.User,
				Memory:  c.Memory.Limits.Memory,
				Project: c.Memory.Limits.Project,
			},
			ApprovalMode: c.Approval.NormalizedMode(),
			ToolsPolicy:  c.Routing.NormalizedToolsPolicy(),
			Sandbox: &sandboxJSON{
				WorkPath: c.Storage.WorkPath,
				Mode:     c.Sandbox.NormalizedMode(),
				Backend:  security.Backend(),
				Active:   c.Sandbox.NormalizedMode() != "off" && security.Backend() != "",
			},
		}
	})
	writeJSON(w, out)
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
	// An absent tools_policy keeps the current one rather than failing: a client
	// written before this field existed still saves the rest of the policy.
	tools := strings.TrimSpace(req.ToolsPolicy)
	if tools == "" {
		h.readCfg(func(c *config.Config) { tools = c.Routing.NormalizedToolsPolicy() })
	}
	switch tools {
	case config.ToolsPolicyMinimal, config.ToolsPolicyExtended:
	default:
		writeErr(w, http.StatusBadRequest, errors.New("tools_policy must be minimal or extended"))
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

	// Persist field by field and move the in-memory config in the same
	// critical section: UpdateSection* is a whole-document read-modify-write,
	// so a concurrent save (another endpoint, an embedded REPL's /config)
	// would interleave it into a lost update without the lock covering both.
	var inj config.InjectionConfig
	var routing config.RoutingConfig
	var workPath string
	err := h.mutateCfgErr(func(c *config.Config) error {
		if h.configPath != "" {
			if err := config.UpdateSectionField(h.configPath, []string{"injection"}, "model", injection); err != nil {
				return err
			}
			if err := config.UpdateSectionList(h.configPath, []string{"routing"}, "preferred_agents", agents); err != nil {
				return err
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
					return err
				}
			}
			if err := config.UpdateSectionField(h.configPath, []string{"approval"}, "mode", approval); err != nil {
				return err
			}
			if err := config.UpdateSectionField(h.configPath, []string{"routing"}, "tools_policy", tools); err != nil {
				return err
			}
		}
		c.Injection.Model = injection
		c.Routing.PreferredAgents = agents
		c.Memory.Limits.User = limits.User
		c.Memory.Limits.Memory = limits.Memory
		c.Memory.Limits.Project = limits.Project
		c.Approval.Mode = approval
		c.Routing.ToolsPolicy = tools
		// Snapshot the copies SetRouterPolicy installs: reading c.Routing
		// after mutateCfg returned would race the next mutation.
		inj = c.Injection
		routing = c.Routing
		workPath = c.Storage.WorkPath
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Routing and injection are read by the router, which holds its own copy, so a
	// change here has to re-enter it or it waits for a restart.
	if eng := h.currentEngine(); eng != nil {
		eng.SetRouterPolicy(inj, routing)
	}

	// sandbox.* is not part of this PUT — it is a startup decision (mode and
	// backend), so the response echoes whatever the live config reports.
	var sbMode string
	var sbBackend string
	h.readCfg(func(c *config.Config) {
		sbMode = c.Sandbox.NormalizedMode()
		sbBackend = security.Backend()
	})
	writeJSON(w, appSettingsJSON{
		InjectionModel:  injection,
		PreferredAgents: agents,
		MemoryLimits:    limits,
		ApprovalMode:    approval,
		ToolsPolicy:     tools,
		Sandbox:         &sandboxJSON{WorkPath: workPath, Mode: sbMode, Backend: sbBackend, Active: sbMode != "off" && sbBackend != ""},
	})
}
