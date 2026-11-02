// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// Codex keeps everything the registry would want in one directory: a main
// config.toml (the provider the CLI dials), any number of *.config.toml
// profiles (routes the person switches between), and a model-catalogs/ folder
// (the ids each provider publishes, cached by the CLI).
//
// It also knows something no file does: the models the signed-in account can
// actually use. `codex app-server` reports those over JSON-RPC. They are read
// here for two reasons and neither of them is a row:
//
//   - they decide whether a bare `model = "…"` may be adopted at all — the
//     official roster is the whitelist the reference implementation uses;
//   - the console shows them, greyed, so "my account has five models and none
//     of them are in the registry" is an answer rather than a mystery. They are
//     reached through the CLI's own OAuth session, not through an endpoint and
//     a key, so they cannot be written as a registry row.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexProvider struct {
	ID      string
	Name    string
	BaseURL string
	EnvKey  string
	// WireAPI and Bearer are read on top of the reference implementation,
	// which only looks at name/base_url/env_key. OpenPanda's registry speaks
	// four dialects (config.APIType*), so a provider that declares
	// `wire_api = "responses"` has to be registered as responses or the row
	// would be written with the wrong body shape. Bearer covers a local proxy
	// that authenticates with a literal placeholder instead of an env var —
	// without it the row would be written keyless. Both are extensions; a stock
	// Codex install has neither.
	WireAPI string
	Bearer  string
}

type codexFile struct {
	Model         string
	ModelProvider string
	Providers     []codexProvider
	// CLIPath is CODEX_CLI_PATH, which the desktop app writes into its own MCP
	// server block. It names the build that wrote this file, and that build is
	// not always the `codex` on PATH — asking the wrong one for the account
	// roster is how a machine running gpt-6.1-sol was shown three older ids.
	CLIPath string
}

// codexHome resolves CODEX_HOME the way the CLI does, falling back to ~/.codex.
// A relative CODEX_HOME is refused: the CLI treats it as invalid, and reading
// "./.codex" out of whatever directory the daemon happens to run in would be a
// different answer every launch.
func codexHome(opt Options) string {
	if explicit := opt.env("CODEX_HOME"); explicit != "" {
		if filepath.IsAbs(explicit) {
			return explicit
		}
		return ""
	}
	return joinHome(opt.Home, ".codex")
}

// parseCodexToml reads the three things the registry needs out of a Codex
// config: the active model, its provider, and every provider's endpoint.
//
// No TOML library: the file is hand-edited, half the keys are irrelevant
// (mcp_servers, marketplaces, notify), and a strict parser fails on the first
// thing it does not recognise. A line scanner that only understands
// `[model_providers.x]` + `key = value` tolerates everything else by ignoring
// it.
func parseCodexToml(text string) codexFile {
	var out codexFile
	byID := make(map[string]*codexProvider)
	var order []string
	var cur *codexProvider

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(stripTomlComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inner := strings.TrimSpace(line[1 : len(line)-1])
			cur = nil
			rest, ok := strings.CutPrefix(inner, "model_providers.")
			if !ok {
				continue // mcp_servers, marketplaces, … — none of our business
			}
			// A sub-table ([model_providers.x.env]) is not the provider itself.
			if strings.Contains(rest, ".") {
				continue
			}
			id := strings.Trim(strings.TrimSpace(rest), `"'`)
			if !providerID.MatchString(id) {
				continue
			}
			p, exists := byID[id]
			if !exists {
				p = &codexProvider{ID: id}
				byID[id] = p
				order = append(order, id)
			}
			cur = p
			continue
		}

		key, value, ok := splitTomlAssignment(line)
		if !ok {
			continue
		}
		// Captured wherever it appears: the key is unambiguous, and the
		// section holding it is an implementation detail of the desktop app.
		if key == "CODEX_CLI_PATH" && out.CLIPath == "" {
			out.CLIPath = value
		}
		if cur == nil {
			switch key {
			case "model":
				out.Model = value
			case "model_provider":
				out.ModelProvider = value
			}
			continue
		}
		switch key {
		case "name":
			cur.Name = value
		case "base_url":
			cur.BaseURL = value
		case "env_key":
			cur.EnvKey = value
		case "wire_api":
			cur.WireAPI = value
		case "experimental_bearer_token":
			cur.Bearer = value
		}
	}
	for _, id := range order {
		out.Providers = append(out.Providers, *byID[id])
	}
	return out
}

