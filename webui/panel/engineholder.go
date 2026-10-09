// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// EngineHolder owns the ask engine's lifecycle so the panel can (re)build it
// at runtime. `panda web` boots zero-config — no model endpoint, engine nil,
// answers-only console — and the first model saved through the settings API
// hot-loads a live engine without restarting the process.
//
// Concurrency: Engine hands out the current snapshot under a read lock;
// Reload serializes rebuilds (reloadMu) and swaps the pointer under a write
// lock, so a request that already resolved its engine keeps a stable
// reference across the swap. A failed rebuild leaves the previous engine
// serving.
type EngineHolder struct {
	// reloadMu serializes Reload calls: the build is slow (DB open, MCP
	// spawn, peer dials) and two interleaved reloads would leak an engine.
	reloadMu sync.Mutex

	mu     sync.RWMutex
	engine *askengine.Engine

	cfg  *config.Config
	opts askengine.Options

	// onReview is the "a task is waiting for the user" hook. The holder keeps
	// it so every engine it builds — including one hot-loaded after the first
	// model is saved — announces pending approvals. Losing it on a rebuild
	// would leave a parked task waiting with nobody told.
	onReview func(core.Task)
}

// NewEngineHolder builds the holder and the initial engine. The engine is
// built even without a model endpoint: the task surface (enqueue, queue,
// nodes, cancel) is model-free — `panda task add` proves it on the CLI —
// and only the ask pipeline needs an entry model, which reports ErrNoModel
// lazily per request. A model-less node is not a console-less node; gating
// the whole engine on model.base_url made the web board silently unusable
// on exactly the worker devices multi-device routing targets.
func NewEngineHolder(cfg *config.Config, opts askengine.Options) (*EngineHolder, error) {
	h := &EngineHolder{cfg: cfg, opts: opts}
	eng, err := askengine.New(context.Background(), cfg, opts)
	if err != nil {
		return nil, err
	}
	// The review hook is installed separately by SetOnReview, which the caller
	// invokes after construction (see cmd/panda/web.go).
	h.engine = eng
	return h, nil
}

// Engine returns the current engine snapshot; nil only after Close (or on a
// failed initial build, which the constructor reports instead). Safe for
// concurrent use.
func (h *EngineHolder) Engine() *askengine.Engine {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.engine
}

// SetOnReview installs the pending-approval hook on the current engine and on
// every engine the holder builds later. The callback only announces the
// approval (a push notification, a log line); the decision itself stays with
// the user in the console.
func (h *EngineHolder) SetOnReview(fn func(core.Task)) {
	h.mu.Lock()
	h.onReview = fn
	eng := h.engine
	h.mu.Unlock()
	if eng != nil {
		eng.SetOnReview(fn)
	}
}

// Reload rebuilds the engine from the holder's live config — the model
// settings API mutates that config, so the rebuild already sees the new
// provider. Clearing model.base_url rebuilds to a model-less engine (task
// surface stays live, asks degrade lazily); a failed build returns the
// error and leaves the previous engine serving.
//
// Lock order is reloadMu → cfgMu, matching MutateConfig: while a live
// engine exists its cfgMu serializes config access, so the config reads
// here (the whole askengine.New build, which scans the struct) run under
// that engine's read lock.
func (h *EngineHolder) Reload() error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()

	readCfg := func(fn func(*config.Config)) {
		if eng := h.Engine(); eng != nil {
			eng.ReadConfig(fn)
			return
		}
		if h.cfg != nil {
			fn(h.cfg)
		}
	}

	// Build before swapping: a failed build must not take the old engine down.
	var eng *askengine.Engine
	var err error
	readCfg(func(c *config.Config) {
		eng, err = askengine.New(context.Background(), c, h.opts)
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	old := h.engine
	h.engine = eng
	fn := h.onReview
	h.mu.Unlock()
	if fn != nil && eng != nil {
		eng.SetOnReview(fn)
	}
	if old != nil {
		old.Close()
	}
	return nil
}

// MutateConfig applies fn to the shared config. With a live engine the write
// goes through the engine's config lock so in-flight asks see a consistent
// view; engineless it runs under reloadMu, the same mutex Reload holds while
// reading the config to build an engine — a mutation can never tear a rebuild.
func (h *EngineHolder) MutateConfig(fn func(*config.Config)) {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if eng := h.Engine(); eng != nil {
		eng.MutateConfig(fn)
		return
	}
	if h.cfg != nil {
		fn(h.cfg)
	}
}

// MutateConfigErr is MutateConfig for fallible mutations (a config-file
// persist paired with the in-memory write). The file I/O runs inside the
// lock so concurrent settings saves cannot interleave config.yaml
// read-modify-writes into lost updates.
func (h *EngineHolder) MutateConfigErr(fn func(*config.Config) error) error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if eng := h.Engine(); eng != nil {
		return eng.MutateConfigErr(fn)
	}
	if h.cfg == nil {
		return nil
	}
	return fn(h.cfg)
}

