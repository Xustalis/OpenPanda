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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// modelEntryJSON is the wire form of one registered (or active) model. The
// API key is never returned — only whether one is stored and a masked tail.
type modelEntryJSON struct {
	Alias          string `json:"alias"`
	Provider       string `json:"provider,omitempty"`
	ProviderLabel  string `json:"provider_label,omitempty"`
	APIType        string `json:"api_type"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
	ContextWindow  int    `json:"context_window,omitempty"`
	Thinking       string `json:"thinking,omitempty"` // "on" | "off"; unset = provider default
	ThinkingBudget int    `json:"thinking_budget,omitempty"`
	MaxRetries     int    `json:"max_retries,omitempty"`
	NoAuth         bool   `json:"no_auth,omitempty"`
	Active         bool   `json:"active"`
	KeySet         bool   `json:"key_set"`
	KeyHint        string `json:"key_hint,omitempty"`
	// KeyFingerprint is a short digest of the stored key. key_hint is the
	// last four characters, which two accounts of one vendor can share; the
	// console groups models into connections by this instead, so "extend"
	// cannot borrow the other account's key. Not the secret, and not
	// reversible.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	// AllowPlaintext is the stored waiver for a plain-http endpoint. It is
	// reported so the edit form can show the box already ticked instead of
	// asking again (or, worse, revoking it by submitting an unticked box).
	AllowPlaintext bool `json:"allow_plaintext,omitempty"`
	// LimitsAuto says the stored value is unset and the numbers below are the
	// catalogue's — the console shows them as placeholders ("auto · 1.0M") so a
	// user can tell what they chose from what the resolver picked.
	ContextAuto       bool `json:"context_auto,omitempty"`
	MaxTokensAuto     bool `json:"max_tokens_auto,omitempty"`
	AutoContextWindow int  `json:"auto_context_window,omitempty"`
	AutoMaxTokens     int  `json:"auto_max_tokens,omitempty"`
	// WarningCode flags a configuration that will dial a roster its provider
	// does not serve (WarningArg names the catalogue row that does own the
	// endpoint). The console turns the pair into a sentence; the raw code keeps
	// the wire stable across locales.
	WarningCode string `json:"warning_code,omitempty"`
	WarningArg  string `json:"warning_arg,omitempty"`
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
		Alias:          mc.Alias(),
		Provider:       mc.Provider,
		APIType:        mc.NormalizedAPIType(),
		BaseURL:        mc.BaseURL,
		Model:          mc.Model,
		MaxTokens:      mc.MaxTokens,
		ContextWindow:  mc.ContextWindow,
		Thinking:       mc.Thinking,
		ThinkingBudget: mc.ThinkingBudget,
		MaxRetries:     mc.MaxRetries,
		NoAuth:         mc.NoAuth || (known && p.NoAuth),
		KeySet:         mc.APIKey != "",
		KeyHint:        maskKey(mc.APIKey),
		KeyFingerprint: keyFingerprint(mc.APIKey),
		AllowPlaintext: mc.AllowPlaintext,
		ContextAuto:    mc.ContextWindow == 0,
		MaxTokensAuto:  mc.MaxTokens == 0,
		WarningCode:    "",
	}
	if code, owner := endpointWarning(mc.Provider, mc.BaseURL); code != "" {
		e.WarningCode, e.WarningArg = code, owner
	}
	if known {
		e.ProviderLabel = p.Label
		// A base that merely restated a catalogue endpoint (including one the
		// catalogue has since retired) is shown as the endpoint and dialect
		// the client will actually dial; a relay the user typed stays as typed.
		if e.BaseURL == "" || p.OwnsEndpoint(e.BaseURL) {
			if base, typ := p.EndpointFor(e.Model); base != "" {
				e.BaseURL = base
				if typ != "" && (p.OwnsLegacyEndpoint(mc.BaseURL) ||
					mc.APIType == "" || e.APIType == p.APIType) {
					e.APIType = typ
				}
			}
		}
		if e.Model == "" {
			e.Model = p.DefaultModel
		}
		// The numbers the resolver would pick — painted as placeholders so an
		// untouched field reads "auto · 1.0M" instead of a bare 0.
		e.AutoContextWindow = p.ContextFor(e.Model)
		e.AutoMaxTokens = p.AutoMaxTokens(e.Model)
		// "Auto" is the catalogue's own answer: either the config left the
		// field at 0, or it stored exactly what the resolver would have. Both
		// mean the user expressed no opinion, which is what the console shows.
		e.ContextAuto = mc.ContextWindow == 0 || mc.ContextWindow == e.AutoContextWindow
		e.MaxTokensAuto = mc.MaxTokens == 0 || mc.MaxTokens == e.AutoMaxTokens
		if e.ContextWindow == 0 {
			e.ContextWindow = e.AutoContextWindow
		}
		if e.MaxTokens == 0 {
			e.MaxTokens = e.AutoMaxTokens
		} else if _, ceil := p.TokenPolicy(e.Model); ceil > 0 && e.MaxTokens > ceil {
			e.MaxTokens = ceil
		}
	}
	e.Active = mc.Alias() == active.Alias() &&
		mc.Model == active.Model &&
		mc.BaseURL == active.BaseURL
	return e
}

// endpointWarning reports the catalogue row that owns base when the entry
// names a different one: "" provider (a hand-written config) and "custom"
// (the relay escape hatch) are deliberately exempt, because pointing a custom
// entry at a vendor URL is a normal setup, not a mistake.
//
// The case it exists for is OpenCode: Zen and Go are sibling rosters on
// sibling endpoints of one host, so a base pasted from the other plan's docs
// dials a catalogue the key does not cover, and nothing else in the config
// looks wrong.
func endpointWarning(providerID, base string) (code, owner string) {
	id := strings.TrimSpace(providerID)
	b := strings.TrimSpace(base)
	if id == "" || id == "custom" || b == "" {
		return "", ""
	}
	for _, q := range providers.All() {
		if q.ID == "custom" || q.ID == id {
			continue
		}
		if q.OwnsEndpoint(b) {
			return "endpoint-owner", q.ID
		}
	}
	return "", ""
}

// listModels serves GET /api/models — the active model, the registry, and
// the provider catalogue in one payload so the models page renders without
// a second round-trip. The live row is listed in both places: the console
// paints it once (as the active card) and needs the registry row too, or it
// cannot tell a live model it may delete from one configured outside the
// registry.
func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	active := h.engineModel()
	var models []config.ModelConfig
	h.readCfg(func(c *config.Config) {
		models = slices.Clone(c.Models)
	})
	out := make([]modelEntryJSON, 0, len(models))
	for _, m := range models {
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
	writeJSON(w, map[string]any{
		"active":    modelEntryOf(active, active),
		"models":    out,
		"providers": provs,
	})
}

// addModelRequest is the body of POST /api/models and PUT /api/models/{alias}.
// Provider is a catalogue id; Model and Alias are optional (default model /
// model id). APIKey is required unless the provider is NoAuth or a key is
// already on file.
//
// Every tuning field is a tri-state: absent keeps whatever the resolver
// picked, a number sets it, and the two Reset flags put the field back on the
// catalogue's answer. Without the flags a console could never *unset* a
// limit it once wrote — the 0 that means "auto" is also what an omitted JSON
// field looks like.
type addModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Alias    string `json:"alias,omitempty"`
	// ReuseFrom names an existing entry whose key this one borrows. Two
	// accounts of one vendor share a provider id and an endpoint, so "the key
	// on file for this provider" is ambiguous — the caller says which row it
	// is extending instead of letting the server guess.
	ReuseFrom string `json:"reuse_from,omitempty"`
	// Custom fields — used when provider == "custom": the catalogue carries
	// no endpoint for it, so the caller supplies the wire shape explicitly.
	// A known provider may carry them too: a self-hosted or relay endpoint
	// behind that vendor's dialect is a normal setup.
	APIType   string `json:"api_type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	// Optional tuning — applies to any provider; zeros keep the catalogue/
	// resolved values the backend fills in.
	ContextWindow       int               `json:"context_window,omitempty"`
	ContextWindowReset  bool              `json:"context_window_reset,omitempty"`
	MaxTokensReset      bool              `json:"max_tokens_reset,omitempty"`
	Thinking            string            `json:"thinking,omitempty"` // "on" | "off" | "auto"
	ThinkingBudget      int               `json:"thinking_budget,omitempty"`
	ThinkingBudgetReset bool              `json:"thinking_budget_reset,omitempty"`
	MaxRetries          int               `json:"max_retries,omitempty"`
	MaxRetriesReset     bool              `json:"max_retries_reset,omitempty"`
	NoAuth              *bool             `json:"no_auth,omitempty"`
	Params              map[string]any    `json:"params,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	// AllowPlaintext is the caller's explicit consent to reach this endpoint
	// over http. The network guard refuses a plaintext dial otherwise, and the
	// consent has to live on the entry: the daemon dials it long after this
	// request is gone. A pointer, not a bool — an edit that does not mention
	// the field must keep the stored waiver instead of silently revoking it.
	AllowPlaintext *bool `json:"allow_plaintext,omitempty"`
	// CopyFrom names an existing alias to duplicate. The panel never echoes a
	// key back, and params/headers are not part of the entry JSON, so a "copy
	// this model" that only re-sent the visible fields would hand the user an
	// empty credential — the source's secret and raw overrides travel here
	// instead, and every field the request does carry still wins.
	CopyFrom string `json:"copy_from,omitempty"`
}

// modelByAlias resolves a saved entry by alias, falling back to the live
// model: the registry paints the active one at the head of the page, so
// "copy this" has to work on it even though it lives in `model:`, not
// `models:`.
func (h *handler) modelByAlias(alias string) (config.ModelConfig, bool) {
	var out config.ModelConfig
	found := false
	h.readCfg(func(c *config.Config) {
		for _, m := range c.Models {
			if m.Alias() == alias {
				out, found = m, true
				return
			}
		}
		if c.Model.Alias() == alias && (c.Model.Model != "" || c.Model.Provider != "") {
			out, found = c.Model, true
		}
	})
	return out, found
}

// modelFromRequest builds the ModelConfig a request describes. existingKey is
// the key already stored on the row being rewritten (empty on create); the
// request's own key wins over it. The returned config has no Name — callers
// resolve the alias inside the mutation lock, where uniqueness can be
// enforced against the live registry.
func (h *handler) modelFromRequest(req addModelRequest, existingKey string) (config.ModelConfig, error) {
	var mc config.ModelConfig
	provider := strings.TrimSpace(req.Provider)
	base := strings.TrimSpace(req.BaseURL)
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		key = strings.TrimSpace(existingKey)
	}

	if provider == "custom" || (provider == "" && base != "") {
		// The endpoint is persisted here and dialed later by the daemon, so
		// it is checked at the moment it enters the config — a refused
		// address never reaches the file.
		if err := checkCustomBaseURL(base); err != nil {
			return config.ModelConfig{}, err
		}
		// "Extend this connection" applies to custom relays too: the form
		// prefills the saved row and submits without a key, so without this the
		// entry persisted keyless (NoAuth: true) and the user lost the key that
		// was already on file.
		if key == "" && req.ReuseFrom != "" {
			key = h.keyForAlias(req.ReuseFrom)
		}
		mc = config.ModelConfig{
			Provider:  "custom",
			APIType:   normalizeAPIType(req.APIType),
			BaseURL:   base,
			Model:     strings.TrimSpace(req.Model),
			MaxTokens: req.MaxTokens,
			APIKey:    key,
			// A keyless relay is a local-style endpoint — the CLI's
			// `model add custom` marks it no-auth for the same reason.
			NoAuth: key == "",
			// The endpoint's own plaintext opt-in travels with the entry: it is
			// the caller who knows whether this relay speaks https.
			AllowPlaintext: req.AllowPlaintext != nil && *req.AllowPlaintext,
		}
	} else {
		p, ok := providers.Lookup(provider)
		if !ok {
			return config.ModelConfig{}, errors.New("unknown provider")
		}
		// An explicit "extend this connection" beats the provider-wide guess:
		// two accounts of one vendor share both id and endpoint, so only the
		// named row says which key this model belongs to.
		if key == "" && req.ReuseFrom != "" {
			key = h.keyForAlias(req.ReuseFrom)
		}
		if key == "" {
			key = h.findProviderKey(p.ID) // reuse a key already on file for this provider
		}
		if key == "" && !p.NoAuth {
			return config.ModelConfig{}, errors.New("api_key is required for this provider")
		}
		mc, _ = providers.ModelConfig(p.ID, strings.TrimSpace(req.Model), key)
		// An explicit endpoint under a known provider: honour it, but only
		// after the same address check a custom row gets.
		if base != "" && !p.OwnsEndpoint(base) {
			if err := checkCustomBaseURL(base); err != nil {
				return config.ModelConfig{}, err
			}
			mc.BaseURL = base
			if t := strings.TrimSpace(req.APIType); t != "" {
				mc.APIType = normalizeAPIType(t)
			}
		}
	}

	// This entry would dial a roster its provider does not serve — the
	// OpenCode Zen/Go mix-up, most often. Refusing here is the difference
	// between a config that fails every call and one that never lands.
	if code, owner := endpointWarning(mc.Provider, mc.BaseURL); code != "" {
		label := owner
		if p, ok := providers.Lookup(owner); ok {
			label = p.Label
		}
		return config.ModelConfig{}, fmt.Errorf("%s is %s's endpoint — pick that provider, or point this entry at its own endpoint", mc.BaseURL, label)
	}

	// A custom endpoint behind a known vendor inherits its catalogue tuning
	// when the request left the fields unset — matching what the CLI's
	// model add and TUI wizard persist for the same endpoint.
	if mc.ContextWindow == 0 || mc.MaxTokens == 0 {
		if p, ok := providers.Detect(mc.BaseURL, mc.Model); ok {
			if mc.ContextWindow == 0 {
				mc.ContextWindow = p.ContextFor(mc.Model)
			}
			if mc.MaxTokens == 0 {
				mc.MaxTokens = p.AutoMaxTokens(mc.Model)
			}
		}
	}
	// Explicit request fields override whatever the catalogue resolved. The
	// completion cap used to be dropped here for every catalogue provider —
	// only the custom branch kept it — so a typed value came back as the auto
	// one with nothing on screen to explain why.
	if req.MaxTokens > 0 {
		mc.MaxTokens = req.MaxTokens
	}
	if req.ContextWindow > 0 {
		mc.ContextWindow = req.ContextWindow
	}
	if req.MaxTokensReset {
		mc.MaxTokens = 0
	}
	if req.ContextWindowReset {
		mc.ContextWindow = 0
	}
	switch strings.ToLower(strings.TrimSpace(req.Thinking)) {
	case "on", "off":
		mc.Thinking = strings.ToLower(strings.TrimSpace(req.Thinking))
	case "auto":
		mc.Thinking = ""
	}
	if req.ThinkingBudget > 0 {
		mc.ThinkingBudget = req.ThinkingBudget
	}
	if req.ThinkingBudgetReset {
		mc.ThinkingBudget = 0
	}
	if req.MaxRetries != 0 {
		mc.MaxRetries = req.MaxRetries
	}
	if req.MaxRetriesReset {
		mc.MaxRetries = 0
	}
	if req.NoAuth != nil {
		mc.NoAuth = *req.NoAuth
		// 免密钥 is a statement that this endpoint takes none: keeping the
		// stored secret would leave a credential the user just said is unused,
		// and the badge would keep claiming one is set.
		if *req.NoAuth && strings.TrimSpace(req.APIKey) == "" {
			mc.APIKey = ""
		}
	}
	if req.Params != nil {
		mc.Params = req.Params
	}
	if req.Headers != nil {
		mc.Headers = req.Headers
	}
	return mc, nil
}

// defaultAlias is the name a row gets when the caller names none: the model
// id, because a connection that holds several models must read as those
// models ("kimi-k3", "deepseek-v4.1-flash"), not as five rows all called
// "opencode".
func defaultAlias(mc config.ModelConfig) string {
	if m := strings.TrimSpace(mc.Model); m != "" {
		return m
	}
	return firstNonEmpty(strings.TrimSpace(mc.Provider), "model")
}

// freeAliasIn returns want, or want-2/want-3… when the registry or the
// active entry already answers to it. skip is the row being rewritten, so a
// rename that keeps the current name is not blocked by itself.
func freeAliasIn(c *config.Config, want string, skip config.ModelConfig) string {
	taken := map[string]bool{}
	add := func(m config.ModelConfig) {
		if sameEntry(m, skip) {
			return
		}
		taken[m.Alias()] = true
		// Model ids answer to `/model <id>` too, so an alias that shadows one
		// would make the switch ambiguous.
		if m.Model != "" {
			taken[m.Model] = true
		}
	}
	add(c.Model)
	for _, m := range c.Models {
		add(m)
	}
	name := strings.TrimSpace(want)
	if name == "" {
		name = "model"
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if !taken[cand] {
			return cand
		}
	}
}

// sameEntry is the identity the console paints as one row: alias, model id
// and endpoint together. Comparing the alias alone is what made a second
// entry under the same name — a second account, or the Zen/Go pair — look
// like the live model and refuse deletion.
func sameEntry(a, b config.ModelConfig) bool {
	return a.Alias() == b.Alias() && a.Model == b.Model && a.BaseURL == b.BaseURL
}

// entryMatches answers "is this the row the request addressed": the alias from
// the path, narrowed by the optional ?model= and ?base_url= disambiguators a
// legacy config with two rows under one name needs.
func entryMatches(m config.ModelConfig, alias, model, base string) bool {
	if m.Alias() != alias {
		return false
	}
	if model != "" && m.Model != model {
		return false
	}
	if base != "" && m.BaseURL != base {
		return false
	}
	return true
}

// onlyByAlias is the fallback when an exact match found nothing: the alias
// alone, provided exactly one row answers to it.
//
// The console addresses a row by alias+model+endpoint, and it gets those back
// from the same renderer that normalizes them — a base that restated a
// catalogue endpoint is reported as the endpoint the client will dial, which
// for a sibling-plan mix-up (Zen row, Go model) is not the base that is stored.
// Without this, the very row the page offers to fix answers "no such model"
// when the fix is submitted. Two rows under one name stay ambiguous on
// purpose: guessing there would delete the wrong account.
func onlyByAlias(models []config.ModelConfig, alias string) (config.ModelConfig, bool) {
	var found config.ModelConfig
	n := 0
	for _, m := range models {
		if m.Alias() == alias {
			found = m
			n++
		}
	}
	return found, n == 1
}

// addModel serves POST /api/models — registers a *new* row in `models:`,
// persisting through UpdateModelsSection (comments preserved). A name that is
// already taken never replaces the row behind it: adding the same provider
// twice is how a second account is configured, so the new row takes a
// suffixed name and both stay switchable. When no active model is configured
// at all, the new one becomes active immediately — the zero-config path the
// onboarding wizard relies on.
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

	// A copy starts from the source entry's invisible half: the credential,
	// the raw params/headers, the plaintext waiver, and the OAuth binding.
	// Anything the request states explicitly still overrides — this only
	// fills what the client could not have sent.
	var copied *config.ModelConfig
	if src := strings.TrimSpace(req.CopyFrom); src != "" {
		from, ok := h.modelByAlias(src)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no model named %q to copy", src))
			return
		}
		copied = &from
		if strings.TrimSpace(req.APIKey) == "" {
			req.APIKey = from.APIKey
		}
		if req.Params == nil {
			req.Params = from.Params
		}
		if req.Headers == nil {
			req.Headers = from.Headers
		}
		if req.AllowPlaintext == nil && from.AllowPlaintext {
			waiver := true
			req.AllowPlaintext = &waiver
		}
	}

	mc, err := h.modelFromRequest(req, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The OAuth binding has no request field: without it a copied
	// subscription-backed entry would silently become a keyless one.
	if copied != nil && mc.Auth == "" {
		mc.Auth = copied.Auth
	}
	alias := strings.TrimSpace(req.Alias)
	if alias == "" {
		alias = defaultAlias(mc)
	}
	// Validate client construction before mutating config on disk.
	mc.Name = alias
	if _, err := entry.NewClient(mc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var stored config.ModelConfig
	err = h.mutateCfgErr(func(c *config.Config) error {
		mc.Name = freeAliasIn(c, alias, config.ModelConfig{})
		stored = mc
		c.Models = append(c.Models, mc)
		// Persist inside the lock: UpdateModelsSection rewrites the whole
		// models block, and an unlocked gap would let a concurrent save
		// read-modify-write over this change.
		if h.configPath != "" {
			return config.UpdateModelsSection(h.configPath, slices.Clone(c.Models))
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
		if err := h.applyModelConfig(stored); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, modelEntryOf(stored, h.engineModel()))
}

// updateModel serves PUT /api/models/{alias} — rewrite one row in place, the
// console's "edit" (as opposed to POST's "add another account"). The row is
// addressed by the alias in the path plus the optional narrowing params; the
// active model is editable even when it was never registered, because that is
// the row the console shows at the top of the page.
func (h *handler) updateModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("alias")
	modelQ, baseQ := r.URL.Query().Get("model"), r.URL.Query().Get("base_url")
	var req addModelRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}

	// Which vendor the *request* named, before the fill-in below restates the
	// old one — the difference is what decides whether the stored key still
	// belongs to this row.
	wantProvider := strings.TrimSpace(req.Provider)

	var old config.ModelConfig
	found, inRegistry := false, false
	h.readCfg(func(c *config.Config) {
		for _, m := range c.Models {
			if entryMatches(m, name, modelQ, baseQ) {
				old, found, inRegistry = m, true, true
				return
			}
		}
	})
	active := h.engineModel()
	wasActive := false
	if found {
		wasActive = sameEntry(old, active)
	}
	if !found && entryMatches(active, name, modelQ, baseQ) {
		old, found, wasActive = active, true, true
	}
	if !found {
		// A base the renderer normalized (a catalogue alias form, a plan's
		// sibling endpoint) matches nothing on the stored value, so a unique
		// name is the remaining address. The registry wins over the live entry
		// because that is the row a rewrite and a later delete address.
		var reg config.ModelConfig
		haveReg := false
		var live config.ModelConfig
		haveLive := false
		h.readCfg(func(c *config.Config) {
			reg, haveReg = onlyByAlias(c.Models, name)
			live = c.Model
			haveLive = c.Model.Alias() == name && (c.Model.Model != "" || c.Model.Provider != "")
		})
		switch {
		case haveReg:
			old, found, inRegistry = reg, true, true
			wasActive = sameEntry(reg, active)
		case haveLive:
			old, found, wasActive = live, true, true
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, errors.New("no such model"))
		return
	}

	// An edit keeps what it does not restate: the vendor, the endpoint and the
	// model id are identity, and a request that only fixes a key or a limit
	// must not silently re-point the entry at the catalogue default.
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = old.Provider
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		req.BaseURL = old.BaseURL
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = old.Model
	}
	// The waiver is not part of the visible form for a provider row, so an
	// edit that omits it means "unchanged", not "revoke".
	if req.AllowPlaintext == nil && old.AllowPlaintext {
		waiver := true
		req.AllowPlaintext = &waiver
	}

	existingKey := old.APIKey
	if wantProvider != "" && effectiveProviderID(old) != wantProvider {
		existingKey = h.keyAcrossVendorSwitch(old, wantProvider)
	}

	mc, err := h.modelFromRequest(req, existingKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	want := strings.TrimSpace(req.Alias)
	if want == "" {
		want = old.Alias()
	}
	mc.Name = want
	if _, err := entry.NewClient(mc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var stored config.ModelConfig
	err = h.mutateCfgErr(func(c *config.Config) error {
		mc.Name = freeAliasIn(c, want, old)
		stored = mc
		if !inRegistry {
			return nil // the live entry is rewritten by applyModelConfig below
		}
		for i := range c.Models {
			if sameEntry(c.Models[i], old) {
				c.Models[i] = mc
				if h.configPath != "" {
					return config.UpdateModelsSection(h.configPath, slices.Clone(c.Models))
				}
				return nil
			}
		}
		// The row went away under us (a concurrent delete) — recreating it is
		// friendlier than losing the edit the user just confirmed.
		c.Models = append(c.Models, mc)
		if h.configPath != "" {
			return config.UpdateModelsSection(h.configPath, slices.Clone(c.Models))
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Editing the live model must re-dial it: the engine keeps a client built
	// from the previous endpoint/key, so a key fix would otherwise take
	// effect only after a restart.
	if wasActive {
		if err := h.applyModelConfig(stored); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, modelEntryOf(stored, h.engineModel()))
}

// useModel serves POST /api/models/{alias}/use — switch the active model to a
// registered alias (or model id), the web counterpart of `/model <alias>`.
// ?model= / ?base_url= narrow the lookup when a legacy config carries two rows
// under one name.
func (h *handler) useModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("alias")
	modelQ, baseQ := r.URL.Query().Get("model"), r.URL.Query().Get("base_url")
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var active, match config.ModelConfig
	isActive := false
	h.readCfg(func(c *config.Config) {
		active = c.Model
		if (c.Model.Model != "" || c.Model.Provider != "") &&
			entryMatches(c.Model, name, modelQ, baseQ) {
			match = c.Model
			isActive = true
			return
		}
		for _, m := range c.Models {
			if entryMatches(m, name, modelQ, baseQ) {
				match = m
				return
			}
			// A bare model id still addresses the one row that serves it, but
			// only when the caller did not narrow the search itself.
			if modelQ == "" && baseQ == "" && m.Model == name {
				match = m
				return
			}
		}
		// An address whose endpoint came back normalized differently from the
		// stored one — the sibling-plan case — still names a unique row.
		if only, ok := onlyByAlias(c.Models, name); ok {
			match = only
		} else if c.Model.Alias() == name && (c.Model.Model != "" || c.Model.Provider != "") {
			match, isActive = c.Model, true
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

// removeModel serves DELETE /api/models/{alias} — drops one registry row.
//
// Two things the earlier version got wrong. It called a row "active" on the
// alias alone, so a second entry that merely shared the name (another account,
// or the Zen row beside the Go row) could never be deleted. And it treated the
// live row as undeletable, which stranded a user who wanted to drop the model
// they were using. Now the identity is alias+model+endpoint, and deleting the
// live row promotes the next remaining one — refusing only when it is the last
// model there is, because that would leave the node with nothing to ask.
func (h *handler) removeModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("alias")
	modelQ, baseQ := r.URL.Query().Get("model"), r.URL.Query().Get("base_url")
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	active := h.engineModel()
	var target, successor config.ModelConfig
	found, hasSuccessor := false, false
	h.readCfg(func(c *config.Config) {
		for _, m := range c.Models {
			if entryMatches(m, name, modelQ, baseQ) {
				target, found = m, true
				break
			}
		}
		if !found {
			only, ok := onlyByAlias(c.Models, name)
			if !ok {
				return
			}
			target, found = only, true
		}
		// The survivor is the first *other* row by identity, not by address:
		// a same-named row on another endpoint is a legitimate successor, and
		// the old address-based skip silently excluded it.
		for _, m := range c.Models {
			if sameEntry(m, target) {
				continue
			}
			successor, hasSuccessor = m, true
			return
		}
	})
	if !found {
		writeErr(w, http.StatusNotFound, errors.New("no such model"))
		return
	}
	wasActive := sameEntry(target, active)
	if wasActive && !hasSuccessor {
		writeErr(w, http.StatusConflict, errors.New("this is the only model — add another one before removing it"))
		return
	}
	err := h.mutateCfgErr(func(c *config.Config) error {
		for i, m := range c.Models {
			if !sameEntry(m, target) {
				continue
			}
			c.Models = append(c.Models[:i:i], c.Models[i+1:]...)
			// Persist inside the lock — the whole models block is rewritten,
			// so a concurrent save between memory and file would lose one.
			if h.configPath != "" {
				return config.UpdateModelsSection(h.configPath, slices.Clone(c.Models))
			}
			return nil
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if wasActive {
		if err := h.applyModelConfig(successor); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	out := map[string]any{"removed": name}
	if wasActive {
		out["switched_to"] = successor.Alias()
	}
	writeJSON(w, out)
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
	// AllowPlaintext carries the form's http consent into fetch/test, so a
	// relay that has not been saved yet can still be probed over plain http.
	AllowPlaintext bool `json:"allow_plaintext,omitempty"`
	// CopyFrom names the entry a draft was duplicated from. A copy has no key
	// of its own yet — the console never receives one — so "fetch the model
	// list" on the copy sheet would otherwise dial the endpoint keyless. The
	// source's credential (and its plaintext waiver) fill the gap; an explicit
	// api_key in the same request still wins.
	CopyFrom string `json:"copy_from,omitempty"`
}

// resolveModelRefErr maps the request onto a ModelConfig, mirroring the REPL's
// resolveModel: alias → registry, provider → catalogue + key. The reason is
// kept, so a caller can tell "you named nothing usable" from "the endpoint you
// named must not be dialed" — the second is a security refusal and deserves
// its own message.
func (h *handler) resolveModelRefErr(req modelRefRequest) (config.ModelConfig, error) {
	// A copy borrows its source's credential until it has one of its own: the
	// form cannot show the key, so a fetch/test on a not-yet-saved duplicate
	// arrives keyless. Explicit fields still win over the borrowed ones.
	if strings.TrimSpace(req.APIKey) == "" {
		if src := strings.TrimSpace(req.CopyFrom); src != "" {
			if from, ok := h.modelByAlias(src); ok {
				req.APIKey = from.APIKey
				if !req.AllowPlaintext {
					req.AllowPlaintext = from.AllowPlaintext
				}
			}
		}
	}
	if req.Alias == "" && req.Provider == "" {
		return h.engineModel(), nil
	}
	if req.Alias != "" {
		var match, loose config.ModelConfig
		found, haveLoose := false, false
		h.readCfg(func(c *config.Config) {
			// The live row first: it is the one the console paints at the top
			// and the one a bare alias has always meant.
			for _, m := range append([]config.ModelConfig{c.Model}, c.Models...) {
				if m.Model == "" && m.Provider == "" {
					continue
				}
				if m.Alias() != req.Alias && m.Model != req.Alias {
					continue
				}
				// A caller that also names the model id and the endpoint is
				// addressing one specific row — two accounts of one vendor
				// share the alias when the config predates suffixed names, and
				// testing the wrong one reports the wrong account's health.
				if !haveLoose {
					loose, haveLoose = m, true
				}
				if req.Model != "" && m.Model != req.Model {
					continue
				}
				if req.BaseURL != "" && m.BaseURL != req.BaseURL {
					continue
				}
				match, found = m, true
				return
			}
		})
		if !found && haveLoose {
			match, found = loose, true
		}
		if found {
			return match, nil
		}
	}
	pid := firstNonEmpty(strings.TrimSpace(req.Provider), strings.TrimSpace(req.Alias))
	// A custom endpoint carries its wire shape inline — the catalogue entry
	// is only the marker that the caller owns the fields.
	if pid == "custom" && strings.TrimSpace(req.BaseURL) != "" {
		base := strings.TrimSpace(req.BaseURL)
		if err := checkCustomBaseURL(base); err != nil {
			return config.ModelConfig{}, err
		}
		key := strings.TrimSpace(req.APIKey)
		if key == "" {
			// A relay that is already on file already has its key on file, and
			// the form can never show it — so "fetch the model list" on a saved
			// endpoint arrives keyless and fails before it dials. Borrow the
			// stored credential, matched on the endpoint itself: the key belongs
			// to the host that issued it, so it never crosses to another base.
			key = h.keyForEndpoint(base)
		}
		return config.ModelConfig{
			Provider: "custom",
			APIType:  normalizeAPIType(req.APIType),
			BaseURL:  base,
			Model:    strings.TrimSpace(req.Model),
			APIKey:   key,
			// A probe carries the form's consent too: the model is not saved
			// yet, so nothing on disk can answer for it.
			AllowPlaintext: req.AllowPlaintext,
		}, nil
	}
	p, ok := providers.Lookup(pid)
	if !ok {
		return config.ModelConfig{}, errUnknownModel
	}
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		key = h.findProviderKey(p.ID)
	}
	if key == "" && !p.NoAuth {
		return config.ModelConfig{}, errUnknownModel
	}
	mc, _ := providers.ModelConfig(p.ID, strings.TrimSpace(req.Model), key)
	return mc, nil
}

// errUnknownModel is the "you named nothing usable" answer — the same text
// the endpoints have always returned, kept as an error so the caller can
// tell it apart from an endpoint refusal.
var errUnknownModel = errors.New("unknown model/provider, or an api_key is required")

// checkCustomBaseURL refuses a model endpoint the daemon must not dial.
//
// The console accepts a base_url in the request body and the daemon then
// talks to it, so a caller who holds the panel token can aim the round trip
// at the instance metadata service (169.254.169.254) and read the answer
// back through the console's own "test this model" response. A token leak
// would then be a cloud-credential leak.
//
// A private address stays allowed on purpose: a self-hosted model on the
// LAN (an Ollama box, a vLLM server) is a normal setup, and the panel
// already requires a token to get here.
func checkCustomBaseURL(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("invalid base_url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("base_url has no host")
	}
	if err := security.CheckEndpointHost(u.Hostname(), true); err != nil {
		return err
	}
	return nil
}

// fetchModels serves POST /api/models/fetch — the provider's remote model
// list, mirroring `/model fetch`.
func (h *handler) fetchModels(w http.ResponseWriter, r *http.Request) {
	var req modelRefRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	mc, err := h.resolveModelRefErr(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
	mc, err := h.resolveModelRefErr(req)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
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
// keyForAlias returns the key stored on the entry with this alias — a row in
// the registry or the live model. Empty when nothing matches.
func (h *handler) keyForAlias(alias string) string {
	name := strings.TrimSpace(alias)
	if name == "" {
		return ""
	}
	key := ""
	h.readCfg(func(c *config.Config) {
		if c.Model.Alias() == name && c.Model.APIKey != "" {
			key = c.Model.APIKey
			return
		}
		for _, m := range c.Models {
			if m.Alias() == name && m.APIKey != "" {
				key = m.APIKey
				return
			}
		}
	})
	return key
}

// keyForEndpoint returns the key stored on an entry that dials exactly this
// endpoint — the live model first, then the registry. The form can never show
// a saved secret, so a fetch/test against an endpoint that is already on file
// would otherwise arrive keyless and fail before it dials. Matching the base
// URL rather than the provider id is deliberate: the credential belongs to the
// host that issued it, so it is never handed to a different base.
func (h *handler) keyForEndpoint(base string) string {
	want := strings.TrimRight(strings.TrimSpace(base), "/")
	if want == "" {
		return ""
	}
	key := ""
	h.readCfg(func(c *config.Config) {
		for _, m := range append([]config.ModelConfig{c.Model}, c.Models...) {
			if m.APIKey == "" {
				continue
			}
			if strings.EqualFold(strings.TrimRight(strings.TrimSpace(m.BaseURL), "/"), want) {
				key = m.APIKey
				return
			}
		}
	})
	return key
}

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

// keyAcrossVendorSwitch decides which credential survives an edit that changes
// the vendor.
//
// A row's key was written for the vendor that row named, so carrying it into
// another vendor means dialing a stranger's API with the old account's token —
// a 401 the console cannot explain, from an edit that looked like it only fixed
// a plan dropdown. Two cases are legitimate, and both are resolved before the
// key is dropped:
//
//   - the new vendor already has a key on file (the user configured it before,
//     or added a second account), which is the right one by construction;
//   - the new vendor is a sibling plan on the same host, which is OpenCode's
//     Zen ⇄ Go: one account, one key, two rosters. Forcing a re-type there
//     would punish exactly the repair this page exists to offer.
//
// Everything else yields "", and modelFromRequest answers "api_key is required"
// instead of persisting a row that cannot authenticate.
func (h *handler) keyAcrossVendorSwitch(old config.ModelConfig, providerID string) string {
	p, ok := providers.Lookup(providerID)
	if !ok {
		return ""
	}
	// A sibling plan is the same account: Zen and Go are one key on one host,
	// and that key is the one the row already carries. Checked before the
	// provider-wide lookup, which could otherwise hand this row a *second*
	// account's key just because that account was registered first.
	if sameEndpointHost(old.BaseURL, p.BaseURL) {
		return old.APIKey
	}
	if key := h.findProviderKey(providerID); key != "" {
		return key
	}
	return ""
}

// sameEndpointHost reports whether two endpoints are served by one host — the
// test for "these are two plans of one account" rather than two vendors.
func sameEndpointHost(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
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

// keyFingerprint is a stable, non-reversible digest of a stored key. It exists
// so the console can tell two accounts of one vendor apart: key_hint (the last
// four characters) collides, and a merged connection makes "extend this
// connection" hand the new row the other account's key.
func keyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}