// stripTomlComment drops a trailing `# …` unless it sits inside a string —
// a base URL with a fragment, or a token with a hash in it, must survive.
func stripTomlComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// splitTomlAssignment splits on the first `=` outside a string and unquotes the
// value. Codex configs mix `key = "v"`, `key = 'v'` and bare values, and a
// hand-edited file is exactly where the three coexist.
func splitTomlAssignment(line string) (key, value string, ok bool) {
	var quote rune
	eq := -1
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '=':
			eq = i
		}
		if eq >= 0 {
			break
		}
	}
	if eq < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:eq])
	value = unquoteToml(strings.TrimSpace(line[eq+1:]))
	return key, value, value != ""
}

func unquoteToml(raw string) string {
	if len(raw) >= 2 {
		q := raw[0]
		if (q == '"' || q == '\'') && raw[len(raw)-1] == q {
			body := raw[1 : len(raw)-1]
			if q == '"' {
				body = strings.ReplaceAll(body, `\"`, `"`)
			}
			return body
		}
	}
	return raw
}

// codexCatalog is one row of model-catalogs/*.json — the CLI's cached answer to
// "what does this provider publish".
type codexCatalog struct {
	provider string
	models   []struct{ id, label string }
}

// readModelCatalogs scans <home>/model-catalogs. The file name is the provider
// and the payload has three shapes in the wild (a bare array, {models: []},
// {data: []}), which is why this does not unmarshal into one struct.
func readModelCatalogs(home string) []codexCatalog {
	dir := filepath.Join(home, "model-catalogs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []codexCatalog
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		provider := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".json"), "-models")
		if !providerID.MatchString(provider) {
			continue
		}
		text, ok := readText(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		var cat codexCatalog
		cat.provider = provider
		for _, rec := range decodeModelRecords([]byte(text)) {
			if id, _ := cleanModelID(rec.id); id != "" {
				cat.models = append(cat.models, struct{ id, label string }{id, rec.label})
			}
		}
		if len(cat.models) > 0 {
			out = append(out, cat)
		}
	}
	return out
}

// readCodexAppServerCatalog asks the CLI which models this account can use.
//
// The reference implementation's first layer, and the only thing that can
// answer "is this model official?". Failure is silent: an older CLI without
// `app-server`, a signed-out account, or a slow machine all return nil, and the
// caller then simply refuses to adopt a bare model id — the safe direction.
//
// More than one build is tried, because the config's own CLI is not always
// launchable from a daemon: the desktop build creates a PATH alias under
// CODEX_HOME on startup and exits if it cannot. A roster from the older CLI on
// PATH is still worth more than no roster at all.
func readCodexAppServerCatalog(ctx context.Context, opt Options, fromConfig string) []string {
	// The whole roster read is bounded, not just each attempt: a signed-in
	// account answers in seconds, but the CLI fetches the list through a child
	// process of its own and that child can stall. Discovery is a sheet the
	// person is waiting on, and one supplementary list must not own its clock.
	deadline := time.Now().Add(22 * time.Second)
	for _, cli := range codexCLIs(opt, fromConfig) {
		remaining := time.Until(deadline)
		if remaining < 3*time.Second {
			break
		}
		if ids := codexCatalogFrom(ctx, cli, remaining); len(ids) > 0 {
			return ids
		}
	}
	return nil
}

