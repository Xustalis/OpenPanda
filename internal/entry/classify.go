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
	system := BuildPrompt(PromptOptions{Devices: devices, Memory: memory})
	raw, err := c.Complete(ctx, system, user)
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
