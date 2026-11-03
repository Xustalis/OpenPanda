// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// InjectionKind classifies why an injection happened, in the compact form the
// task-output notice prints. The full sentence lives in Decision.Reason,
// which is audit and event material — the notice is a one-line footnote on
// the agent's own output, so it carries the config value, not prose.
type InjectionKind string

const (
	// InjectionKindAuto is the "agent had no credentials of its own" path.
	InjectionKindAuto InjectionKind = "auto"
	// InjectionKindAlways is the injection.model=always override.
	InjectionKindAlways InjectionKind = "always"
	// InjectionKindRescue is the failure-time retry: the agent's own model
	// failed with a provider/auth error, so panda's model ran the second
	// attempt.
	InjectionKindRescue InjectionKind = "rescue"
)

// InjectionDecision is the outcome of the model-injection policy check for
// one agent adapter: whether panda overrides the agent's model endpoint via
// env, and a secret-free human-readable reason for announcements/audit.
type InjectionDecision struct {
	Inject  bool
	Reason  string
	Model   string        // model name injected (empty when Inject is false)
	BaseURL string        // endpoint injected (empty when Inject is false)
	Kind    InjectionKind // why it injected; empty when Inject is false
}

// injectionModelFor resolves the model used when injecting into one agent.
// A per-agent pin (injection.agents) wins over the node model; every unset
// field is inherited from the node model so the API key and model name are
// declared once. Inheritance deliberately stops at the transport knobs
// (Params/Headers/Thinking): an override exists because the target speaks a
// different dialect, and carrying an OpenAI-only body field into an Anthropic
// endpoint is exactly the mismatch this pin is here to avoid.
//
// Resolution is by agent name first (the capability card's key), then by
// adapter file, so a card that aliases the agent still finds its entry.
func (r *Router) injectionModelFor(name, adapter string) config.ModelConfig {
	base := r.model
	if len(r.injectionAgents) == 0 {
		return base
	}
	ov, ok := r.injectionAgents[name]
	if !ok {
		ov, ok = r.injectionAgents[adapter]
	}
	if !ok {
		return base
	}
	out := ov
	if out.APIKey == "" {
		out.APIKey = base.APIKey
	}
	if out.Provider == "" {
		out.Provider = base.Provider
	}
	if out.Name == "" {
		out.Name = base.Name
	}
	if out.Model == "" {
		out.Model = base.Model
	}
	// The endpoint and its dialect inherit too. A pin exists to change one
	// fact about one agent — usually just the dialect, which is exactly what
	// the route refusal tells an operator to set ("set
	// injection.agents.deepseek_harness.api_type to match the endpoint").
	// Leaving these two out made that advice impossible to follow: the pin
	// then resolved to no base URL at all, and the same agent failed a second
	// time with "model has no endpoint: base_url is empty", which reads as a
	// different problem entirely.
	if out.BaseURL == "" {
		out.BaseURL = base.BaseURL
	}
	if out.APIType == "" {
		out.APIType = base.APIType
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = base.MaxTokens
	}
	if out.ContextWindow == 0 {
		out.ContextWindow = base.ContextWindow
	}
	// The plaintext waiver belongs to the endpoint, not to the pin. A node
	// model that points at a plaintext relay keeps its waiver when a pin only
	// overrides the dialect; dropping it made the documented fix ("set
	// injection.agents.<name>.api_type") land on a guard refusal, which reads
	// as a different problem entirely.
	if !out.AllowPlaintext {
		out.AllowPlaintext = base.AllowPlaintext
	}
	return out
}