// ReadConfig runs fn on the shared config under the same serialization as
// MutateConfig — engine read lock when live, reloadMu otherwise.
func (h *EngineHolder) ReadConfig(fn func(*config.Config)) {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if eng := h.Engine(); eng != nil {
		eng.ReadConfig(fn)
		return
	}
	if h.cfg != nil {
		fn(h.cfg)
	}
}

// Close releases the current engine's resources (DB handle, scheduler core,
// MCP server). The holder must not be used afterwards.
func (h *EngineHolder) Close() {
	h.mu.Lock()
	old := h.engine
	h.engine = nil
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// currentEngine resolves the live ask engine for one request — through the
// holder when one is wired (hot-reloadable), else the static Deps.Engine.
// Nil means degraded mode: /api/ask and friends answer 503 while everything
// else keeps serving.
func (h *handler) currentEngine() *askengine.Engine {
	if h.engines != nil {
		return h.engines.Engine()
	}
	if h.engineFn != nil {
		return h.engineFn()
	}
	return h.engine
}

// mutateCfg applies fn to the shared config under whichever lock owns it:
// the live engine's write lock when one exists (so its ask/tool goroutines
// see a consistent view), else the holder's reloadMu (keeping Reload's reads
// serialized), else this handler's cfgMu. h.cfg aliases the engine config —
// Deps.Cfg is the same pointer engines are built from — so a direct write
// here would race every reader.
func (h *handler) mutateCfg(fn func(*config.Config)) {
	if h.engines != nil {
		h.engines.MutateConfig(fn)
		return
	}
	if eng := h.currentEngine(); eng != nil {
		eng.MutateConfig(fn)
		return
	}
	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()
	if h.cfg != nil {
		fn(h.cfg)
	}
}

// mutateCfgErr is mutateCfg for fallible mutations. Config-file persists
// belong inside fn: holding the lock across file+memory writes serializes
// them with every other surface touching the shared cfg (concurrent HTTP
// requests, an embedded REPL's mutateConfig).
func (h *handler) mutateCfgErr(fn func(*config.Config) error) error {
	if h.engines != nil {
		return h.engines.MutateConfigErr(fn)
	}
	if eng := h.currentEngine(); eng != nil {
		return eng.MutateConfigErr(fn)
	}
	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()
	if h.cfg == nil {
		return nil
	}
	return fn(h.cfg)
}

// readCfg is the read half of mutateCfg: same routing, read locks.
func (h *handler) readCfg(fn func(*config.Config)) {
	if h.engines != nil {
		h.engines.ReadConfig(fn)
		return
	}
	if eng := h.currentEngine(); eng != nil {
		eng.ReadConfig(fn)
		return
	}
	h.cfgMu.RLock()
	defer h.cfgMu.RUnlock()
	if h.cfg != nil {
		fn(h.cfg)
	}
}
