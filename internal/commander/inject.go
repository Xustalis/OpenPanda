package commander

import (
	"os"
	"path/filepath"
	"strings"

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
//     own (env vars, login state / config files) AND panda has a model
//     configured; otherwise the agent's native model wins.
func (r *Router) InjectionDecision(adapter string) InjectionDecision {
	switch r.injectionModel {
	case config.InjectionModelNever:
		return InjectionDecision{Inject: false, Reason: "injection.model=never"}
	case config.InjectionModelAlways:
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
	if r.model.APIKey == "" && r.model.BaseURL == "" {
		return InjectionDecision{
			Inject: false,
			Reason: "agent has no own credentials but panda has no model configured",
		}
	}
	return InjectionDecision{
		Inject:  true,
		Reason:  "agent has no own model credentials and panda has a model configured",
		Model:   effectiveModelName(r.model),
		BaseURL: effectiveBaseURL(r.model),
	}
}

// effectiveBaseURL/effectiveModelName mirror the defaults modelEnv applies, so
// the announcement and the injected env never diverge.
func effectiveBaseURL(model config.ModelConfig) string {
	if model.BaseURL == "" {
		return "https://api.deepseek.com/anthropic"
	}
	return model.BaseURL
}

func effectiveModelName(model config.ModelConfig) string {
	if model.Model == "" {
		return "deepseek-chat"
	}
	return model.Model
}

// homeDir is a test seam over os.UserHomeDir so credential-file probes can be
// pointed at a temp dir.
var homeDir = os.UserHomeDir

// agentCredentialEnvVars lists the environment variables that prove an agent
// has model credentials of its own, per adapter script. Unknown adapters fall
// back to the union of common provider keys.
var agentCredentialEnvVars = map[string][]string{
	"claude_code.py": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
	"codex.py":       {"OPENAI_API_KEY"},
	"opencode.py":    {"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
}

// agentCredentialFiles lists home-relative files whose presence (non-empty)
// proves the agent is logged in / configured with its own provider — e.g.
// codex stores its auth and provider sections in ~/.codex.
var agentCredentialFiles = map[string][]string{
	"claude_code.py": {".claude.json", ".claude/.credentials.json"},
	"codex.py":       {".codex/auth.json", ".codex/config.toml"},
	"opencode.py":    {".config/opencode/opencode.json", ".config/opencode/auth.json"},
}

// probeAgentCredentials reports whether the agent driven by adapter carries
// model credentials of its own, plus a short description of the evidence
// (safe to surface in announcements/audit — never includes secret values).
func probeAgentCredentials(adapter string) (found bool, source string) {
	envs := agentCredentialEnvVars[adapter]
	if envs == nil {
		envs = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}
	}
	for _, key := range envs {
		if os.Getenv(key) != "" {
			return true, "env " + key
		}
	}
	files := agentCredentialFiles[adapter]
	if home, err := homeDir(); err == nil {
		for _, rel := range files {
			p := filepath.Join(home, filepath.FromSlash(rel))
			if st, err := os.Stat(p); err == nil && st.Size() > 0 {
				return true, "config file ~/" + rel
			}
		}
	}
	return false, ""
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
