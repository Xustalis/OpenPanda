package main

// `/model` — the multi-model registry front end. It lifts the old single-model
// switch into four verbs over a list of named models plus a built-in provider
// catalogue:
//
//	/model                          list models, star the active one
//	/model <alias>                  switch to a registered model
//	/model list                     list built-in providers
//	/model add <provider> [model] <key>   add a provider (key only) to the registry
//	/model remove <alias>           drop a registered model
//	/model fetch [alias|provider] [key]   pull the provider's model list
//	/model test [alias]             one-word connectivity check
//
// The active model lives in config's `model:` section (unchanged, so every
// existing read path keeps working); the registry lives in `models:`. A switch
// writes the chosen entry into `model:` and hot-swaps the engine's client, so
// the next ask uses it immediately rather than on the next process start.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

func (r *repl) cmdModel(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		r.modelStatus()
		return
	}
	switch fields[0] {
	case "list", "providers":
		r.modelListProviders()
	case "add":
		r.modelAdd(fields[1:])
	case "remove", "rm", "del":
		r.modelRemove(fields[1:])
	case "fetch", "models":
		r.modelFetch(fields[1:])
	case "test":
		r.modelTest(fields[1:])
	case "help", "-h", "--help":
		r.outln(i18n.T(r.loc, "repl.model.usage"))
	default:
		r.modelSwitch(fields[0])
	}
}

// applyModel persists mc as the active model and hot-swaps the engine client.
// It is the single write path a switch/add funnel through, so the three stores
// (config file, in-memory cfg, engine client) can never drift.
func (r *repl) applyModel(mc config.ModelConfig) error {
	// Validate client construction before mutating persistent config on disk.
	if _, err := entry.NewClient(mc); err != nil {
		return err
	}
	if err := config.UpdateModelSection(configWritePath(r.configPath), mc); err != nil {
		return err
	}
	r.cfg.Model = mc
	if r.engine != nil {
		if err := r.engine.SetModel(mc); err != nil {
			return err
		}
	}
	return nil
}

// findProviderKey looks up an API key already configured for providerID,
// checking the active model first and then the registered models.
func (r *repl) findProviderKey(providerID string) string {
	if r.cfg == nil {
		return ""
	}
	if (effectiveProvider(r.cfg.Model) == providerID || r.cfg.Model.Alias() == providerID) && r.cfg.Model.APIKey != "" {
		return r.cfg.Model.APIKey
	}
	for _, m := range r.cfg.Models {
		if (effectiveProvider(m) == providerID || m.Alias() == providerID) && m.APIKey != "" {
			return m.APIKey
		}
	}
	return ""
}

// looksLikeAPIKey heuristically checks if s appears to be an API secret rather
// than a model name (e.g. prefix "sk-", token length).
func looksLikeAPIKey(s string) bool {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "sk-") {
		return true
	}
	if strings.Count(s, ".") == 1 && len(s) > 30 {
		return true
	}
	if len(s) >= 32 && !strings.ContainsAny(s, "/:@") {
		return true
	}
	return false
}

// effectiveModel returns the displayed model identifier, falling back to the
// provider's default model when Model is omitted.
func effectiveModel(m config.ModelConfig) string {
	if m.Model != "" {
		return m.Model
	}
	if p, ok := providers.Lookup(m.Provider); ok && p.DefaultModel != "" {
		return p.DefaultModel
	}
	return "-"
}

// effectiveBaseURL returns the displayed endpoint URL, falling back to the
// provider's default endpoint when BaseURL is omitted.
func effectiveBaseURL(m config.ModelConfig) string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	if p, ok := providers.Lookup(m.Provider); ok && p.BaseURL != "" {
		return p.BaseURL
	}
	if p, ok := providers.Detect(m.BaseURL, m.Model); ok && p.BaseURL != "" {
		return p.BaseURL
	}
	return "-"
}

// effectiveProvider returns the provider ID, falling back to auto-detection from
// the endpoint URL or model name.
func effectiveProvider(m config.ModelConfig) string {
	if m.Provider != "" {
		return m.Provider
	}
	if p, ok := providers.Detect(m.BaseURL, m.Model); ok {
		return p.ID
	}
	return "-"
}

// effectiveContextWindow returns the advertised context size in tokens,
// falling back to the provider default when not explicitly configured.
func effectiveContextWindow(m config.ModelConfig) int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	if p, ok := providers.Lookup(m.Provider); ok && p.ContextWindow > 0 {
		return p.ContextWindow
	}
	if p, ok := providers.Detect(m.BaseURL, m.Model); ok && p.ContextWindow > 0 {
		return p.ContextWindow
	}
	return 0
}

