// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// OpenCode is the one source that has to be asked, not read: it keeps its
// provider registry inside the app, so the CLI is the only way to see what it
// can serve.
//
// Two catalogue surfaces exist, and the installed build decides which one
// answers:
//
//   - v2 (the 2.x line) publishes `opencode api model.list`: one JSON envelope
//     served by the CLI's own background service, carrying each model's
//     provider, endpoint, limits and cost. The bare `models` subcommand still
//     exists there but prints nothing, and `models --verbose` was removed
//     outright — on 2.0.20 it answers "Unrecognized flag: --verbose" and exits
//     1, which used to fail this collector on every fresh install.
//   - 1.x answers `opencode models --verbose` with a `provider/model` header
//     per model followed by one pretty-printed JSON object each.
//
// The v2 surface is asked first and the 1.x dump is the fallback; see
// readOpenCodeCatalogue for why that order is the cheap one.
//
// The endpoint is the field that matters. A catalogue row is only honest about
// where its model lives if it carries the model's own `api.url` (1.x) or
// `settings.baseURL` (v2) — the namespace alone is not enough: OpenCode's
// `alibaba` and `alibaba-cn` are two different services (dashscope-intl vs
// dashscope), so folding both into one provider writes half the rows against
// an endpoint their key cannot reach.
//
// Two things this collector deliberately does not do:
//
//   - It does not treat the catalogue as proof of access. `opencode models`
//     prints the whole built-in list whether or not a key is configured; a
//     model listed there fails on the first turn without credentials. Every row
//     is unsuggested, and importability is decided by probing the model's own
//     endpoint with the provider's key.
//   - It does not read OpenCode's credential store. Newer builds moved
//     credentials from auth.json into opencode.db, and the app owns that file;
//     what matters here is only whether a provider's environment variable is
//     set, which is a question the environment can answer on its own.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/providers"
)

// providerKeyEnv names the environment variable each OpenCode namespace
// authenticates with. Discovery only reads whether it is set; ResolveKey fetches
// the value at import time.
var providerKeyEnv = map[string]string{
	"alibaba":     "DASHSCOPE_API_KEY",
	"alibaba-cn":  "DASHSCOPE_API_KEY",
	"anthropic":   "ANTHROPIC_API_KEY",
	"deepseek":    "DEEPSEEK_API_KEY",
	"moonshot":    "MOONSHOT_API_KEY",
	"openai":      "OPENAI_API_KEY",
	"opencode":    "OPENCODE_API_KEY",
	"openrouter":  "OPENROUTER_API_KEY",
	"siliconflow": "SILICONFLOW_API_KEY",
	"zhipu":       "ZHIPU_API_KEY",
}

// ocModel is one catalogue entry. Both surfaces are decoded into it: they
// overlap on the id, the name and the limits, and differ on where the endpoint
// lives (1.x `api.url`, v2 `settings.baseURL`) and on which id field carries
// the model (v2 repeats it as `modelID`, which is the one a run passes to
// --model). Reading both into one struct keeps a single row-building path.
type ocModel struct {
	ID         string `json:"id"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Enabled    *bool  `json:"enabled"`
	API        struct {
		URL string `json:"url"`
	} `json:"api"`
	Settings struct {
		BaseURL  string `json:"baseURL"`
		Provider string `json:"provider"`
	} `json:"settings"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost []struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

// Model is the id a run would pass to --model. v2 carries it twice; `modelID`
// is the resolved one, and the 1.x shape has only `id`.
func (m ocModel) Model() string {
	if id := strings.TrimSpace(m.ModelID); id != "" {
		return id
	}
	return strings.TrimSpace(m.ID)
}

// BaseURL is the endpoint this model lives on, on either shape.
func (m ocModel) BaseURL() string {
	if base := strings.TrimSpace(m.Settings.BaseURL); base != "" {
		return base
	}
	return strings.TrimSpace(m.API.URL)
}

// Namespace is the catalogue's own provider id — the key a credential
// variable is looked up by.
func (m ocModel) Namespace() string {
	if ns := strings.TrimSpace(m.Settings.Provider); ns != "" {
		return ns
	}
	return strings.TrimSpace(m.ProviderID)
}

// Retired reports a row the catalogue itself says is not servable. An absent
// status is not retirement: the 1.x dump does not always carry one, and
// dropping those records would empty the sheet on an older build.
func (m ocModel) Retired() bool {
	if m.Enabled != nil && !*m.Enabled {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.Status)) {
	case "", "active":
		return false
	}
	return true
}

