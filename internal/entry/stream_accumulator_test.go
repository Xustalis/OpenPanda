// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func newAnthAccumulatorForTest() *anthAccumulator {
	return &anthAccumulator{}
}

func newOAIAccumulatorForTest() *oaiAccumulator {
	return &oaiAccumulator{}
}

func toolFragment(index int, name, arguments string) oaiToolCall {
	return oaiToolCall{Index: index, Function: oaiFunctionCall{Name: name, Arguments: arguments}}
}

func toolChunk(t testing.TB, calls ...oaiToolCall) oaiChunk {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}}}})
	if err != nil {
		t.Fatal(err)
	}
	var chunk oaiChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatal(err)
	}
	return chunk
}

func TestAnthAccumulatorInterleavedText(t *testing.T) {
	a := newAnthAccumulatorForTest()
	for _, i := range []int{0, 2} {
		a.start(&anthropicStreamEvent{Index: i, ContentBlock: ContentBlock{Type: "text"}})
	}
	var streamed strings.Builder
	for range 1000 {
		for _, item := range []struct {
			index int
			text  string
		}{{0, "一"}, {2, "two"}} {
			a.delta(&anthropicStreamEvent{Index: item.index, Delta: anthropicDelta{Type: "text_delta", Text: item.text}}, func(s string) { streamed.WriteString(s) }, nil)
		}
	}
	want := strings.Repeat("一", 1000) + strings.Repeat("two", 1000)
	first := a.result()
	if first.Text != want || streamed.String() != strings.Repeat("一two", 1000) {
		t.Fatal("text blocks or callback order were mixed")
	}
	a.delta(&anthropicStreamEvent{Index: 2, Delta: anthropicDelta{Type: "text_delta", Text: "!"}}, nil, nil)
	if got := a.result().Text; got != want+"!" || first.Text != want {
		t.Fatal("a later delta changed an earlier result")
	}
}

func TestOAIAccumulatorInterleavedArguments(t *testing.T) {
	a := newOAIAccumulatorForTest()
	first := toolFragment(1, "write_", "{\"text\":\"")
	first.ID = "call_b"
	start := toolChunk(t, first, toolFragment(0, "read_", "{\"path\":\""))
	a.feed(&start, nil, nil)
	part := toolChunk(t, toolFragment(0, "", "文"), toolFragment(1, "", "x"))
	for range 1000 {
		a.feed(&part, nil, nil)
	}
	end := toolChunk(t, toolFragment(1, "file", "\"}"), toolFragment(0, "file", "\"}"))
	a.feed(&end, nil, nil)
	got := a.result().ToolUses
	if len(got) != 2 || got[0].ID != "call_b" || got[0].Name != "write_file" || got[0].Input["text"] != strings.Repeat("x", 1000) || got[1].Name != "read_file" || got[1].Input["path"] != strings.Repeat("文", 1000) {
		t.Fatalf("interleaved tool calls lost their identity or arguments: %+v", got)
	}
	if got[1].ID != "call_0_read_file" {
		t.Fatalf("missing-ID fallback = %q", got[1].ID)
	}
}

var benchmarkStreamResponse Response

func BenchmarkStreamAccumulation(b *testing.B) {
	const fragment = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, size := range []int{16 << 10, 64 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("AnthropicText/%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				a := newAnthAccumulatorForTest()
				a.start(&anthropicStreamEvent{ContentBlock: ContentBlock{Type: "text"}})
				delta := anthropicStreamEvent{Delta: anthropicDelta{Type: "text_delta", Text: fragment}}
				for range size / len(fragment) {
					a.delta(&delta, nil, nil)
				}
				benchmarkStreamResponse = a.result()
			}
		})
		b.Run(fmt.Sprintf("OpenAIToolArguments/%dKiB", size>>10), func(b *testing.B) {
			start := toolChunk(b, toolFragment(0, "write_file", "{\"text\":\""))
			part := toolChunk(b, toolFragment(0, "", fragment))
			end := toolChunk(b, toolFragment(0, "", "\"}"))
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				a := newOAIAccumulatorForTest()
				a.feed(&start, nil, nil)
				for range size / len(fragment) {
					a.feed(&part, nil, nil)
				}
				a.feed(&end, nil, nil)
				benchmarkStreamResponse = a.result()
			}
		})
	}
}
