// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/localmodels"
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
	case "discover":
		r.modelDiscover(fields[1:])
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
	// Persist + in-memory write run in one critical section: without that,
	// an embedded /web panel sharing cfgMu could interleave a whole-document
	// rewrite between the two and lose this change.
	if err := r.mutateConfigErr(func(c *config.Config) error {
		if err := config.UpdateModelSection(configWritePath(r.configPath), mc); err != nil {
			return err
		}
		c.Model = mc
		return nil
	}); err != nil {
		return err
	}
	// The in-memory write already happened under cfgMu; SetModel swaps the
	// engine's live client to match.
	if eng := r.engine.Load(); eng != nil {
		return eng.SetModel(mc)
	}
	return nil
}

// clearActiveModel empties `model:` on purpose and drops the engine's client
// with it — the last-model delete path, shared by `/model remove` and the TUI
// panel.
//
// ClearModelSection is the deliberate clear (it bypasses the downgrade guard
// that exists to catch an accidental one), and the engine half is what makes
// the delete real: applyModel is the only other writer of the client slot, and
// it can never express "no model". Without it the row leaves the config and the
// engine keeps dialing it, so the next turn answers from a model the user has
// just deleted — the exact failure a delete is supposed to prevent.
func (r *repl) clearActiveModel() error {
	if err := r.mutateConfigErr(func(c *config.Config) error {
		if err := config.ClearModelSection(configWritePath(r.configPath)); err != nil {
			return err
		}
		c.Model = config.ModelConfig{}
		return nil
	}); err != nil {
		return err
	}
	if eng := r.engine.Load(); eng != nil {
		eng.ClearModel()
	}
	return nil
}

// findProviderKey looks up an API key already configured for providerID,
// checking the active model first and then the registered models.
func (r *repl) findProviderKey(providerID string) string {
	key := ""
	r.readConfig(func(c *config.Config) {
		if (effectiveProvider(c.Model) == providerID || c.Model.Alias() == providerID) && c.Model.APIKey != "" {
			key = c.Model.APIKey
			return
		}
		for _, m := range c.Models {
			if (effectiveProvider(m) == providerID || m.Alias() == providerID) && m.APIKey != "" {
				key = m.APIKey
				return
			}
		}
	})
	return key
}