// FreeTier reports a model the catalogue prices at zero on every entry. The
// cost array is the only evidence accepted: a v2 catalogue with no cost block
// says nothing about price, and guessing from the slug is how a paid flagship
// ends up filed as free.
func (m ocModel) FreeTier() bool {
	if len(m.Cost) == 0 {
		return false
	}
	for _, c := range m.Cost {
		if c.Input != 0 || c.Output != 0 {
			return false
		}
	}
	return true
}

// collectOpenCode runs the CLI and groups its catalogue by (provider, endpoint).
func collectOpenCode(ctx context.Context, opt Options) Source {
	src := Source{ID: SourceOpenCode, Label: "OpenCode", Status: StatusMissing}
	cli := findCLI(opt.OpenCodeCLI, "OPENCODE_CLI", "opencode")
	if cli == "" {
		src.Detail = "opencode CLI not on PATH — set OPENCODE_CLI to its full path"
		return src
	}

	runCtx, cancel := context.WithTimeout(ctx, opt.CLITimeout)
	defer cancel()
	models, err := readOpenCodeCatalogue(runCtx, cli)
	if err != nil {
		src.Status = StatusError
		src.Detail = "opencode catalogue: " + firstLine(err.Error())
		return src
	}

	// Group by the pair that actually decides where a request goes.
	type key struct{ provider, url string }
	order := make([]key, 0, 4)
	groups := make(map[key][]ocModel)
	for _, m := range models {
		k := key{m.Namespace(), strings.TrimRight(m.BaseURL(), "/")}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], m)
	}

	importable, blocked := 0, 0
	// Namespaces whose credential variable is simply not exported, tracked so
	// the refusal line can name the one variable that would unlock the sheet.
	missingEnv := make(map[string]bool)
	for _, k := range order {
		rows := groups[k]
		keyEnv := providerKeyEnv[k.provider]
		keyRef := ""
		if keyEnv != "" && opt.env(keyEnv) != "" {
			keyRef = "env:" + keyEnv
		} else if keyEnv != "" {
			missingEnv[keyEnv] = true
		}

		// Probe this endpoint once with this provider's key. A roster answer is
		// both "the key works here" and the list of ids that exist here — the
		// two facts the namespace alone cannot give.
		var roster map[string]bool
		if keyRef != "" && k.url != "" && !opt.NoProbe {
			if ids := probeModelIDs(ctx, k.url, ResolveKey(keyRef, opt), opt.ProbeTimeout); len(ids) > 0 {
				roster = make(map[string]bool, len(ids))
				for _, id := range ids {
					roster[id] = true
				}
			}
		}

		providerID := "custom"
		if id, ok := providerForCatalogue(k.provider, k.url); ok {
			providerID = id
		}
		// OpenCode's own gateway meters an anonymous request against a free
		// usage quota instead of rejecting it, so a zero-cost row there is
		// addressable with no credential at all.
		gateway := opencodeGateway(k.url)

		for _, m := range rows {
			model := m.Model()
			c := Candidate{
				Source:        SourceOpenCode,
				Provider:      providerID,
				Model:         model,
				Label:         firstNonEmpty(m.Name, model),
				BaseURL:       k.url,
				APIType:       "openai",
				KeyRef:        keyRef,
				ContextWindow: m.Limit.Context,
				Note:          k.provider,
			}
			switch {
			case keyRef == "" && gateway && k.url != "" && m.FreeTier():
				// The free tier is the reason this agent exists in the mesh:
				// no key, no config, just a per-account quota. Refusing these
				// rows for a missing variable would report a working install
				// as unusable — verified on v2.0.20, where a keyless POST to
				// the gateway answers 429 FreeUsageLimitError (a quota), not
				// 401 (a credential problem).
				c.NoAuth = true
				c.Importable = true
				c.Note = k.provider + ": free tier, no key needed (usage is metered per account)"
				importable++
			case keyRef == "":
				// The environment is the only credential this collector reads:
				// OpenCode keeps its own login in auth.json / opencode.db, and
				// that store belongs to the app (see the package comment). Naming
				// the variable is the whole fix — "no credential" alone reads as
				// a bug to anyone who has already run `opencode auth login`.
				switch {
				case freeOnProvider(k.provider, m.ID):
					// The free tier is a client-side grant, not a keyless API:
					// OpenCode's own CLI rides it and every outside call answers
					// 429 FreeUsageLimitError. Saying "export OPENCODE_API_KEY"
					// here would promise a free model the person cannot dial, so
					// the row says what is actually true and what would change it.
					c.Note = "opencode-free"
				case keyEnv != "":
					c.Note = k.provider + ": no " + keyEnv + " in this environment" +
						" (OpenCode's own login is not read — export it to import these)"
				default:
					c.Note = k.provider + ": no credential variable known for this provider"
				}
			case roster == nil:
				c.Note = k.provider + ": endpoint did not answer"
			case !roster[model]:
				c.Note = k.provider + ": not in the endpoint roster"
			default:
				c.Importable = true
				// Say why a row is usable: the endpoint answered for it with
				// this provider's key. Without this the note is the bare
				// namespace, which looks like a label rather than a verdict.
				c.Note = k.provider + ": endpoint answered for this model"
				importable++
			}
			if !c.Importable {
				blocked++
			}
			src.Candidates = append(src.Candidates, c)
		}
	}

	switch {
	case len(src.Candidates) > 0:
		src.Status = StatusOK
		src.Detail = "opencode catalogue · " + strconv.Itoa(len(src.Candidates)) + " model(s)"
		if importable > 0 {
			src.Detail += ", " + strconv.Itoa(importable) + " usable with this machine's keys"
		}
		if blocked > 0 && importable == 0 {
			src.Detail += ", none of them usable with this machine's keys"
			// A person who ran `opencode auth login` and still sees nothing
			// importable needs the missing fact, not a blank refusal: this
			// collector reads the environment only, by design.
			if one := singleEnvName(missingEnv); one != "" {
				src.Detail += " · OpenCode's own login (auth.json / opencode.db) is not read;" +
					" export " + one + " to make these importable"
			}
		}
	case len(models) == 0:
		src.Status = StatusEmpty
		src.Detail = "opencode returned no models" +
			" (v2 answers via api model.list, 1.x via models --verbose)"
	}
	return src
}

