// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The multi-model registry endpoints — the web counterpart of the REPL's
// `/model` verb table (list / switch / add / remove / fetch / test). The
// active model lives in config's `model:` section, the registry in
// `models:`; a switch writes the chosen entry into `model:` and hot-swaps
// the engine's client so the next ask uses it immediately. The provider
// catalogue is internal/providers — the same table the TUI's add wizard
// reads, so the two surfaces can never drift on what a vendor needs.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// modelEntryJSON is the wire form of one registered (or active) model. The
// API key is never returned — only whether one is stored and a masked tail.
type modelEntryJSON struct {
	Alias         string `json:"alias"`
	Provider      string `json:"provider,omitempty"`
	ProviderLabel string `json:"provider_label,omitempty"`
	APIType       string `json:"api_type"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	NoAuth        bool   `json:"no_auth,omitempty"`
	Active        bool   `json:"active"`
	KeySet        bool   `json:"key_set"`
	KeyHint       string `json:"key_hint,omitempty"`
}

// providerJSON is the wire form of one catalogue entry — what the "add
// model" picker needs and nothing more.
type providerJSON struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	APIType       string `json:"api_type"`
	BaseURL       string `json:"base_url"`
	DefaultModel  string `json:"default_model"`
	ContextWindow int    `json:"context_window,omitempty"`
	NoAuth        bool   `json:"no_auth"`
	KeySaved      bool   `json:"key_saved"` // a key for this provider exists somewhere in config
	Region        string `json:"region"`    // "cn" | "global"
}

// modelEntryOf renders a ModelConfig for the wire, resolving the catalogue
// fallbacks the REPL's table view resolves (default model, base URL,
// context window, provider label).
func modelEntryOf(mc config.ModelConfig, active config.ModelConfig) modelEntryJSON {
	p, known := providers.Lookup(mc.Provider)
	if !known {
		if d, ok := providers.Detect(mc.BaseURL, mc.Model); ok {
			p, known = d, true
		}
	}
	e := modelEntryJSON{
		Alias:         mc.Alias(),
		Provider:      mc.Provider,
		APIType:       mc.NormalizedAPIType(),
		BaseURL:       mc.BaseURL,
		Model:         mc.Model,
		MaxTokens:     mc.MaxTokens,
		ContextWindow: mc.ContextWindow,
		NoAuth:        mc.NoAuth || (known && p.NoAuth),
		KeySet:        mc.APIKey != "",
		KeyHint:       maskKey(mc.APIKey),
	}
	if known {
		e.ProviderLabel = p.Label
		if e.BaseURL == "" {
			e.BaseURL = p.BaseURL
		}
		if e.Model == "" {
			e.Model = p.DefaultModel
		}
		if e.ContextWindow == 0 {
			e.ContextWindow = p.ContextWindow
		}
	}
	e.Active = mc.Alias() == active.Alias() &&
		mc.Model == active.Model &&
		mc.BaseURL == active.BaseURL
	return e
}

// listModels serves GET /api/models — the active model, the registry, and
// the provider catalogue in one payload so the models page renders without
// a second round-trip.
func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	active := h.engineModel()
	var models []config.ModelConfig
	h.readCfg(func(c *config.Config) {
		models = slices.Clone(c.Models)
	})
	out := make([]modelEntryJSON, 0, len(models)+1)
	out = append(out, modelEntryOf(active, active))
	for _, m := range models {
		if m.Alias() == active.Alias() && m.Model == active.Model && m.BaseURL == active.BaseURL {
			continue // already painted as the active row
		}
		out = append(out, modelEntryOf(m, active))
	}
	provs := make([]providerJSON, 0, len(providers.All()))
	for _, p := range providers.All() {
		provs = append(provs, providerJSON{
			ID:            p.ID,
			Label:         p.Label,
			APIType:       p.APIType,
			BaseURL:       p.BaseURL,
			DefaultModel:  p.DefaultModel,
			ContextWindow: p.ContextWindow,
			NoAuth:        p.NoAuth,
			KeySaved:      h.findProviderKey(p.ID) != "",
			Region:        string(p.GeographicOrigin),
		})
	}
	writeJSON(w, map[string]any{"active": out[0], "models": out[1:], "providers": provs})
}

// addModelRequest is the body of POST /api/models. Provider is a catalogue
// id; Model and Alias are optional (default model / provider id). APIKey is
// required unless the provider is NoAuth or a key is already on file.
type addModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Alias    string `json:"alias,omitempty"`
	// Custom fields — used when provider == "custom": the catalogue carries
	// no endpoint for it, so the caller supplies the wire shape explicitly.
	APIType   string `json:"api_type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// addModel serves POST /api/models — registers a model into `models:`,
// persisting through UpdateModelsSection (comments preserved). When no
// active model is configured at all, the new one becomes active immediately
// — the zero-config path the onboarding wizard relies on.
func (h *handler) addModel(w http.ResponseWriter, r *http.Request) {
	var req addModelRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}

	var mc config.ModelConfig
	if strings.TrimSpace(req.Provider) == "custom" || strings.TrimSpace(req.Provider) == "" && req.BaseURL != "" {
		mc = config.ModelConfig{
			Provider:  "custom",
			APIType:   normalizeAPIType(req.APIType),
			BaseURL:   strings.TrimSpace(req.BaseURL),
			Model:     strings.TrimSpace(req.Model),
			MaxTokens: req.MaxTokens,
			APIKey:    strings.TrimSpace(req.APIKey),
		}
	} else {
		p, ok := providers.Lookup(strings.TrimSpace(req.Provider))
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("unknown provider"))
			return
		}
		key := strings.TrimSpace(req.APIKey)
		if key == "" {
			key = h.findProviderKey(p.ID) // reuse a key already on file for this provider
		}
		if key == "" && !p.NoAuth {
			writeErr(w, http.StatusBadRequest, errors.New("api_key is required for this provider"))
			return
		}
		mc, _ = providers.ModelConfig(p.ID, strings.TrimSpace(req.Model), key)
	}
	alias := strings.TrimSpace(req.Alias)
	if alias == "" {
		alias = mc.Provider
		if alias == "" {
			alias = "custom"
		}
		// Avoid silently overwriting a same-named entry that points at a
		// different model — derive the alias from the model id instead.
		if mc.Model != "" {
			h.readCfg(func(c *config.Config) {
				for _, existing := range c.Models {
					if existing.Alias() == alias && existing.Model != mc.Model {
						alias = mc.Model
						break
					}
				}
			})
		}
	}
	mc.Name = alias

	// Validate client construction before mutating config on disk.
	if _, err := entry.NewClient(mc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var models []config.ModelConfig
	err := h.mutateCfgErr(func(c *config.Config) error {
		replaced := false
		for i := range c.Models {
			if c.Models[i].Alias() == alias {
				c.Models[i] = mc
				replaced = true
				break
			}
		}
		if !replaced {
			c.Models = append(c.Models, mc)
		}
		models = slices.Clone(c.Models)
		// Persist inside the lock: UpdateModelsSection rewrites the whole
		// models block, and an unlocked gap would let a concurrent save
		// read-modify-write over this change.
		if h.configPath != "" {
			return config.UpdateModelsSection(h.configPath, models)
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// No active model at all → make the fresh one active so the first ask works.
	var noActive bool
	h.readCfg(func(c *config.Config) {
		noActive = c.Model.BaseURL == "" && c.Model.Provider == "" && c.Model.Model == ""
	})
	if noActive {
		if err := h.applyModelConfig(mc); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, modelEntryOf(mc, h.engineModel()))
}

// useModel serves POST /api/models/{alias}/use — switch the active model to a
// registered alias (or model id), the web counterpart of `/model <alias>`.
func (h *handler) useModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("alias")
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var active, match config.ModelConfig
	isActive := false
	h.readCfg(func(c *config.Config) {
		active = c.Model
		if c.Model.Alias() == name && (c.Model.Model != "" || c.Model.Provider != "") {
			match = c.Model
			isActive = true
			return
		}
		for _, m := range c.Models {
			if m.Alias() == name || m.Model == name {
				match = m
				return
			}
		}
	})
	if isActive {
		writeJSON(w, modelEntryOf(active, active))
		return
	}
	if match.Model != "" || match.Provider != "" {
		if err := h.applyModelConfig(match); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, modelEntryOf(match, h.engineModel()))
		return
	}
	writeErr(w, http.StatusNotFound, errors.New("no such model"))
}

// removeModel serves DELETE /api/models/{alias} — drops a registry entry.
// Removing the active model is refused: the active row lives in `model:`,
// not `models:`, and clearing it is what the settings form's empty-save is
// for.
func (h *handler) removeModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("alias")
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	found, isActive := false, false
	var models []config.ModelConfig
	err := h.mutateCfgErr(func(c *config.Config) error {
		for i, m := range c.Models {
			if m.Alias() != name && m.Model != name {
				continue
			}
			found = true
			if m.Alias() == c.Model.Alias() {
				isActive = true
				return nil
			}
			c.Models = append(c.Models[:i:i], c.Models[i+1:]...)
			models = slices.Clone(c.Models)
			// Persist inside the lock — the whole models block is rewritten,
			// so a concurrent save between memory and file would lose one.
			if h.configPath != "" {
				return config.UpdateModelsSection(h.configPath, models)
			}
			return nil
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if isActive {
		writeErr(w, http.StatusConflict, errors.New("model is active — switch first"))
		return
	}
	if found {
		writeJSON(w, map[string]any{"removed": name})
		return
	}
	writeErr(w, http.StatusNotFound, errors.New("no such model"))
}

// modelRefRequest resolves a fetch/test target: a registered alias, a
// catalogue provider id (with an inline or saved key), or empty = the
// active model.
type modelRefRequest struct {
	Alias    string `json:"alias,omitempty"`    // registry alias or model id
	Provider string `json:"provider,omitempty"` // catalogue id — used with inline key
	APIKey   string `json:"api_key,omitempty"`
	Model    string `json:"model,omitempty"` // override model id for fetch/test on a provider
	// Custom endpoint fields — the catalogue's "custom" entry has no base
	// URL of its own, so a fetch/test against a relay supplies the wire
	// shape inline rather than resolving it from config.
	APIType string `json:"api_type,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// resolveModelRef maps the request onto a ModelConfig, mirroring the REPL's
// resolveModel: alias → registry, provider → catalogue + key. Returns false
// when nothing usable was named.
func (h *handler) resolveModelRef(req modelRefRequest) (config.ModelConfig, bool) {
	if req.Alias == "" && req.Provider == "" {
		return h.engineModel(), true
	}
	if req.Alias != "" {
		var match config.ModelConfig
		found := false
		h.readCfg(func(c *config.Config) {
			if (c.Model.Alias() == req.Alias || c.Model.Model == req.Alias) &&
				(c.Model.Model != "" || c.Model.Provider != "") {
				match, found = c.Model, true
				return
			}
			for _, m := range c.Models {
				if m.Alias() == req.Alias || m.Model == req.Alias {
					match, found = m, true
					return
				}
			}
		})
		if found {
			return match, true
		}
	}
	pid := firstNonEmpty(strings.TrimSpace(req.Provider), strings.TrimSpace(req.Alias))
	// A custom endpoint carries its wire shape inline — the catalogue entry
	// is only the marker that the caller owns the fields.
	if pid == "custom" && strings.TrimSpace(req.BaseURL) != "" {
		return config.ModelConfig{
			Provider: "custom",
			APIType:  normalizeAPIType(req.APIType),
			BaseURL:  strings.TrimSpace(req.BaseURL),
			Model:    strings.TrimSpace(req.Model),
			APIKey:   strings.TrimSpace(req.APIKey),
		}, true
	}
	p, ok := providers.Lookup(pid)
	if !ok {
		return config.ModelConfig{}, false
	}
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		key = h.findProviderKey(p.ID)
	}
	if key == "" && !p.NoAuth {
		return config.ModelConfig{}, false
	}
	mc, _ := providers.ModelConfig(p.ID, strings.TrimSpace(req.Model), key)
	return mc, true
}

// fetchModels serves POST /api/models/fetch — the provider's remote model
// list, mirroring `/model fetch`.
func (h *handler) fetchModels(w http.ResponseWriter, r *http.Request) {
	var req modelRefRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	mc, ok := h.resolveModelRef(req)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown model/provider, or an api_key is required"))
		return
	}
	client, err := entry.NewClient(mc)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	writeJSON(w, map[string]any{"ok": true, "alias": mc.Alias(), "models": ids})
}

// testModel serves POST /api/models/test — a one-word completion against the
// resolved model, mirroring `/model test`.
func (h *handler) testModel(w http.ResponseWriter, r *http.Request) {
	var req modelRefRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	mc, ok := h.resolveModelRef(req)
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "unknown model/provider, or an api_key is required"})
		return
	}
	client, err := entry.NewClient(mc)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	answer, err := client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "alias": mc.Alias(), "reply": answer})
}

// findProviderKey looks up an API key already configured for providerID —
// the active model first, then the registry. Mirrors the REPL's helper.
func (h *handler) findProviderKey(providerID string) string {
	key := ""
	h.readCfg(func(c *config.Config) {
		if (effectiveProviderID(c.Model) == providerID || c.Model.Alias() == providerID) && c.Model.APIKey != "" {
			key = c.Model.APIKey
			return
		}
		for _, m := range c.Models {
			if (effectiveProviderID(m) == providerID || m.Alias() == providerID) && m.APIKey != "" {
				key = m.APIKey
				return
			}
		}
	})
	return key
}

// effectiveProviderID resolves a config's provider, falling back to
// endpoint/model auto-detection like the REPL's effectiveProvider.
func effectiveProviderID(m config.ModelConfig) string {
	if m.Provider != "" {
		return m.Provider
	}
	if p, ok := providers.Detect(m.BaseURL, m.Model); ok {
		return p.ID
	}
	return ""
}

// applyModelConfig persists mc as the active model and hot-swaps the engine
// client — the panel's single write path a switch/add funnel through, so
// config file, in-memory cfg, and engine client can never drift.
func (h *handler) applyModelConfig(mc config.ModelConfig) error {
	if _, err := entry.NewClient(mc); err != nil {
		return err
	}
	// Same shape as putModelSettings: the persist and the shared-config write
	// run in the same critical section (engineless saves still update what
	// Reload builds from), then SetModel covers the live-engine client swap.
	if err := h.mutateCfgErr(func(c *config.Config) error {
		if h.configPath != "" {
			if err := config.UpdateModelSection(h.configPath, mc); err != nil {
				return err
			}
		}
		c.Model = mc
		return nil
	}); err != nil {
		return err
	}
	if eng := h.currentEngine(); eng != nil {
		if err := eng.SetModel(mc); err != nil {
			return err
		}
	} else if h.engines != nil {
		// Zero-config start, first model saved: hot-load the engine now.
		return h.engines.Reload()
	}
	return nil
}