// looksLikeAPIKey heuristically checks if s appears to be an API secret rather
// than a model name (e.g. prefix "sk-" / OpenCode's "oc_sk_", token length).
func looksLikeAPIKey(s string) bool {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "oc_sk_") {
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

// effectiveBaseURL returns the displayed endpoint URL: the configured one,
// rebased onto the catalogue's current surface when the config merely
// restated one of the provider's own (including a retired endpoint), then the
// provider's default.
func effectiveBaseURL(m config.ModelConfig) string {
	if m.BaseURL != "" {
		if p, ok := providers.Lookup(m.Provider); ok && p.OwnsEndpoint(m.BaseURL) {
			if base, _ := p.EndpointFor(m.Model); base != "" {
				return base
			}
		}
		return m.BaseURL
	}
	if p, ok := providers.Lookup(m.Provider); ok {
		// An aggregator serves a routed family on its own surface.
		if base, _ := p.EndpointFor(m.Model); base != "" {
			return base
		}
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

// modelRowConfigured reports whether a ModelConfig describes a real entry
// rather than the zero value a missing section leaves behind.
func modelRowConfigured(m config.ModelConfig) bool {
	return m.Model != "" || m.Provider != "" || m.BaseURL != ""
}

// sameModelRow is the identity of one configured model — the row, not the
// address. Two accounts of one vendor share the provider, the model id and the
// endpoint, and the alias is the only way a human tells them apart, so alias
// plus credential are what make two rows the same entry.
//
// This replaced an alias-only comparison on every write path. Adding, editing
// and deleting all keyed off "alias (or model id)", which is exactly the pair
// two accounts share: the second DeepSeek account overwrote the first on save,
// and deleting either one took both.
func sameModelRow(a, b config.ModelConfig) bool {
	return a.Alias() == b.Alias() &&
		a.Model == b.Model &&
		effectiveProvider(a) == effectiveProvider(b) &&
		sameEndpointURL(a.BaseURL, b.BaseURL) &&
		a.APIKey == b.APIKey
}

// sameEndpointURL compares two endpoints ignoring a trailing slash and case in
// the host — the two spellings a config collects for one address.
func sameEndpointURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"),
		strings.TrimRight(strings.TrimSpace(b), "/"))
}

// freeModelAlias returns want, or want-2 / want-3 …, when another row already
// answers to it. skip is the row being rewritten, so an edit that keeps its
// own name is not blocked by itself.
//
// A row's name is its primary key, so a save that is *adding* must always land
// on a free one. The old rule exempted rows that matched on provider, model
// and endpoint on the grounds that they were "the same entry" — which is
// precisely what a second account of one vendor looks like, so it never got a
// name of its own and the upsert replaced the first account instead.
func freeModelAlias(c *config.Config, want string, skip config.ModelConfig) string {
	taken := func(name string) bool {
		if name == "" {
			return false
		}
		if modelRowConfigured(c.Model) && c.Model.Alias() == name && !sameModelRow(c.Model, skip) {
			return true
		}
		for _, m := range c.Models {
			if modelRowConfigured(m) && m.Alias() == name && !sameModelRow(m, skip) {
				return true
			}
		}
		return false
	}
	name := strings.TrimSpace(want)
	if name == "" {
		name = "model"
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if !taken(cand) {
			return cand
		}
	}
}

// resolveModelRow finds the row a name addresses, and says why when it cannot:
// several rows answering to one name is a different answer from no row at all.
//
// The name is the alias first — the registry, then the live model, which a
// config written outside the registry still holds — and a model id second, but
// only when exactly one row serves it. Two accounts of one vendor share the id,
// and guessing there acts on the wrong account. Switch and remove both use this:
// before, each carried its own resolution and only one of them refused the
// ambiguous case, so `/model <alias>` silently took the first of two accounts
// while `/model remove <alias>` refused the same name.
func resolveModelRow(c *config.Config, name string) (mc config.ModelConfig, found, ambiguous bool) {
	if name == "" {
		return config.ModelConfig{}, false, false
	}
	// Alias matches, counting the live row when the registry does not already
	// hold it: a config written by hand can carry a model: entry no row lists.
	var hits []config.ModelConfig
	for _, m := range c.Models {
		if modelRowConfigured(m) && m.Alias() == name {
			hits = append(hits, m)
		}
	}
	if modelRowConfigured(c.Model) && c.Model.Alias() == name {
		listed := false
		for _, m := range hits {
			if sameModelRow(m, c.Model) {
				listed = true
				break
			}
		}
		if !listed {
			hits = append(hits, c.Model)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], true, false
	case 0:
		// no alias answers — try the model id below
	default:
		return config.ModelConfig{}, false, true
	}

	var idHits []config.ModelConfig
	for _, m := range c.Models {
		if m.Model == name {
			idHits = append(idHits, m)
		}
	}
	// The live row answers to its model id too: a single-model config an older
	// build wrote has no registry row at all, and `/model test <id>` resolved it
	// before this became the one resolver — dropping that would have made the
	// only configured model unaddressable by the id the vendor prints.
	if modelRowConfigured(c.Model) && c.Model.Model == name {
		listed := false
		for _, m := range idHits {
			if sameModelRow(m, c.Model) {
				listed = true
				break
			}
		}
		if !listed {
			idHits = append(idHits, c.Model)
		}
	}
	switch len(idHits) {
	case 1:
		return idHits[0], true, false
	case 0:
		return config.ModelConfig{}, false, false
	}
	return config.ModelConfig{}, false, true
}

// maskedKeyTail is the last four characters of a stored key — the one part of a
// secret the console shows, and the only thing distinguishing two rows that
// share a name in a config written before names were kept unique.
func maskedKeyTail(m config.ModelConfig) string {
	r := []rune(m.APIKey)
	if len(r) == 0 {
		return ""
	}
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return "…" + string(r)
}

// effectiveContextWindow returns the advertised context size in tokens,
// falling back to the model's catalogue spec (then provider row) when not
// explicitly configured.
func effectiveContextWindow(m config.ModelConfig) int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	if p, ok := providers.Lookup(m.Provider); ok {
		if ctx := p.ContextFor(m.Model); ctx > 0 {
			return ctx
		}
	}
	if p, ok := providers.Detect(m.BaseURL, m.Model); ok {
		if ctx := p.ContextFor(m.Model); ctx > 0 {
			return ctx
		}
	}
	return 0
}

