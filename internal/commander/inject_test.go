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

// --- Claude Code DeepSeek flash injection (registry-driven credential
// manifest + cost control): the three decision scenarios and the hard
// deepseek-v4-pro ban. ---

// flashModel mirrors the node's DeepSeek config (config.yaml):
// model.base_url=https://api.deepseek.com/anthropic, model.api_key non-empty,
// model.model=deepseek-v4-flash.
var flashModel = config.ModelConfig{
	BaseURL: "https://api.deepseek.com/anthropic",
	APIKey:  "sk-flash-test",
	Model:   "deepseek-v4-flash",
}

// assertEnv checks that every wanted key=value is present in the env entries.
func assertEnv(t *testing.T, env []string, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("env %s = %q, want %q (all=%v)", k, got[k], v, env)
		}
	}
}

// TestDeepSeekFlashInjection: no agent credentials + DeepSeek endpoint →
// inject, with Claude Code's effective model env var (ANTHROPIC_MODEL, the
// one claude_code.py maps to --model) pointed at flash.
func TestDeepSeekFlashInjection(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	d := r.InjectionDecision("claude_code.py")
	if !d.Inject {
		t.Fatalf("DeepSeek config + no agent creds must inject: %+v", d)
	}
	if d.Model != "deepseek-v4-flash" {
		t.Fatalf("decision model = %q, want deepseek-v4-flash", d.Model)
	}
	env := modelEnvForAdapter(flashModel, "claude_code.py")
	assertEnv(t, env, map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
		"ANTHROPIC_API_KEY":  "sk-flash-test",
		"ANTHROPIC_MODEL":    "deepseek-v4-flash",
	})
	for _, kv := range env {
		if strings.Contains(kv, "deepseek-v4-pro") {
			t.Fatalf("pro model must never appear in the injected env: %q", kv)
		}
	}
}

// TestDeepSeekInjectionSkippedWhenAgentHasKey: an environment that already
// carries ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN) is never injected.
func TestDeepSeekInjectionSkippedWhenAgentHasKey(t *testing.T) {
	cleanCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("agent with ANTHROPIC_API_KEY must never be injected: %+v", d)
	}
}

// TestDeepSeekInjectionSkippedForNonDeepSeekEndpoint: a base_url that does
// not point at api.deepseek.com is not injected.
func TestDeepSeekInjectionSkippedForNonDeepSeekEndpoint(t *testing.T) {
	cleanCredentialEnv(t)
	other := flashModel
	other.BaseURL = "https://api.anthropic.com"
	r := injectionRouter(other, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("non-DeepSeek base_url must not be injected: %+v", d)
	}
	if env := modelEnvForAdapter(other, "claude_code.py"); len(env) != 0 {
		t.Fatalf("non-DeepSeek base_url must produce no env override: %v", env)
	}
}

// TestDeepSeekProModelNeverInjected: the hard cost constraint — no path ever
// injects deepseek-v4-pro; the flash model is substituted instead.
func TestDeepSeekProModelNeverInjected(t *testing.T) {
	cleanCredentialEnv(t)
	pro := flashModel
	pro.Model = "deepseek-v4-pro"
	r := injectionRouter(pro, config.InjectionModelAuto)
	d := r.InjectionDecision("claude_code.py")
	if !d.Inject {
		t.Fatalf("injection should still happen, just never with pro: %+v", d)
	}
	if strings.Contains(d.Model, "deepseek-v4-pro") {
		t.Fatalf("pro model leaked into the decision: %q", d.Model)
	}
	env := modelEnvForAdapter(pro, "claude_code.py")
	assertEnv(t, env, map[string]string{"ANTHROPIC_MODEL": "deepseek-v4-flash"})
	for _, kv := range env {
		if strings.Contains(kv, "deepseek-v4-pro") {
			t.Fatalf("pro model leaked into the injected env: %q", kv)
		}
	}
	// The legacy always path is guarded too.
	rAlways := injectionRouter(pro, config.InjectionModelAlways)
	da := rAlways.InjectionDecision("claude_code.py")
	if !da.Inject || strings.Contains(da.Model, "deepseek-v4-pro") {
		t.Fatalf("always mode must inject flash, never pro: %+v", da)
	}
}

// TestDeepSeekInjectionOnlyForClaudeCode: agents that bring their own keys
// (no registry model-env mapping) never receive a DeepSeek injection.
func TestDeepSeekInjectionOnlyForClaudeCode(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	for _, adapter := range []string{
		"codex.py", "opencode.py", "grok_build.py",
		"deepseek_harness.py", "openclaw.py", "hermes.py",
	} {
		if d := r.InjectionDecision(adapter); d.Inject {
			t.Fatalf("%s must not be DeepSeek-injected (no registry model-env mapping): %+v", adapter, d)
		}
	}
}

