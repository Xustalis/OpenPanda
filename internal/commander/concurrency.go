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
	leases           map[string]*agentPermit
	max, childMax    int
	perAgent         map[string]int
}

type agentPermit struct {
	name, adapter         string
	child, active, closed bool
}

func NewAgentConcurrency() *AgentConcurrency {
	return &AgentConcurrency{changed: make(chan struct{}), agents: make(map[string]int), leases: make(map[string]*agentPermit)}
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
			p := &agentPermit{name: name, adapter: adapter, child: child, active: true}
			taskID := TaskID(ctx)
			if taskID != "" {
				g.leases[taskID] = p
			}
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				defer g.mu.Unlock()
				if p.closed {
					return
				}
				p.closed = true
				if p.active {
					g.adjustPermit(p, -1)
				}
				if g.leases[taskID] == p {
					delete(g.leases, taskID)
				}
				g.signalChanged()
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

func (g *AgentConcurrency) signalChanged() { close(g.changed); g.changed = make(chan struct{}) }
func (g *AgentConcurrency) adjustPermit(p *agentPermit, delta int) {
	g.active += delta
	g.agents["name:"+p.name] += delta
	g.agents["adapter:"+p.adapter] += delta
	if p.child {
		g.children += delta
	}
	p.active = delta > 0
}

// YieldTask frees only the identified live adapter's permit while its MCP call
// awaits children. The resume closure observes cancellation and a concurrently
// exited adapter; it cannot release a newer run's permit or double-count one.
func (g *AgentConcurrency) YieldTask(ctx context.Context, taskID string) func() {
	if g == nil {
		return func() {}
	}
	g.mu.Lock()
	p := g.leases[taskID]
	if p == nil || !p.active || p.closed {
		g.mu.Unlock()
		return func() {}
	}
	g.adjustPermit(p, -1)
	g.signalChanged()
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			for !p.closed && ctx.Err() == nil {
				if g.active < g.max && (g.perAgent[p.name] == 0 || g.agents["name:"+p.name] < g.perAgent[p.name]) && (g.perAgent[p.adapter] == 0 || g.agents["adapter:"+p.adapter] < g.perAgent[p.adapter]) && (!p.child || g.childMax == 0 || g.children < g.childMax) {
					g.adjustPermit(p, 1)
					return
				}
				changed := g.changed
				g.mu.Unlock()
				select {
				case <-ctx.Done():
				case <-changed:
				}
				g.mu.Lock()
			}
		})
	}
}