// InjectionDecision evaluates the configured injection.model strategy for the
// adapter that drives an agent. It never logs or returns secrets — only the
// model name and endpoint, which are not sensitive.
//
// Strategy (injection.model):
//   - never:  no injection, unconditionally.
//   - always: always inject the panda model endpoint (legacy behavior).
//   - auto:   inject only when the agent carries no model credentials of its
//     own (env vars, login state / config files — both from the agent's
//     registry credential manifest) AND panda has a model key configured;
//     otherwise the agent's native model wins.
//
// name is the card's agent entry ("claude_code"), adapter its script
// ("claude_code.py"); both go to agents.Lookup so a generic.py custom CLI
// resolves to no manifest instead of borrowing zcode's.
func (r *Router) InjectionDecision(name, adapter string) InjectionDecision {
	// m is the node model unless this agent carries a pin (injection.agents):
	// one agent's dialect must not decide what the others get.
	m := r.injectionModelFor(name, adapter)
	switch r.injectionModel {
	case config.InjectionModelNever:
		return InjectionDecision{Inject: false, Reason: "injection.model=never"}
	case config.InjectionModelAlways:
		if !modelConfigured(m) {
			return InjectionDecision{Inject: false, Reason: "no model configured"}
		}
		if blocker := injectionBlocker(name, adapter, m); blocker != "" {
			return InjectionDecision{Inject: false, Reason: "model injection refused: " + blocker}
		}
		return InjectionDecision{
			Inject:  true,
			Reason:  "injection.model=always",
			Kind:    InjectionKindAlways,
			Model:   effectiveModelNameFor(name, adapter, m),
			BaseURL: effectiveBaseURLFor(name, adapter, m),
		}
	}
	// auto: an agent that ships its own model (OpenCode's free tier) is
	// self-sufficient. Injecting panda's endpoint would override the model the
	// agent is built around — and opencode.py substitutes a request outside its
	// own roster right back to a free model, so the injection would be recorded
	// as applied while changing nothing.
	//
	// This does not lose the rescue: with Inject false here, the failure-time
	// retry in runAdapterDefault still fires when the free tier errors out or
	// runs out of quota, and that retry injects panda's model. "Own model
	// first, panda's model on failure" is exactly the policy.
	//
	// injection.model=always still overrides (the switch above returned
	// already), so a user who wants panda's model on every run can say so.
	if k, ok := agents.Lookup(name, adapter); ok && k.SelfContainedModel {
		return InjectionDecision{
			Inject: false,
			Reason: "agent ships its own model (self-contained)",
		}
	}
	// auto: agent-native credentials win.
	if own, source := probeAgentCredentials(name, adapter); own {
		return InjectionDecision{
			Inject: false,
			Reason: "agent carries its own model credentials (" + source + ")",
		}
	}
	if !modelConfigured(m) || m.APIKey == "" {
		return InjectionDecision{
			Inject: false,
			Reason: "agent has no own credentials but panda has no model key configured",
		}
	}
	if blocker := injectionBlocker(name, adapter, m); blocker != "" {
		return InjectionDecision{
			Inject: false,
			Reason: "model injection refused: " + blocker,
		}
	}
	return InjectionDecision{
		Inject:  true,
		Reason:  "agent has no own model credentials and panda has a model configured",
		Kind:    InjectionKindAuto,
		Model:   effectiveModelNameFor(name, adapter, m),
		BaseURL: effectiveBaseURLFor(name, adapter, m),
	}
}

// modelConfigured reports whether the config names a real endpoint — a
// built-in provider id or an explicit base_url. An empty ModelConfig is never
// injected: the historical "empty means DeepSeek" fallbacks would aim the
// author's vendor choice at a user who never picked one.
func modelConfigured(model config.ModelConfig) bool {
	return strings.TrimSpace(model.Provider) != "" || strings.TrimSpace(model.BaseURL) != ""
}

// supportsModelInjection is registry-driven: an agent is injectable when its
// registry entry declares a model-env mapping and panda's model speaks a
// compatible protocol (Anthropic or OpenAI). Resolution is by name+adapter
// (agents.Lookup): a generic.py entry for an unknown CLI has no mapping and
// is never injectable.
func supportsModelInjection(name, adapter string, model config.ModelConfig) bool {
	return injectionBlocker(name, adapter, model) == ""
}

