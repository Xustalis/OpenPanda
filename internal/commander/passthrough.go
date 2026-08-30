package commander

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// mcpPassthroughAdapters are the adapter scripts whose CLIs discover a
// project-level MCP config (.mcp.json) in their working directory. Only
// these receive the materialized config under the extended tools policy;
// every other agent's MCP story stays governed by its own config files.
var mcpPassthroughAdapters = map[string]bool{
	"claude_code.py": true,
}

// mcpProjectFile is the project-level MCP config name the supported CLIs
// auto-discover in their cwd.
const mcpProjectFile = ".mcp.json"

// materializeMCPPassthrough writes the node's configured MCP server into the
// agent's work directory as a project .mcp.json for the duration of one run,
// and returns the cleanup that removes it again. It is a no-op (returning a
// no-op cleanup) unless the extended tools policy is active, a server is
// configured, and the adapter's CLI actually discovers project MCP configs —
// the default minimal policy never widens the agent's tool face.
//
// The file is removed after the run rather than left behind: a plan stage's
// work directory becomes a content-addressed artifact for its successors,
// and a panda-owned MCP config has no business riding inside that tree.
func (r *Router) materializeMCPPassthrough(adapter, cwd string) func() {
	noop := func() {}
	if r.toolsPolicy != "extended" || r.mcpCommand == "" || cwd == "" {
		return noop
	}
	if !mcpPassthroughAdapters[adapter] {
		return noop
	}
	parts := splitArgv(r.mcpCommand)
	if len(parts) == 0 {
		return noop
	}
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}
	cfg := struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}{MCPServers: map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{
		"panda": {Command: parts[0], Args: args},
	}}
	blob, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return noop
	}
	// An existing project config (a repo's own .mcp.json) wins: panda never
	// clobbers user content, and the passthrough is best-effort either way.
	// The target is a fixed constant filename joined under the task's own
	// work dir; the containment check is defense in depth against a cwd that
	// ever resolves outside the intended tree.
	path := filepath.Clean(filepath.Join(cwd, mcpProjectFile))
	if !strings.HasPrefix(path, filepath.Clean(cwd)+string(os.PathSeparator)) {
		return noop
	}
	if _, err := os.Stat(path); err == nil {
		return noop
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
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