// modelStatus prints the active model and registered models with width-aware
// alignment, color highlights, and context length indicators.
func (r *repl) modelStatus() {
	p := pal()
	active := r.cfg.Model
	if active.BaseURL == "" && active.Provider == "" && len(r.cfg.Models) == 0 {
		r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.none")))
		r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
		return
	}

	r.outln(p.Heading(i18n.T(r.loc, "repl.model.head") + ":"))
	aliasW, modelW, provW, ctxW := 12, 16, 12, 7
	aliasW = max(aliasW, cliui.DisplayWidth(active.Alias()))
	modelW = max(modelW, cliui.DisplayWidth(effectiveModel(active)))
	provW = max(provW, cliui.DisplayWidth(effectiveProvider(active)))
	for _, m := range r.cfg.Models {
		aliasW = max(aliasW, cliui.DisplayWidth(m.Alias()))
		modelW = max(modelW, cliui.DisplayWidth(effectiveModel(m)))
		provW = max(provW, cliui.DisplayWidth(effectiveProvider(m)))
	}

	ctxStr := "-"
	if cw := effectiveContextWindow(active); cw > 0 {
		ctxStr = fmt.Sprintf("%dk", cw/1000)
	}
	mark := p.Success(p.MarkOK())
	activeBadge := p.Success("[active]")
	r.outf("  %s %s  %s  %s  %s  %s  %s\n",
		mark,
		p.Accent(cell(active.Alias(), aliasW)),
		cell(effectiveModel(active), modelW),
		p.Muted(cell(effectiveProvider(active), provW)),
		p.Muted(cell(ctxStr, ctxW)),
		activeBadge,
		p.Muted(effectiveBaseURL(active)),
	)

	for _, m := range r.cfg.Models {
		if m.Alias() == active.Alias() && effectiveModel(m) == effectiveModel(active) && effectiveProvider(m) == effectiveProvider(active) {
			continue // already displayed as active
		}
		mCtx := "-"
		if cw := effectiveContextWindow(m); cw > 0 {
			mCtx = fmt.Sprintf("%dk", cw/1000)
		}
		r.outf("    %s  %s  %s  %s  %s  %s\n",
			cell(m.Alias(), aliasW),
			cell(effectiveModel(m), modelW),
			p.Muted(cell(effectiveProvider(m), provW)),
			p.Muted(cell(mCtx, ctxW)),
			cell("", cliui.DisplayWidth("[active]")),
			p.Muted(effectiveBaseURL(m)),
		)
	}

	r.outln()
	r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
}

// locActiveMark returns the "active" marker glyph. It is localised via the
// i18n key so CJK locales get a full-width-safe char.
func (r *repl) locActiveMark() string {
	return i18n.T(r.loc, "repl.model.active")
}

// modelSwitch selects a registered model by alias or model id.
func (r *repl) modelSwitch(name string) {
	if r.cfg.Model.Alias() == name && (r.cfg.Model.Model != "" || r.cfg.Model.Provider != "") {
		r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", r.cfg.Model.Alias(), "model", effectiveModel(r.cfg.Model)))
		return
	}
	for _, m := range r.cfg.Models {
		if m.Alias() == name {
			if err := r.applyModel(m); err != nil {
				r.storeErr(err)
				return
			}
			r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", m.Alias(), "model", effectiveModel(m)))
			return
		}
	}
	for _, m := range r.cfg.Models {
		if m.Model == name {
			if err := r.applyModel(m); err != nil {
				r.storeErr(err)
				return
			}
			r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", m.Alias(), "model", effectiveModel(m)))
			return
		}
	}
	if r.cfg.Model.Model == name && (r.cfg.Model.Model != "" || r.cfg.Model.Provider != "") {
		r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", r.cfg.Model.Alias(), "model", effectiveModel(r.cfg.Model)))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.model.switch.none", "name", name))
}