// injectionBlocker explains why an agent cannot take panda's model, or ""
// when it can. The distinction matters for diagnosis: "adapter does not
// support injection" and "the endpoint speaks the wrong dialect" used to be
// the same sentence, so a node whose model is OpenAI-dialect got told its
// Anthropic-only agent was an unsupported adapter. The fix is usually a
// per-agent pin (injection.agents.<name>), which the message names.
func injectionBlocker(name, adapter string, model config.ModelConfig) string {
	k, ok := agents.Lookup(name, adapter)
	if !ok {
		return "no model-env mapping is registered for " + adapter
	}
	if k.ModelEnv == nil {
		return adapter + " declares no model-env contract, so panda cannot point it at another endpoint"
	}
	base, apiType := injectionEndpoint(model)
	// Nothing to dial. A provider id whose catalogue row declares no endpoint
	// (custom) with no base_url of its own resolves to "", and injecting it
	// would either set an empty endpoint in the adapter's environment or —
	// under the old fallback — send the key to a vendor the config never
	// named. Refuse; injectionRefusal names what is actually missing.
	if base == "" {
		return injectionRefusal(adapter, model)
	}
	// The agent CLIs speak Messages or Chat Completions, with one exception:
	// agy always sends Gemini generateContent, so it is the one adapter that
	// needs a google-dialect endpoint and must refuse everything else. A model
	// on a surface only panda's own client drives (Responses) is never
	// injected: the CLI would send the wrong request shape to a valid
	// endpoint.
	if adapter == "antigravity.py" {
		if apiType != config.APITypeGoogle {
			return "antigravity (agy) always sends Gemini generateContent, but the configured model speaks " +
				apiType + "; set injection.agents." + name + " to a google-dialect endpoint"
		}
	} else if apiType != config.APITypeAnthropic && apiType != config.APITypeOpenAI {
		return "the configured model speaks " + apiType +
			", a surface only panda's own client drives; an agent CLI needs a " +
			config.APITypeAnthropic + " or " + config.APITypeOpenAI +
			" model — set injection.agents." + name + " to one"
	}
	// A CLI that declares its dialect cannot take a model served on the other
	// one. A polyglot agent (empty APIType) speaks whatever the model does.
	if k.ModelEnv.APIType != "" && k.ModelEnv.APIType != apiType {
		return "the configured model speaks " + apiType + " but " + adapter +
			" only accepts " + k.ModelEnv.APIType +
			"; set injection.agents." + name + ".api_type to match the endpoint"
	}
	if adapter == "claude_code.py" && !isDeepSeekEndpoint(base) {
		return "claude_code injection targets DeepSeek endpoints only"
	}
	return ""
}

// injectionRefusal names why an agent that would otherwise be injectable gets
// no model. "No endpoint" is the one case that used to be papered over with a
// vendor default, so it says what is actually missing; every other refusal
// keeps the historical wording.
func injectionRefusal(adapter string, model config.ModelConfig) string {
	if base, _ := injectionEndpoint(model); base == "" {
		return "model has no endpoint: base_url is empty and provider " + strings.TrimSpace(model.Provider) + " declares none"
	}
	return "model injection is not safely supported for " + adapter
}

// deepSeekAPIHost is the DeepSeek API host panda injects credentials for.
const deepSeekAPIHost = "api.deepseek.com"

// isDeepSeekEndpoint reports whether base_url points at DeepSeek's API. An
// empty base_url is not DeepSeek's endpoint — it is no endpoint at all (see
// injectionEndpoint), and treating it as DeepSeek was how a config with no
// destination ended up dialing api.deepseek.com/anthropic.
func isDeepSeekEndpoint(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == deepSeekAPIHost
}

// Cost control (hard constraint): deepseek-v4-pro is never injected or called
// on any path — when the node configures it, the flash model is substituted.
const (
	deepseekProModel   = "deepseek-v4-pro"
	deepseekFlashModel = "deepseek-flash"
)

// injectionEndpoint resolves the endpoint and dialect an injection should aim
// at, following the same chain entry.NewClient uses: an explicit base_url the
// user pointed somewhere else wins, an empty one takes the catalogue row's,
// and a base_url that merely restated one of the row's own endpoints is
// rebased onto the surface this model is actually served on.
//
// The previous shape skipped the catalogue entirely and fell back to a
// hard-coded vendor when base_url was empty, so a node configured with a
// provider id alone (`provider: opencode-go`) handed that provider's key to
// api.openai.com — or to DeepSeek's Anthropic endpoint — and every run died on
// a 401 that named nothing in the config.
//
// An empty return is now a verdict, not a gap to fill: the config names no
// endpoint and the catalogue row contributes none, so the caller must refuse
// the injection. `custom` is that row today (it carries an empty BaseURL by
// design), and a hand-written config.yaml can produce the pair without going
// through `model add`, which refuses an empty URL.
func injectionEndpoint(model config.ModelConfig) (base, apiType string) {
	base = strings.TrimSpace(model.BaseURL)
	apiType = model.NormalizedAPIType()
	name := effectiveModelName(model)
	p, ok := providers.Lookup(model.Provider)
	if !ok {
		return base, apiType
	}
	routedBase, routedType := p.EndpointFor(name)
	if base == "" || p.OwnsEndpoint(base) {
		if routedBase != "" {
			base = routedBase
		}
		if routedType != "" {
			apiType = strings.ToLower(strings.TrimSpace(routedType))
		}
	}
	// No endpoint, no injection. A catalogue row with no endpoint of its own
	// (custom) and no base_url names nothing to dial, and the old fallback —
	// api.openai.com, or DeepSeek's Anthropic surface — handed that row's key
	// to a vendor the user never named. Returning "" lets
	// supportsModelInjection refuse the injection instead of substituting a
	// destination nobody chose; every caller below treats empty as "do not
	// inject". A future catalogue row that declares no endpoint inherits the
	// same rule, so the defect cannot return by adding data.
	return base, apiType
}

