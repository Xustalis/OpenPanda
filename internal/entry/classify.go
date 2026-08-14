package entry

import (
	"context"

	"github.com/xenith/panda/internal/ledger"
)

// Classify runs the unified entry model once and returns the parsed Output.
// It is the single entry point for the entry layer: build the prompt, call the
// model (non-streaming, so answer/tool_call/task all share one path), and
// validate the structured output. Errors are wrapped as *ClassifyError for a
// friendly message; the raw error is preserved for logging.
func Classify(ctx context.Context, c *Client, devices []ledger.Node, memory, user string) (Output, error) {
	return classify(ctx, c, devices, memory, []Turn{{Role: "user", Content: user}})
}

// ClassifyTurns is Classify with a conversation history, so a tool result can
// be fed back for another round (e.g. the memory-merge loop: read, then
// replace/add). The turns carry the prior user/assistant messages.
func ClassifyTurns(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn) (Output, error) {
	return classify(ctx, c, devices, memory, turns)
}

func classify(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn) (Output, error) {
	system := BuildPrompt(PromptOptions{Devices: devices, Memory: memory})
	raw, err := c.CompleteTurns(ctx, system, turns)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	out, err := ParseOutput(raw)
	if err != nil {
		// A validation failure on a structured output is a model error; surface
		// it rather than degrading silently, so the user can retry.
		return Output{}, &ClassifyError{
			UserMsg: "模型输出校验失败：" + err.Error(),
			Err:     err,
		}
	}
	return out, nil
}