// modelListProviders prints the built-in provider catalogue with width-aware
// alignment (CJK-safe) and auth status indicators.
func (r *repl) modelListProviders() {
	p := pal()
	r.outln(p.Heading(i18n.T(r.loc, "repl.model.providers.head") + ":"))

	all := providers.All()
	idW, labelW, modelW, authW := 12, 22, 26, 12
	for _, prov := range all {
		idW = max(idW, cliui.DisplayWidth(prov.ID))
		labelW = max(labelW, cliui.DisplayWidth(prov.Label))
		modelW = max(modelW, cliui.DisplayWidth(prov.DefaultModel))
	}

	for _, prov := range all {
		authText := "needs key"
		authStyle := p.Muted
		if prov.NoAuth {
			authText = "no key"
			authStyle = p.Success
		} else if r.findProviderKey(prov.ID) != "" {
			authText = "key saved"
			authStyle = p.Success
		}
		r.outf("  %s  %s  %s  %s  %s\n",
			p.Accent(cell(prov.ID, idW)),
			cell(prov.Label, labelW),
			cell(prov.DefaultModel, modelW),
			authStyle(cell(authText, authW)),
			p.Muted(prov.BaseURL),
		)
	}
	r.outln()
	r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.add.usage")))
}

// modelAddArgs is the parsed form of `/model add`: positional args plus the
// flag set that tailors a registration — wire dialect, custom endpoint,
// context window, thinking mode, extra params/headers, and whether the
// connectivity probe runs before the entry is persisted.
type modelAddArgs struct {
	positional []string
	apiType    string // --type openai|anthropic
	baseURL    string // --url
	ctxWindow  int    // --ctx
	thinking   string // --thinking on|off|auto
	budget     int    // --budget
	verify     bool   // default; --force/--no-test disables
	params     map[string]any
	headers    map[string]string
}

// parseModelAddArgs lifts the "--flag [value]" switches out of args, leaving
// the positional provider/model/key/alias list behind. Values may be given
// as --flag=value or --flag value; --param and --header repeat.
func parseModelAddArgs(args []string) modelAddArgs {
	a := modelAddArgs{verify: true}
	for i := 0; i < len(args); i++ {
		s := args[i]
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.positional = append(a.positional, s)
			continue
		}
		name := strings.TrimLeft(s, "-")
		val := ""
		hasVal := false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, hasVal = name[:j], name[j+1:], true
		}
		take := func() string {
			if hasVal {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch strings.ToLower(name) {
		case "t", "type", "api-type", "apitype":
			a.apiType = strings.ToLower(take())
		case "url", "base-url", "base_url", "baseurl":
			a.baseURL = take()
		case "ctx", "context", "context-window", "context_window":
			a.ctxWindow, _ = strconv.Atoi(take())
		case "thinking":
			a.thinking = take()
		case "budget", "thinking-budget":
			a.budget, _ = strconv.Atoi(take())
		case "param", "params": // --param temperature=0.7 (repeatable)
			if kv := take(); kv != "" {
				if k, v, ok := strings.Cut(kv, "="); ok {
					if a.params == nil {
						a.params = map[string]any{}
					}
					a.params[k] = scalarOrString(v)
				}
			}
		case "header", "headers": // --header X-Tenant=blue (repeatable)
			if kv := take(); kv != "" {
				if k, v, ok := strings.Cut(kv, "="); ok {
					if a.headers == nil {
						a.headers = map[string]string{}
					}
					a.headers[k] = v
				}
			}
		case "force", "f", "no-test", "no-verify", "skip-test":
			a.verify = false
		default:
			a.positional = append(a.positional, s)
		}
	}
	return a
}

// scalarOrString parses a --param value into its natural JSON type — numbers
// and booleans — falling back to a plain string so `temperature=0.2` lands as
// a float rather than "0.2".
func scalarOrString(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		return out
	}
	return v
}