// agentDialect resolves the wire dialect an agent's CLI expects: a declared
// ModelEnv.APIType pins it (a CLI that only speaks Messages), and a polyglot
// one (empty) takes the model's own.
func agentDialect(name, adapter, modelAPIType string) string {
	k, _ := agents.Lookup(name, adapter)
	if k.ModelEnv != nil && k.ModelEnv.APIType != "" {
		return k.ModelEnv.APIType
	}
	return modelAPIType
}

// injectedDialect is the dialect the injected endpoint actually speaks, as the
// adapter has to be told it. The agent's own constraint wins (a Messages-only
// CLI is handed an Anthropic endpoint and told so), and otherwise the resolved
// surface decides.
func injectedDialect(name, adapter string, model config.ModelConfig) string {
	_, apiType := injectionEndpoint(model)
	return agentDialect(name, adapter, apiType)
}

// effectiveBaseURLFor returns the target-appropriate endpoint URL for an agent.
// DeepSeek supports both Anthropic and OpenAI endpoints.
func effectiveBaseURLFor(name, adapter string, model config.ModelConfig) string {
	base, apiType := injectionEndpoint(model)
	targetAPIType := agentDialect(name, adapter, apiType)
	if isDeepSeekEndpoint(base) {
		if targetAPIType == config.APITypeOpenAI {
			return "https://api.deepseek.com/v1"
		}
		return "https://api.deepseek.com/anthropic"
	}
	return base
}

// effectiveModelNameFor returns the target-appropriate model name for an agent.
func effectiveModelNameFor(name, adapter string, model config.ModelConfig) string {
	base, apiType := injectionEndpoint(model)
	if isDeepSeekEndpoint(base) && agentDialect(name, adapter, apiType) == config.APITypeOpenAI {
		return "deepseek-flash"
	}
	return effectiveModelName(model)
}

// effectiveBaseURL/effectiveModelName mirror the defaults modelEnv applies, so
// the announcement and the injected env never diverge.
func effectiveBaseURL(model config.ModelConfig) string {
	base, _ := injectionEndpoint(model)
	return base
}

func effectiveModelName(model config.ModelConfig) string {
	name := model.Model
	if name == "" {
		if p, ok := providers.Lookup(model.Provider); ok && p.DefaultModel != "" {
			return p.DefaultModel
		}
		if model.NormalizedAPIType() == config.APITypeAnthropic {
			return deepseekFlashModel
		}
		return "gpt-6.1-sol"
	}
	if name == deepseekProModel {
		return deepseekFlashModel
	}
	return name
}

// EffectiveBaseURL returns the endpoint base URL to use for injection.
func EffectiveBaseURL(model config.ModelConfig) string {
	return effectiveBaseURL(model)
}

// homeDir is a test seam over os.UserHomeDir so credential-file probes can be
// pointed at a temp dir.
var homeDir = os.UserHomeDir

// credentialManifest resolves the agent's credential manifest from the
// registry (the single source of truth) by name+adapter. Unknown agents — or
// agents without a declared manifest — fall back to the union of common
// provider keys so a not-yet-registered adapter is still probed
// conservatively; a generic.py custom CLI lands there too rather than
// borrowing whichever registry agent shares the script.
func credentialManifest(name, adapter string) (envVars, files []string) {
	if k, ok := agents.Lookup(name, adapter); ok &&
		(len(k.CredentialEnvVars) > 0 || len(k.CredentialFiles) > 0) {
		return k.CredentialEnvVars, k.CredentialFiles
	}
	return []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}, nil
}