// codexCatalogFrom runs one `codex app-server` handshake.
func codexCatalogFrom(ctx context.Context, cli string, budget time.Duration) []string {
	if budget > 20*time.Second {
		budget = 20 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := shimCommand(runCtx, cli, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil
	}
	// Wait runs beside the JSON-RPC read: on the deadline it is what closes the
	// pipe (via WaitDelay) and unblocks the decoder.
	go func() { _ = cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()

	write := func(v any) bool {
		body, err := json.Marshal(v)
		if err != nil {
			return false
		}
		_, err = stdin.Write(append(body, '\n'))
		return err == nil
	}
	if !write(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "openpanda", "version": "0.0.0"}},
	}) {
		return nil
	}

	var (
		ids     []string
		pending = 1
		nextID  = 2
	)
	dec := json.NewDecoder(bufio.NewReader(stdout))
	for pending != 0 {
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := dec.Decode(&msg); err != nil {
			return ids // EOF, or the deadline closed the pipe
		}
		if msg.ID == nil || *msg.ID != pending {
			continue
		}
		switch pending {
		case 1:
			if !write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}) {
				return ids
			}
			if !write(map[string]any{
				"jsonrpc": "2.0", "id": nextID, "method": "model/list",
				"params": map[string]any{"cursor": nil, "limit": 100},
			}) {
				return ids
			}
			pending, nextID = nextID, nextID+1
		default:
			var page struct {
				Data []struct {
					ID     string `json:"id"`
					Hidden bool   `json:"hidden"`
				} `json:"data"`
				NextCursor string `json:"nextCursor"`
			}
			if err := json.Unmarshal(msg.Result, &page); err != nil {
				return ids
			}
			for _, row := range page.Data {
				if row.Hidden || !modelID.MatchString(row.ID) {
					continue
				}
				ids = append(ids, row.ID)
			}
			// One extra page is enough for any account; the cap keeps a
			// misbehaving cursor from spinning until the deadline.
			if page.NextCursor != "" && nextID <= 4 {
				if !write(map[string]any{
					"jsonrpc": "2.0", "id": nextID, "method": "model/list",
					"params": map[string]any{"cursor": page.NextCursor, "limit": 100},
				}) {
					return ids
				}
				pending, nextID = nextID, nextID+1
				continue
			}
			pending = 0
		}
	}
	return ids
}

