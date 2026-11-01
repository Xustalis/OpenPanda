// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Lifecycle hooks: the operator's own commands, bound to the task state
// machine's edges.
//
// The wiring lives here rather than in internal/hooks because only this package
// knows what a recorded event type means — "result" is a done or a failure
// depending on the row's state, and the mapping from `EvSubmit` to a
// user-facing edge name is a property of this state machine, not of the runner.
//
// The observer is registered through TaskStore.AddOnEvent, whose contract is
// strict: it fires inside the writer's transaction and must not touch the store.
// So the dispatch below does two cheap things there — a type switch and a
// goroutine — and the store read happens off the writer's path.

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/hooks"
)

// hookOwners remembers which databases already have a hook observer. Two cores
// in one process routinely share a database handle — the daemon's and the ask
// engine's — and each would otherwise register its own listener on the shared
// event hub and fire every hook twice. The first runner wins; the second core
// borrows it.
var hookOwners = struct {
	sync.Mutex
	m map[*sql.DB]*hooks.Runner
}{m: make(map[*sql.DB]*hooks.Runner)}

// SetHooks installs the configured lifecycle hooks. A node with no hooks (the
// default) never registers the observer at all, so the common path pays nothing.
func (c *Core) SetHooks(cfg config.HooksConfig) {
	if !cfg.Enabled || len(cfg.On) == 0 {
		return
	}
	bound := make([]hooks.Hook, 0, len(cfg.On))
	for _, e := range cfg.On {
		if !hooks.ValidEvent(e.Event) {
			c.logger.Warn("core: hook names an unknown event, ignored", "event", e.Event)
			continue
		}
		if e.Command == "" {
			c.logger.Warn("core: hook has no command, ignored", "event", e.Event)
			continue
		}
		bound = append(bound, hooks.Hook{
			Event:   e.Event,
			Command: e.Command,
			Timeout: hooks.ParseTimeout(e.TimeoutS),
		})
	}
	if len(bound) == 0 {
		return
	}
	hookOwners.Lock()
	if existing := hookOwners.m[c.db]; existing != nil {
		hookOwners.Unlock()
		c.hookRunner = existing
		return
	}
	runner := hooks.New(hooks.Config{
		Enabled:    true,
		Timeout:    hooks.ParseTimeout(cfg.TimeoutS),
		AllowTier2: cfg.AllowTier2,
		On:         bound,
	}, c.logger, c.auditLog, c.nodeID)
	if runner == nil {
		hookOwners.Unlock()
		return
	}
	hookOwners.m[c.db] = runner
	hookOwners.Unlock()
	c.hookRunner = runner
	// The observer outlives this call by design (stores are long-lived per
	// daemon run); the unsubscribe is kept for tests that build many cores on
	// one database handle.
	c.hookUnsub = c.store.AddOnEvent(func(taskID, typ string, _ any) {
		c.dispatchHook(taskID, typ)
	})
}

// detachHooks removes this core's observer and forgets the runner, so a test
// (or an embedder recycling a database handle) starts clean.
func (c *Core) detachHooks() {
	if c.hookUnsub != nil {
		c.hookUnsub()
		c.hookUnsub = nil
	}
	hookOwners.Lock()
	delete(hookOwners.m, c.db)
	hookOwners.Unlock()
	c.hookRunner = nil
}

// HookBindings lists what this node has bound, for `panda hooks list`.
func (c *Core) HookBindings() []hooks.Hook {
	if c.hookRunner == nil {
		return nil
	}
	return c.hookRunner.Bindings()
}

// dispatchHook maps one recorded event onto a lifecycle edge and fires it. It
// runs inside the writer's transaction, so everything expensive — the store
// read, the template expansion, the process spawn — happens on the goroutine it
// hands off to.
func (c *Core) dispatchHook(taskID, typ string) {
	if c.hookRunner == nil {
		return
	}
	switch typ {
	case EvSubmit, EvAccept, EvDelegate, EvReview, EvResult:
	default:
		return // queue, progress, retry and the rest are not lifecycle edges
	}
	guard.Go(c.logger, "core: hook dispatch", nil, func() {
		// The row is read fresh: the event carries no state, and a result's
		// meaning (done or failed) is exactly the state.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		t, err := c.store.Get(ctx, taskID)
		if err != nil {
			return
		}
		var name string
		switch typ {
		case EvSubmit:
			name = hooks.EventSubmitted
		case EvAccept, EvDelegate:
			name = hooks.EventStarted
		case EvReview:
			name = hooks.EventReview
		case EvResult:
			switch t.State {
			case StateDone:
				name = hooks.EventDone
			case StateFailed:
				name = hooks.EventFailed
			default:
				return
			}
		}
		c.hookRunner.Fire(hooks.Event{
			Name:    name,
			TaskID:  t.TaskID,
			Title:   t.Title,
			State:   t.State,
			Project: t.Project,
			Owner:   t.OwnerNode,
		})
	})
}