// probeAgentCredentials reports whether the agent named name driven by
// adapter carries model credentials of its own, plus a short description of
// the evidence (safe to surface in announcements/audit — never includes
// secret values).
func probeAgentCredentials(name, adapter string) (found bool, source string) {
	envs, files := credentialManifest(name, adapter)
	for _, key := range envs {
		if os.Getenv(key) != "" {
			return true, "env " + key
		}
	}
	k, _ := agents.Lookup(name, adapter)
	if home, err := homeDir(); err == nil {
		for _, rel := range files {
			p := filepath.Join(home, filepath.FromSlash(rel))
			st, err := os.Stat(p)
			if err != nil || st.Size() == 0 {
				continue
			}
			// A dotenv file of comments and placeholder lines is not
			// credentials; one real KEY=value assignment is.
			if isDotenvFile(rel) {
				if !dotenvFileHasAssignment(p) {
					continue
				}
				return true, "config file ~/" + rel
			}
			// A file with declared field requirements counts only when one
			// of those fields is actually set: Claude Code's state file
			// exists from first run, logged in or not; dsh's credentials
			// file exists with an empty refs block.
			if fields := k.CredentialFileFields[rel]; len(fields) > 0 {
				ok := false
				switch {
				case isYAMLFile(rel):
					ok = yamlFileHasAnyField(p, fields)
				case isTOMLFile(rel):
					ok = tomlFileHasAnyField(p, fields)
				default:
					ok = jsonFileHasAnyField(p, fields)
				}
				if !ok {
					continue
				}
			}
			return true, "config file ~/" + rel
		}
	}
	return false, ""
}

// isDotenvFile reports whether rel names a dotenv-style file
// (.env, .env.local, config.env).
func isDotenvFile(rel string) bool {
	base := filepath.Base(rel)
	return base == ".env" || strings.HasPrefix(base, ".env.") ||
		strings.HasSuffix(base, ".env")
}

// isYAMLFile reports whether rel names a YAML file.
func isYAMLFile(rel string) bool {
	return strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml")
}

// isTOMLFile reports whether rel names a TOML file.
func isTOMLFile(rel string) bool {
	return strings.HasSuffix(rel, ".toml")
}

// dotenvFileHasAssignment reports whether a dotenv file carries at least one
// real KEY=value assignment — comments, blank lines and empty values do not
// count, so a template shipped with placeholders only is not "credentials".
func dotenvFileHasAssignment(path string) bool {
	blob, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		if v := strings.Trim(strings.TrimSpace(val), `"'`); v != "" {
			return true
		}
	}
	return false
}

// yamlFileHasAnyField reports whether a YAML file carries at least one of
// fields (dotted paths, e.g. "refs" or "model.default") as a non-empty
// scalar or a mapping/sequence with entries — the same contract
// jsonFileHasAnyField gives JSON manifests, for YAML-shaped stores like
// dsh's .credentials.yaml.
func yamlFileHasAnyField(path string, fields []string) bool {
	blob, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(blob), "\n")
	for _, f := range fields {
		if yamlFieldNonEmpty(lines, f) {
			return true
		}
	}
	return false
}

// yamlFieldNonEmpty resolves a dotted path by indentation — a cheap scan
// sufficient for harness config shapes, no YAML dependency needed.
func yamlFieldNonEmpty(lines []string, field string) bool {
	parts := strings.Split(field, ".")
	start, parentIndent := 0, -1
	for depth, part := range parts {
		found := false
		for i := start; i < len(lines); i++ {
			ind, key, val, ok := yamlKV(lines[i])
			if !ok {
				continue
			}
			if parentIndent >= 0 && ind <= parentIndent {
				return false // dedented out of the parent block
			}
			if key != part {
				continue
			}
			if depth == len(parts)-1 {
				return yamlValueNonEmpty(lines, i, ind, val)
			}
			start, parentIndent, found = i+1, ind, true
			break
		}
		if !found {
			return false
		}
	}
	return false
}

// yamlKV splits one YAML line into (indent, key, value); comments, blanks
// and sequence items are skipped.
func yamlKV(line string) (indent int, key, val string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") {
		return 0, "", "", false
	}
	k, v, found := strings.Cut(t, ":")
	if !found || strings.TrimSpace(k) == "" {
		return 0, "", "", false
	}
	return len(line) - len(strings.TrimLeft(line, " ")),
		strings.TrimSpace(k), strings.TrimSpace(v), true
}

