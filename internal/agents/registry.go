// Package agents is the single source of truth for the agent CLIs PANDA can
// delegate to. It maps each agent's registry key to its adapter script, the
// binary names to probe on PATH, install/update guidance (download link +
// install command), and the agent's credential manifest (which env vars and
// config files prove the agent brings its own model, and how PANDA's model
// config maps onto the agent's env contract). The CLI (`panda agents`), the
// capability-card generator (`panda detect`), the web settings API
// (webui/panel), the commander's availability probe, and the commander's
// credential probe/injection all read from here instead of each hardcoding
// their own table, so adding an agent (with credentials) is a single-entry
// change.
package agents

import "sort"

// Known describes one agent CLI PANDA recognises. It is deliberately static
// data with no I/O: probes, adapter names and guidance are derived from it by
// the consumers that need them.
type Known struct {
	// Name is the registry key (also the capability-card agent key), e.g.
	// "grok_build". It is stable and used for routing.
	Name string
	// Adapter is the adapter script under adapters/, e.g. "grok_build.py".
	Adapter string
	// Binaries are the CLI binary names to probe, in preference order. The
	// first one is the canonical probe binary (also the availability probe
	// fallback when a card declares no install_check).
	Binaries []string
	// DisplayName is the human-readable label shown in the CLI/Web UI.
	DisplayName string
	// InstallHint is a one-line shell command to install/update the CLI.
	// Empty when the agent has no public installer (self-hosted/custom CLIs).
	InstallHint string
	// InstallURL is the documentation or download page. Empty when unknown.
	InstallURL string
	// InitHint is a one-line command to initialize an installed-but-unconfigured
	// agent CLI (e.g. accepting terms, generating project files). Empty when the
	// agent needs no initialization beyond installation. `panda agents` and
	// `panda detect` surface this when the binary is present but credentials
	// or state files are missing, so the operator can distinguish "not installed"
	// from "installed but not initialized".
	InitHint string
	// CredentialEnvVars is the probe side of the agent's credential
	// manifest: env var names that prove the agent carries model
	// credentials of its own (e.g. claude's ANTHROPIC_API_KEY /
	// ANTHROPIC_AUTH_TOKEN). When one is set, the commander leaves the
	// agent's native model alone and only forwards these vars through the
	// sandbox. Empty means "unknown" — probes fall back to the union of
	// common provider keys.
	CredentialEnvVars []string
	// CredentialFiles lists home-relative files whose presence (non-empty)
	// proves the agent is logged in / configured with its own provider —
	// e.g. codex stores its auth and provider sections in ~/.codex.
	CredentialFiles []string
	// CredentialFileFields narrows selected CredentialFiles to the JSON
	// fields whose (non-empty) presence marks real credentials. A file
	// listed here counts only when at least one named field is set — some
	// agents keep a state file that exists long before any login (Claude
	// Code writes ~/.claude.json on first run), and treating its mere
	// existence as credentials would wrongly disable model injection.
	CredentialFileFields map[string][]string
	// ModelEnv is the injection side of the credential manifest: the env
	// vars the agent CLI reads for its model endpoint. Nil means model
	// injection is not safely supported for this agent (its env contract
	// is ambiguous or it always brings its own key), so PANDA never
	// overrides its endpoint.
	ModelEnv *ModelEnvMapping
	// Capabilities declares what the agent's CLI natively supports.
	// `panda agents` displays these flags today; the routing layer and
	// prompt builder are planned to read them instead of each hard-coding
	// per-adapter knowledge. An agent that supports Skills can reach its
	// native skill library when the tools policy is extended; an agent
	// that supports MCP can discover project .mcp.json servers; an agent
	// that supports Subagents can delegate work to its own child agents.
	Capabilities Capabilities
}

// Capabilities describes the native feature surface one agent CLI exposes.
// Each flag is true when the agent's documented CLI surface includes the
// corresponding feature; `panda agents` displays them today, and the routing
// layer / prompt builder are planned to read them instead of hard-coding
// per-adapter knowledge.
type Capabilities struct {
	// SupportsSkills means the agent has a native skill/library concept
	// reachable when the tool whitelist is lifted (extended policy).
	SupportsSkills bool
	// SupportsMCP means the agent auto-discovers project-level MCP
	// servers (.mcp.json in its cwd) when the extended policy writes one.
	SupportsMCP bool
	// SupportsSubagents means the agent can spawn its own child agents
	// (e.g. Claude's Task tool); the orchestration layer records the
	// delegation events when the extended policy lifts the whitelist.
	SupportsSubagents bool
}

// ModelEnvMapping names the env vars one agent CLI reads for its model
// endpoint. It is data, not code: the commander translates its model config
// through the mapping, so a new agent in the registry gets credential probing
// and (when a mapping is declared) injection without any commander change.
type ModelEnvMapping struct {
	BaseURL string // env var carrying the provider base URL, e.g. ANTHROPIC_BASE_URL
	APIKey  string // env var carrying the API key, e.g. ANTHROPIC_API_KEY
	Model   string // env var carrying the model name, e.g. ANTHROPIC_MODEL
}

