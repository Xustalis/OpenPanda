package commander

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
)

// InjectionDecision is the outcome of the model-injection policy check for
// one agent adapter: whether panda overrides the agent's model endpoint via
// env, and a secret-free human-readable reason for announcements/audit.
type InjectionDecision struct {
	Inject  bool
	Reason  string
	Model   string // model name injected (empty when Inject is false)
	BaseURL string // endpoint injected (empty when Inject is false)
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
func (r *Router) InjectionDecision(adapter string) InjectionDecision {
	switch r.injectionModel {
	case config.InjectionModelNever:
		return InjectionDecision{Inject: false, Reason: "injection.model=never"}
	case config.InjectionModelAlways:
		if !supportsModelInjection(adapter, r.model) {
			return InjectionDecision{Inject: false, Reason: "model injection is not safely supported for " + adapter}
		}
		return InjectionDecision{
			Inject:  true,
			Reason:  "injection.model=always",
			Model:   effectiveModelName(r.model),
			BaseURL: effectiveBaseURL(r.model),
		}
	}
	// auto: agent-native credentials win.
	if own, source := probeAgentCredentials(adapter); own {
		return InjectionDecision{
			Inject: false,
			Reason: "agent carries its own model credentials (" + source + ")",
		}
	}
	if r.model.APIKey == "" {
		return InjectionDecision{
			Inject: false,
			Reason: "agent has no own credentials but panda has no model key configured",
		}
	}
	if !supportsModelInjection(adapter, r.model) {
		return InjectionDecision{
			Inject: false,
			Reason: "model injection is not safely supported for " + adapter,
		}
	}
	return InjectionDecision{
		Inject:  true,
		Reason:  "agent has no own model credentials and panda has a model configured",
		Model:   effectiveModelName(r.model),
		BaseURL: effectiveBaseURL(r.model),
	}
}

// supportsModelInjection is registry-driven: an agent is injectable when its
// registry entry declares a model-env mapping (an unambiguous env contract —
// currently only Claude Code's Anthropic variables) and panda's model speaks
// the matching wire protocol against a DeepSeek endpoint, the one provider
// panda vends credentials for. Codex and OpenCode bring their own keys and
// declare no mapping, so they are never injected.
func supportsModelInjection(adapter string, model config.ModelConfig) bool {
	k, ok := agents.ByAdapter(adapter)
	if !ok || k.ModelEnv == nil {
		return false
	}
	if model.NormalizedAPIType() != config.APITypeAnthropic {
		return false
	}
	return isDeepSeekEndpoint(model.BaseURL)
}

// deepSeekAPIHost is the DeepSeek API host panda injects credentials for.
const deepSeekAPIHost = "api.deepseek.com"

// isDeepSeekEndpoint reports whether base_url points at DeepSeek's API. An
// empty base_url means the default endpoint (see effectiveBaseURL), which is
// DeepSeek's anthropic API.
func isDeepSeekEndpoint(baseURL string) bool {
	if baseURL == "" {
		return true
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
	deepseekFlashModel = "deepseek-v4-flash"
)

// effectiveBaseURL/effectiveModelName mirror the defaults modelEnv applies, so
// the announcement and the injected env never diverge.
func effectiveBaseURL(model config.ModelConfig) string {
	if model.BaseURL == "" {
		return "https://api.deepseek.com/anthropic"
	}
	return model.BaseURL
}

func effectiveModelName(model config.ModelConfig) string {
	name := model.Model
	if name == "" {
		return "deepseek-chat"
	}
	if name == deepseekProModel {
		return deepseekFlashModel
	}
	return name
}

// homeDir is a test seam over os.UserHomeDir so credential-file probes can be
// pointed at a temp dir.
var homeDir = os.UserHomeDir

// credentialManifest resolves the agent's credential manifest from the
// registry (the single source of truth). Unknown adapters — or agents without
// a declared manifest — fall back to the union of common provider keys so a
// not-yet-registered adapter is still probed conservatively.
func credentialManifest(adapter string) (envVars, files []string) {
	if k, ok := agents.ByAdapter(adapter); ok &&
		(len(k.CredentialEnvVars) > 0 || len(k.CredentialFiles) > 0) {
		return k.CredentialEnvVars, k.CredentialFiles
	}
	return []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}, nil
}

// probeAgentCredentials reports whether the agent driven by adapter carries
// model credentials of its own, plus a short description of the evidence
// (safe to surface in announcements/audit — never includes secret values).
func probeAgentCredentials(adapter string) (found bool, source string) {
	envs, files := credentialManifest(adapter)
	for _, key := range envs {
		if os.Getenv(key) != "" {
			return true, "env " + key
		}
	}
	k, _ := agents.ByAdapter(adapter)
	if home, err := homeDir(); err == nil {
		for _, rel := range files {
			p := filepath.Join(home, filepath.FromSlash(rel))
			st, err := os.Stat(p)
			if err != nil || st.Size() == 0 {
				continue
			}
			// A file with declared field requirements counts only when one
			// of those JSON fields is actually set: Claude Code's state file
			// exists from first run, logged in or not.
			if fields := k.CredentialFileFields[rel]; len(fields) > 0 {
				if !jsonFileHasAnyField(p, fields) {
					continue
				}
			}
			return true, "config file ~/" + rel
		}
	}
	return false, ""
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

// InjectionNotice renders the explicit one-line announcement prepended to
// the task output whenever a model injection happens, so the user always
// sees what was injected and why (no secrets included).
func InjectionNotice(d InjectionDecision, agent string) string {
	var b strings.Builder
	b.WriteString("[panda] 注意：本任务已向 agent「" + agent + "」注入 panda 配置的模型端点")
	if d.Model != "" {
		b.WriteString("（model=" + d.Model)
		if d.BaseURL != "" {
			b.WriteString("，endpoint=" + d.BaseURL)
		}
		b.WriteString("）")
	}
	if d.Reason != "" {
		b.WriteString("，原因：" + d.Reason)
	}
	return b.String()
}
