// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Xustalis/OpenPanda/internal/config"
)

type executionSettingsJSON struct {
	AgentModels      map[string][]string     `json:"agent_models"`
	SubagentModels   map[string][]string     `json:"subagent_models"`
	MaxConcurrent    int                     `json:"max_concurrent"`
	AgentConcurrency map[string]int          `json:"agent_concurrency"`
	Delegation       config.DelegationConfig `json:"delegation"`
}

func (h *handler) getExecutionSettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var out executionSettingsJSON
	h.readCfg(func(c *config.Config) {
		out = executionSettingsJSON{c.Routing.AgentModels, c.Routing.SubagentModels, c.Routing.MaxConcurrent, c.Routing.AgentConcurrency, c.Delegation.NormalizedDelegation()}
	})
	writeJSON(w, out)
}

func (h *handler) putExecutionSettings(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var req executionSettingsJSON
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid execution settings JSON"))
		return
	}
	routing := config.RoutingConfig{AgentModels: req.AgentModels, SubagentModels: req.SubagentModels, MaxConcurrent: req.MaxConcurrent, AgentConcurrency: req.AgentConcurrency}
	if err := config.ValidateAgentExecution(routing, req.Delegation); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Defaults are resolved once at this boundary, not in every child worker.
	req.Delegation = req.Delegation.NormalizedDelegation()
	err := h.mutateCfgErr(func(c *config.Config) error {
		if h.configPath != "" {
			if err := config.UpdateAgentExecution(h.configPath, routing, req.Delegation); err != nil {
				return err
			}
		}
		c.Routing.AgentModels, c.Routing.SubagentModels = routing.AgentModels, routing.SubagentModels
		c.Routing.MaxConcurrent, c.Routing.AgentConcurrency = routing.MaxConcurrent, routing.AgentConcurrency
		c.Delegation = req.Delegation
		routing = c.Routing // preserve tool policy, roster preferences and credential policy
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if eng := h.currentEngine(); eng != nil {
		eng.SetAgentExecutionPolicy(routing, req.Delegation)
	}
	writeJSON(w, req)
}
