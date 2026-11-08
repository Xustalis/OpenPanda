// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// AgentConcurrency counts subprocess calls, not parked parent orchestrators.
// It is shared by router snapshots so hot model changes cannot reset capacity.
type AgentConcurrency struct {
	mu               sync.Mutex
	changed          chan struct{}
	active, children int
	agents           map[string]int
	max, childMax    int
	perAgent         map[string]int
}

func NewAgentConcurrency() *AgentConcurrency {
	return &AgentConcurrency{changed: make(chan struct{}), agents: make(map[string]int)}
}

func (g *AgentConcurrency) Configure(capacity int, r config.RoutingConfig, d config.DelegationConfig) {
	max := r.MaxConcurrent
	if max == 0 {
		max = capacity
	}
	if max < 1 {
		max = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.max, g.childMax, g.perAgent = max, d.MaxConcurrent, r.AgentConcurrency
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *AgentConcurrency) acquire(ctx context.Context, name, adapter string, child bool) (func(), error) {
	g.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return nil, err
		}
		nameLimit, adapterLimit := g.perAgent[name], g.perAgent[adapter]
		if g.active < g.max &&
			(nameLimit == 0 || g.agents["name:"+name] < nameLimit) &&
			(adapterLimit == 0 || g.agents["adapter:"+adapter] < adapterLimit) &&
			(!child || g.childMax == 0 || g.children < g.childMax) {
			g.active++
			g.agents["name:"+name]++
			g.agents["adapter:"+adapter]++
			if child {
				g.children++
			}
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				defer g.mu.Unlock()
				g.active--
				g.agents["name:"+name]--
				g.agents["adapter:"+adapter]--
				if child {
					g.children--
				}
				close(g.changed)
				g.changed = make(chan struct{})
			}, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
		g.mu.Lock()
	}
}