// PrimaryBinary returns the canonical CLI binary to probe, or "" if none.
func (k Known) PrimaryBinary() string {
	if len(k.Binaries) == 0 {
		return ""
	}
	return k.Binaries[0]
}

var known = []Known{
	{
		Name:        "claude_code",
		Adapter:     "claude_code.py",
		Binaries:    []string{"claude"},
		DisplayName: "Claude Code",
		InstallHint: "npm install -g @anthropic-ai/claude-code",
		InstallURL:  "https://docs.anthropic.com/en/docs/claude-code/setup",
		InitHint:    "claude init  # accept terms and create ~/.claude/ state files",
		// Claude Code has an unambiguous Anthropic env contract, so it is
		// the one agent PANDA can inject a model endpoint into (see
		// commander's DeepSeek flash injection).
		CredentialEnvVars: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		CredentialFiles: []string{
			".claude/config.json",       // 2.1.x API-key login (primaryApiKey)
			".claude/settings.json",     // user-managed env (ANTHROPIC_AUTH_TOKEN / BASE_URL)
			".claude.json",              // legacy: oauthAccount / primaryApiKey fields
			".claude/.credentials.json", // subscription OAuth tokens
		},
		// ~/.claude.json is Claude Code's onboarding/state file: it exists
		// after the very first run even with no login — only its
		// oauthAccount / primaryApiKey fields mean credentials. config.json
		// carries primaryApiKey; settings.json authenticates through its env
		// block (dotted paths). ~/.claude/.credentials.json is written only
		// after OAuth login, so its presence alone suffices.
		CredentialFileFields: map[string][]string{
			".claude.json":        {"oauthAccount", "primaryApiKey"},
			".claude/config.json": {"primaryApiKey"},
			".claude/settings.json": {
				"env.ANTHROPIC_AUTH_TOKEN",
				"env.ANTHROPIC_API_KEY",
				"apiKeyHelper",
			},
		},
		ModelEnv: &ModelEnvMapping{
			BaseURL: "ANTHROPIC_BASE_URL",
			APIKey:  "ANTHROPIC_API_KEY",
			Model:   "ANTHROPIC_MODEL",
		},
		Capabilities: Capabilities{
			SupportsSkills:    true,
			SupportsMCP:       true,
			SupportsSubagents: true,
		},
	},
	{
		Name:              "opencode",
		Adapter:           "opencode.py",
		Binaries:          []string{"opencode"},
		DisplayName:       "OpenCode",
		InstallHint:       "curl -fsSL https://opencode.ai/install | bash",
		InstallURL:        "https://opencode.ai/docs",
		CredentialEnvVars: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialFiles:   []string{".config/opencode/opencode.json", ".config/opencode/auth.json"},
	},
	{
		Name:              "codex",
		Adapter:           "codex.py",
		Binaries:          []string{"codex"},
		DisplayName:       "Codex (OpenAI)",
		InstallHint:       "npm install -g @openai/codex",
		InstallURL:        "https://developers.openai.com/codex/",
		InitHint:          "codex --help  # first run creates ~/.codex/ config directory",
		CredentialEnvVars: []string{"OPENAI_API_KEY"},
		CredentialFiles:   []string{".codex/auth.json", ".codex/config.toml"},
	},
	{
		Name:        "grok_build",
		Adapter:     "grok_build.py",
		Binaries:    []string{"grok"},
		DisplayName: "Grok Build (xAI)",
		InstallHint: "curl -fsSL https://x.ai/cli/install.sh | bash",
		InstallURL:  "https://docs.x.ai/build/overview",
	},
	{
		Name:        "deepseek_harness",
		Adapter:     "deepseek_harness.py",
		Binaries:    []string{"dsh"},
		DisplayName: "DeepSeek Harness (dsh)",
		InstallHint: "npm install -g @deepseek-ai/dsh",
		InstallURL:  "https://github.com/deepseek-ai/deepseek-harness",
	},
	{
		Name:        "openclaw",
		Adapter:     "openclaw.py",
		Binaries:    []string{"openclaw"},
		DisplayName: "OpenClaw",
		InstallHint: "curl -fsSL https://openclaw.ai/install.sh | bash",
		InstallURL:  "https://docs.openclaw.ai/",
	},
	{
		Name:        "hermes",
		Adapter:     "hermes.py",
		Binaries:    []string{"hermes"},
		DisplayName: "Hermes",
		InstallHint: "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash",
		InstallURL:  "https://hermes-agent.nousresearch.com/docs/getting-started/installation",
	},
}

// Registry returns every known agent in deterministic (name-sorted) order.
// The slice is a copy, so callers may reorder or filter freely.
func Registry() []Known {
	out := make([]Known, len(known))
	copy(out, known)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ByName returns the agent with the given registry key, or ok=false.
func ByName(name string) (Known, bool) {
	for _, k := range known {
		if k.Name == name {
			return k, true
		}
	}
	return Known{}, false
}

// ByAdapter returns the agent whose adapter script is adapter, or ok=false.
// Used by the commander to derive a probe binary when a card declares no
// install_check.
func ByAdapter(adapter string) (Known, bool) {
	for _, k := range known {
		if k.Adapter == adapter {
			return k, true
		}
	}
	return Known{}, false
}