// freeOnProvider reports whether the catalogue serves this id on a free tier.
// Most free ids say so in the name ("deepseek-v4.1-flash-free"); big-pickle is
// the one the provider table has to name explicitly, which is why both
// spellings are checked.
func freeOnProvider(providerID, model string) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-free") {
		return true
	}
	p, ok := providers.Lookup(providerID)
	if !ok {
		return false
	}
	spec, ok := p.SpecFor(model)
	return ok && spec.Free
}

// singleEnvName returns the one variable in the set, or "" when there are none
// or several — several means the fix is not a single export, so naming one
// would send the person after the wrong thing.
func singleEnvName(names map[string]bool) string {
	if len(names) != 1 {
		return ""
	}
	for name := range names {
		return name
	}
	return ""
}

// readOpenCodeCatalogue asks both catalogue surfaces, newest first.
//
// v2 first: `opencode api model.list` is what 2.x publishes, and it is the
// same call the agent adapter probes. A build that predates it fails in
// milliseconds ("unknown command") and falls through to the 1.x dump, while
// the reverse order would make every 2.x run pay for a removed flag — 2.0
// answers `models --verbose` with "Unrecognized flag: --verbose" and exit 1.
//
// Both surfaces empty is "empty", not "broken": a v2 install whose background
// service is disabled answers a well-formed envelope with no entries, and
// calling that an error would send the person to fix a machine that works.
func readOpenCodeCatalogue(ctx context.Context, cli string) ([]ocModel, error) {
	models, apiErr := readOpenCodeAPI(ctx, cli)
	if len(models) > 0 {
		return models, nil
	}
	legacy, legacyErr := readOpenCodeVerbose(ctx, cli)
	if len(legacy) > 0 {
		return legacy, nil
	}
	if apiErr == nil || legacyErr == nil {
		return nil, nil
	}
	return nil, fmt.Errorf("api model.list: %v; models --verbose: %v", apiErr, legacyErr)
}

// readOpenCodeAPI reads the v2 catalogue envelope:
//
//	{"location":{…},"data":[{"id","modelID","providerID","name","settings":
//	 {"baseURL","provider"},"limit":{…},"cost":[…],"status","enabled"}]}
//
// The background service does the work (`--standalone` starts a private one
// with no providers configured and answers an empty list), so this is the same
// surface a person sees when they run the command by hand.
func readOpenCodeAPI(ctx context.Context, cli string) ([]ocModel, error) {
	// The service builds its provider registry on first use: on 2.0.20 the
	// first call after it starts answers a well-formed envelope with no
	// entries, and the same call a moment later lists the whole catalogue.
	// One retry separates that warm-up from an install that serves nothing.
	for attempt := 0; ; attempt++ {
		out, err := runOpenCodeAPI(ctx, cli)
		if err != nil {
			return nil, err
		}
		if models := parseOpenCodeCatalogue(out); len(models) > 0 {
			return models, nil
		}
		if !json.Valid(out) {
			// A different build's `api` command, or noise: fall through to the
			// dump rather than read it as "this install serves nothing".
			return nil, errString("api model.list printed something that is not a catalogue")
		}
		if attempt > 0 || !sleepCtx(ctx, 400*time.Millisecond) {
			return nil, nil
		}
	}
}

