// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"strings"
)

// RuntimeConfig is the execution configuration refreshed across local processes.
// Other config sections retain their own startup/reload contracts.
type RuntimeConfig struct {
	Model      ModelConfig
	Injection  InjectionConfig
	Routing    RoutingConfig
	Delegation DelegationConfig
}

func (c *Config) RuntimeConfig() RuntimeConfig {
	return RuntimeConfig{c.Model, c.Injection, c.Routing, c.Delegation}
}

// ValidateAgentExecution is used at the file/HTTP boundary, never inside a run.
func ValidateAgentExecution(r RoutingConfig, d DelegationConfig) error {
	for name, n := range map[string]int{
		"routing.max_concurrent":    r.MaxConcurrent,
		"delegation.max_concurrent": d.MaxConcurrent,
		"delegation.max_children":   d.MaxChildren,
		"delegation.fanout":         d.FanOut,
		"delegation.max_depth":      d.MaxDepth,
	} {
		if n < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	for agent, n := range r.AgentConcurrency {
		if strings.TrimSpace(agent) == "" || n < 1 {
			return fmt.Errorf("agent_concurrency requires non-empty names and positive limits")
		}
	}
	for _, models := range []map[string][]string{r.AgentModels, r.SubagentModels} {
		for agent, prefs := range models {
			if strings.TrimSpace(agent) == "" {
				return fmt.Errorf("model selection requires a non-empty agent name")
			}
			for _, model := range prefs {
				if strings.TrimSpace(model) == "" || strings.ContainsAny(model, "\x00\r\n") {
					return fmt.Errorf("model for %s must be non-empty and single-line", agent)
				}
			}
		}
	}
	return nil
}
