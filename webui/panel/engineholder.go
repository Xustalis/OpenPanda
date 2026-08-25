package panel

import (
	"context"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
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
}

// NewEngineHolder builds the holder and, when a model endpoint is already
// configured, the initial engine. A zero-config start (model.base_url empty)
// yields a holder with a nil engine — degraded, answers-only mode — and no
// error; the first Reload after a model is configured brings the engine up.
func NewEngineHolder(cfg *config.Config, opts askengine.Options) (*EngineHolder, error) {
	h := &EngineHolder{cfg: cfg, opts: opts}
	if cfg.Model.BaseURL == "" {
		return h, nil
	}
	eng, err := askengine.New(context.Background(), cfg, opts)
	if err != nil {
		return nil, err
	}
	h.engine = eng
	return h, nil
}

// Engine returns the current engine snapshot; nil in degraded (no model
// configured) mode. Safe for concurrent use.
func (h *EngineHolder) Engine() *askengine.Engine {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.engine
}

// Reload rebuilds the engine from the holder's live config — the model
// settings API mutates that config, so the rebuild already sees the new
// provider. An empty model.base_url tears the engine down (back to
// zero-config degraded mode); a failed build returns the error and leaves
// the previous engine serving.
func (h *EngineHolder) Reload() error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()

	if h.cfg.Model.BaseURL == "" {
		h.mu.Lock()
		old := h.engine
		h.engine = nil
		h.mu.Unlock()
		if old != nil {
			old.Close()
		}
		return nil
	}
	// Build before swapping: a failed build must not take the old engine down.
	eng, err := askengine.New(context.Background(), h.cfg, h.opts)
	if err != nil {
		return err
	}
	h.mu.Lock()
	old := h.engine
	h.engine = eng
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
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
	return h.engine
}
