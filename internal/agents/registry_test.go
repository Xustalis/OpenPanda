package agents

import "testing"

// TestCredentialManifest pins the credential manifest (the registry's job as
// the single source of truth the commander's probe/injection reads from):
// claude declares both sides (probe vars + injectable Anthropic mapping);
// codex/opencode declare probe-only manifests and bring their own keys.
func TestCredentialManifest(t *testing.T) {
	c, ok := ByAdapter("claude_code.py")
	if !ok {
		t.Fatal("claude_code.py missing from registry")
	}
	want := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	if len(c.CredentialEnvVars) != len(want) {
		t.Fatalf("claude credential env vars = %v, want %v", c.CredentialEnvVars, want)
	}
	for i, v := range want {
		if c.CredentialEnvVars[i] != v {
			t.Fatalf("claude credential env vars = %v, want %v", c.CredentialEnvVars, want)
		}
	}
	if c.ModelEnv == nil {
		t.Fatal("claude must declare a model-env mapping (injectable agent)")
	}
	if c.ModelEnv.BaseURL != "ANTHROPIC_BASE_URL" || c.ModelEnv.APIKey != "ANTHROPIC_API_KEY" || c.ModelEnv.Model != "ANTHROPIC_MODEL" {
		t.Fatalf("claude model env mapping = %+v", c.ModelEnv)
	}

	codex, ok := ByAdapter("codex.py")
	if !ok || len(codex.CredentialEnvVars) == 0 || codex.ModelEnv != nil {
		t.Fatalf("codex must declare probe credentials but no model-env mapping: %+v", codex)
	}
	oc, ok := ByAdapter("opencode.py")
	if !ok || len(oc.CredentialEnvVars) == 0 || oc.ModelEnv != nil {
		t.Fatalf("opencode must declare probe credentials but no model-env mapping: %+v", oc)
	}
}

// TestInjectedAgentsDeclareProbeVars keeps the manifest coherent: an agent
// PANDA can inject a model into must also declare the credentials that prove
// it brought its own key, otherwise auto injection has no probe side.
func TestInjectedAgentsDeclareProbeVars(t *testing.T) {
	for _, k := range Registry() {
		if k.ModelEnv != nil && len(k.CredentialEnvVars) == 0 {
			t.Fatalf("%s declares a model-env mapping without credential env vars", k.Name)
		}
	}
}
