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
	if !ok || len(codex.CredentialEnvVars) == 0 || codex.ModelEnv == nil {
		t.Fatalf("codex must declare probe credentials and OpenAI model-env mapping: %+v", codex)
	}
	if codex.ModelEnv.BaseURL != "OPENAI_BASE_URL" || codex.ModelEnv.APIKey != "OPENAI_API_KEY" || codex.ModelEnv.Model != "OPENAI_MODEL" {
		t.Fatalf("codex model env mapping = %+v", codex.ModelEnv)
	}
	oc, ok := ByAdapter("opencode.py")
	if !ok || len(oc.CredentialEnvVars) == 0 {
		t.Fatalf("opencode must declare probe credentials: %+v", oc)
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

// TestPiEntry pins the pi harness manifest: adapter script, install
// guidance, the polyglot model-env mapping, the restricted/capability face,
// and pi's own project MCP discovery path (.pi/mcp.json — not .mcp.json).
func TestPiEntry(t *testing.T) {
	pi, ok := ByName("pi")
	if !ok {
		t.Fatal("pi missing from registry")
	}
	if pi.Adapter != "pi.py" {
		t.Fatalf("pi adapter = %q", pi.Adapter)
	}
	if len(pi.Binaries) == 0 || pi.Binaries[0] != "pi" {
		t.Fatalf("pi binaries = %v", pi.Binaries)
	}
	if len(pi.CredentialEnvVars) == 0 || pi.ModelEnv == nil {
		t.Fatal("pi must declare credential probe vars and a model-env mapping")
	}
	// Polyglot: APIType empty means the adapter speaks the model's own
	// protocol — the commander passes OPENPANDA_MODEL_API_TYPE through.
	if pi.ModelEnv.APIType != "" || pi.ModelEnv.Model != "PI_MODEL" ||
		pi.ModelEnv.APIKey != "PI_API_KEY" || pi.ModelEnv.BaseURL != "PI_BASE_URL" {
		t.Fatalf("pi model env mapping = %+v", pi.ModelEnv)
	}
	if !pi.Capabilities.SupportsRestricted || !pi.Capabilities.DiscoversProjectMCP {
		t.Fatalf("pi capabilities = %+v", pi.Capabilities)
	}
	if pi.Capabilities.MCPProjectFile != ".pi/mcp.json" {
		t.Fatalf("pi MCP project file = %q", pi.Capabilities.MCPProjectFile)
	}
	if pi.DefaultTier != TierAutoApproved {
		t.Fatalf("pi default tier = %d", pi.DefaultTier)
	}
	// Lookup by name+adapter is the identity path the commander uses.
	if _, ok := Lookup("pi", "pi.py"); !ok {
		t.Fatal("Lookup(pi, pi.py) must resolve")
	}
}

// TestGenericLookupIdentity pins the generic identity rule: generic.py and
// the native "generic" adapter are shared by any card agent, so an unknown
// card name on either must resolve to NOTHING rather than borrowing another
// generic entry's binaries, credentials, or endpoint.
func TestGenericLookupIdentity(t *testing.T) {
	// A card naming its agent "zcode" (a registry entry on generic.py)
	// resolves; a card naming it anything else on the same adapter must not.
	if _, ok := Lookup("zcode", "generic.py"); !ok {
		t.Fatal("Lookup(zcode, generic.py) should resolve — that is its entry")
	}
	if _, ok := Lookup("some-custom-cli", "generic.py"); ok {
		t.Fatal("unknown name on generic.py must not borrow another entry")
	}
	if _, ok := Lookup("zcode", "generic"); ok {
		t.Fatal("zcode's manifest is generic.py — the native adapter is a different identity")
	}
	if _, ok := Lookup("some-custom-cli", "generic"); ok {
		t.Fatal("unknown name on native generic must resolve to nothing")
	}
}

// TestDetectedAgentsAreAutoApproved pins the tier a registry entry contributes
// to a generated capability card. commander.Route defaults an agent's tier to 1
// ("delegating to an agent is auto-approved") and lets a card declaration win —
// so a registry default of 2 pre-selects the approval gate on every detected
// node and makes unattended work impossible. That is not hypothetical: it is
// what shipped, and it parked every agent task in review.
func TestDetectedAgentsAreAutoApproved(t *testing.T) {
	reg := Registry()
	if len(reg) == 0 {
		t.Fatal("empty registry")
	}
	for _, k := range reg {
		if k.DefaultTier != TierAutoApproved {
			t.Errorf("agent %q declares DefaultTier %d; a generated card must declare %d",
				k.Name, k.DefaultTier, TierAutoApproved)
		}
	}
}
