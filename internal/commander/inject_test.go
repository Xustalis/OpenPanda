// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"os"
	"os/exec"
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
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY",
		// Endpoint/model overrides feed the probe target too — a developer
		// shell that exports them would otherwise leak into the assertions.
		"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "GROK_BASE_URL", "DEEPSEEK_BASE_URL",
		"ANTHROPIC_MODEL", "OPENAI_MODEL", "GROK_MODEL", "DEEPSEEK_MODEL",
		"XAI_API_KEY", "GROK_API_KEY", "DEEPSEEK_API_KEY",
		"OPENCLAW_API_KEY", "HERMES_API_KEY", "OPENCODE_MODEL",
		"OPENCODE_API_KEY", "OPENPANDA_MODEL_BASE_URL", "OPENPANDA_MODEL_API_KEY",
		"OPENPANDA_MODEL_NAME", "OPENPANDA_MODEL_API_TYPE",
	} {
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
	Model:   "deepseek-v4-flash",
}

func TestInjectionNever(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(testModel, config.InjectionModelNever)
	d := r.InjectionDecision("claude_code", "claude_code.py")
	if d.Inject {
		t.Fatalf("never mode must not inject, got %+v", d)
	}
}

func TestInjectionAlways(t *testing.T) {
	cleanCredentialEnv(t)
	// always injects even when the agent has its own credentials.
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(testModel, config.InjectionModelAlways)
	d := r.InjectionDecision("claude_code", "claude_code.py")
	if !d.Inject {
		t.Fatalf("always mode must inject, got %+v", d)
	}
	if d.Model != "deepseek-v4-flash" || d.BaseURL == "" {
		t.Fatalf("decision should carry the injected model/endpoint, got %+v", d)
	}
}

// TestInjectionAutoSkipsAgentWithEnvCreds verifies the default strategy: an
// agent that carries its own provider key in the environment is left alone.
func TestInjectionAutoSkipsAgentWithEnvCreds(t *testing.T) {
	cleanCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(testModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("auto must not inject when agent has env creds: %+v", d)
	}
	// An unrelated provider key does not count for a codex agent's probe...
	// OPENAI_API_KEY is codex's own key, so set the anthropic-only case:
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
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
	d := r.InjectionDecision("codex", "codex.py")
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
	d := r.InjectionDecision("claude_code", "claude_code.py")
	if !d.Inject {
		t.Fatalf("auto must inject when agent has no creds and panda has a model: %+v", d)
	}
	if d.Model != "deepseek-v4-flash" {
		t.Fatalf("decision model = %q, want deepseek-v4-flash", d.Model)
	}
}

// TestInjectionAutoNoModelConfigured: nothing to inject when panda itself
// has no model endpoint configured.
func TestInjectionAutoNoModelConfigured(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(config.ModelConfig{}, config.InjectionModelAuto)
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("auto must not inject without a configured model: %+v", d)
	}
}

func TestInjectionCodexDoesNotPretendAnthropicMapping(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(testModel, config.InjectionModelAuto)
	if d := r.InjectionDecision("codex", "codex.py"); d.Inject {
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("default mode must behave as auto: %+v", d)
	}
}

