// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// The regression these tests pin: a run of workspace reads used to append
// every full result to the replayed transcript, so each later classification
// carried a bigger request than the last until the provider rejected it.

func TestTurnBudgetForWindow(t *testing.T) {
	cases := []struct {
		window, want int
	}{
		{0, toolTurnUnknownWindow},
		{-1, toolTurnUnknownWindow},
		{1000, toolTurnMinBudget},
		{4000, toolTurnMinBudget},
		{64000, 32000},
		{200000, toolTurnMaxBudget},
	}
	for _, tc := range cases {
		if got := turnBudgetForWindow(tc.window); got != tc.want {
			t.Errorf("turnBudgetForWindow(%d) = %d, want %d", tc.window, got, tc.want)
		}
	}
}

func TestBoundToolResult(t *testing.T) {
	short := "a short result"
	if got := boundToolResult(short, 1000); got != short {
		t.Fatalf("short result changed: %q", got)
	}
	if got := boundToolResult(short, 0); got != toolFoldMarker {
		t.Fatalf("limit 0 = %q, want fold marker", got)
	}
	// Mixed CJK/ASCII: slicing must land on rune boundaries, and the omitted
	// middle has to be visible to the model rather than a silent truncation.
	text := strings.Repeat("数", 5000) + "MIDDLE" + strings.Repeat("据", 5000)
	got := boundToolResult(text, 300)
	if !utf8.ValidString(got) {
		t.Fatal("boundToolResult produced invalid UTF-8")
	}
	if n := utf8.RuneCountInString(got); n > 300 {
		t.Fatalf("bounded result has %d runes, want <= 300", n)
	}
	if !strings.Contains(got, toolOmissionMarker) {
		t.Fatal("bounded result lost the omission marker")
	}
	if !strings.HasPrefix(got, strings.Repeat("数", 10)) || !strings.HasSuffix(got, strings.Repeat("据", 10)) {
		t.Fatal("bounded result lost its head or tail")
	}
	if strings.Contains(got, "MIDDLE") {
		t.Fatal("bounded result kept the middle it claims to omit")
	}
}

func toolResultTurn(id, content string) entry.Turn {
	return entry.Turn{Role: "user", Blocks: []entry.ContentBlock{
		{Type: "tool_result", ToolUseID: id, Content: content},
	}}
}

func TestFitToolTurnsShrinksOldestFirst(t *testing.T) {
	big := strings.Repeat("x", 5000)
	question := "what did you read?"
	answer := "here is what I found"
	original := []entry.Turn{
		{Role: "user", Content: question},
		toolResultTurn("1", big),
		toolResultTurn("2", big),
		toolResultTurn("3", big),
		{Role: "assistant", Content: answer},
	}
	out, freed := fitToolTurns(original, 12000)
	if freed <= 0 {
		t.Fatal("fitToolTurns freed nothing over budget")
	}
	if n := toolResultRunes(out); n > 12000 {
		t.Fatalf("tool results = %d runes, want <= 12000", n)
	}
	if !strings.Contains(out[1].Blocks[0].Content, toolOmissionMarker) {
		t.Fatal("oldest result was not compressed")
	}
	if out[2].Blocks[0].Content != big || out[3].Blocks[0].Content != big {
		t.Fatal("newer observations were compressed before the oldest one")
	}
	if out[0].Content != question || out[4].Content != answer {
		t.Fatal("user/assistant prose was touched")
	}
	// The caller's transcript is a snapshot: trimming must not mutate it.
	if original[1].Blocks[0].Content != big {
		t.Fatal("fitToolTurns mutated the caller's turns")
	}
}

func TestFitToolTurnsFoldsWhenStillOver(t *testing.T) {
	big := strings.Repeat("x", 5000)
	turns := []entry.Turn{
		toolResultTurn("1", big), toolResultTurn("2", big), toolResultTurn("3", big),
	}
	out, _ := fitToolTurns(turns, 10)
	if n := toolResultRunes(out); n > 500 {
		t.Fatalf("folded transcript still has %d tool-result runes", n)
	}
	for i, turn := range out {
		if turn.Blocks[0].Content != toolFoldMarker {
			t.Fatalf("turn %d was not folded: %q", i, turn.Blocks[0].Content[:min(80, len(turn.Blocks[0].Content))])
		}
	}
	// Folding leaves markers, never an empty transcript.
	if len(out) != len(turns) {
		t.Fatalf("fitToolTurns dropped turns: %d -> %d", len(turns), len(out))
	}
}