// isBaseURL reports whether s looks like an endpoint URL the custom provider
// expects as its first positional argument.
func isBaseURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// modelAdd registers a model — a built-in provider (key only) or a custom
// relay endpoint (base_url + wire dialect + key). The entry is probed with a
// one-word completion before it is persisted so a typo'd URL or wrong key
// never lands in the registry; --force skips the probe (offline, batch).
//
//	/model add <provider> [model] <key> [alias]
//	/model add custom <baseURL> [model] <key> [alias] [--type anthropic]
//	/model add ollama [model]                    (no key)
//	flags: --type, --url, --ctx N, --thinking on|off|auto, --budget N,
//	       --param k=v, --header k=v, --force
func (r *repl) modelAdd(args []string) {
	pa := parseModelAddArgs(args)
	args = pa.positional
	if len(args) == 0 {
		r.outln(i18n.T(r.loc, "repl.model.add.usage"))
		return
	}
	id := args[0]
	p, ok := providers.Lookup(id)
	if !ok {
		r.outln(i18n.Tf(r.loc, "repl.model.add.badprovider", "provider", id))
		return
	}
	rest := args[1:]
	var mc config.ModelConfig
	var model, key, alias string
	if id == "custom" {
		// Relay stations take their endpoint first: `custom <baseURL>
		// [model] <key> [alias]`. The URL may also come from --url.
		base := pa.baseURL
		if base == "" && len(rest) > 0 && isBaseURL(rest[0]) {
			base = rest[0]
			rest = rest[1:]
		}
		if base == "" {
			r.outln(i18n.T(r.loc, "repl.model.add.nourl"))
			return
		}
		model, key, alias = splitModelKeyAlias(rest)
		mc = config.ModelConfig{
			Provider: "custom",
			APIType:  config.APITypeOpenAI,
			BaseURL:  base,
			APIKey:   key,
			Model:    model,
			// A keyless relay is a local-style endpoint: mark it no-auth so
			// the client does not hard-fail with ErrNoKey before the probe.
			NoAuth: key == "",
		}
	} else {
		model, key, alias = r.splitProviderArgs(p, rest)
		if key == "" && !p.NoAuth {
			r.outln(i18n.Tf(r.loc, "repl.model.add.nokey", "provider", id))
			return
		}
		mc, _ = providers.ModelConfig(id, model, key)
	}
	// Flag overrides apply to both paths: --url rebases a built-in provider
	// onto a relay, --type switches the wire dialect, --ctx/--thinking/--budget
	// tune the model entry, --param/--header extend the request.
	if pa.baseURL != "" {
		mc.BaseURL = pa.baseURL
	}
	if pa.apiType != "" {
		if pa.apiType != config.APITypeOpenAI && pa.apiType != config.APITypeAnthropic {
			r.outln(i18n.Tf(r.loc, "repl.model.add.badtype", "type", pa.apiType))
			return
		}
		mc.APIType = pa.apiType
	}
	if pa.ctxWindow > 0 {
		mc.ContextWindow = pa.ctxWindow
	}
	if pa.thinking != "" {
		switch strings.ToLower(pa.thinking) {
		case "on", "off", "auto":
			mc.Thinking = strings.ToLower(pa.thinking)
		default:
			r.outln(i18n.Tf(r.loc, "repl.model.add.badthinking", "value", pa.thinking))
			return
		}
	}
	if pa.budget > 0 {
		mc.ThinkingBudget = pa.budget
	}
	mc.Params = pa.params
	mc.Headers = pa.headers
	if mc.Model == "" {
		r.outln(i18n.T(r.loc, "repl.model.add.nomodel"))
		return
	}

	if alias == "" {
		alias = id
		// If an existing model already uses the provider id as alias but
		// points to a different endpoint, derive a distinct alias — keep
		// suffixing until nothing clashes, so a third relay serving the same
		// model name never silently overwrites the second.
		collides := func(a string) bool {
			for _, existing := range r.cfg.Models {
				if existing.Alias() == a &&
					(existing.Model != mc.Model || existing.BaseURL != mc.BaseURL || existing.Provider != mc.Provider) {
					return true
				}
			}
			return false
		}
		if collides(alias) && mc.Model != "" {
			alias = mc.Model
		}
		base := alias
		for i := 2; collides(alias); i++ {
			alias = fmt.Sprintf("%s-%d", base, i)
		}
	}
	mc.Name = alias

	// Verify before persist: a one-word completion proves the endpoint,
	// dialect and key actually work, so a broken entry never reaches the
	// registry. --force skips the probe (offline shells, batch setup).
	if pa.verify {
		if !r.verifyModel(mc) {
			return
		}
	}

	// Upsert into the registry, then persist.
	replaced := false
	for i := range r.cfg.Models {
		if r.cfg.Models[i].Alias() == alias {
			r.cfg.Models[i] = mc
			replaced = true
			break
		}
	}
	if !replaced {
		r.cfg.Models = append(r.cfg.Models, mc)
	}
	if err := config.UpdateModelsSection(configWritePath(r.configPath), r.cfg.Models); err != nil {
		r.storeErr(err)
		return
	}
	// If no active model is configured at all, make the newly added model active immediately.
	if r.cfg.Model.BaseURL == "" && r.cfg.Model.Provider == "" && r.cfg.Model.Model == "" {
		_ = r.applyModel(mc)
	}
	r.outln(i18n.Tf(r.loc, "repl.model.add.done", "alias", alias, "model", mc.Model))
}

