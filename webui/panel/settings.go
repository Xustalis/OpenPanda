package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// modelSettingsJSON is the wire form of the model configuration. The API key
// is write-only: GET reports only whether one is set, PUT treats an empty key
// as "keep the existing one".
type modelSettingsJSON struct {
	APIType    string `json:"api_type"` // "anthropic" | "openai"
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	MaxTokens  int    `json:"max_tokens"`
	APIKey     string `json:"api_key,omitempty"` // write-only on PUT/POST; never returned
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint,omitempty"` // masked tail, e.g. "…f3ab"
}

// getModelSettings serves GET /api/settings/model — the current model
// configuration minus the secret itself.
func (h *handler) getModelSettings(w http.ResponseWriter, r *http.Request) {
	mc := h.engineModel()
	writeJSON(w, modelSettingsJSON{
		APIType:    mc.NormalizedAPIType(),
		BaseURL:    mc.BaseURL,
		Model:      mc.Model,
		MaxTokens:  mc.MaxTokens,
		APIKeySet:  mc.APIKey != "",
		APIKeyHint: maskKey(mc.APIKey),
	})
}

// putModelSettings serves PUT /api/settings/model — validate, persist to the
// config file (comments preserved), then apply live: an existing engine
// hot-swaps its model client (SetModel); a zero-config panel (engine nil,
// holder wired) builds the engine right here, so the first saved model
// enables /api/ask without a restart. On a static answers-only panel (no
// engine, no holder) the settings still persist for the next start.
func (h *handler) putModelSettings(w http.ResponseWriter, r *http.Request) {
	var req modelSettingsJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}

	cur := h.engineModel()
	mc := config.ModelConfig{
		APIType:   normalizeAPIType(req.APIType),
		BaseURL:   firstNonEmpty(strings.TrimSpace(req.BaseURL), cur.BaseURL),
		APIKey:    cur.APIKey, // empty request key keeps the stored secret
		Model:     firstNonEmpty(strings.TrimSpace(req.Model), cur.Model),
		MaxTokens: firstPositive(req.MaxTokens, cur.MaxTokens),
	}
	if key := strings.TrimSpace(req.APIKey); key != "" {
		mc.APIKey = key
	}
	if mc.BaseURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("base_url must not be empty"))
		return
	}

	// Build a client first: invalid base URLs fail here without touching the
	// stored config.
	if _, err := entry.NewClient(mc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if h.configPath != "" {
		if err := config.UpdateModelSection(h.configPath, mc); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Keep the live config in sync: GET falls back to it without an engine,
	// and a holder reload builds the next engine from it.
	if h.cfg != nil {
		h.cfg.Model = mc
	}
	if eng := h.currentEngine(); eng != nil {
		if err := eng.SetModel(mc); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if cfg := eng.Config(); cfg != nil {
			cfg.Model = mc
		}
	} else if h.engines != nil {
		// Zero-config start, first model saved: hot-load the engine now.
		if err := h.engines.Reload(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, modelSettingsJSON{
		APIType:    mc.NormalizedAPIType(),
		BaseURL:    mc.BaseURL,
		Model:      mc.Model,
		MaxTokens:  mc.MaxTokens,
		APIKeySet:  mc.APIKey != "",
		APIKeyHint: maskKey(mc.APIKey),
	})
}

// testModelSettings serves POST /api/settings/model/test — runs a one-word
// completion against the given (or current) provider settings and reports
// whether the endpoint answered, so the settings page — and the onboarding
// form on a zero-config panel — can verify before saving. It needs no live
// engine: the candidate settings alone are enough to dial the provider.
func (h *handler) testModelSettings(w http.ResponseWriter, r *http.Request) {
	var req modelSettingsJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	cur := h.engineModel()
	mc := config.ModelConfig{
		APIType:   normalizeAPIType(req.APIType),
		BaseURL:   firstNonEmpty(strings.TrimSpace(req.BaseURL), cur.BaseURL),
		APIKey:    cur.APIKey,
		Model:     firstNonEmpty(strings.TrimSpace(req.Model), cur.Model),
		MaxTokens: firstPositive(req.MaxTokens, cur.MaxTokens),
	}
	if key := strings.TrimSpace(req.APIKey); key != "" {
		mc.APIKey = key
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
	writeJSON(w, map[string]any{"ok": true, "reply": answer})
}

// mcpSettingsJSON is the wire form of the MCP server configuration.
type mcpSettingsJSON struct {
	Command string `json:"command"`     // space-separated stdio MCP server argv; "" = disabled
	FromCfg bool   `json:"from_config"` // true when the value came from config.yaml rather than the engine
}

// getMCPSettings serves GET /api/settings/mcp — the current MCP server
// command (from the engine's live state, falling back to config.yaml).
func (h *handler) getMCPSettings(w http.ResponseWriter, r *http.Request) {
	cmd := ""
	eng := h.currentEngine()
	if eng != nil {
		cmd = eng.MCPCommand()
	} else if h.cfg != nil {
		cmd = h.cfg.MCP.Command
	}
	writeJSON(w, mcpSettingsJSON{Command: cmd, FromCfg: eng == nil})
}

// putMCPSettings serves PUT /api/settings/mcp — validate the command by
// actually spawning the server and listing its tools, persist to config.yaml,
// then hot-swap the engine's MCP client. An empty command disables MCP.
// Needs a live engine (MCP tools live inside it); configure the model first
// — the settings save hot-loads one.
func (h *handler) putMCPSettings(w http.ResponseWriter, r *http.Request) {
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured (save a model first)"))
		return
	}
	var req mcpSettingsJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	cmd := strings.TrimSpace(req.Command)

	// SetMCPCommand spawns and lists tools first; a bad command fails here
	// without touching the stored config or the running registry.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := eng.SetMCPCommand(ctx, cmd); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if h.configPath != "" {
		if err := config.UpdateMCPSection(h.configPath, cmd); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if h.cfg != nil {
		h.cfg.MCP.Command = cmd
	}
	writeJSON(w, mcpSettingsJSON{Command: cmd})
}

// engineModel returns the engine's current model config; a nil engine (answers
// panel without a provider) falls back to the loaded config.
func (h *handler) engineModel() config.ModelConfig {
	if eng := h.currentEngine(); eng != nil {
		return eng.ModelConfig()
	}
	if h.cfg != nil {
		return h.cfg.Model
	}
	return config.Default().Model
}

func normalizeAPIType(v string) string {
	if v == config.APITypeOpenAI {
		return config.APITypeOpenAI
	}
	return config.APITypeAnthropic
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func firstPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// maskKey renders a display hint like "…a1b2" for a configured secret.
func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "••••"
	}
	return "…" + key[len(key)-4:]
}