// effectiveMaxTokens mirrors what NewClient will actually send: the
// configured cap, the catalogue's auto-loaded budget when unset, clamped to
// the resolved vendor ceiling. 0 for fully unknown endpoints.
func effectiveMaxTokens(m config.ModelConfig) int {
	p, ok := providers.Lookup(m.Provider)
	if !ok {
		if d, dok := providers.Detect(m.BaseURL, m.Model); dok {
			p, ok = d, true
		}
	}
	if !ok {
		return m.MaxTokens
	}
	mt := m.MaxTokens
	_, ceil := p.TokenPolicy(m.Model)
	if mt <= 0 {
		mt = p.AutoMaxTokens(m.Model)
	}
	if ceil > 0 && mt > ceil {
		mt = ceil
	}
	return mt
}

// modelStatus prints the active model and registered models with width-aware
// alignment, color highlights, and context length indicators.
func (r *repl) modelStatus() {
	p := pal()
	var active config.ModelConfig
	var models []config.ModelConfig
	r.readConfig(func(c *config.Config) {
		active = c.Model
		models = slices.Clone(c.Models)
	})
	if active.BaseURL == "" && active.Provider == "" && len(models) == 0 {
		r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.none")))
		r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
		return
	}

	r.outln(p.Heading(i18n.T(r.loc, "repl.model.head") + ":"))
	// Two rows can still answer to one name (a config written by an older
	// build, or by hand). The masked key tail is what tells them apart, so the
	// repeated names carry it — a table that prints the same line twice names
	// nothing the user can switch to or remove.
	repeats := map[string]int{}
	if modelRowConfigured(active) {
		repeats[active.Alias()]++
	}
	for _, m := range models {
		repeats[m.Alias()]++
	}
	label := func(m config.ModelConfig) string {
		name := m.Alias()
		if repeats[name] > 1 {
			if tail := maskedKeyTail(m); tail != "" {
				name += " " + tail
			}
		}
		return name
	}

	aliasW, modelW, provW, ctxW := 12, 16, 12, 7
	aliasW = max(aliasW, cliui.DisplayWidth(label(active)))
	modelW = max(modelW, cliui.DisplayWidth(effectiveModel(active)))
	provW = max(provW, cliui.DisplayWidth(effectiveProvider(active)))
	for _, m := range models {
		aliasW = max(aliasW, cliui.DisplayWidth(label(m)))
		modelW = max(modelW, cliui.DisplayWidth(effectiveModel(m)))
		provW = max(provW, cliui.DisplayWidth(effectiveProvider(m)))
	}

	capStr := func(m config.ModelConfig) string {
		cw, mt := effectiveContextWindow(m), effectiveMaxTokens(m)
		switch {
		case cw > 0 && mt > 0:
			return fmt.Sprintf("%dk/%dk", cw/1000, mt/1000)
		case cw > 0:
			return fmt.Sprintf("%dk", cw/1000)
		case mt > 0:
			return fmt.Sprintf("-/%dk", mt/1000)
		}
		return "-"
	}
	ctxW = max(ctxW, cliui.DisplayWidth(capStr(active))+2)
	for _, m := range models {
		ctxW = max(ctxW, cliui.DisplayWidth(capStr(m))+2)
	}
	mark := p.Success(p.MarkOK())
	activeBadge := p.Success("[active]")
	r.outf("  %s %s  %s  %s  %s  %s  %s\n",
		mark,
		p.Accent(cell(label(active), aliasW)),
		cell(effectiveModel(active), modelW),
		p.Muted(cell(effectiveProvider(active), provW)),
		p.Muted(cell(capStr(active), ctxW)),
		activeBadge,
		p.Muted(effectiveBaseURL(active)),
	)

	for _, m := range models {
		// Only the live row's own entry is skipped — it is printed above. A
		// second account shares the name, the model id and the endpoint with
		// it, so comparing those printed one account and hid the other: a row
		// nobody can see is a row nobody can switch to or remove.
		if sameModelRow(m, active) {
			continue
		}
		r.outf("    %s  %s  %s  %s  %s  %s\n",
			cell(label(m), aliasW),
			cell(effectiveModel(m), modelW),
			p.Muted(cell(effectiveProvider(m), provW)),
			p.Muted(cell(capStr(m), ctxW)),
			cell("", cliui.DisplayWidth("[active]")),
			p.Muted(effectiveBaseURL(m)),
		)
	}

	r.outln()
	r.outln(p.Muted("  " + i18n.T(r.loc, "repl.model.hint")))
}

