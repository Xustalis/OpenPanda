// Package agents is the single source of truth for the agent CLIs PANDA can
// delegate to. It maps each agent's registry key to its adapter script, the
// binary names to probe on PATH, and install/update guidance (download link +
// install command). The CLI (`panda agents`), the capability-card generator
// (`panda detect`), the web settings API (webui/panel), and the commander's
// availability probe all read from here instead of each hardcoding their own
// table, so adding an agent is a single-entry change.
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
	},
	{
		Name:        "opencode",
		Adapter:     "opencode.py",
		Binaries:    []string{"opencode"},
		DisplayName: "OpenCode",
		InstallHint: "curl -fsSL https://opencode.ai/install | bash",
		InstallURL:  "https://opencode.ai/docs",
	},
	{
		Name:        "codex",
		Adapter:     "codex.py",
		Binaries:    []string{"codex"},
		DisplayName: "Codex (OpenAI)",
		InstallHint: "npm install -g @openai/codex",
		InstallURL:  "https://developers.openai.com/codex/",
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