// SPDX-License-Identifier: AGPL-3.0-or-later

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

import (
	"sort"

	"github.com/Xustalis/OpenPanda/internal/defense"
)

// TierAutoApproved is the tier a generated capability card declares for an
// agent. It equals defense.TierReversible on purpose: delegating to an agent is
// the product's purpose, so a detected node runs unattended. An operator who
// wants an agent to ask before it acts declares `tier: 2` on that agent in
// capabilities.yaml, and commander.Route lets that declaration win.
//
// It is bound to the defense constant rather than written as 1 so the two
// cannot drift: carddetect used to write a literal 2 here, which silently
// contradicted commander.Route's documented "undeclared defaults to 1" and
// parked every agent task in review on every detected node.
const TierAutoApproved = defense.TierReversible

// Known describes one agent CLI PANDA recognises. It is deliberately static
// data with no I/O: probes, adapter names and guidance are derived from it by
// the consumers that need them.
type Known struct {
	// Name is the registry key (also the capability-card agent key), e.g.
	// "grok_build". It is stable and used for routing.
	Name string
	// Adapter is the adapter script under adapters/, e.g. "grok_build.py".
	Adapter string
	// Command is the argv template the generic adapter (generic.py) expands
	// when this agent's card entry declares it — e.g. "zcode --prompt
	// {prompt}". It is the card's agents.<name>.command field verbatim;
	// {prompt} substitutes as one literal argv element. Only meaningful when
	// Adapter is generic.py — bespoke adapters carry their own command line.
	Command string
	// Endpoint is the provider base URL this harness talks to when it runs on
	// its own credentials or its self-contained model ("" when unknown). The
	// pre-dispatch reachability probe hits it so a harness whose provider is
	// down is skipped before the task can block inside the adapter. When the
	// run would inject PANDA's model instead, the injected model's base URL
	// wins; an env override in the agent's own ModelEnv mapping wins over the
	// registry default.
	Endpoint string
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
	// CredentialFileFields narrows selected CredentialFiles to the fields
	// whose (non-empty) presence marks real credentials. A file listed here
	// counts only when at least one named field is set — some agents keep a
	// state file that exists long before any login (Claude Code writes
	// ~/.claude.json on first run; dsh writes ~/.dsh/.credentials.yaml with
	// an empty refs block), and treating its mere existence as credentials
	// would wrongly disable model injection. Field names are dotted paths;
	// the probe reads JSON files as objects and YAML files by indentation.
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
	// SelfContainedModel is true when the agent ships with its own built-in
	// free or local model provider (e.g. OpenCode's opencode/deepseek-v4-flash-free),
	// requiring no external API keys or configuration to be viable.
	SelfContainedModel bool
	// DefaultCapabilities are the task capabilities this agent provides when
	// generating capability cards (panda detect).
	DefaultCapabilities []string
	// DefaultBestAt are the specialized task tags this agent excels at.
	DefaultBestAt []string
	// DefaultCostTier is the routing cost tier ("low", "low_medium", "medium", "medium_high", "high").
	DefaultCostTier string
	// DefaultTier is the capability tier written into a generated card.
	// It is TierAutoApproved: delegating to an agent is what the product is
	// for, so a detected node must run unattended. An operator who wants an
	// agent to ask first sets `tier: 2` on it in capabilities.yaml, and that
	// declaration still wins in commander.Route.
	DefaultTier int
}