// --- Claude Code state-file vs credential distinction ---
// ~/.claude.json exists from the CLI's very first run (onboarding state,
// machineID, project history) long before any login; only the oauthAccount /
// primaryApiKey fields mean real credentials. A state-only file must NOT
// block injection — that is exactly the "Claude Code installed but no key"
// deployment the DeepSeek flash injection exists for.

// TestClaudeStateFileDoesNotBlockInjection: a ~/.claude.json with only
// onboarding state leaves the agent credential-free → injection proceeds.
func TestClaudeStateFileDoesNotBlockInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	state := `{"hasCompletedOnboarding":true,"numStartups":3,"machineID":"m-1","projects":{}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	d := r.InjectionDecision("claude_code.py")
	if !d.Inject {
		t.Fatalf("state-only ~/.claude.json must not block injection: %+v", d)
	}
	if d.Model != "deepseek-v4-flash" {
		t.Fatalf("injected model = %q, want deepseek-v4-flash", d.Model)
	}
}

// TestClaudeApiKeyLoginBlocksInjection: a ~/.claude.json carrying
// primaryApiKey (API-key login) counts as the agent's own credentials.
func TestClaudeApiKeyLoginBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	loggedIn := `{"hasCompletedOnboarding":true,"primaryApiKey":"sk-ant-own"}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(loggedIn), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("primaryApiKey login must keep the agent's own model: %+v", d)
	}
}

// TestClaudeOAuthLoginBlocksInjection: oauthAccount (subscription login) in
// ~/.claude.json also counts; a null oauthAccount does not.
func TestClaudeOAuthLoginBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	loggedIn := `{"oauthAccount":{"emailAddress":"a@b.c"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(loggedIn), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("oauthAccount login must keep the agent's own model: %+v", d)
	}
	// A null oauthAccount with other state present must inject.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"numStartups":1,"oauthAccount":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := r.InjectionDecision("claude_code.py"); !d.Inject {
		t.Fatalf("null oauthAccount is state, not a credential: %+v", d)
	}
}

// TestClaudeCredentialsFileBlocksInjection: ~/.claude/.credentials.json
// (subscription OAuth tokens, written only after authentication) blocks
// injection by its presence alone — no field requirements declared for it.
func TestClaudeCredentialsFileBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("credentials.json must keep the agent's own model: %+v", d)
	}
}

// TestCodexAuthStillBlocksInjection: the field refinement is scoped to files
// that declare it — codex's ~/.codex/auth.json (no declared fields) still
// blocks by presence, as before.
func TestCodexAuthStillBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("codex.py"); d.Inject {
		t.Fatalf("codex auth.json presence must still count: %+v", d)
	}
}

// TestClaudeConfigJsonBlocksInjection: Claude Code 2.1.x stores its API key
// in ~/.claude/config.json (primaryApiKey) — a logged-in CLI keeps its own
// model and is never injected.
func TestClaudeConfigJsonBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "config.json"),
		[]byte(`{"primaryApiKey":"sk-ant-own"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	d := r.InjectionDecision("claude_code.py")
	if d.Inject {
		t.Fatalf("~/.claude/config.json login must keep the agent's own model: %+v", d)
	}
	if !strings.Contains(d.Reason, ".claude/config.json") {
		t.Fatalf("reason should name the evidence file, got %q", d.Reason)
	}
}

// TestClaudeSettingsEnvBlocksInjection: ~/.claude/settings.json whose env
// block carries ANTHROPIC_AUTH_TOKEN (the user-managed endpoint config)
// proves own credentials; an env block without auth vars does not.
func TestClaudeSettingsEnvBlocksInjection(t *testing.T) {
	home := cleanCredentialEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	withAuth := `{"env":{"ANTHROPIC_AUTH_TOKEN":"tok","ANTHROPIC_BASE_URL":"https://gw"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(withAuth), 0o600); err != nil {
		t.Fatal(err)
	}
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code.py"); d.Inject {
		t.Fatalf("settings.json env auth must keep the agent's own model: %+v", d)
	}
	// env present but without auth-bearing vars → not a credential.
	withoutAuth := `{"env":{"SOME_OTHER_VAR":"1"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(withoutAuth), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := r.InjectionDecision("claude_code.py"); !d.Inject {
		t.Fatalf("settings.json without auth env must not block injection: %+v", d)
	}
}