// verifyModel probes mc with a one-word completion and reports the outcome.
// A reachable endpoint that answers proves the base URL, wire dialect, key
// and model id all line up — the strongest check an add can run.
func (r *repl) verifyModel(mc config.ModelConfig) bool {
	client, err := entry.NewClient(mc)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.add.verifyfail", "err", err.Error()))
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.add.verifyfail", "err", err.Error()))
		r.outln(i18n.T(r.loc, "repl.model.add.verifyhint"))
		return false
	}
	r.outln(i18n.T(r.loc, "repl.model.add.verified"))
	return true
}

// splitModelKeyAlias separates [model] <key> [alias] positionals using the
// API-key heuristic: the token that looks like a secret is the key, tokens
// before it are the model, tokens after the alias.
func splitModelKeyAlias(args []string) (model, key, alias string) {
	var pos []string
	for _, a := range args {
		if looksLikeAPIKey(a) && key == "" {
			key = a
			continue
		}
		pos = append(pos, a)
	}
	if key != "" {
		// A token was recognised as the key; the rest are model/alias.
		switch len(pos) {
		case 1:
			model = pos[0]
		default:
			model, alias = pos[0], pos[1]
		}
		return model, key, alias
	}
	// No token looked like a key: with two+ positionals the last is the key
	// and the first the model; a single positional is the model alone.
	switch len(pos) {
	case 1:
		model = pos[0]
	case 2:
		model, key = pos[0], pos[1]
	default:
		model, key = pos[0], pos[len(pos)-1]
		if len(pos) >= 3 {
			alias = pos[1]
		}
	}
	return model, key, alias
}

// splitProviderArgs resolves the built-in provider's [model] <key> [alias]
// positionals, reusing a stored key when the provider already has one.
func (r *repl) splitProviderArgs(p providers.Provider, args []string) (model, key, alias string) {
	if p.NoAuth {
		if len(args) >= 1 {
			model = args[0]
		}
		if len(args) >= 2 {
			alias = args[1]
		}
		return model, key, alias
	}
	switch len(args) {
	case 0:
		key = r.findProviderKey(p.ID)
	case 1:
		// If args[0] does not look like an API key and a key is already known,
		// treat args[0] as the model name.
		if existingKey := r.findProviderKey(p.ID); existingKey != "" && !looksLikeAPIKey(args[0]) {
			model, key = args[0], existingKey
		} else {
			key = args[0]
		}
	case 2:
		if looksLikeAPIKey(args[0]) {
			key, alias = args[0], args[1]
		} else if looksLikeAPIKey(args[1]) {
			model, key = args[0], args[1]
		} else if existingKey := r.findProviderKey(p.ID); existingKey != "" {
			model, alias, key = args[0], args[1], existingKey
		} else {
			model, key = args[0], args[1]
		}
	default:
		if looksLikeAPIKey(args[0]) {
			key, alias = args[0], args[1]
		} else {
			model, key, alias = args[0], args[1], args[2]
		}
	}
	return model, key, alias
}

