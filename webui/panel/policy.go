package panel

// The policy settings the console could not reach: approval, routing, memory
// limits and model injection.
//
// `panda config` has edited all four from the CLI since v0.0.5; the console had
// only the model endpoint and MCP. That mattered most for approval — the gate
// that decides whether a task runs unattended or waits for a human is the single
// setting a user is most likely to want to change after watching the queue for an
// afternoon, and it was the one setting they had to leave the console to change.
//
// Every write goes through config.UpdateSectionField, which rewrites one key and
// preserves the file's comments, and then applies the change to the live engine
// where the engine reads it at request time. Persist-then-apply, in that order: a
// setting that took effect but was not saved would silently revert on restart.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// policySettingsJSON is the whole policy surface in one document. The console
// edits these together — they are all "how should this node behave" — and one
// round trip keeps the view from showing four independently stale sections.
type policySettingsJSON struct {
	// Approval gates irreversible work: always | on-request | never.
	ApprovalMode string `json:"approval_mode"`
	// Routing.
	PreferredAgents []string `json:"preferred_agents"`
	ToolsPolicy     string   `json:"tools_policy"`
	// Memory caps, in characters.
	LimitUser    int `json:"limit_user"`
	LimitMemory  int `json:"limit_memory"`
	LimitProject int `json:"limit_project"`
	// Injection strategy for the agent's model endpoint: auto | always | never.
	InjectionModel string `json:"injection_model"`
}

// getPolicySettings serves GET /api/settings/policy.
func (h *handler) getPolicySettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	writeJSON(w, policySettingsJSON{
		ApprovalMode:    h.cfg.Approval.NormalizedMode(),
		PreferredAgents: h.cfg.Routing.PreferredAgents,
		ToolsPolicy:     h.cfg.Routing.NormalizedToolsPolicy(),
		LimitUser:       h.cfg.Memory.Limits.User,
		LimitMemory:     h.cfg.Memory.Limits.Memory,
		LimitProject:    h.cfg.Memory.Limits.Project,
		InjectionModel:  h.cfg.Injection.NormalizedModel(),
	})
}

// putPolicySettings serves PUT /api/settings/policy. Absent fields are left
// alone, so the console can save one section without sending the others.
func (h *handler) putPolicySettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var req struct {
		ApprovalMode    *string   `json:"approval_mode,omitempty"`
		PreferredAgents *[]string `json:"preferred_agents,omitempty"`
		ToolsPolicy     *string   `json:"tools_policy,omitempty"`
		LimitUser       *int      `json:"limit_user,omitempty"`
		LimitMemory     *int      `json:"limit_memory,omitempty"`
		LimitProject    *int      `json:"limit_project,omitempty"`
		InjectionModel  *string   `json:"injection_model,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}

	// Validate everything before writing anything: a half-applied policy is worse
	// than a rejected one, because the user cannot tell which half took.
	if req.ApprovalMode != nil {
		switch *req.ApprovalMode {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("approval_mode must be always, on-request or never"))
			return
		}
	}
	if req.ToolsPolicy != nil {
		switch *req.ToolsPolicy {
		case config.ToolsPolicyMinimal, config.ToolsPolicyExtended:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("tools_policy must be minimal or extended"))
			return
		}
	}
	if req.InjectionModel != nil {
		switch *req.InjectionModel {
		case config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("injection_model must be auto, always or never"))
			return
		}
	}
	for _, lim := range []*int{req.LimitUser, req.LimitMemory, req.LimitProject} {
		if lim != nil && *lim < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("memory limits must not be negative"))
			return
		}
	}

	// Persist first. A setting that took effect but was not written would revert
	// on the next start, which is the confusing failure of the two.
	if h.configPath != "" {
		if err := h.persistPolicy(req.ApprovalMode, req.ToolsPolicy, req.InjectionModel,
			req.PreferredAgents, req.LimitUser, req.LimitMemory, req.LimitProject); err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("save config failed"))
			return
		}
	}

	// Then apply to the in-memory config the engine and core read from.
	if req.ApprovalMode != nil {
		h.cfg.Approval.Mode = *req.ApprovalMode
	}
	if req.ToolsPolicy != nil {
		h.cfg.Routing.ToolsPolicy = *req.ToolsPolicy
	}
	if req.InjectionModel != nil {
		h.cfg.Injection.Model = *req.InjectionModel
	}
	if req.PreferredAgents != nil {
		h.cfg.Routing.PreferredAgents = *req.PreferredAgents
	}
	if req.LimitUser != nil {
		h.cfg.Memory.Limits.User = *req.LimitUser
	}
	if req.LimitMemory != nil {
		h.cfg.Memory.Limits.Memory = *req.LimitMemory
	}
	if req.LimitProject != nil {
		h.cfg.Memory.Limits.Project = *req.LimitProject
	}
	// Routing and injection are read by the router, which holds its own copy.
	if eng := h.currentEngine(); eng != nil {
		eng.SetRouterPolicy(h.cfg.Injection, h.cfg.Routing)
	}
	h.getPolicySettings(w, r)
}

// persistPolicy writes the provided fields into config.yaml, one key at a time so
// the file's comments and unrelated keys survive.
func (h *handler) persistPolicy(approval, tools, injection *string, agents *[]string,
	limUser, limMemory, limProject *int) error {
	if approval != nil {
		if err := config.UpdateSectionField(h.configPath, []string{"approval"}, "mode", *approval); err != nil {
			return err
		}
	}
	if tools != nil {
		if err := config.UpdateSectionField(h.configPath, []string{"routing"}, "tools_policy", *tools); err != nil {
			return err
		}
	}
	if injection != nil {
		if err := config.UpdateSectionField(h.configPath, []string{"injection"}, "model", *injection); err != nil {
			return err
		}
	}
	if agents != nil {
		// A list is written as its comma-joined form, which is how the CLI's
		// `panda config routing set preferred_agents` writes it too.
		if err := config.UpdateSectionField(h.configPath, []string{"routing"},
			"preferred_agents", strings.Join(*agents, ",")); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		key string
		val *int
	}{
		{"user", limUser}, {"memory", limMemory}, {"project", limProject},
	} {
		if f.val == nil {
			continue
		}
		if err := config.UpdateSectionFieldInt(h.configPath,
			[]string{"memory", "limits"}, f.key, *f.val); err != nil {
			return err
		}
	}
	return nil
}