func TestFitToolTurnsIdempotent(t *testing.T) {
	big := strings.Repeat("x", 5000)
	turns := []entry.Turn{toolResultTurn("1", big), toolResultTurn("2", big)}
	once, firstFreed := fitToolTurns(turns, 6000)
	twice, secondFreed := fitToolTurns(once, 6000)
	if firstFreed <= 0 || secondFreed != 0 {
		t.Fatalf("freed = %d then %d, want >0 then 0", firstFreed, secondFreed)
	}
	if toolResultRunes(twice) != toolResultRunes(once) {
		t.Fatal("second pass changed an already-fitted transcript")
	}
}

func TestFitToolTurnsPlainTextToolResults(t *testing.T) {
	payload := strings.Repeat("中", 5000)
	ordinary := "an ordinary user message"
	turns := []entry.Turn{
		{Role: "user", Content: "工具结果：" + payload},
		{Role: "user", Content: "Tool result: " + payload},
		{Role: "user", Content: ordinary},
	}
	out, freed := fitToolTurns(turns, 2000)
	if freed <= 0 {
		t.Fatal("plain-text tool results were not trimmed")
	}
	if !strings.HasPrefix(out[0].Content, "工具结果：") || !strings.HasPrefix(out[1].Content, "Tool result: ") {
		t.Fatal("locale prefixes were lost")
	}
	if toolResultRunes(out) > 2000 {
		t.Fatalf("plain-text tool results = %d runes, want <= 2000", toolResultRunes(out))
	}
	if out[2].Content != ordinary {
		t.Fatal("a non-tool user message was trimmed")
	}
}

func TestFitToolTurnsPreservesToolUseArguments(t *testing.T) {
	args := map[string]any{"path": strings.Repeat("a", 8000)}
	turns := []entry.Turn{{Role: "assistant", Blocks: []entry.ContentBlock{
		{Type: "tool_use", ID: "u1", Name: "workspace_read", Input: args},
	}}}
	out, freed := fitToolTurns(turns, 10)
	if freed != 0 || len(out[0].Blocks[0].Input) != len(args) {
		t.Fatal("tool_use arguments were treated as replayed evidence")
	}
}

func TestResultAt(t *testing.T) {
	if got := resultAt(nil, 0); got != "" {
		t.Fatalf("resultAt(nil) = %q", got)
	}
	if got := resultAt([]string{"one"}, 5); got != "" {
		t.Fatalf("out-of-range resultAt = %q", got)
	}
	if got := resultAt([]string{"one"}, -1); got != "" {
		t.Fatalf("negative index resultAt = %q", got)
	}
	if got := resultAt([]string{"one"}, 0); got != "one" {
		t.Fatalf("resultAt = %q, want %q", got, "one")
	}
	big := resultAt([]string{strings.Repeat("数", 50000)}, 0)
	if n := utf8.RuneCountInString(big); n > toolResultHardCap {
		t.Fatalf("resultAt returned %d runes, cap is %d", n, toolResultHardCap)
	}
	if !utf8.ValidString(big) {
		t.Fatal("resultAt produced invalid UTF-8")
	}
}

func TestAppendToolCallsDefensive(t *testing.T) {
	// A nil call and a result slice shorter than the call list must not panic.
	out := appendToolCalls(nil, []*entry.ToolCall{
		nil,
		{ID: "u1", Tool: "workspace_read"},
	}, "", []string{"only-one"})
	if len(out) != 2 || len(out[1].Blocks) != 1 {
		t.Fatalf("native replay = %+v, want one assistant + one user turn", out)
	}
	if out[1].Blocks[0].Content != "" {
		t.Fatalf("missing result = %q, want empty", out[1].Blocks[0].Content)
	}
	// The id-less prose path has the same short-slice hazard.
	out = appendToolCalls(nil, []*entry.ToolCall{
		{Tool: "a"}, {Tool: "b"},
	}, "", []string{"one"})
	if len(out) != 4 {
		t.Fatalf("prose replay = %+v, want 4 turns", out)
	}
	if out[3].Content != "工具结果：" {
		t.Fatalf("missing result = %q, want the bare prefix", out[3].Content)
	}
	if !strings.HasPrefix(out[1].Content, "工具结果：") || !strings.HasPrefix(out[3].Content, "工具结果：") {
		t.Fatalf("prose replay lost its prefix: %+v", out)
	}
	// One oversized result is capped before it enters the transcript.
	big := strings.Repeat("x", 50000)
	out = appendToolCalls(nil, []*entry.ToolCall{{ID: "u2", Tool: "a"}}, "", []string{big})
	if n := utf8.RuneCountInString(out[1].Blocks[0].Content); n > toolResultHardCap {
		t.Fatalf("replayed result = %d runes, cap is %d", n, toolResultHardCap)
	}
}
