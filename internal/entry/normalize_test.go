package entry

import "testing"

// TestNormalizeTurnsMergesConsecutiveSameRole guards the wire contract that
// broke with a 400 storm: a session replay that doubled (or left dangling)
// the user turn used to send two consecutive user messages, which strict
// Messages-API providers reject outright. normalizeTurns must fold such
// plain-text runs into one message per role.
func TestNormalizeTurnsMergesConsecutiveSameRole(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "user", Content: "q2"},
	}
	got := turnsToMessages(turns)
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3 (consecutive user turns merged): %+v", len(got), got)
	}
	if got[2].Role != "user" || got[2].Content != "q2\n\nq2" {
		t.Fatalf("merged user message = %+v", got[2])
	}
}

// TestNormalizeTurnsLeavesBlockTurnsAlone: the tool_use/tool_result contract
// is exact by construction — a block-bearing turn must never be merged with
// its plain-text neighbour, or the tool linkage corrupts.
func TestNormalizeTurnsLeavesBlockTurnsAlone(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "q"},
		{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", ID: "t1", Name: "time_now", Input: map[string]any{}}}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: "ok"}}},
		{Role: "user", Content: "follow-up"},
	}
	got := normalizeTurns(turns)
	if len(got) != 4 {
		t.Fatalf("turns = %d, want 4 untouched: %+v", len(got), got)
	}
}

// TestTurnsToOpenAIMergesConsecutiveSameRole: the Chat Completions path gets
// the same guard — several OpenAI-compatible providers 400 on same-role runs
// too.
func TestTurnsToOpenAIMergesConsecutiveSameRole(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "q1"},
		{Role: "user", Content: "q1"},
	}
	got := turnsToOpenAI("sys", turns)
	// system + one merged user message.
	if len(got) != 2 || got[1].Role != "user" || got[1].Content != "q1\n\nq1" {
		t.Fatalf("openai messages = %+v", got)
	}
}