// Capabilities describes the native feature surface one agent CLI exposes.
// Each flag is true when the agent's documented CLI surface includes the
// corresponding feature; the commander reads them instead of hard-coding
// per-adapter tables (the restricted-mode and MCP-passthrough allowlists
// used to live in their own maps and drifted out of sync with this one).
type Capabilities struct {
	// SupportsSkills means the agent has a native skill/library concept
	// reachable when the tool whitelist is lifted (extended policy).
	SupportsSkills bool
	// SupportsMCP means the agent can consume MCP servers in some form
	// (its own config files, flags, or project discovery). This is the
	// broad capability; DiscoversProjectMCP is the specific mechanism the
	// commander's .mcp.json passthrough requires.
	SupportsMCP bool
	// SupportsSubagents means the agent can spawn its own child agents
	// (e.g. Claude's Task tool); the orchestration layer records the
	// delegation events when the extended policy lifts the whitelist.
	SupportsSubagents bool
	// SupportsRestricted means the adapter can express a read-only tool
	// face (no shell, no writes) for unconsented remote tasks. Adapters
	// without the flag must never be asked for one — a restricted request
	// sent to a full-power CLI would silently run unconfined, so the
	// scheduler refuses the attempt instead of degrading (fail closed).
	SupportsRestricted bool
	// DiscoversProjectMCP means the agent CLI auto-discovers a project-level
	// MCP config in its working directory, so the commander can materialize
	// the configured passthrough servers for one run.
	DiscoversProjectMCP bool
	// MCPProjectFile names the project-level config path the CLI discovers
	// (".mcp.json" for claude-style CLIs, ".pi/mcp.json" for pi). Empty
	// defaults to ".mcp.json" — only read when DiscoversProjectMCP is set.
	MCPProjectFile string
	// SupportsStructuredOutput means the adapter's CLI accepts a result
	// schema (claude --json-schema): the run's final reply validates
	// against it, so the protocol fields (status/question/delegate_requests)
	// arrive parsed rather than as text markers.
	SupportsStructuredOutput bool
	// SupportsSession means the adapter can keep the agent CLI alive across
	// turns (claude --input-format stream-json): a supervision "continue"
	// costs a message write instead of a process spawn + session reload.
	SupportsSession bool
	// MCPConfigFlag names the CLI flag that accepts an MCP config document
	// (e.g. claude's "--mcp-config"). When set, the commander passes the
	// passthrough servers on the request instead of writing .mcp.json into
	// the task's work dir.
	MCPConfigFlag string
}