// modelRemove drops a registered model by alias or model id.
func (r *repl) modelRemove(args []string) {
	if len(args) == 0 {
		r.outln(i18n.T(r.loc, "repl.model.remove.usage"))
		return
	}
	name := args[0]
	for i, m := range r.cfg.Models {
		if m.Alias() != name && m.Model != name {
			continue
		}
		if m.Alias() == r.cfg.Model.Alias() {
			r.outln(i18n.Tf(r.loc, "repl.model.remove.active", "alias", name))
			return
		}
		r.cfg.Models = append(r.cfg.Models[:i], r.cfg.Models[i+1:]...)
		if err := config.UpdateModelsSection(configWritePath(r.configPath), r.cfg.Models); err != nil {
			r.storeErr(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.model.remove.done", "alias", name))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.model.remove.none", "alias", name))
}

// modelFetch pulls the model catalogue for the active model, a registered
// alias, or a built-in provider (with an inline key when it is not yet added).
func (r *repl) modelFetch(args []string) {
	mc, alias, ok := r.resolveModel(args, "fetch")
	if !ok {
		return
	}
	client, err := entry.NewClient(mc)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	if len(models) == 0 {
		r.outln(i18n.T(r.loc, "repl.model.fetch.empty"))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.model.fetch.head", "alias", alias))
	for _, m := range models {
		r.outln("  " + m.ID)
	}
}

// modelTest runs a one-word completion against the active model or a named
// alias, mirroring `panda config model test` without leaving the REPL.
func (r *repl) modelTest(args []string) {
	mc, _, ok := r.resolveModel(args, "test")
	if !ok {
		return
	}
	client, err := entry.NewClient(mc)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if mc.Model == "" {
		// A bare custom endpoint carries no default model — pick the first
		// advertised model so `model test custom <url> <key>` still probes
		// something real instead of submitting an empty model field.
		models, lerr := client.ListModels(ctx)
		if lerr != nil || len(models) == 0 {
			r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", i18n.T(r.loc, "repl.model.add.nomodel")))
			return
		}
		mc.Model = models[0].ID
		client, err = entry.NewClient(mc)
		if err != nil {
			r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
			return
		}
	}
	answer, err := client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.model.test.ok", "alias", mc.Alias(), "reply", answer))
}

// resolveModel maps fetch/test arguments onto a ModelConfig. With no argument
// it returns the active model; with one it matches a registered alias first,
// then a built-in provider (whose key may be the second argument). The bool
// reports whether a usable config was resolved.
func (r *repl) resolveModel(args []string, verb string) (config.ModelConfig, string, bool) {
	if len(args) == 0 {
		mc := r.cfg.Model
		return mc, mc.Alias(), true
	}
	name := args[0]
	if (r.cfg.Model.Alias() == name || r.cfg.Model.Model == name) && (r.cfg.Model.Model != "" || r.cfg.Model.Provider != "") {
		return r.cfg.Model, r.cfg.Model.Alias(), true
	}
	for _, m := range r.cfg.Models {
		if m.Alias() == name || m.Model == name {
			return m, m.Alias(), true
		}
	}
	p, ok := providers.Lookup(name)
	if !ok {
		r.outln(i18n.Tf(r.loc, "repl.model.switch.none", "name", name))
		return config.ModelConfig{}, "", false
	}
	rest := args[1:]
	var base, model string
	if p.ID == "custom" {
		if len(rest) > 0 && isBaseURL(rest[0]) {
			base = rest[0]
			rest = rest[1:]
		}
		// custom <url> [model] <key>: a bare custom endpoint has no default
		// model, so the name is taken from the positionals — without it the
		// probe would send an empty model field and always fail.
		if len(rest) >= 2 {
			model = rest[0]
			rest = rest[1:]
		}
	}
	var key string
	if len(rest) >= 1 {
		key = rest[0]
	} else if existingKey := r.findProviderKey(p.ID); existingKey != "" {
		key = existingKey
	} else if !p.NoAuth && p.ID != "custom" {
		// A keyless custom endpoint is legitimate (local gateways), so the
		// probe decides whether auth was needed — not the argument check.
		r.outln(i18n.Tf(r.loc, "repl.model.add.nokey", "provider", name))
		return config.ModelConfig{}, "", false
	}
	mc, _ := providers.ModelConfig(name, model, key)
	if base != "" {
		mc.BaseURL = base
		mc.NoAuth = key == ""
	}
	if p.ID == "custom" && mc.BaseURL == "" {
		r.outln(i18n.T(r.loc, "repl.model.add.nourl"))
		return config.ModelConfig{}, "", false
	}
	return mc, name, true
}

// runModel implements `panda model [status|list|add|remove|switch|fetch|test]`.
func runModel(args []string) {
	fs := flag.NewFlagSet("model", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	rest := fs.Args()

	loc := i18n.Detect()
	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	r := &repl{
		cfg:        cfg,
		configPath: *configPath,
		loc:        loc,
	}
	if len(rest) == 0 {
		r.modelStatus()
		return
	}
	switch rest[0] {
	case "status":
		r.modelStatus()
	case "list", "providers":
		r.modelListProviders()
	case "add":
		r.modelAdd(rest[1:])
	case "remove", "rm", "del":
		r.modelRemove(rest[1:])
	case "switch", "use", "set":
		if len(rest) < 2 {
			fatalMsg("usage: panda model switch <alias>")
		}
		r.modelSwitch(rest[1])
	case "fetch", "models":
		r.modelFetch(rest[1:])
	case "test":
		r.modelTest(rest[1:])
	case "help", "-h", "--help":
		fmt.Println(i18n.T(loc, "repl.model.usage"))
	default:
		r.modelSwitch(rest[0])
	}
}