// runOpenCodeAPI is one attempt at the v2 catalogue, stderr folded into the
// error so a refusal reads as the CLI's own sentence.
func runOpenCodeAPI(ctx context.Context, cli string) ([]byte, error) {
	cmd := shimCommand(ctx, cli, "api", "model.list")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, errString(firstLine(detail))
		}
		return nil, err
	}
	return out, nil
}

// sleepCtx waits, but not past the caller's deadline. Returns false when the
// context ended first, so a wait never turns into a timeout of its own.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// parseOpenCodeCatalogue decodes a v2 catalogue body and keeps the entries a
// row can be built from, in catalogue order.
func parseOpenCodeCatalogue(raw []byte) []ocModel {
	var doc struct {
		Data []ocModel `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	models := make([]ocModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.Model() == "" || m.Namespace() == "" || m.Retired() {
			continue
		}
		models = append(models, m)
	}
	return models
}

// readOpenCodeVerbose parses the 1.x dump (`models --verbose`): a
// `provider/model` header line followed by one pretty-printed JSON object per
// model. The objects are multi-line and nested, so the parser buffers until a
// top-level `}` closes the record rather than trying to read a line at a time.
func readOpenCodeVerbose(ctx context.Context, cli string) ([]ocModel, error) {
	cmd := shimCommand(ctx, cli, "models", "--verbose")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Wait runs beside the read, not after it: it is what closes the pipe when
	// the deadline kills the process, and the read is blocked on that pipe.
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	var out []ocModel
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		var rec ocModel
		if json.Unmarshal([]byte(buf.String()), &rec) == nil && rec.Model() != "" &&
			rec.Namespace() != "" && !rec.Retired() {
			out = append(out, rec)
		}
		buf.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A header line: `provider/model`, no quotes, not JSON.
		if buf.Len() == 0 && !strings.HasPrefix(trimmed, "{") && !strings.Contains(trimmed, `"`) && strings.Contains(trimmed, "/") {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		// Only a closing brace in column 0 ends the record. Nested objects close
		// with an indented "}" that trims to the same character, and treating
		// those as the end cut every record at its first nested field.
		if strings.TrimRight(line, " \t\r") == "}" {
			flush()
		}
	}
	flush()
	<-waited

	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, errString(detail)
		}
		if ctx.Err() != nil {
			return nil, errString("timed out waiting for the catalogue")
		}
	}
	return out, nil
}

// providerForCatalogue maps one catalogue group onto a registry provider.
//
// The catalogue states which provider serves an entry, in the registry's own
// id vocabulary, so that namespace is tried first — it is the only thing that
// distinguishes `alibaba` from `alibaba-cn`, which share nothing but a vendor.
// An unknown namespace falls back to matching the endpoint, and a relay behind
// a known vendor stays "custom".
func providerForCatalogue(namespace, base string) (string, bool) {
	if p, ok := providers.Lookup(strings.TrimSpace(namespace)); ok {
		return p.ID, true
	}
	return providerForBase(base)
}

// opencodeGateway reports whether an endpoint is OpenCode's own inference
// gateway. That gateway accepts an anonymous request and meters it against a
// free-usage quota instead of rejecting it, which is what makes a zero-cost
// row there importable without any credential.
func opencodeGateway(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// providerForBase maps an endpoint onto the registry provider that publishes
// it. It is the fallback for a catalogue namespace the registry does not know:
// matching the URL is still better than calling every relay "custom".
func providerForBase(base string) (string, bool) {
	if strings.TrimSpace(base) == "" {
		return "", false
	}
	for _, p := range providers.All() {
		if sameURL(p.BaseURL, base) {
			return p.ID, true
		}
	}
	return "", false
}

// firstLine keeps a CLI's multi-line complaint to the one line a status chip
// can show.
func firstLine(s string) string {
	if line, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(line)
	}
	return s
}

// errString is a plain error for a subprocess's own words.
type errString string

func (e errString) Error() string { return string(e) }
