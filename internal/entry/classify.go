package entry

import (
	"context"

	"github.com/xenith/panda/internal/ledger"
)

// Classify runs the unified entry model once with no tools and returns the
// parsed Output. It is the answer/fallback entry point; the tool path is
// ClassifyTurnsWithTools.
func Classify(ctx context.Context, c *Client, devices []ledger.Node, memory, user string) (Output, error) {
	return classify(ctx, c, devices, memory, []Turn{{Role: "user", Content: user}}, nil)
}

// ClassifyTurns is Classify with a conversation history and no tools.
func ClassifyTurns(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn) (Output, error) {
	return ClassifyTurnsWithTools(ctx, c, devices, memory, turns, nil)
}

// ClassifyTurnsWithTools runs the entry model with a conversation history and a
// tool registry. A native tool_use response becomes a KindToolCall output; a
// text response falls through to the existing JSON/prose parsing (answer/task).
func ClassifyTurnsWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry) (Output, error) {
	return classify(ctx, c, devices, memory, turns, registry)
}

func classify(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry) (Output, error) {
	system := BuildPrompt(PromptOptions{Devices: devices, Memory: memory})
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, specs)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	// A tool_use is authoritative: the model chose a controlled tool, so route to
	// the registry rather than the text parser.
	if len(resp.ToolUses) > 0 {
		tu := resp.ToolUses[0]
		return Output{Kind: KindToolCall, Tool: &ToolCall{ID: tu.ID, Tool: tu.Name, Arguments: tu.Input}}, nil
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
