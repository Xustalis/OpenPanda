package commander

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
)

// adapterDiscoversProjectMCP reports whether the CLI an adapter drives
// auto-discovers a project-level MCP config (.mcp.json) in its working
// directory. The answer comes from the registry's capability declaration
// (Capabilities.DiscoversProjectMCP), so the passthrough allowlist cannot
// drift from the manifest that describes the agent.
func adapterDiscoversProjectMCP(agent, adapter string) bool {
	k, ok := agents.Lookup(agent, adapter)
	return ok && k.Capabilities.DiscoversProjectMCP
}

// adapterMCPConfigFlag reports whether the agent's CLI takes an MCP config
// document on a flag (claude --mcp-config). When it does, the passthrough
// rides the adapter request — nothing touches the task's work directory.
// Lookup (name + adapter), not ByAdapter: generic.py serves many unrelated
// CLIs, so an adapter-only hit would lend the wrong entry's capabilities.
func adapterMCPConfigFlag(agent, adapter string) bool {
	k, ok := agents.Lookup(agent, adapter)
	return ok && k.Capabilities.MCPConfigFlag != ""
}

// mcpProjectFile is the project-level MCP config name the supported CLIs
// auto-discover in their cwd.
const mcpProjectFile = ".mcp.json"

// serverSpec is one MCP server entry in the passthrough document.
type serverSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// mcpPassthroughServers builds the server map the agent run may reach under
// the extended tools policy: "openpanda", the node's own self-management
// surface (`panda mcp` — skill install, task submission, queue/mesh
// status), and "panda", the operator-configured stdio server (mcp.command).
// Nil/empty unless the policy is extended and at least one server is
// enabled — the default minimal policy never widens the agent's tool face.
func (r *Router) mcpPassthroughServers() map[string]serverSpec {
	if r.toolsPolicy != "extended" {
		return nil
	}
	servers := map[string]serverSpec{}
	// The node's own tool surface rides the extended policy unless the
	// operator opted out — the agent can reach skills, queue and task
	// submission as MCP tools instead of scraping raw `panda` CLI output.
	if r.pandaTools {
		if exe, err := os.Executable(); err == nil && exe != "" {
			args := []string{"mcp"}
			if r.selfConfigPath != "" {
				args = append(args, "--config", r.selfConfigPath)
			}
			servers["openpanda"] = serverSpec{Command: exe, Args: args}
		}
	}
	if r.mcpCommand != "" {
		if parts := splitArgv(r.mcpCommand); len(parts) > 0 {
			var args []string
			if len(parts) > 1 {
				args = parts[1:]
			}
			servers["panda"] = serverSpec{Command: parts[0], Args: args}
		}
	}
	return servers
}

// mcpConfigDocument renders the servers map as the {"mcpServers":{…}}
// document an MCPConfigFlag-capable adapter passes to its CLI.
func mcpConfigDocument(servers map[string]serverSpec) string {
	cfg := struct {
		MCPServers map[string]serverSpec `json:"mcpServers"`
	}{MCPServers: servers}
	blob, err := json.Marshal(&cfg)
	if err != nil {
		return ""
	}
	return string(blob)
}

// wireMCPPassthrough routes the passthrough servers to the agent run and
// returns the run context plus a cleanup func. For a CLI that takes an MCP
// config flag (registry MCPConfigFlag) the document rides the adapter
// request — nothing is written into the task's work dir. For a CLI that
// only auto-discovers .mcp.json, the file materializes for the run's
// duration and cleanup removes it again: a plan stage's work directory
// becomes a content-addressed artifact for its successors, and a
// panda-owned MCP config has no business riding inside that tree.
func (r *Router) wireMCPPassthrough(ctx context.Context, agent, adapter, cwd string) (context.Context, func()) {
	noop := func() {}
	servers := r.mcpPassthroughServers()
	if len(servers) == 0 {
		return ctx, noop
	}
	if adapterMCPConfigFlag(agent, adapter) {
		return WithMCPConfig(ctx, mcpConfigDocument(servers)), noop
	}
	if !adapterDiscoversProjectMCP(agent, adapter) || cwd == "" {
		return ctx, noop
	}
	return ctx, materializeMCPFile(cwd, servers)
}

// materializeMCPFile writes the servers map as a project .mcp.json in cwd
// for the duration of one run and returns the cleanup that removes it
// again. An existing project config wins: panda never clobbers user
// content, and the passthrough is best-effort either way.
func materializeMCPFile(cwd string, servers map[string]serverSpec) func() {
	noop := func() {}
	blob := mcpConfigDocument(servers)
	if blob == "" {
		return noop
	}
	// The target is a fixed constant filename joined under the task's own
	// work dir; the containment check is defense in depth against a cwd that
	// ever resolves outside the intended tree.
	path := filepath.Clean(filepath.Join(cwd, mcpProjectFile))
	if !strings.HasPrefix(path, filepath.Clean(cwd)+string(os.PathSeparator)) {
		return noop
	}
	// Lstat, not Stat: a dangling symlink would fail Stat and then pass the
	// write straight through to its target outside the work dir.
	if _, err := os.Lstat(path); err == nil {
		return noop
	}
	if err := os.WriteFile(path, []byte(blob), 0o600); err != nil {
		return noop
	}
	return func() { _ = os.Remove(path) }
}

// splitArgv splits a command line into argv honoring single and double
// quotes (the same convention mcp.command documents: quotes honored). An
// empty input yields nil. Quotes are removed from the resulting tokens;
// escaped quotes inside a quoted run are kept literal.
func splitArgv(s string) []string {
	var parts []string
	var cur strings.Builder
	var quote rune
	inToken := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case r == ' ' || r == '\t':
			if inToken {
				parts = append(parts, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if inToken {
		parts = append(parts, cur.String())
	}
	return parts
}
