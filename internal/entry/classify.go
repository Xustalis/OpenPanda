package entry

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/xenith/openpanda/internal/ledger"
)

// Classify runs the unified entry model once with no tools and returns the
// parsed Output. It is the answer/fallback entry point; the tool path is
// ClassifyTurnsWithTools.
func Classify(ctx context.Context, c *Client, devices []ledger.Node, memory, user string) (Output, error) {
	return ClassifyTurnsWithTools(ctx, c, devices, memory, []Turn{{Role: "user", Content: user}}, nil)
}

// ClassifyTurns is Classify with a conversation history and no tools.
func ClassifyTurns(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn) (Output, error) {
	return ClassifyTurnsWithTools(ctx, c, devices, memory, turns, nil)
}

// ClassifyTurnsWithTools runs the entry model with a conversation history and a
// tool registry. A native tool_use response becomes a KindToolCall output; a
// text response falls through to the existing JSON/prose parsing (answer/task).
func ClassifyTurnsWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry) (Output, error) {
	system := BuildPrompt(PromptOptions{Devices: devices, Memory: memory})
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, specs)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	return resolveResponse(resp)
}

// ClassifyStreamWithTools is ClassifyTurnsWithTools with live text streaming:
// answer deltas are delivered to onDelta as the provider emits them, while the
// parsed Output is returned once complete.
func ClassifyStreamWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry, onDelta func(string)) (Output, error) {
	system := BuildPrompt(PromptOptions{Devices: devices, Memory: memory})
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.StreamTurnsWithTools(ctx, system, turns, specs, onDelta)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	return resolveResponse(resp)
}

// resolveResponse routes one completed model response: a native tool_use is
// authoritative (route to the registry); text falls through to the JSON/prose
// parser (answer/task).
func resolveResponse(resp Response) (Output, error) {
	// A tool_use is authoritative: the model chose a controlled tool, so route to
	// the registry rather than the text parser.
	if len(resp.ToolUses) > 0 {
		tu := resp.ToolUses[0]
		out := Output{Kind: KindToolCall, Tool: &ToolCall{ID: tu.ID, Tool: tu.Name, Arguments: tu.Input}}
		if note := droppedToolNote(resp); note != "" {
			out.Note = note
		}
		return out, nil
	}
	out, err := ParseOutput(resp.Text)
	if err != nil {
		// A validation failure on a structured output is a model error; surface
		// it rather than degrading silently, so the user can retry.
		return Output{}, &ClassifyError{
			UserMsg: "模型输出校验失败：" + err.Error(),
			Err:     err,
		}
	}
	if out.Kind == KindAnswer && resp.Truncated {
		// The provider stopped at max_tokens; mark the answer so the user knows
		// it is incomplete rather than silently passing a cut-off reply through.
		out.Answer += "\n\n[回答因长度上限被截断]"
	}
	return out, nil
}

// droppedToolNote captures the content the executor will not act on — text the
// model emitted alongside a tool call, and any tool_use after the first — so
// the ask loop can surface it to the user and replay it to the model instead of
// silently discarding it.
func droppedToolNote(resp Response) string {
	var parts []string
	if resp.Text != "" {
		parts = append(parts, resp.Text)
	}
	for _, extra := range resp.ToolUses[1:] {
		keys := make([]string, 0, len(extra.Input))
		for k := range extra.Input {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var args []string
		for _, k := range keys {
			args = append(args, fmt.Sprintf("%s=%v", k, extra.Input[k]))
		}
		parts = append(parts, fmt.Sprintf("tool %s(%s)", extra.Name, strings.Join(args, ", ")))
	}
	return strings.Join(parts, "\n")
}