// yamlValueNonEmpty reports whether the value at line idx is a non-empty
// scalar, or — when the scalar is empty — a nested block follows (any
// deeper-indented content line counts, sequence items included).
func yamlValueNonEmpty(lines []string, idx, ind int, val string) bool {
	if val != "" {
		switch strings.Trim(val, `"'`) {
		case "", "{}", "[]", "null", "~":
			return false
		}
		return true
	}
	for i := idx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return len(lines[i])-len(strings.TrimLeft(lines[i], " ")) > ind
	}
	return false
}

// jsonFileHasAnyField reports whether the JSON object in path carries at
// least one of fields, non-empty. A field may be dotted ("env.ANTHROPIC_AUTH_TOKEN")
// to descend one level into a nested object. Any read/parse failure counts
// as "no" — the probe then falls through to the other evidence, never errors.
func jsonFileHasAnyField(path string, fields []string) bool {
	blob, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(blob, &obj); err != nil {
		return false
	}
	for _, f := range fields {
		if jsonFieldNonEmpty(obj, f) {
			return true
		}
	}
	return false
}

// jsonFieldNonEmpty resolves a possibly dotted field path against obj and
// reports whether it lands on a non-empty value: a non-empty string, or a
// non-null non-empty object (e.g. oauthAccount). Missing, null, "", and {}
// do not count.
func jsonFieldNonEmpty(obj map[string]json.RawMessage, path string) bool {
	parts := strings.Split(path, ".")
	cur, ok := obj[parts[0]]
	if !ok {
		return false
	}
	for _, p := range parts[1:] {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(cur, &nested); err != nil {
			return false
		}
		cur, ok = nested[p]
		if !ok {
			return false
		}
	}
	switch string(cur) {
	case "null", `""`, "{}":
		return false
	}
	var s string
	if err := json.Unmarshal(cur, &s); err == nil {
		return s != ""
	}
	return true // non-string, non-empty value (object/array)
}

// tomlFileHasAnyField reports whether a TOML file sets at least one of fields
// to a non-empty value. Fields are matched by leaf name, not by table path:
// grok keeps its provider key in a [model.*] table and its endpoint in
// [endpoints], and a state file that carries only [marketplace] or [cli]
// sections is not a login. Values are compared as written — a `key = ""` or
// `key = "   "` assignment does not count, and neither does a bare
// `[section]` header or a commented-out line.
func tomlFileHasAnyField(path string, fields []string) bool {
	blob, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		leaf := f
		if i := strings.LastIndexByte(f, '.'); i >= 0 {
			leaf = f[i+1:]
		}
		want[leaf] = true
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !want[key] {
			continue
		}
		if v := strings.Trim(strings.TrimSpace(val), `"'`); v != "" {
			return true
		}
	}
	return false
}

// InjectedModelEndpoint returns the model name and endpoint this node would
// inject into the named agent, resolved through the per-agent pin
// (injection.agents) and the adapter's dialect. The failure-time rescue needs
// it: the policy decision said "don't inject", so it carries no endpoint of
// its own, and falling back to the node model's URL would announce an
// endpoint the retry never used whenever that agent pins its own.
func (r *Router) InjectedModelEndpoint(name, adapter string) (string, string) {
	m := r.injectionModelFor(name, adapter)
	return effectiveModelNameFor(name, adapter, m), effectiveBaseURLFor(name, adapter, m)
}

// InjectionNotice renders the one-line announcement prepended to a task's
// output whenever a model injection happens, so the user always sees what was
// injected and why (no secrets included).
//
// Deliberately one short line, one language: it is a footnote on someone
// else's output. The endpoint drops its scheme (the full URL is in the audit
// log and the model_injection event) and the reason collapses to the config
// value that caused it — the old form duplicated the whole sentence in
// English and Chinese, which buried the agent's actual answer.
func InjectionNotice(d InjectionDecision, agent string) string {
	var b strings.Builder
	b.WriteString("[panda] injected model")
	if agent != "" {
		b.WriteString(" for ")
		b.WriteString(agent)
	}
	if d.Model != "" {
		b.WriteString(": ")
		b.WriteString(d.Model)
	}
	if host := noticeEndpoint(d.BaseURL); host != "" {
		b.WriteString(" @ ")
		b.WriteString(host)
	}
	if d.Kind != "" {
		b.WriteString(" (")
		b.WriteString(string(d.Kind))
		b.WriteString(")")
	}
	return b.String()
}

// noticeEndpoint trims a base URL to host+path for the notice line.
func noticeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return u.Host + strings.TrimRight(u.Path, "/")
}
