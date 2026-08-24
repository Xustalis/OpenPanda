package commander

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// cleanCredentialEnv isolates the credential probes from the developer's
// real environment: provider env vars are emptied and homeDir is pointed at
// an empty temp dir so no real ~/.codex or ~/.claude interferes.
func cleanCredentialEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })
	return home
}

func injectionRouter(model config.ModelConfig, mode string) *Router {
	return NewRouter(testCard(), NewExecutor(), model,
		config.InjectionConfig{Model: mode}, config.RoutingConfig{})
}

var testModel = config.ModelConfig{
	BaseURL: "https://api.deepseek.com/anthropic",
	APIKey:  "sk-test",
	Model:   "deepseek-chat",
}

func TestInjectionNever(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(testModel, config.InjectionModelNever)
	d := r.InjectionDecision("claude_code.py")
	if d.Inject {
		t.Fatalf("never mode must not inject, got %+v", d)
	}
}

func TestInjectionAlways(t *testing.T) {
	cleanCredentialEnv(t)
	// always injects even when the agent has its own credentials.
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(testModel, config.InjectionModelAlways)
	d := r.InjectionDecision("claude_code.py")
	if !d.Inject {
		t.Fatalf("always mode must inject, got %+v", d)
	}
	if d.Model != "deepseek-chat" || d.BaseURL == "" {
		t.Fatalf("decision should carry the injected model/endpoint, got %+v", d)
	}
}

// TestInjectionAutoSkipsAgentWithEnvCreds verifies the default strategy: an
// agent that carries its own provider key in the environment is left alone.
func TestInjectionAutoSkipsAgentWithEnvCreds(t *testing.T) {
	cleanCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(testModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("auto must not inject when agent has env creds: %+v", d)
	}
	// An unrelated provider key does not count for a codex agent's probe...
	// OPENAI_API_KEY is codex's own key, so set the anthropic-only case:
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN counts as claude creds: %+v", d)
	}
}

// TestInjectionAutoSkipsAgentWithConfigFile verifies login-state detection:
// a non-empty ~/.codex/auth.json (or config.toml with provider sections)
// proves codex has its own model.
func TestInjectionAutoSkipsAgentWithConfigFile(t *testing.T) {
	home := cleanCredentialEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	r := injectionRouter(testModel, config.InjectionModelAuto)
	d := r.InjectionDecision("codex.py")
	if d.Inject {
		t.Fatalf("auto must not inject when codex has a login file: %+v", d)
	}
	if !strings.Contains(d.Reason, ".codex/auth.json") {
		t.Fatalf("reason should name the evidence, got %q", d.Reason)
	}
}

// TestInjectionAutoInjectsWhenNoCredentials verifies the fallback half of
// auto: no agent credentials + panda model configured → inject.
func TestInjectionAutoInjectsWhenNoCredentials(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(testModel, config.InjectionModelAuto)
	d := r.InjectionDecision("claude_code.py")
	if !d.Inject {
		t.Fatalf("auto must inject when agent has no creds and panda has a model: %+v", d)
	}
	if d.Model != "deepseek-chat" {
		t.Fatalf("decision model = %q, want deepseek-chat", d.Model)
	}
}

// TestInjectionAutoNoModelConfigured: nothing to inject when panda itself
// has no model endpoint configured.
func TestInjectionAutoNoModelConfigured(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(config.ModelConfig{}, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("auto must not inject without a configured model: %+v", d)
	}
}

func TestInjectionCodexDoesNotPretendAnthropicMapping(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(testModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("codex.py"); d.Inject {
		t.Fatalf("codex must not receive an unverified Anthropic injection: %+v", d)
	}
}

// TestInjectionDecisionDefaultMode verifies the zero-value policy (old
// configs / zero InjectionConfig) normalizes to auto.
func TestInjectionDecisionDefaultMode(t *testing.T) {
	cleanCredentialEnv(t)
	r := NewRouter(testCard(), NewExecutor(), testModel, config.InjectionConfig{}, config.RoutingConfig{})
	if r.injectionModel != config.InjectionModelAuto {
		t.Fatalf("zero injection config = %q, want auto", r.injectionModel)
	}
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("default mode must behave as auto: %+v", d)
	}
}

// TestInjectionNoticeNoSecrets: the announcement names the model/endpoint
// and reason but never the API key.
func TestInjectionNoticeNoSecrets(t *testing.T) {
	d := InjectionDecision{Inject: true, Reason: "injection.model=always", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/anthropic"}
	note := InjectionNotice(d, "claude_code")
	if !strings.Contains(note, "deepseek-chat") || !strings.Contains(note, "claude_code") {
		t.Fatalf("notice should name model and agent: %q", note)
	}
	if strings.Contains(note, "sk-") {
		t.Fatalf("notice must never contain secrets: %q", note)
	}
}