// TestInjectionNoticeNoSecrets: the announcement names the model/endpoint
// and strategy but never the API key, and stays one short line — it is a
// footnote on the agent's own output, not a report.
func TestInjectionNoticeNoSecrets(t *testing.T) {
	d := InjectionDecision{Inject: true, Kind: InjectionKindAlways, Reason: "injection.model=always", Model: "deepseek-v4-flash", BaseURL: "https://api.deepseek.com/anthropic"}
	note := InjectionNotice(d, "claude_code")
	if !strings.Contains(note, "deepseek-v4-flash") || !strings.Contains(note, "claude_code") {
		t.Fatalf("notice should name model and agent: %q", note)
	}
	if strings.Contains(note, "sk-") {
		t.Fatalf("notice must never contain secrets: %q", note)
	}
	if strings.Contains(note, "\n") {
		t.Fatalf("notice must be a single line: %q", note)
	}
	if len(note) > 140 {
		t.Fatalf("notice must stay short (%d chars): %q", len(note), note)
	}
	if !strings.Contains(note, "(always)") || !strings.Contains(note, "api.deepseek.com/anthropic") {
		t.Fatalf("notice should carry the strategy and endpoint host: %q", note)
	}
	// A rescue reads differently from a policy injection: the reader has to
	// know the agent's own model failed before panda's ran.
	rescue := InjectionNotice(InjectionDecision{Inject: true, Kind: InjectionKindRescue, Model: "glm-5.3-flash", BaseURL: "https://open.bigmodel.cn/api/paas/v4"}, "opencode")
	if !strings.Contains(rescue, "(rescue)") || strings.Contains(rescue, "https://") {
		t.Fatalf("rescue notice should say rescue and drop the scheme: %q", rescue)
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
	d := r.InjectionDecision("claude_code", "claude_code.py")
	if !d.Inject {
		t.Fatalf("DeepSeek config + no agent creds must inject: %+v", d)
	}
	if d.Model != "deepseek-v4-flash" {
		t.Fatalf("decision model = %q, want deepseek-v4-flash", d.Model)
	}
	env := modelEnvForAdapter(flashModel, "claude_code", "claude_code.py")
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("non-DeepSeek base_url must not be injected: %+v", d)
	}
	if env := modelEnvForAdapter(other, "claude_code", "claude_code.py"); len(env) != 0 {
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
	d := r.InjectionDecision("claude_code", "claude_code.py")
	if !d.Inject {
		t.Fatalf("injection should still happen, just never with pro: %+v", d)
	}
	if strings.Contains(d.Model, "deepseek-v4-pro") {
		t.Fatalf("pro model leaked into the decision: %q", d.Model)
	}
	env := modelEnvForAdapter(pro, "claude_code", "claude_code.py")
	assertEnv(t, env, map[string]string{"ANTHROPIC_MODEL": "deepseek-flash"})
	for _, kv := range env {
		if strings.Contains(kv, "deepseek-v4-pro") {
			t.Fatalf("pro model leaked into the injected env: %q", kv)
		}
	}
	// The legacy always path is guarded too.
	rAlways := injectionRouter(pro, config.InjectionModelAlways)
	da := rAlways.InjectionDecision("claude_code", "claude_code.py")
	if !da.Inject || strings.Contains(da.Model, "deepseek-v4-pro") {
		t.Fatalf("always mode must inject flash, never pro: %+v", da)
	}
}

// TestDeepSeekInjectionSkipsOtherDialects: an Anthropic-dialect DeepSeek
// model reaches only the agents whose registry mapping speaks Anthropic.
// Codex/hermes declare OpenAI, opencode/grok/openclaw carry no mapping — all
// of them are refused rather than handed an endpoint their wire format
// cannot address. deepseek_harness used to sit in this list because it had no
// mapping at all; it now declares Anthropic and is covered by
// TestDeepSeekHarnessInjection.
func TestDeepSeekInjectionSkipsOtherDialects(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	for _, adapter := range []string{
		"codex.py", "opencode.py", "grok_build.py",
		"openclaw.py", "hermes.py",
	} {
		if d := r.InjectionDecision(strings.TrimSuffix(adapter, ".py"), adapter); d.Inject {
			t.Fatalf("%s must not be DeepSeek-injected (dialect/mapping mismatch): %+v", adapter, d)
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
	d := r.InjectionDecision("claude_code", "claude_code.py")
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("oauthAccount login must keep the agent's own model: %+v", d)
	}
	// A null oauthAccount with other state present must inject.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"numStartups":1,"oauthAccount":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := r.InjectionDecision("claude_code", "claude_code.py"); !d.Inject {
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
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
	if d := r.InjectionDecision("codex", "codex.py"); d.Inject {
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
	d := r.InjectionDecision("claude_code", "claude_code.py")
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
	if d := r.InjectionDecision("claude_code", "claude_code.py"); d.Inject {
		t.Fatalf("settings.json env auth must keep the agent's own model: %+v", d)
	}
	// env present but without auth-bearing vars → not a credential.
	withoutAuth := `{"env":{"SOME_OTHER_VAR":"1"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(withoutAuth), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := r.InjectionDecision("claude_code", "claude_code.py"); !d.Inject {
		t.Fatalf("settings.json without auth env must not block injection: %+v", d)
	}
}

// TestAgentViableLockedOutCLI reproduces the three-device lab failure: a
// claude CLI installed on PATH but with no login state on a node that has no
// model key configured. The old PATH-only probe advertised it, routing sent
// work there, and the task died after a long runtime hang. Viability must
// fail closed in exactly this shape.
func TestAgentViableLockedOutCLI(t *testing.T) {
	cleanCredentialEnv(t)
	// claude must resolve on PATH for the probe to reach the credential
	// stage; skip when the host has no claude binary.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not installed on this host")
	}
	card := testCard()
	ag := card.Agents["claude_code"]
	if ag.Adapter == "" {
		t.Fatal("test card has no claude_code agent")
	}
	// No model configured, no agent credentials → not viable.
	r := injectionRouter(config.ModelConfig{}, config.InjectionModelAuto)
	if r.AgentViable("claude_code", ag) {
		t.Fatal("installed-but-locked-out agent must not be viable")
	}
	// With a model key configured, injection makes it viable again.
	r2 := injectionRouter(testModel, config.InjectionModelAuto)
	if !r2.AgentViable("claude_code", ag) {
		t.Fatal("agent with injectable model must be viable")
	}
	// And with its own credentials, viable regardless of model config.
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r3 := injectionRouter(config.ModelConfig{}, config.InjectionModelAuto)
	if !r3.AgentViable("claude_code", ag) {
		t.Fatal("agent with own credentials must be viable")
	}
}

func TestOpenAIModelInjectionForCodexAndHermes(t *testing.T) {
	cleanCredentialEnv(t)
	openaiModel := config.ModelConfig{
		APIType: "openai",
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-openai-test",
		Model:   "gpt-4o",
	}

	// Codex with OpenAI model configured in auto mode (no own credentials).
	r := injectionRouter(openaiModel, config.InjectionModelAuto)
	dCodex := r.InjectionDecision("codex", "codex.py")
	if !dCodex.Inject {
		t.Fatalf("Codex should receive OpenAI model injection in auto mode: %+v", dCodex)
	}
	if dCodex.Model != "gpt-4o" || dCodex.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("Codex decision model/url mismatch: %+v", dCodex)
	}
	envCodex := modelEnvForAdapter(openaiModel, "codex", "codex.py")
	assertEnv(t, envCodex, map[string]string{
		"OPENAI_BASE_URL": "https://api.openai.com/v1",
		"OPENAI_API_KEY":  "sk-openai-test",
		"OPENAI_MODEL":    "gpt-4o",
	})

	// Hermes with OpenAI model configured.
	dHermes := r.InjectionDecision("hermes", "hermes.py")
	if !dHermes.Inject {
		t.Fatalf("Hermes should receive OpenAI model injection in auto mode: %+v", dHermes)
	}
	envHermes := modelEnvForAdapter(openaiModel, "hermes", "hermes.py")
	assertEnv(t, envHermes, map[string]string{
		"OPENAI_BASE_URL": "https://api.openai.com/v1",
		"OPENAI_API_KEY":  "sk-openai-test",
		"OPENAI_MODEL":    "gpt-4o",
	})

	// Claude Code should NOT be injected with OpenAI model because it expects Anthropic APIType.
	dClaude := r.InjectionDecision("claude_code", "claude_code.py")
	if dClaude.Inject {
		t.Fatalf("Claude Code must not receive OpenAI model injection: %+v", dClaude)
	}
}

// TestDynamicModelInjectionFallbackOnAuthError verifies Requirement 5:
// When an agent fails with an authentication or quota error (e.g. 403 用户额度不足 / 401 invalid token),
// and the router has a configured model API key, runAdapterDefault dynamically retries with injected
// model credentials rather than discarding the task.
func TestDynamicModelInjectionFallbackOnAuthError(t *testing.T) {
	cleanCredentialEnv(t)
	// Agent declares own key in environment
	t.Setenv("ANTHROPIC_API_KEY", "stale-key-that-will-403")

	r := injectionRouter(flashModel, config.InjectionModelAuto)
	// First call with native env fails with 403, second call with injected key succeeds
	attempts := 0

	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		attempts++
		if attempts == 1 {
			return AgentResult{OK: false, Result: "API Error: 403 用户额度不足", ExitCode: 1}
		}
		// Second attempt should carry the injected key
		found := false
		for _, kv := range env {
			if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=sk-flash-test") {
				found = true
				break
			}
		}
		if !found {
			return AgentResult{OK: false, Result: "injected key missing in retry env", ExitCode: 1}
		}
		return AgentResult{OK: true, Result: "completed via injected key", ExitCode: 0}
	})

	res := r.runAdapterDefault(context.Background(), "claude_code.py", "test prompt", "")
	if !res.OK || attempts != 2 {
		t.Fatalf("expected dynamic injection retry to succeed in 2 attempts: ok=%v attempts=%d res=%+v", res.OK, attempts, res)
	}
}

// TestInjectionNeverBlocksRescue: injection.model=never is documented as
// "agents always use their own models", so the failure-time rescue must not
// quietly hand the task panda's key when the agent's own model errors out.
func TestInjectionNeverBlocksRescue(t *testing.T) {
	cleanCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key-that-403s")
	r := injectionRouter(flashModel, config.InjectionModelNever)
	attempts := 0
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		attempts++
		return AgentResult{OK: false, Result: "API Error: 403 用户额度不足", ExitCode: 1}
	})
	res := r.runAdapterDefault(context.Background(), "claude_code.py", "p", "")
	if attempts != 1 {
		t.Fatalf("never mode must not retry with panda's model, attempts=%d", attempts)
	}
	if res.Injected {
		t.Fatalf("never mode must not report an injection: %+v", res)
	}
	if !strings.Contains(res.Result, "403") {
		t.Fatalf("the agent's own failure must be reported verbatim, got %q", res.Result)
	}
}

// TestInjectedModelEndpointUsesAgentPin: the rescue notice resolves the
// endpoint the retry actually used — the per-agent pin, in that agent's
// dialect — not the node model's URL.
func TestInjectedModelEndpointUsesAgentPin(t *testing.T) {
	node := config.ModelConfig{
		Provider: "zhipu", APIType: "openai",
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey:  "sk-node", Model: "glm-5.3-flash",
	}
	pin := config.ModelConfig{APIType: "anthropic", BaseURL: "https://open.bigmodel.cn/api/anthropic"}
	r := NewRouter(testCard(), NewExecutor(), node,
		config.InjectionConfig{Model: config.InjectionModelAuto,
			Agents: map[string]config.ModelConfig{"deepseek_harness": pin}}, config.RoutingConfig{})

	if model, endpoint := r.InjectedModelEndpoint("deepseek_harness", "deepseek_harness.py"); endpoint != "https://open.bigmodel.cn/api/anthropic" || model != "glm-5.3-flash" {
		t.Fatalf("pinned agent must resolve its own endpoint, got model=%q endpoint=%q", model, endpoint)
	}
	if _, endpoint := r.InjectedModelEndpoint("opencode", "opencode.py"); endpoint != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("unpinned agent must fall back to the node endpoint, got %q", endpoint)
	}
}

// TestModelCredsWithheldSuppressesInjection: a trust-zone withheld context
// must not put this node's model key into the adapter env — the whole point
// is that a sub-mesh task's text runs inside a subprocess it can read.
func TestModelCredsWithheldSuppressesInjection(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	// Sanity: without the marker the same setup WOULD inject.
	if d := r.InjectionDecision("claude_code", "claude_code.py"); !d.Inject {
		t.Fatalf("fixture broken: auto mode should inject here: %+v", d)
	}
	var gotEnv []string
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		gotEnv = env
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	})
	ctx := WithModelCredsWithheld(context.Background())
	res := r.runAdapterDefault(ctx, "claude_code.py", "test prompt", "")
	if !res.OK {
		t.Fatalf("run failed: %+v", res)
	}
	if res.Injected {
		t.Fatalf("withheld run must not report an injection: %+v", res)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.Contains(kv, "sk-flash-test") {
			t.Fatalf("withheld env carried the model key: %q (all=%v)", kv, gotEnv)
		}
	}
}

// TestModelCredsWithheldSuppressesCredentialForward: the adapter credential
// bridge forwards this daemon's own provider env vars into the subprocess —
// under withholding none of them may cross either.
func TestModelCredsWithheldSuppressesCredentialForward(t *testing.T) {
	cleanCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "agent-own-key")
	r := injectionRouter(config.ModelConfig{}, config.InjectionModelAuto)
	var gotEnv []string
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		gotEnv = env
		return AgentResult{OK: true, Result: "done", ExitCode: 0}
	})
	// Control: without the marker the credential is forwarded.
	res := r.runAdapterDefault(context.Background(), "claude_code.py", "p", "")
	if !res.OK {
		t.Fatalf("control run failed: %+v", res)
	}
	fwd := false
	for _, kv := range gotEnv {
		if kv == "ANTHROPIC_API_KEY=agent-own-key" {
			fwd = true
		}
	}
	if !fwd {
		t.Fatalf("fixture broken: credential env should forward by default (all=%v)", gotEnv)
	}
	// Withheld: nothing.
	gotEnv = nil
	res = r.runAdapterDefault(WithModelCredsWithheld(context.Background()), "claude_code.py", "p", "")
	if !res.OK {
		t.Fatalf("withheld run failed: %+v", res)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Fatalf("withheld env forwarded the credential: %q (all=%v)", kv, gotEnv)
		}
	}
}

// TestModelCredsWithheldBlocksAuthFallback: the dynamic injection fallback
// fires precisely on auth failures — under withholding it is the SECOND
// leak channel and must stay closed (attempts stays 1, no retry).
func TestModelCredsWithheldBlocksAuthFallback(t *testing.T) {
	cleanCredentialEnv(t)
	r := injectionRouter(flashModel, config.InjectionModelAuto)
	attempts := 0
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		attempts++
		for _, kv := range env {
			if strings.Contains(kv, "sk-flash-test") {
				t.Fatalf("withheld retry env carried the model key: %q", kv)
			}
		}
		return AgentResult{OK: false, Result: "401 invalid api key", ExitCode: 1}
	})
	res := r.runAdapterDefault(WithModelCredsWithheld(context.Background()), "claude_code.py", "p", "")
	if res.OK {
		t.Fatalf("auth-failed run should stay failed: %+v", res)
	}
	if attempts != 1 {
		t.Fatalf("withholding must not trigger the injection retry, attempts=%d", attempts)
	}
}

// TestDeepSeekHarnessInjection pins dsh's dialect and env contract. The
// harness speaks Anthropic Messages ({base}/v1/messages), so panda injects
// into it only when its own model is Anthropic-dialect, and the endpoint it
// hands over is DeepSeek's /anthropic surface — not an OpenAI-style /v1 base
// that dsh would append /v1/messages to. An OpenAI-dialect model is refused
// instead of being aimed at an Anthropic client.
func TestDeepSeekHarnessInjection(t *testing.T) {
	cleanCredentialEnv(t)

	anthropicModel := config.ModelConfig{
		BaseURL: "https://api.deepseek.com/anthropic",
		APIKey:  "sk-dsh-test",
		Model:   "deepseek-v4-flash",
	}
	r := injectionRouter(anthropicModel, config.InjectionModelAuto)
	d := r.InjectionDecision("deepseek_harness", "deepseek_harness.py")
	if !d.Inject {
		t.Fatalf("dsh with no own credentials must receive the anthropic model: %+v", d)
	}
	if d.BaseURL != "https://api.deepseek.com/anthropic" || d.Model != "deepseek-v4-flash" {
		t.Fatalf("dsh decision model/url mismatch: %+v", d)
	}
	env := modelEnvForAdapter(anthropicModel, "deepseek_harness", "deepseek_harness.py")
	assertEnv(t, env, map[string]string{
		"DEEPSEEK_BASE_URL": "https://api.deepseek.com/anthropic",
		"DEEPSEEK_API_KEY":  "sk-dsh-test",
		"DEEPSEEK_MODEL":    "deepseek-v4-flash",
	})

	openaiModel := config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey:  "sk-glm-test",
		Model:   "glm-5.3-flash",
	}
	r2 := injectionRouter(openaiModel, config.InjectionModelAuto)
	if d2 := r2.InjectionDecision("deepseek_harness", "deepseek_harness.py"); d2.Inject {
		t.Fatalf("dsh must not take an OpenAI-dialect model: %+v", d2)
	}
	if env := modelEnvForAdapter(openaiModel, "deepseek_harness", "deepseek_harness.py"); len(env) != 0 {
		t.Fatalf("dsh env must stay empty for an OpenAI model: %v", env)
	}
}

// TestInjectionPerAgentModel: one node model cannot satisfy every agent's
// dialect. A pin on injection.agents lets deepseek_harness (Anthropic) and
// codex (OpenAI) both stay injectable from a single OpenAI-dialect node
// model — the failure-time rescue keeps covering codex while dsh gets an
// endpoint it can actually speak to. Unset fields inherit the node model, so
// the key is declared once.
func TestInjectionPerAgentModel(t *testing.T) {
	cleanCredentialEnv(t)

	node := config.ModelConfig{
		APIType:  config.APITypeOpenAI,
		Provider: "zhipu",
		BaseURL:  "https://open.bigmodel.cn/api/paas/v4",
		APIKey:   "sk-node-test",
		Model:    "glm-5.3-flash",
	}
	r := NewRouter(testCard(), NewExecutor(), node, config.InjectionConfig{
		Model: config.InjectionModelAuto,
		Agents: map[string]config.ModelConfig{
			"deepseek_harness": {
				APIType: config.APITypeAnthropic,
				BaseURL: "https://open.bigmodel.cn/api/anthropic",
			},
		},
	}, config.RoutingConfig{})

	// The pinned agent speaks Anthropic: injected model/endpoint come from the
	// pin, while key and model name are inherited from the node model.
	d := r.InjectionDecision("deepseek_harness", "deepseek_harness.py")
	if !d.Inject {
		t.Fatalf("pinned dsh must be injectable: %+v", d)
	}
	if d.BaseURL != "https://open.bigmodel.cn/api/anthropic" || d.Model != "glm-5.3-flash" {
		t.Fatalf("dsh decision = %+v, want the pinned anthropic endpoint", d)
	}
	assertEnv(t, modelEnvForAdapter(r.injectionModelFor("deepseek_harness", "deepseek_harness.py"),
		"deepseek_harness", "deepseek_harness.py"), map[string]string{
		"DEEPSEEK_BASE_URL": "https://open.bigmodel.cn/api/anthropic",
		"DEEPSEEK_API_KEY":  "sk-node-test",
		"DEEPSEEK_MODEL":    "glm-5.3-flash",
	})

	// An unpinned agent keeps the node dialect — this is the regression the
	// pin exists to prevent: flipping the node model to anthropic would have
	// silently dropped codex out of the failure-time rescue.
	cx := r.InjectionDecision("codex", "codex.py")
	if !cx.Inject {
		t.Fatalf("codex must stay injectable under the node's openai dialect: %+v", cx)
	}
	if cx.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("codex took the pinned endpoint: %+v", cx)
	}

	// A pin may also be keyed by the adapter file, so a card that aliases the
	// agent still finds it (agents.Lookup falls back to the adapter too).
	rByAdapter := NewRouter(testCard(), NewExecutor(), node, config.InjectionConfig{
		Model: config.InjectionModelAuto,
		Agents: map[string]config.ModelConfig{
			"deepseek_harness.py": {
				APIType: config.APITypeAnthropic,
				BaseURL: "https://open.bigmodel.cn/api/anthropic",
			},
		},
	}, config.RoutingConfig{})
	for _, name := range []string{"deepseek_harness", "dsh-renamed"} {
		d2 := rByAdapter.InjectionDecision(name, "deepseek_harness.py")
		if !d2.Inject || d2.BaseURL != "https://open.bigmodel.cn/api/anthropic" {
			t.Fatalf("adapter-keyed pin must apply to %q: %+v", name, d2)
		}
	}
}

// TestSelfContainedAgentSkipsInjectionButRescuesOnFailure: OpenCode ships its
// own free tier (registry SelfContainedModel). Under auto panda must NOT inject
// — opencode.py substitutes a model outside its own roster back to a free one,
// so injecting would be recorded as applied while changing nothing. When that
// free tier fails or runs out of quota, the failure-time rescue must inject
// panda's model: "own model first, panda's model on failure".
func TestSelfContainedAgentSkipsInjectionButRescuesOnFailure(t *testing.T) {
	cleanCredentialEnv(t)
	node := config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey:  "sk-node-test",
		Model:   "glm-5.3-flash",
	}
	r := injectionRouter(node, config.InjectionModelAuto)

	d := r.InjectionDecision("opencode", "opencode.py")
	if d.Inject {
		t.Fatalf("self-contained agent must not be injected in auto mode: %+v", d)
	}
	if !strings.Contains(d.Reason, "self-contained") {
		t.Fatalf("reason must name the self-contained model: %q", d.Reason)
	}
	// An explicit always is still an override.
	if dAlways := injectionRouter(node, config.InjectionModelAlways).InjectionDecision("opencode", "opencode.py"); !dAlways.Inject {
		t.Fatalf("injection.model=always must still override: %+v", dAlways)
	}

	// Free tier fails → one retry carrying panda's model.
	attempts := 0
	r.SetAdapterProcessRunner(func(ctx context.Context, adapter, prompt, cwd string, env []string) AgentResult {
		attempts++
		if attempts == 1 {
			if hasEnvPrefix(env, "OPENPANDA_MODEL_API_KEY=") {
				t.Fatalf("first attempt must run on the agent's own model, env=%v", env)
			}
			return AgentResult{OK: false, Result: "provider failure: Free usage limit reached", ExitCode: 1}
		}
		assertEnv(t, env, map[string]string{
			"OPENPANDA_MODEL_BASE_URL": "https://open.bigmodel.cn/api/paas/v4",
			"OPENPANDA_MODEL_API_KEY":  "sk-node-test",
			"OPENPANDA_MODEL_NAME":     "glm-5.3-flash",
			"OPENPANDA_MODEL_API_TYPE": "openai",
		})
		return AgentResult{OK: true, Result: "completed on the injected model", ExitCode: 0}
	})

	res := r.runAdapterDefault(context.Background(), "opencode.py", "test prompt", "")
	if !res.OK || attempts != 2 {
		t.Fatalf("free-tier failure must be rescued by injection: ok=%v attempts=%d res=%+v", res.OK, attempts, res)
	}
	if !res.Injected {
		t.Fatalf("rescued run must report the injection: %+v", res)
	}
}

// hasEnvPrefix reports whether any env entry starts with prefix.
func hasEnvPrefix(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
