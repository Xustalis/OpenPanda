package commander

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestContextOverflowPatterns pins the classification: provider window
// overflow qualifies, real task failures do not. A retry of an overflowed
// prompt cannot fit, so the upper layer must park, not re-run.
func TestContextOverflowPatterns(t *testing.T) {
	yes := []string{
		"API Error: 400 prompt is too long: 210000 tokens > 200000 maximum",
		"context_length_exceeded",
		"This model's maximum context length is 128000 tokens",
		"Request exceeds the context window",
		"Please reduce the length of your input",
	}
	no := []string{
		"command not found",
		"processed 5000 tokens of input data",
		"rate limit exceeded",
		"",
	}
	for _, s := range yes {
		if !ContextOverflow(s) {
			t.Errorf("should be overflow: %q", s)
		}
	}
	for _, s := range no {
		if ContextOverflow(s) {
			t.Errorf("should NOT be overflow: %q", s)
		}
	}
}

// TestSplitArgv covers the mcp.command argv splitter: quotes honored,
// whitespace collapsed, quote characters removed from tokens.
func TestSplitArgv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"npx -y @modelcontextprotocol/server-filesystem /tmp", []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"}},
		{`server --root "/path with space"`, []string{"server", "--root", "/path with space"}},
		{`server --name 'a b'`, []string{"server", "--name", "a b"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"", nil},
	}
	for _, c := range cases {
		got := splitArgv(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitArgv(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitArgv(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestMaterializeMCPPassthrough covers the policy gating and the file
// lifecycle: extended policy + configured server + discovering adapter writes
// a valid project .mcp.json and the cleanup removes it; minimal policy (the
// default) never writes; an existing project config is never clobbered.
func TestMaterializeMCPPassthrough(t *testing.T) {
	mk := func(policy string) *Router {
		r := NewRouter(testCard(), NewExecutor(), config.ModelConfig{},
			config.InjectionConfig{}, config.RoutingConfig{ToolsPolicy: policy})
		r.SetMCPPassthrough("npx -y @modelcontextprotocol/server-filesystem /tmp")
		return r
	}

	// Extended + claude: file appears, parses, cleanup removes it.
	dir := t.TempDir()
	r := mk("extended")
	cleanup := r.materializeMCPPassthrough("claude_code.py", dir)
	blob, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("extended policy should write .mcp.json: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		t.Fatalf(".mcp.json not JSON: %v", err)
	}
	srv := cfg.MCPServers["panda"]
	if srv.Command != "npx" || strings.Join(srv.Args, " ") != "-y @modelcontextprotocol/server-filesystem /tmp" {
		t.Fatalf("server materialized wrong: %+v", srv)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove .mcp.json, err=%v", err)
	}

	// Minimal policy: nothing written.
	dir = t.TempDir()
	cleanup = mk("minimal").materializeMCPPassthrough("claude_code.py", dir)
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("minimal policy must not write .mcp.json")
	}
	cleanup()

	// An adapter whose CLI does not discover project configs gets nothing.
	dir = t.TempDir()
	cleanup = mk("extended").materializeMCPPassthrough("codex.py", dir)
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("non-discovering adapter must not receive .mcp.json")
	}
	cleanup()

	// A repo's own .mcp.json wins: panda never clobbers user content.
	dir = t.TempDir()
	own := []byte(`{"mcpServers":{"repo":{"command":"own"}}}`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), own, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cleanup = mk("extended").materializeMCPPassthrough("claude_code.py", dir)
	if got, _ := os.ReadFile(filepath.Join(dir, ".mcp.json")); string(got) != string(own) {
		t.Fatalf("existing .mcp.json was clobbered: %s", got)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatalf("cleanup must not remove a user-owned .mcp.json: %v", err)
	}
}

// TestExtendedRunThreadsRequestAndMCP drives the REAL process path with the
// extended policy: the stub adapter reads the request JSON (resume +
// tools_policy must arrive), sees the .mcp.json present during the run, and
// returns usage + session_id that must propagate into Result. After Execute
// returns, the passthrough file is gone.
func TestExtendedRunThreadsRequestAndMCP(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()

	stub := `import json, os, sys
req = json.loads(sys.stdin.read())
seen = {
    "resume": req.get("resume", ""),
    "tools_policy": req.get("tools_policy", ""),
    "mcp_present": os.path.exists(os.path.join(os.getcwd(), ".mcp.json")),
}
print(json.dumps({"ok": True, "result": json.dumps(seen), "exit_code": 0,
                  "tokens": 9, "usage": {"input_tokens": 4, "output_tokens": 5},
                  "session_id": "sess-abc"}))
`
	if err := os.WriteFile(filepath.Join(dir, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}

	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{},
		config.InjectionConfig{}, config.RoutingConfig{ToolsPolicy: "extended"})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	r.SetMCPPassthrough("npx -y @modelcontextprotocol/server-filesystem /tmp")
	plan := tier1Plan(t, r)

	cwd := t.TempDir()
	ctx := WithResume(context.Background(), "sess-prev")
	res := r.Execute(ctx, plan, "do it", cwd, true)
	if !res.OK {
		t.Fatalf("exec failed: %+v", res)
	}
	var seen struct {
		Resume      string `json:"resume"`
		ToolsPolicy string `json:"tools_policy"`
		MCPPresent  bool   `json:"mcp_present"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &seen); err != nil {
		t.Fatalf("stub result: %v (%q)", err, res.Stdout)
	}
	if seen.Resume != "sess-prev" {
		t.Errorf("resume not threaded into request: %+v", seen)
	}
	if seen.ToolsPolicy != "extended" {
		t.Errorf("tools_policy not threaded into request: %+v", seen)
	}
	if !seen.MCPPresent {
		t.Errorf(".mcp.json missing during the extended run: %+v", seen)
	}
	if res.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want sess-abc", res.SessionID)
	}
	if res.Usage == nil || res.Usage.InputTokens != 4 || res.Usage.OutputTokens != 5 {
		t.Errorf("Usage not propagated: %+v", res.Usage)
	}
	if res.Tokens != 9 {
		t.Errorf("Tokens = %d, want 9", res.Tokens)
	}
	// The passthrough file never outlives the run (stage dirs become artifacts).
	if _, err := os.Stat(filepath.Join(cwd, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf(".mcp.json survived the run (err=%v)", err)
	}
}

// TestMinimalRunKeepsDefaultContract verifies the default (minimal) policy
// keeps the whitelist contract: no resume, tools_policy stays "minimal"
// (self-describing; adapters treat anything else as extended) and no
// .mcp.json is ever written.
func TestMinimalRunKeepsDefaultContract(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()

	stub := `import json, os, sys
req = json.loads(sys.stdin.read())
seen = {
    "resume": req.get("resume", ""),
    "tools_policy": req.get("tools_policy", ""),
    "mcp_present": os.path.exists(os.path.join(os.getcwd(), ".mcp.json")),
}
print(json.dumps({"ok": True, "result": json.dumps(seen), "exit_code": 0}))
`
	if err := os.WriteFile(filepath.Join(dir, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}

	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{},
		config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	r.SetMCPPassthrough("npx -y @modelcontextprotocol/server-filesystem /tmp")
	plan := tier1Plan(t, r)

	cwd := t.TempDir()
	res := r.Execute(context.Background(), plan, "do it", cwd, true)
	if !res.OK {
		t.Fatalf("exec failed: %+v", res)
	}
	var seen struct {
		Resume      string `json:"resume"`
		ToolsPolicy string `json:"tools_policy"`
		MCPPresent  bool   `json:"mcp_present"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &seen); err != nil {
		t.Fatalf("stub result: %v", err)
	}
	if seen.Resume != "" || seen.ToolsPolicy != "minimal" {
		t.Errorf("minimal run must send no resume and a minimal policy: %+v", seen)
	}
	if seen.MCPPresent {
		t.Errorf("minimal run must not materialize .mcp.json")
	}
}
