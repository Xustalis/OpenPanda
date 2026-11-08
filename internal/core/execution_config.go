// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "github.com/Xustalis/OpenPanda/internal/config"

func (c *Core) RuntimeConfig() config.RuntimeConfig {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return config.RuntimeConfig{Model: c.model, Injection: c.routerInjection, Routing: c.routerRouting, Delegation: c.delegLimits}
}

// ApplyRuntimeConfig publishes one coherent execution revision. Validation of
// external configuration belongs to config.Load / the settings API boundary.
func (c *Core) ApplyRuntimeConfig(cfg config.RuntimeConfig) error {
	supervisor, err := c.supervisorForModel(cfg.Model)
	if err != nil {
		return err
	}
	c.cardMu.Lock()
	c.model, c.supervisor = cfg.Model, supervisor
	c.routerInjection, c.routerRouting = cfg.Injection, cfg.Routing
	c.delegLimits = cfg.Delegation.NormalizedDelegation()
	c.rebuildRouterLocked()
	c.cardMu.Unlock()
	c.updateQueueLimit()
	return nil
}

func (c *Core) executionConcurrency() int {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	if c.routerRouting.MaxConcurrent > 0 {
		return c.routerRouting.MaxConcurrent
	}
	return c.card.Capacity.MaxConcurrent
}

func (c *Core) updateQueueLimit() {
	max := c.executionConcurrency()
	c.mu.RLock()
	sched := c.queueSched
	c.mu.RUnlock()
	if sched != nil {
		sched.SetMaxConcurrent(max)
	}
}