// modelSwitch selects a registered model by alias or model id.
func (r *repl) modelSwitch(name string) {
	var active, match config.ModelConfig
	var found, isActive, ambiguous bool
	r.readConfig(func(c *config.Config) {
		active = c.Model
		match, found, ambiguous = resolveModelRow(c, name)
		if found && sameModelRow(c.Model, match) && modelRowConfigured(c.Model) {
			isActive = true
		}
	})
	if ambiguous {
		r.outln(i18n.Tf(r.loc, "repl.model.shared", "alias", name))
		return
	}
	if isActive {
		r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", active.Alias(), "model", effectiveModel(active)))
		return
	}
	if found {
		if err := r.applyModel(match); err != nil {
			r.modelErr = true
			r.storeErr(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.model.set", "alias", match.Alias(), "model", effectiveModel(match)))
		return
	}
	r.modelErr = true
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
	maxTokens  int    // --max-tokens
	thinking   string // --thinking on|off|auto
	budget     int    // --budget
	retries    int    // --retry N
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
		case "max-tokens", "maxtokens", "max_tokens", "maxtok":
			a.maxTokens, _ = strconv.Atoi(take())
		case "thinking":
			a.thinking = take()
		case "budget", "thinking-budget":
			a.budget, _ = strconv.Atoi(take())
		case "retry", "retries", "max-retries", "max_retries":
			a.retries, _ = strconv.Atoi(take())
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
//	       --retry N, --param k=v, --header k=v, --force
func (r *repl) modelAdd(args []string) {
	pa := parseModelAddArgs(args)
	args = pa.positional
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		r.outln(i18n.T(r.loc, "repl.model.add.usage"))
		r.outln(i18n.T(r.loc, "repl.model.add.flags"))
		return
	}
	if len(args) == 0 {
		r.modelErr = true
		r.outln(i18n.T(r.loc, "repl.model.add.usage"))
		r.outln(i18n.T(r.loc, "repl.model.add.flags"))
		return
	}
	id := args[0]
	p, ok := providers.Lookup(id)
	if !ok {
		r.modelErr = true
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
			r.modelErr = true
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
			r.modelErr = true
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
		switch pa.apiType {
		case config.APITypeOpenAI, config.APITypeAnthropic, config.APITypeResponses, config.APITypeGoogle:
		default:
			r.modelErr = true
			r.outln(i18n.Tf(r.loc, "repl.model.add.badtype", "type", pa.apiType))
			return
		}
		mc.APIType = pa.apiType
	}
	if pa.ctxWindow > 0 {
		mc.ContextWindow = pa.ctxWindow
	}
	// A custom (or rebased) endpoint behind a known vendor inherits its
	// catalogue tuning when the row doesn't already carry resolved values —
	// explicit --ctx wins, and a builtin provider's ModelConfig filled both
	// fields already.
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
	if pa.maxTokens > 0 {
		mc.MaxTokens = pa.maxTokens
	}
	if pa.thinking != "" {
		switch strings.ToLower(pa.thinking) {
		case "on", "off":
			mc.Thinking = strings.ToLower(pa.thinking)
		case "auto":
			// Auto is the provider default: store it as unset, the same
			// value the wizard and the web form write. Storing "auto" sent
			// an explicit reasoning request instead.
			mc.Thinking = ""
		default:
			r.modelErr = true
			r.outln(i18n.Tf(r.loc, "repl.model.add.badthinking", "value", pa.thinking))
			return
		}
	}
	if pa.budget > 0 {
		mc.ThinkingBudget = pa.budget
	}
	if pa.retries != 0 {
		// A negative value disables retries (ModelConfig's contract); only
		// zero means "unset", so the old >0 test made --retry -1 a no-op.
		mc.MaxRetries = pa.retries
	}
	mc.Params = pa.params
	mc.Headers = pa.headers
	if mc.Model == "" {
		r.modelErr = true
		r.outln(i18n.T(r.loc, "repl.model.add.nomodel"))
		return
	}
	// A model listed by a sibling row (Zen vs Go on OpenCode) will not be
	// served by this provider's own endpoint. Warn instead of letting the
	// entry fail later with an opaque gateway error; the user may still add
	// it. A foreign relay the user pointed at may serve the model under its
	// own catalogue, so the warning is limited to the provider's own/default
	// endpoint.
	if other, ok := providers.SiblingFor(id, mc.Model); ok {
		if p, known := providers.Lookup(id); known && (mc.BaseURL == "" || p.OwnsEndpoint(mc.BaseURL)) {
			r.outln(i18n.Tf(r.loc, "repl.model.add.sibling",
				"model", mc.Model, "provider", other.ID, "current", id))
		}
	}

	// The row's name. An explicit alias is an address — naming a row that
	// already exists updates it, which is the only "edit" the CLI has. An
	// implicit one (the provider id) is derived, so it is always made free:
	// a second account of the same vendor must not land on the first one's
	// name, or the upsert below replaces it. That derivation used to exempt
	// rows matching on provider+model+endpoint — exactly what two accounts
	// look like — so "add the same model again" silently edited the first.
	explicitAlias := strings.TrimSpace(alias)
	if explicitAlias == "" {
		want := id
		r.readConfig(func(c *config.Config) {
			alias = freeModelAlias(c, want, config.ModelConfig{})
		})
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

	// Upsert into the registry, then persist. Only an explicitly named alias
	// can arrive here already taken (an implicit one was made free above), so
	// "replaced" means the user asked for that name — and the message says so
	// instead of reporting an add that silently rewrote a row.
	var models []config.ModelConfig
	var replaced bool
	if err := r.mutateConfigErr(func(c *config.Config) error {
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
		return config.UpdateModelsSection(configWritePath(r.configPath), models)
	}); err != nil {
		r.modelErr = true
		r.storeErr(err)
		return
	}
	// If no active model is configured at all, make the newly added model active immediately.
	var noActive bool
	r.readConfig(func(c *config.Config) {
		noActive = c.Model.BaseURL == "" && c.Model.Provider == "" && c.Model.Model == ""
	})
	if noActive {
		_ = r.applyModel(mc)
	}
	if replaced {
		r.outln(i18n.Tf(r.loc, "repl.model.add.replaced", "alias", alias, "model", mc.Model))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.model.add.done", "alias", alias, "model", mc.Model))
}

// verifyModel probes mc with a one-word completion and reports the outcome.
// A reachable endpoint that answers proves the base URL, wire dialect, key
// and model id all line up — the strongest check an add can run.
func (r *repl) verifyModel(mc config.ModelConfig) bool {
	client, err := entry.NewClient(mc)
	if err != nil {
		r.modelErr = true
		r.outln(i18n.Tf(r.loc, "repl.model.add.verifyfail", "err", err.Error()))
		return false
	}
	ctx, cancel := context.WithTimeout(r.commandContext(), 20*time.Second)
	defer cancel()
	_, err = client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		r.modelErr = true
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
		// A token was recognised as the key; whatever remains is model and
		// then alias. Zero remaining is valid: `model add custom <key>` names
		// no model and the provider default applies.
		if len(pos) > 0 {
			model = pos[0]
		}
		if len(pos) > 1 {
			alias = pos[1]
		}
		return model, key, alias
	}
	// No token looked like a key: with two+ positionals the last is the key
	// and the first the model; a single positional is the model alone. No
	// positionals at all is also legal (the caller reports missing input);
	// indexing pos[0] unconditionally turned it into a panic.
	switch len(pos) {
	case 0:
		return "", "", ""
	case 1:
		model = pos[0]
	case 2:
		model, key = pos[0], pos[1]
	default:
		model, key = pos[0], pos[len(pos)-1]
		alias = pos[1]
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

// modelRemove drops one registry row by alias — or by model id when exactly one
// row serves it — and keeps the live model pointing at something that exists.
//
// Three things the earlier version got wrong. It matched the name against both
// the alias *and* the model id, so removing a model that two accounts share
// dropped both. It refused to touch the active row outright, which left a user
// unable to delete the model they were actually using. And its comparison
// accepted the first row that answered to the name, so an edit made just before
// it could be applied to the wrong account. Now the target is resolved once, by
// identity, and deleting the live model promotes the next one — or clears the
// section when it was the last, which is the explicit intent of the command.
func (r *repl) modelRemove(args []string) {
	if len(args) == 0 {
		r.modelErr = true
		r.outln(i18n.T(r.loc, "repl.model.remove.usage"))
		return
	}
	name := args[0]

	var target config.ModelConfig
	var found, ambiguous bool
	r.readConfig(func(c *config.Config) {
		target, found, ambiguous = resolveModelRow(c, name)
	})
	if ambiguous {
		r.outln(i18n.Tf(r.loc, "repl.model.shared", "alias", name))
		return
	}
	if !found {
		r.outln(i18n.Tf(r.loc, "repl.model.remove.none", "alias", name))
		return
	}

	var kept []config.ModelConfig
	var liveHit bool
	err := r.mutateConfigErr(func(c *config.Config) error {
		kept = make([]config.ModelConfig, 0, len(c.Models))
		for _, m := range c.Models {
			if sameModelRow(m, target) {
				continue // exactly the addressed row
			}
			kept = append(kept, m)
		}
		c.Models = kept
		liveHit = sameModelRow(c.Model, target)
		return config.UpdateModelsSection(configWritePath(r.configPath), kept)
	})
	if err != nil {
		r.modelErr = true
		r.storeErr(err)
		return
	}

	switch {
	case liveHit && len(kept) > 0:
		// The live row went away: promote the next one rather than leaving
		// model: pointing at an entry the registry no longer holds.
		if err := r.applyModel(kept[0]); err != nil {
			r.storeErr(err)
			return
		}
	case liveHit:
		// Deleting the last model clears model: on purpose — this is the
		// deliberate clear ClearModelSection exists for, not a stale write.
		// The engine's client goes with it, or the next ask would still be
		// sent to the row that was just removed.
		if err := r.clearActiveModel(); err != nil {
			r.storeErr(err)
			return
		}
	}
	r.outln(i18n.Tf(r.loc, "repl.model.remove.done", "alias", name))
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
		r.modelErr = true
		r.outln(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.commandContext(), 20*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		r.modelErr = true
		r.outln(i18n.Tf(r.loc, "repl.model.fetch.err", "err", err.Error()))
		return
	}
	if len(models) == 0 {
		r.modelErr = true
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
		r.modelErr = true
		r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.commandContext(), 20*time.Second)
	defer cancel()
	if mc.Model == "" {
		// A bare custom endpoint carries no default model — pick the first
		// advertised model so `model test custom <url> <key>` still probes
		// something real instead of submitting an empty model field.
		models, lerr := client.ListModels(ctx)
		if lerr != nil || len(models) == 0 {
			r.modelErr = true
			r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", i18n.T(r.loc, "repl.model.add.nomodel")))
			return
		}
		mc.Model = models[0].ID
		client, err = entry.NewClient(mc)
		if err != nil {
			r.modelErr = true
			r.outln(i18n.Tf(r.loc, "repl.model.test.fail", "err", err.Error()))
			return
		}
	}
	answer, err := client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	if err != nil {
		r.modelErr = true
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
		var mc config.ModelConfig
		r.readConfig(func(c *config.Config) { mc = c.Model })
		return mc, mc.Alias(), true
	}
	name := args[0]
	var match config.ModelConfig
	var found, ambiguous bool
	r.readConfig(func(c *config.Config) {
		match, found, ambiguous = resolveModelRow(c, name)
	})
	if ambiguous {
		// Fetching or testing "the first row that answers to this name" would
		// probe one account and report on another — the same wrong-account
		// mistake switch and remove already refuse.
		r.outln(i18n.Tf(r.loc, "repl.model.shared", "alias", name))
		return config.ModelConfig{}, "", false
	}
	if found {
		return match, match.Alias(), true
	}
	p, ok := providers.Lookup(name)
	if !ok {
		r.modelErr = true
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
//
// Only the global --config flag is lifted here. The subcommands carry their
// own flags (--force, --type, --url, --thinking…), and a generic flag parse
// with reorderFlags would move them in front of the subcommand name, where the
// top-level FlagSet rejects them as undefined (`panda model add … --force`
// failed with "flag provided but not defined: -force").
func runModel(args []string) {
	configPath := cliConfigPath
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-config":
			if i+1 < len(args) {
				i++
				configPath = args[i]
			}
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-config="):
			configPath = strings.TrimPrefix(a, "-config=")
		default:
			rest = append(rest, a)
		}
	}

	loc := i18n.Detect()
	cfg, err := loadConfigQuietly(configPath)
	if err != nil {
		fatal("load config", err)
	}
	r := &repl{
		cfg:        cfg,
		configPath: configPath,
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
	case "discover":
		r.modelDiscover(rest[1:])
	case "help", "-h", "--help":
		fmt.Println(i18n.T(loc, "repl.model.usage"))
	default:
		r.modelSwitch(rest[0])
	}
	if r.modelErr {
		os.Exit(1)
	}
}

// modelDiscover implements `panda model discover [--json] [--import] [--all]`.
//
// The same collection the console's discover sheet runs, exposed as a verb so
// it can be scripted and so the panel has a non-browser way to answer "what can
// this machine serve". Without --import it only reports; with it, every
// suggested candidate that is not already registered becomes a row.
func (r *repl) modelDiscover(args []string) {
	var jsonOut, doImport, all bool
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--import":
			doImport = true
		case "--all":
			all = true
		default:
			// This method runs inside the REPL/TUI command loop too: an
			// os.Exit here would take the whole session down over a typo.
			r.errf("usage: panda model discover [--json] [--import] [--all]\n")
			return
		}
	}

	// --json is a *global* flag: extractGlobalFlags eats it before the
	// subcommand ever sees the argv, so the switch above only catches it when
	// someone passes it twice. Honor the global too.
	jsonOut = jsonOut || jsonOutput

	ctx, cancel := context.WithTimeout(r.commandContext(), 90*time.Second)
	defer cancel()
	sources := localmodels.Discover(ctx, localmodels.Options{})

	if jsonOut {
		// `existing` is the panel's answer, and the CLI has the same config in
		// hand: leaving it out made `discover --json` claim nothing was
		// registered while the registry held four of these rows, which is a
		// trap for any script that reads this output.
		markExisting(sources, r.cfg)
		enc := json.NewEncoder(r.commandOutput())
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"sources": sources})
	}

	if !doImport {
		if !jsonOut {
			r.printDiscovery(sources)
		}
		return
	}

	// What is already registered, keyed the way a duplicate would collide: the
	// same endpoint serving the same model id is the same row, whatever alias
	// it was saved under.
	existing := make(map[string]bool, len(r.cfg.Models))
	taken := make(map[string]bool, len(r.cfg.Models)+1)
	for _, m := range r.cfg.Models {
		existing[discoverKey(m.NormalizedAPIType(), m.BaseURL, m.Model)] = true
		taken[m.Alias()] = true
	}
	if r.cfg.Model.Model != "" {
		taken[r.cfg.Model.Alias()] = true
	}

	added := make([]config.ModelConfig, 0, 8)
	for _, s := range sources {
		for _, c := range s.Candidates {
			if !c.Importable || (!all && !c.Suggested) {
				continue
			}
			if existing[discoverKey(c.APIType, c.BaseURL, c.Model)] {
				r.errf("skip %s: already registered\n", c.Model)
				continue
			}
			mc, err := c.Row(localmodels.Options{})
			if err != nil {
				r.errf("skip %s: %v\n", c.Model, err)
				continue
			}
			if _, err := entry.NewClient(mc); err != nil {
				r.errf("skip %s: %v\n", c.Model, err)
				continue
			}
			// Same check the console's import runs: a loopback row is asked once
			// whether it serves the model, because a config that names a model is
			// not proof the endpoint has it.
			if err := localmodels.Verify(ctx, mc, 12*time.Second); err != nil {
				r.errf("skip %s: %v\n", c.Model, err)
				continue
			}
			mc.Name = freeDiscoverAlias(taken, mc.Model)
			taken[mc.Name] = true
			existing[discoverKey(mc.NormalizedAPIType(), mc.BaseURL, mc.Model)] = true
			added = append(added, mc)
		}
	}
	if len(added) == 0 {
		r.outln("nothing new to import")
		return
	}
	models := append(slices.Clone(r.cfg.Models), added...)
	// configWritePath, not r.configPath: without --config the flag is empty and
	// the path the user actually edits is the user-level one `panda web` reads.
	// Writing to "" here failed the whole import on a machine whose config was
	// never named on the command line.
	//
	// The write goes through the same mutation path every other `/model`
	// verb uses: taking the file lock and the in-memory config together is
	// what keeps a TUI's next save from rewriting the registry without the
	// rows imported here.
	if err := r.mutateConfigErr(func(c *config.Config) error {
		c.Models = models
		return config.UpdateModelsSection(configWritePath(r.configPath), slices.Clone(c.Models))
	}); err != nil {
		r.storeErr(err)
		return
	}
	r.outf("imported %d model(s)\n", len(added))
	for _, m := range added {
		r.outf("  %-28s %s\n", m.Alias(), m.BaseURL)
	}
}

// printDiscovery renders the report for a terminal: one block per source, the
// suggested rows marked with "+" so `--import` has no surprises.
func (r *repl) printDiscovery(sources []localmodels.Source) {
	for _, s := range sources {
		r.outf("%s [%s] %s\n", s.ID, s.Status, s.Detail)
		for _, c := range s.Candidates {
			mark := " "
			switch {
			case !c.Importable:
				mark = "-"
			case c.Suggested:
				mark = "+"
			}
			where := c.BaseURL
			if where == "" {
				// No endpoint to print, so the column carries the reason the
				// row cannot be imported. "catalogue" alone reads as "a model
				// the vendor publishes", which is not what a login-only row is.
				switch c.Note {
				case "cli-login":
					where = "(CLI login only)"
				case "no-endpoint":
					where = "(no endpoint)"
				default:
					where = "(catalogue)"
				}
			}
			r.outf("  %s %-30s %-10s %s\n", mark, c.Model, c.Provider, where)
		}
	}
}

// markExisting flags the candidates the registry already holds, using the same
// identity the import dedupe uses.
func markExisting(sources []localmodels.Source, cfg *config.Config) {
	if cfg == nil {
		return
	}
	known := make(map[string]bool, len(cfg.Models)+1)
	note := func(m config.ModelConfig) {
		if m.Model != "" {
			known[discoverKey(m.NormalizedAPIType(), m.BaseURL, m.Model)] = true
		}
	}
	for _, m := range cfg.Models {
		note(m)
	}
	note(cfg.Model)
	for si := range sources {
		for ci := range sources[si].Candidates {
			cand := &sources[si].Candidates[ci]
			cand.Existing = known[discoverKey(cand.APIType, cand.BaseURL, cand.Model)]
		}
	}
}

// discoverKey is the identity of a row: the triple entry.Client keys on, so a
// second import of the same endpoint+dialect+model is recognised as the same
// row instead of being added again under a -2 alias.
func discoverKey(apiType, base, model string) string {
	return strings.ToLower(apiType) + "\x00" +
		strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/")) + "\x00" + model
}

// freeDiscoverAlias keeps a model id as the alias, suffixing on collision the
// way the panel's add does.
func freeDiscoverAlias(taken map[string]bool, alias string) string {
	if !taken[alias] {
		return alias
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", alias, n)
		if !taken[candidate] {
			return candidate
		}
	}
	return alias + "-copy"
}
