// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// AdapterExecutionProfile is a control-plane observation, NOT an assertion
// about the CLI's effective model/tools/configuration. It intentionally excludes
// prompts, argv templates, MCP bodies, provider URLs, env values and credentials.
type AdapterExecutionProfile struct {
	Version              int                  `json:"version"`
	Observation          string               `json:"observation"`
	Agent                string               `json:"agent"`
	Adapter              string               `json:"adapter"`
	CWD                  string               `json:"cwd"`
	Transport            string               `json:"transport"`
	AdapterPath          string               `json:"adapter_path,omitempty"`
	AdapterSHA256        string               `json:"adapter_sha256,omitempty"`
	SandboxMode          string               `json:"sandbox_mode"`
	SandboxBackend       string               `json:"sandbox_backend"`
	SandboxNetwork       bool                 `json:"sandbox_network"`
	ExternalToolPolicy   string               `json:"external_tool_policy"`
	NativeToolAccess     string               `json:"native_tool_access"`
	NativeSettings       string               `json:"native_settings_policy"`
	ModelPreference      string               `json:"model_preference,omitempty"`
	ModelInjection       bool                 `json:"model_injection"`
	RequestedEffort      string               `json:"requested_effort,omitempty"`
	NativeModel          string               `json:"native_model"`
	NativeEffort         string               `json:"native_effort"`
	NativeConfig         string               `json:"native_config"`
	NativeVersion        string               `json:"native_version"`
	ToolsPolicy          string               `json:"requested_tools_policy,omitempty"`
	Restricted           bool                 `json:"restricted"`
	Authorized           bool                 `json:"authorized"`
	ResumeRequested      bool                 `json:"resume_requested"`
	TimeoutS             int                  `json:"timeout_s"`
	MaxTurns             int                  `json:"requested_max_turns,omitempty"`
	DeclaredCapabilities *ProfileCapabilities `json:"declared_capabilities,omitempty"`
}

// These are registry declarations, not probes of installed CLI features.
type ProfileCapabilities struct {
	Restricted       bool `json:"restricted"`
	Session          bool `json:"session"`
	Skills           bool `json:"skills"`
	MCP              bool `json:"mcp"`
	Subagents        bool `json:"subagents"`
	StructuredOutput bool `json:"structured_output"`
}

type executionProfileKey struct{}

// WithExecutionProfiles installs a trusted process-boundary sink separately
// from adapter-authored NDJSON. An adapter cannot spoof an authoritative
// configuration observation by emitting an event with the same name.
func WithExecutionProfiles(ctx context.Context, sink func(AdapterExecutionProfile)) context.Context {
	return context.WithValue(ctx, executionProfileKey{}, sink)
}

func emitExecutionProfile(ctx context.Context, p AdapterExecutionProfile) {
	if sink, _ := ctx.Value(executionProfileKey{}).(func(AdapterExecutionProfile)); sink != nil {
		sink(p)
	}
}

func adapterExecutionProfile(ctx context.Context, adapter string, req AdapterRequest, env []string, transport string) AdapterExecutionProfile {
	// Only a single boolean is read from the process environment. Do not
	// marshal AdapterRequest, env, or arbitrary command/config values.
	injected := false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "OPENPANDA_INJECTED_MODEL="); ok {
			injected = v == "1"
		}
	}
	bounded := func(s string) string {
		r := []rune(security.Redact(s))
		if len(r) > 256 {
			r = r[:256]
		}
		return string(r)
	}
	p := AdapterExecutionProfile{
		Version: 1, Observation: "adapter_request_not_native_confirmation",
		Agent: AgentName(ctx), Adapter: adapter, CWD: req.CWD, Transport: transport,
		ModelPreference: bounded(req.Model), ModelInjection: injected,
		RequestedEffort: bounded(req.Effort), NativeModel: "unknown", NativeEffort: "unknown",
		NativeConfig: "unverified", NativeVersion: "unknown",
		ToolsPolicy: req.ToolsPolicy, Restricted: req.Restricted, Authorized: req.Authorized,
		ResumeRequested: req.Resume != "", TimeoutS: req.TimeoutS, MaxTurns: req.MaxTurns,
	}
	policy := security.BasePolicy()
	p.SandboxMode, p.SandboxNetwork = string(policy.Mode), policy.AllowNetwork
	p.SandboxBackend = "off"
	if policy.Mode != security.ModeOff {
		p.SandboxBackend = security.Backend()
		if p.SandboxBackend == "" {
			p.SandboxBackend = "unavailable"
		}
	}
	p.ExternalToolPolicy = "unknown_not_controlled_by_panda_approval"
	p.NativeSettings = "inherited_unverified"
	p.NativeToolAccess = "native_unverified"
	if req.Restricted {
		p.NativeToolAccess = "restricted"
	} else if adapter == "claude_code.py" && req.ToolsPolicy != "extended" {
		p.NativeToolAccess = "minimal_allowlist"
	}
	if adapter == "claude_code.py" && injected {
		p.NativeSettings = "disabled_for_injection"
	}
	if path, err := adapterScript(adapter); err == nil {
		p.AdapterPath = path
		if data, err := os.ReadFile(path); err == nil {
			p.AdapterSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	}
	if k, ok := agents.Lookup(p.Agent, adapter); ok {
		c := k.Capabilities
		p.DeclaredCapabilities = &ProfileCapabilities{
			Restricted: c.SupportsRestricted, Session: c.SupportsSession,
			Skills: c.SupportsSkills, MCP: c.SupportsMCP, Subagents: c.SupportsSubagents,
			StructuredOutput: c.SupportsStructuredOutput,
		}
	}
	return p
}