// ModelEnvMapping names the env vars one agent CLI reads for its model
// endpoint. It is data, not code: the commander translates its model config
// through the mapping, so a new agent in the registry gets credential probing
// and (when a mapping is declared) injection without any commander change.
type ModelEnvMapping struct {
	// APIType is the protocol the agent expects: "anthropic" | "openai".
	// Empty matches any configured protocol — a polyglot agent (pi, which
	// selects the wire dialect per-provider) speaks whatever the model
	// config speaks, so the effective api type is the model's own.
	APIType string
	BaseURL string // env var carrying the provider base URL, e.g. ANTHROPIC_BASE_URL / OPENAI_BASE_URL
	APIKey  string // env var carrying the API key, e.g. ANTHROPIC_API_KEY / OPENAI_API_KEY
	Model   string // env var carrying the model name, e.g. ANTHROPIC_MODEL / OPENAI_MODEL
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
		Endpoint:    "https://api.anthropic.com",
		Binaries:    []string{"claude", "claude-code"},
		DisplayName: "Claude Code",
		InstallHint: "npm install -g @anthropic-ai/claude-code",
		InstallURL:  "https://docs.anthropic.com/en/docs/claude-code/setup",
		InitHint:    "claude init  # accept terms and create ~/.claude/ state files",
		// Claude Code has an unambiguous Anthropic env contract.
		CredentialEnvVars: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		CredentialFiles: []string{
			".claude/config.json",       // 2.1.x API-key login (primaryApiKey)
			".claude/settings.json",     // user-managed env (ANTHROPIC_AUTH_TOKEN / BASE_URL)
			".claude.json",              // legacy: oauthAccount / primaryApiKey fields
			".claude/.credentials.json", // subscription OAuth tokens
		},
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
			APIType: "anthropic",
			BaseURL: "ANTHROPIC_BASE_URL",
			APIKey:  "ANTHROPIC_API_KEY",
			Model:   "ANTHROPIC_MODEL",
		},
		Capabilities: Capabilities{
			SupportsSkills:    true,
			SupportsMCP:       true,
			SupportsSubagents: true,
			// claude --allowedTools can express a read-only face, and the CLI
			// auto-discovers .mcp.json in its working directory.
			SupportsRestricted:  true,
			DiscoversProjectMCP: true,
			// claude -p takes --json-schema, --mcp-config and stream-json
			// input — the structured contract, file-free passthrough and the
			// in-session multi-turn the supervision loop drives.
			SupportsStructuredOutput: true,
			SupportsSession:          true,
			MCPConfigFlag:            "--mcp-config",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "refactoring"},
		DefaultBestAt:       []string{"multi_file_edits", "code_search", "refactoring", "complex_reasoning"},
		DefaultCostTier:     "medium_high",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:               "opencode",
		Adapter:            "opencode.py",
		Endpoint:           "https://opencode.ai/zen",
		Binaries:           []string{"opencode"},
		DisplayName:        "OpenCode",
		InstallHint:        "curl -fsSL https://opencode.ai/install | bash",
		InstallURL:         "https://opencode.ai/docs",
		SelfContainedModel: true,
		CredentialEnvVars:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialFiles:    []string{".local/share/opencode/auth.json", ".config/opencode/opencode.json", ".config/opencode/opencode.jsonc"},
		CredentialFileFields: map[string][]string{
			".config/opencode/opencode.json":  {"model", "provider", "apiKey", "providers"},
			".config/opencode/opencode.jsonc": {"model", "provider", "apiKey", "providers"},
			".local/share/opencode/auth.json": {"apiKey", "token", "access_token"},
		},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "OPENAI_BASE_URL",
			APIKey:  "OPENAI_API_KEY",
			Model:   "OPENCODE_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "scripts"},
		DefaultBestAt:       []string{"fast_scripts", "quick_edits", "code_search"},
		DefaultCostTier:     "low",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:              "codex",
		Adapter:           "codex.py",
		Endpoint:          "https://api.openai.com",
		Binaries:          []string{"codex"},
		DisplayName:       "Codex (OpenAI)",
		InstallHint:       "npm install -g @openai/codex",
		InstallURL:        "https://developers.openai.com/codex/",
		InitHint:          "codex --help  # first run creates ~/.codex/ config directory",
		CredentialEnvVars: []string{"OPENAI_API_KEY"},
		CredentialFiles:   []string{".codex/auth.json", ".codex/config.toml"},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "OPENAI_BASE_URL",
			APIKey:  "OPENAI_API_KEY",
			Model:   "OPENAI_MODEL",
		},
		// codex's own sandbox levels include a real read-only mode
		// (--sandbox read-only), so it can honor restricted runs.
		Capabilities:        Capabilities{SupportsRestricted: true},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "code_review"},
		DefaultBestAt:       []string{"code_review", "running_tests", "multi_file_edits"},
		DefaultCostTier:     "medium",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:              "grok_build",
		Adapter:           "grok_build.py",
		Endpoint:          "https://api.x.ai",
		Binaries:          []string{"grok", "grok-build"},
		DisplayName:       "Grok Build (xAI)",
		InstallHint:       "curl -fsSL https://x.ai/cli/install.sh | bash",
		InstallURL:        "https://docs.x.ai/build/overview",
		CredentialEnvVars: []string{"XAI_API_KEY", "GROK_API_KEY"},
		CredentialFiles:   []string{".grok/config.toml", ".grok/auth.json"},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "GROK_BASE_URL",
			APIKey:  "XAI_API_KEY",
			Model:   "GROK_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "build"},
		DefaultBestAt:       []string{"build_diagnostics", "code_search", "refactoring"},
		DefaultCostTier:     "medium",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:              "deepseek_harness",
		Adapter:           "deepseek_harness.py",
		Endpoint:          "https://api.deepseek.com",
		Binaries:          []string{"dsh", "deepseek-harness"},
		DisplayName:       "DeepSeek Harness (dsh)",
		InstallHint:       "npm install -g @deepseek-ai/dsh",
		InstallURL:        "https://github.com/deepseek-ai/deepseek-harness",
		CredentialEnvVars: []string{"DEEPSEEK_API_KEY"},
		// dsh stores secrets in ~/.dsh/.credentials.yaml's `refs` map
		// (env-name → secret); the file itself exists from first run, so
		// only a non-empty refs block counts as configured.
		CredentialFiles: []string{".dsh/.credentials.yaml", ".dsh/.env", ".dsh/config.json", ".dsh/auth.json"},
		CredentialFileFields: map[string][]string{
			".dsh/.credentials.yaml": {"refs"},
		},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "DEEPSEEK_BASE_URL",
			APIKey:  "DEEPSEEK_API_KEY",
			Model:   "DEEPSEEK_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "deepseek"},
		DefaultBestAt:       []string{"code_generation", "code_explanation"},
		DefaultCostTier:     "low",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:              "openclaw",
		Adapter:           "openclaw.py",
		Endpoint:          "https://api.openai.com",
		Binaries:          []string{"openclaw"},
		DisplayName:       "OpenClaw",
		InstallHint:       "curl -fsSL https://openclaw.ai/install.sh | bash",
		InstallURL:        "https://docs.openclaw.ai/",
		CredentialEnvVars: []string{"OPENCLAW_API_KEY", "OPENAI_API_KEY"},
		CredentialFiles:   []string{".openclaw/config.json", ".openclaw/auth.json"},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "OPENAI_BASE_URL",
			APIKey:  "OPENCLAW_API_KEY",
			Model:   "OPENCLAW_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "automation"},
		DefaultBestAt:       []string{"automation", "shell_execution"},
		DefaultCostTier:     "medium",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:        "antigravity",
		Adapter:     "antigravity.py",
		Endpoint:    "https://generativelanguage.googleapis.com",
		Binaries:    []string{"agy", "antigravity"},
		DisplayName: "Antigravity (Google)",
		InstallURL:  "https://antigravity.google/docs/cli/install",
		InitHint:    "agy  # sign in once interactively — headless runs reuse the keyring session",
		// agy authenticates through the OS keyring or a Gemini API key; its env
		// contract is not the OpenAI/Anthropic pair, so ModelEnv stays nil and
		// PANDA never injects a model endpoint into it.
		CredentialEnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		Capabilities: Capabilities{
			SupportsSkills:    true,
			SupportsMCP:       true,
			SupportsSubagents: true,
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "build"},
		DefaultBestAt:       []string{"multi_file_edits", "code_search", "complex_reasoning"},
		DefaultCostTier:     "medium_high",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:    "zcode",
		Adapter: "generic.py",
		// zcode's headless contract is `zcode --prompt "…"` — community-
		// verified (Z.AI documents no non-interactive mode yet). The prompt
		// rides as one literal argv element; there is no session/resume flag,
		// which is exactly the plain-argv surface generic.py exists for.
		Command:     "zcode --prompt {prompt}",
		Endpoint:    "https://api.z.ai/api/anthropic",
		Binaries:    []string{"zcode"},
		DisplayName: "ZCode (Z.AI)",
		InstallURL:  "https://github.com/zai-org/ZCode",
		// `zcode login` OAuth is buggy upstream: the reliable auth paths are
		// the TUI setup wizard's ~/.zcode/cli/config.json (credential-free
		// until configured — so its mere existence is NOT a credential
		// signal and stays out of CredentialFiles) or the ZCODE_* env trio,
		// which doubles as the injection surface below.
		InitHint:          "zcode  # run once for the setup wizard, or set ZCODE_API_KEY/ZCODE_MODEL/ZCODE_BASE_URL",
		CredentialEnvVars: []string{"ZCODE_API_KEY"},
		ModelEnv: &ModelEnvMapping{
			APIType: "anthropic",
			BaseURL: "ZCODE_BASE_URL",
			APIKey:  "ZCODE_API_KEY",
			Model:   "ZCODE_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit"},
		DefaultBestAt:       []string{"multi_file_edits", "code_search"},
		DefaultCostTier:     "low",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:              "hermes",
		Adapter:           "hermes.py",
		Endpoint:          "https://api.openai.com",
		Binaries:          []string{"hermes", "hermes-agent"},
		DisplayName:       "Hermes",
		InstallHint:       "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash",
		InstallURL:        "https://hermes-agent.nousresearch.com/docs/getting-started/installation",
		CredentialEnvVars: []string{"OPENAI_API_KEY", "HERMES_API_KEY"},
		CredentialFiles:   []string{".hermes/auth.json", ".hermes/config.yaml", ".hermes/.env"},
		ModelEnv: &ModelEnvMapping{
			APIType: "openai",
			BaseURL: "OPENAI_BASE_URL",
			APIKey:  "OPENAI_API_KEY",
			Model:   "OPENAI_MODEL",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "long_running"},
		DefaultBestAt:       []string{"long_running_tasks", "shell_execution", "multi_file_edits"},
		DefaultCostTier:     "low_medium",
		DefaultTier:         TierAutoApproved,
	},
	{
		Name:    "pi",
		Adapter: "pi.py",
		// pi is multi-provider — /login to any vendor, or its env vars. There
		// is no single provider endpoint to probe, so Endpoint stays empty
		// and the pre-dispatch reachability check is skipped for it.
		Binaries:    []string{"pi"},
		DisplayName: "Pi",
		InstallHint: "npm install -g @mariozechner/pi-coding-agent",
		InstallURL:  "https://pi.dev",
		InitHint:    "pi  # run once and /login to connect a provider",
		// pi authenticates through ~/.pi/agent/auth.json (written by /login),
		// a custom provider block in models.json, or the provider's own env
		// var — the union of the common vendor keys is the env signal.
		CredentialEnvVars: []string{
			"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY",
			"OPENROUTER_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
			"XAI_API_KEY", "MISTRAL_API_KEY", "GROQ_API_KEY",
			"CEREBRAS_API_KEY", "ZAI_API_KEY", "KIMI_API_KEY",
			"MOONSHOT_API_KEY", "MINIMAX_API_KEY",
		},
		CredentialFiles: []string{".pi/agent/auth.json", ".pi/agent/models.json"},
		CredentialFileFields: map[string][]string{
			// auth.json maps provider id → credential; a provider key with a
			// value is the login signal. models.json counts when it declares
			// a custom providers block (a compatible endpoint configured).
			".pi/agent/auth.json": {
				"anthropic", "openai", "google", "deepseek", "openrouter",
				"github-copilot", "xai", "mistral", "groq", "cerebras",
				"zai", "kimi", "moonshot", "minimax", "qwen", "ollama",
			},
			".pi/agent/models.json": {"providers"},
		},
		// pi speaks whatever protocol the configured model speaks — the
		// adapter declares the dialect per-provider in a generated
		// models.json — so the mapping matches any api_type and the
		// commander passes the model's own type as OPENPANDA_MODEL_API_TYPE.
		ModelEnv: &ModelEnvMapping{
			APIType: "",
			BaseURL: "PI_BASE_URL",
			APIKey:  "PI_API_KEY",
			Model:   "PI_MODEL",
		},
		Capabilities: Capabilities{
			SupportsSkills: true,
			SupportsMCP:    true,
			// --tools read,grep,find,ls --no-mcp --no-extensions --no-approve
			// gives a real read-only face, and project .pi/mcp.json is pi's
			// discovery point for the passthrough servers.
			SupportsRestricted:  true,
			DiscoversProjectMCP: true,
			MCPProjectFile:      ".pi/mcp.json",
		},
		DefaultCapabilities: []string{"coding", "shell", "file_edit", "refactoring"},
		DefaultBestAt:       []string{"multi_file_edits", "code_search", "scripting"},
		DefaultCostTier:     "medium",
		DefaultTier:         TierAutoApproved,
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

// GenericAdapter is the script name of the template-driven adapter
// (adapters/generic.py). Any card agent may point at it, so an adapter-script
// lookup cannot identify WHICH agent a generic run belongs to — the card's
// agent name is the identity there, not the adapter.
const GenericAdapter = "generic.py"

// GenericNativeAdapter is the Python-free twin of GenericAdapter: a card
// declaring adapter: "generic" (no .py) gets the identical argv-template
// expansion implemented inside the commander itself. It exists for nodes
// where a Python runtime cannot be assumed (bare Windows/macOS, minimal
// containers, embedded boards) — the place where "any device can serve"
// would otherwise die on a missing interpreter. Same identity rule: an
// unknown card name on it resolves to no manifest.
const GenericNativeAdapter = "generic"

// Lookup resolves a card agent entry (name + adapter) to its registry record.
//
// The card's own agent name wins, but only when the entry's adapter agrees
// with the registry record's — a card naming an agent "codex" while pointing
// at claude_code.py must not inherit codex's credential manifest. For bespoke
// adapters the adapter script alone identifies the contract, so a renamed
// card entry still resolves (a card may call the claude adapter anything).
//
// GenericAdapter is the exception that makes the name lookup load-bearing:
// any number of unrelated CLIs share the script, so an unknown name on it
// resolves to nothing rather than to whichever registry agent happens to use
// generic.py. GenericNativeAdapter follows the same rule — an unknown name
// resolves to nothing rather than borrowing a manifest. Without this rule
// every custom CLI silently inherited zcode's binaries, credential manifest
// and endpoint.
func Lookup(name, adapter string) (Known, bool) {
	if k, ok := ByName(name); ok && k.Adapter == adapter {
		return k, true
	}
	if adapter == GenericAdapter || adapter == GenericNativeAdapter {
		return Known{}, false
	}
	return ByAdapter(adapter)
}
