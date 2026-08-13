package ledger

import (
	"path/filepath"
	"testing"
)

// TestLoadCardWithAgents verifies multiple agents round-trip through the card
// file format (the second adapter, OpenCode, must coexist with claude_code).
func TestLoadCardWithAgents(t *testing.T) {
	c := testCard()
	c.Agents = map[string]Agent{
		"claude_code": {Adapter: "claude_code.py", Capabilities: []string{"code:modify"}},
		"opencode":    {Adapter: "opencode.py", Capabilities: []string{"web:search"}},
	}
	p := filepath.Join(t.TempDir(), "capabilities.yaml")
	writeCard(t, p, c)

	got, err := LoadCard(p)
	if err != nil {
		t.Fatalf("load card: %v", err)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(got.Agents))
	}
	if got.Agents["opencode"].Adapter != "opencode.py" {
		t.Fatalf("opencode adapter = %q", got.Agents["opencode"].Adapter)
	}
	if len(got.Agents["opencode"].Capabilities) != 1 || got.Agents["opencode"].Capabilities[0] != "web:search" {
		t.Fatalf("opencode capabilities = %v", got.Agents["opencode"].Capabilities)
	}
}