// codexCLIs lists the builds worth asking, best first.
//
// The config names one — the desktop app stores its bundled build under
// CODEX_CLI_PATH — and that build is the one whose model list matches the app
// the person is looking at. A `codex` on PATH can be an older npm install:
// measured on one machine, the PATH build answered with three ids while the
// desktop build listed nine, including the very model the config ran.
//
// It is a preference, not a replacement: the desktop build is a 350 MB launcher
// that prepares a PATH alias beside the config and gives up when it cannot, so
// the PATH build stays as the fallback that at least says something.
func codexCLIs(opt Options, fromConfig string) []string {
	if opt.CodexCLI != "" {
		return []string{opt.CodexCLI}
	}
	var out []string
	seen := make(map[string]bool, 2)
	add := func(p string) {
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	if v := opt.env("CODEX_CLI"); v != "" {
		add(v)
		return out
	}
	if fromConfig != "" {
		if st, err := os.Stat(fromConfig); err == nil && !st.IsDir() {
			add(fromConfig)
		}
	}
	add(findCLI("", "", "codex"))
	return out
}

// codexResponsesURL is the endpoint the Codex CLI itself dials for a ChatGPT
// plan. It is not api.openai.com: a plan token carries no api.responses.write
// scope, so the public API answers 401, while this backend accepts the token
// together with the account it already names.
//
// Spelled with /responses on the end because that is the one shape the entry
// client leaves alone — every other base gets /v1/responses appended, which
// here would address a path that does not exist.
const codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

// codexLogin reports whether ~/.codex/auth.json holds a ChatGPT login.
//
// Presence, not validity: an expired token is reported by the auth package on
// the first call, with the one instruction that fixes it. A scan that silently
// dropped every row because a token had aged out would be the same dead end
// this path exists to remove.
func codexLogin(home string) bool {
	text, ok := readText(filepath.Join(home, "auth.json"))
	if !ok {
		return false
	}
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal([]byte(text), &f) != nil {
		return false
	}
	return strings.EqualFold(f.AuthMode, "chatgpt") && strings.TrimSpace(f.Tokens.AccessToken) != ""
}

// collectCodex builds the Codex source.
func collectCodex(ctx context.Context, opt Options) Source {
	src := Source{ID: SourceCodex, Label: "Codex", Status: StatusMissing}
	home := codexHome(opt)
	if home == "" {
		src.Detail = "CODEX_HOME is not an absolute path"
		return src
	}
	mainPath := filepath.Join(home, "config.toml")
	text, ok := readText(mainPath)
	if !ok {
		src.Detail = mainPath + " not found"
		return src
	}
	main := parseCodexToml(text)

	byID := make(map[string]codexProvider, len(main.Providers))
	var dialable []codexProvider
	for _, p := range main.Providers {
		byID[p.ID] = p
		if p.BaseURL != "" {
			dialable = append(dialable, p)
		}
	}

	// The account's own roster. Also the whitelist a bare `model` needs.
	var official []string
	if !opt.NoProbe {
		official = readCodexAppServerCatalog(ctx, opt, main.CLIPath)
	}
	officialSet := make(map[string]bool, len(official))
	for _, id := range official {
		officialSet[id] = true
	}

	type pair struct{ provider, model string }
	seen := make(map[pair]bool)
	var pairs []pair
	add := func(provider, model string) {
		id, _ := cleanModelID(model)
		if provider == "" || id == "" || seen[pair{provider, id}] {
			return
		}
		seen[pair{provider, id}] = true
		pairs = append(pairs, pair{provider, id})
	}

	// Models this file names, in file order. Kept beside `pairs` because a
	// configured model has to reach the sheet even when no provider can dial it:
	// "the model I am running" is the row a person looks for first.
	type configuredModel struct{ provider, model string }
	var configured []configuredModel

	// The root model, under the reference implementation's rule:
	//
	//   provider named            → that provider's row
	//   no provider, official     → the official (openai) provider, bare id
	//   no provider, not official → no row, but still listed (see below)
	//
	// The third branch is the important one. Codex defaults model_provider to
	// "openai", so a file that names none is not saying "use the one provider I
	// declared". Guessing that is how `model = "gpt-6.1-sol"` got attached to a
	// local relay that had never heard of it. It is not dropped either — it is
	// listed as login-only, because the row a person is looking for must not
	// disappear just because it cannot be registered.
	if main.Model != "" {
		switch {
		case main.ModelProvider != "":
			add(main.ModelProvider, main.Model)
		case officialSet[main.Model]:
			add(officialProviderID(byID), main.Model)
		}
		configured = append(configured, configuredModel{main.ModelProvider, main.Model})
	}

	// Profiles: every `<name>.config.toml` beside the main file is a saved
	// route (work laptop, a relay, a subscription). Its model is as real as the
	// root one, and its providers extend the known set.
	if entries, err := os.ReadDir(home); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".config.toml") {
				continue
			}
			body, ok := readText(filepath.Join(home, e.Name()))
			if !ok {
				continue
			}
			profile := parseCodexToml(body)
			for _, p := range profile.Providers {
				if _, known := byID[p.ID]; known || p.BaseURL == "" {
					continue
				}
				byID[p.ID] = p
				dialable = append(dialable, p)
			}
			provider := profile.ModelProvider
			if provider == "" {
				provider = main.ModelProvider
			}
			if provider == "" && officialSet[profile.Model] {
				provider = officialProviderID(byID)
			}
			add(provider, profile.Model)
			if profile.Model != "" {
				configured = append(configured, configuredModel{provider, profile.Model})
			}
		}
	}

	// The cached catalogues. Independent channel: deleting the reference from
	// config.toml does not remove these, which is why they are read from disk
	// rather than derived from the providers block.
	for _, cat := range readModelCatalogs(home) {
		if _, known := byID[cat.provider]; !known {
			// A catalogue whose provider is no longer configured still names
			// models, but there is no endpoint to dial them on.
			continue
		}
		for _, m := range cat.models {
			add(cat.provider, m.id)
		}
	}

	// Live probe: the provider's own answer, which is the only list that
	// reflects what the key can actually reach today.
	if !opt.NoProbe {
		for _, p := range dialable {
			key, _ := p.key(opt)
			for _, id := range probeModelIDs(ctx, p.BaseURL, key, opt.ProbeTimeout) {
				add(p.ID, id)
			}
		}
	}

	for _, pr := range pairs {
		p, known := byID[pr.provider]
		if !known || p.BaseURL == "" {
			continue
		}
		keyValue, keyRef := p.key(opt)
		src.Candidates = append(src.Candidates, Candidate{
			Source:      SourceCodex,
			Provider:    "custom",
			Model:       pr.model,
			Label:       pr.model,
			BaseURL:     strings.TrimRight(p.BaseURL, "/"),
			APIType:     wireAPIToType(p.WireAPI),
			KeyRef:      keyRef,
			Placeholder: strings.HasPrefix(keyRef, "codex:") && !looksLikeCredential(keyValue),
			Suggested:   true,
			Importable:  true,
			Note:        p.Name,
		})
	}

	// A Codex login opens ChatGPT's own Responses backend — the surface the CLI
	// dials itself — so the account's models become ordinary rows. The row
	// carries `auth: codex` instead of a key: the credential is read from
	// ~/.codex/auth.json on every call, which keeps the CLI's rotating refresh
	// token in one owner.
	//
	// This is the difference between "my account has nine models and none of
	// them can be registered" and a working picker. A plan token carries no
	// api.responses.write scope, so api.openai.com is not an option (it answers
	// 401); Codex's backend is.
	if codexLogin(home) {
		listed := make(map[string]bool, len(src.Candidates)+len(official))
		for _, c := range src.Candidates {
			listed[c.Model] = true
		}
		addLogin := func(id string, suggested bool) {
			id, _ = cleanModelID(id)
			if id == "" || listed[id] {
				return
			}
			listed[id] = true
			src.Candidates = append(src.Candidates, Candidate{
				Source:     SourceCodex,
				Provider:   "codex",
				Model:      id,
				Label:      id,
				BaseURL:    codexResponsesURL,
				APIType:    "responses",
				Auth:       "codex",
				Suggested:  suggested,
				Importable: true,
			})
		}
		// The model the file runs is the one the person came for; the rest of
		// the roster is offered, unticked, because importing nine rows to use
		// one is not a favour.
		for _, cm := range configured {
			addLogin(cm.model, cm.model == main.Model)
		}
		for _, id := range official {
			addLogin(id, false)
		}
	}

	// Models this registry can see but cannot dial: the model this file runs,
	// then the account's roster.
	//
	// The configured one is listed even when the roster does not name it. The
	// roster belongs to whichever build answered — a desktop install and an npm
	// install of the same CLI ship different lists — so "not in the roster" is
	// not the same as "not real". A model the person is running must never be
	// missing from the one sheet whose job is to show it.
	nImportable := len(src.Candidates)
	listed := make(map[string]bool, len(src.Candidates))
	for _, c := range src.Candidates {
		listed[c.Model] = true
	}
	loginOnly := func(id, note string) {
		id, _ = cleanModelID(id)
		if id == "" || listed[id] {
			return
		}
		listed[id] = true
		src.Candidates = append(src.Candidates, Candidate{
			Source:     SourceCodex,
			Provider:   "codex",
			Model:      id,
			Label:      id,
			Importable: false,
			Note:       note,
		})
	}
	for _, cm := range configured {
		// A bare model, or one whose provider is the official entry, answers
		// through the CLI's login. Anything else was pointed at a provider that
		// this file never gave an endpoint.
		note := "cli-login"
		if cm.provider != "" && !strings.EqualFold(cm.provider, officialProviderID(byID)) {
			note = "no-endpoint"
		}
		loginOnly(cm.model, note)
	}
	// The account's own models answer through the CLI's login, not through an
	// endpoint this registry can dial. Skipped when the file points the
	// official provider at a real base_url — then they are ordinary rows and
	// appear above.
	if p, ok := byID[officialProviderID(byID)]; !ok || p.BaseURL == "" {
		for _, id := range official {
			loginOnly(id, "cli-login")
		}
	}

	switch {
	case len(src.Candidates) > 0:
		src.Status = StatusOK
		src.Detail = fmt.Sprintf("%s · %d provider(s)", mainPath, len(dialable))
		if len(official) > 0 {
			src.Detail += fmt.Sprintf(", %d account model(s)", len(official))
		}
		// The count of providers is not a promise of rows: a proxy that does
		// not publish a model list yields none, and a sheet that only says
		// "1 provider(s)" reads like a bug.
		if nImportable == 0 {
			src.Detail += " · none importable"
		}
	case len(dialable) == 0:
		src.Status = StatusEmpty
		src.Detail = "no [model_providers.*] with a base_url in " + mainPath
	default:
		src.Status = StatusEmpty
		src.Detail = "providers found, but no model id to register"
	}
	return src
}

// officialProviderID names the provider a bare model belongs to. Codex calls it
// "openai", but a hand-written file may spell it [model_providers.OpenAI], so
// the file's own spelling wins when it exists.
func officialProviderID(byID map[string]codexProvider) string {
	for id := range byID {
		if strings.EqualFold(id, "openai") {
			return id
		}
	}
	return "openai"
}

// wireAPIToType maps a Codex provider's declared wire shape onto a registry
// dialect. Absent means the classic chat-completions shape.
func wireAPIToType(wire string) string {
	if strings.EqualFold(strings.TrimSpace(wire), "responses") {
		return "responses"
	}
	return "openai"
}

// key resolves the credential a provider would send: the environment variable
// it names, else the literal bearer token written into the file.
func (p codexProvider) key(opt Options) (value, ref string) {
	if p.EnvKey != "" {
		if v := opt.env(p.EnvKey); v != "" {
			return v, "env:" + p.EnvKey
		}
	}
	if p.Bearer != "" {
		return p.Bearer, "codex:" + p.ID
	}
	return "", ""
}
