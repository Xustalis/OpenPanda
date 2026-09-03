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
	"flag"
	"fmt"
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
		fmt.Println(i18n.T(r.loc, "repl.model.usage"))
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
		fmt.Println(p.Muted("  " + i18n.T(r.loc, "repl.model.none")))
		fmt.Println(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
		return
	}

	fmt.Println(p.Heading(i18n.T(r.loc, "repl.model.head") + ":"))
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
	fmt.Printf("  %s %s  %s  %s  %s  %s  %s\n",
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
		fmt.Printf("    %s  %s  %s  %s  %s  %s\n",
			cell(m.Alias(), aliasW),
			cell(effectiveModel(m), modelW),
			p.Muted(cell(effectiveProvider(m), provW)),
			p.Muted(cell(mCtx, ctxW)),
			cell("", cliui.DisplayWidth("[active]")),
			p.Muted(effectiveBaseURL(m)),
		)
	}

	fmt.Println()
	fmt.Println(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
}

// locActiveMark returns the "active" marker glyph. It is localised via the
// i18n key so CJK locales get a full-width-safe char.
func (r *repl) locActiveMark() string {
	return i18n.T(r.loc, "repl.model.active")
}

// modelSwitch selects a registered model by alias or model id.
func (r *repl) modelSwitch(name string) {
	if r.cfg.Model.Alias() == name && (r.cfg.Model.Model != "" || r.cfg.Model.Provider != "") {
		fmt.Println(i18n.Tf(r.loc, "repl.model.set", "alias", r.cfg.Model.Alias(), "model", effectiveModel(r.cfg.Model)))
		return
	}
	for _, m := range r.cfg.Models {
		if m.Alias() == name {
			if err := r.applyModel(m); err != nil {
				r.storeErr(err)
				return
			}
			fmt.Println(i18n.Tf(r.loc, "repl.model.set", "alias", m.Alias(), "model", effectiveModel(m)))
			return
		}
	}
	for _, m := range r.cfg.Models {
		if m.Model == name {
			if err := r.applyModel(m); err != nil {
				r.storeErr(err)
				return
			}
			fmt.Println(i18n.Tf(r.loc, "repl.model.set", "alias", m.Alias(), "model", effectiveModel(m)))
			return
		}
	}
	if r.cfg.Model.Model == name && (r.cfg.Model.Model != "" || r.cfg.Model.Provider != "") {
		fmt.Println(i18n.Tf(r.loc, "repl.model.set", "alias", r.cfg.Model.Alias(), "model", effectiveModel(r.cfg.Model)))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.model.switch.none", "name", name))
}

// modelListProviders prints the built-in provider catalogue with width-aware
// alignment (CJK-safe) and auth status indicators.
func (r *repl) modelListProviders() {
	p := pal()
	fmt.Println(p.Heading(i18n.T(r.loc, "repl.model.providers.head") + ":"))

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
		fmt.Printf("  %s  %s  %s  %s  %s\n",
			p.Accent(cell(prov.ID, idW)),
			cell(prov.Label, labelW),
			cell(prov.DefaultModel, modelW),
			authStyle(cell(authText, authW)),
			p.Muted(prov.BaseURL),
		)
	}
	fmt.Println()
	fmt.Println(p.Muted("  " + i18n.T(r.loc, "repl.model.add.usage")))
}

// modelAdd registers a built-in provider, needing only its API key (or no key
// at all for local providers). `model` is optional and overrides the
// provider's default; `alias` is optional (defaults to provider id or model id
// when the provider id is already registered with another model). If a key
// is already known for the provider, it can be reused automatically.
func (r *repl) modelAdd(args []string) {
	if len(args) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.model.add.usage"))
		return
	}
	id := args[0]
	p, ok := providers.Lookup(id)
	if !ok {
		fmt.Println(i18n.Tf(r.loc, "repl.model.add.badprovider", "provider", id))
		return
	}
	var model, key, alias string
	if p.NoAuth {
		if len(args) >= 2 {
			model = args[1]
		}
		if len(args) >= 3 {
			alias = args[2]
		}
	} else {
		switch len(args) {
		case 1:
			if existingKey := r.findProviderKey(id); existingKey != "" {
				key = existingKey
			} else {
				fmt.Println(i18n.Tf(r.loc, "repl.model.add.nokey", "provider", id))
				return
			}
		case 2:
			// If args[1] does not look like an API key and a key is already known,
			// treat args[1] as the model name.
			if existingKey := r.findProviderKey(id); existingKey != "" && !looksLikeAPIKey(args[1]) {
				model = args[1]
				key = existingKey
			} else {
				key = args[1]
			}
		case 3:
			if looksLikeAPIKey(args[1]) {
				key, alias = args[1], args[2]
			} else if looksLikeAPIKey(args[2]) {
				model, key = args[1], args[2]
			} else if existingKey := r.findProviderKey(id); existingKey != "" {
				model, alias, key = args[1], args[2], existingKey
			} else {
				model, key = args[1], args[2]
			}
		default:
			if looksLikeAPIKey(args[1]) {
				key, alias = args[1], args[2]
			} else {
				model, key, alias = args[1], args[2], args[3]
			}
		}
	}
	mc, _ := providers.ModelConfig(id, model, key)

	if alias == "" {
		alias = id
		// If an existing model already uses the provider id as alias but
		// points to a different model, derive a distinct alias to avoid
		// silent overwriting.
		if model != "" {
			for _, existing := range r.cfg.Models {
				if existing.Alias() == id && existing.Model != mc.Model {
					alias = model
					break
				}
			}
		}
	}
	mc.Name = alias

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
	fmt.Println(i18n.Tf(r.loc, "repl.model.add.done", "alias", alias, "model", mc.Model))
}

// modelRemove drops a registered model by alias or model id.
func (r *repl) modelRemove(args []string) {
	if len(args) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.model.remove.usage"))
		return
	}
	name := args[0]
	for i, m := range r.cfg.Models {
		if m.Alias() != name && m.Model != name {
			continue
		}
		if m.Alias() == r.cfg.Model.Alias() {
			fmt.Println(i18n.Tf(r.loc, "repl.model.remove.active", "alias", name))
			return
		}
		r.cfg.Models = append(r.cfg.Models[:i], r.cfg.Models[i+1:]...)
		if err := config.UpdateModelsSection(configWritePath(r.configPath), r.cfg.Models); err != nil {
			r.storeErr(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.model.remove.done", "alias", name))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.model.remove.none", "alias", name))
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
		fmt.Println(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	if len(models) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.model.fetch.empty"))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.model.fetch.head", "alias", alias))
	for _, m := range models {
		fmt.Println("  " + m.ID)
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
		fmt.Println(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	answer, err := client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.model.test.ok", "alias", mc.Alias(), "reply", answer))
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
		fmt.Println(i18n.Tf(r.loc, "repl.model.switch.none", "name", name))
		return config.ModelConfig{}, "", false
	}
	var key string
	if len(args) >= 2 {
		key = args[1]
	} else if existingKey := r.findProviderKey(p.ID); existingKey != "" {
		key = existingKey
	} else if !p.NoAuth {
		fmt.Println(i18n.Tf(r.loc, "repl.model.add.nokey", "provider", name))
		return config.ModelConfig{}, "", false
	}
	mc, _ := providers.ModelConfig(name, "", key)
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
