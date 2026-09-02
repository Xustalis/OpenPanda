package entry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- OpenAI Chat Completions wire types (/v1/chat/completions) ----

// oaiRequest is the Chat Completions request body. ToolChoice is omitted: the
// API defaults to "auto" when tools are present, mirroring the Anthropic path.
// PromptCacheKey and StreamOptions are the provider-native prompt-cache /
// usage hints, attached only while prompt caching is enabled so a strict
// oaiRequest is the Chat Completions request body. ToolChoice is omitted: the
// API defaults to "auto" when tools are present, mirroring the Anthropic path.
// PromptCacheKey and StreamOptions are the provider-native prompt-cache /
// usage hints, attached only while prompt caching is enabled so a strict
// legacy provider can opt out (SetPromptCaching(false)).
// MaxCompletionTokens is used for OpenAI reasoning models (o1/o3 series) which
// reject max_tokens.
type oaiRequest struct {
	Model               string            `json:"model"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	Messages            []oaiMessage      `json:"messages"`
	Tools               []oaiTool         `json:"tools,omitempty"`
	PromptCacheKey      string            `json:"prompt_cache_key,omitempty"`
	StreamOptions       *oaiStreamOptions `json:"stream_options,omitempty"`
}

// buildOAIRequest constructs an oaiRequest, automatically choosing between
// max_completion_tokens (for o1/o3 reasoning models) and max_tokens.
func buildOAIRequest(model string, maxTokens int, stream bool, msgs []oaiMessage, tools []ToolSpec, promptCacheKey string) oaiRequest {
	req := oaiRequest{
		Model:          model,
		Stream:         stream,
		Messages:       msgs,
		PromptCacheKey: promptCacheKey,
	}
	if isOpenAIReasoningModel(model) {
		req.MaxCompletionTokens = maxTokens
	} else {
		req.MaxTokens = maxTokens
	}
	if len(tools) > 0 {
		req.Tools = specsToOpenAI(tools)
	}
	if stream {
		req.StreamOptions = &oaiStreamOptions{IncludeUsage: true}
	}
	return req
}

// isOpenAIReasoningModel reports whether model requires max_completion_tokens (o1/o3/o4/gpt-4.5 series).
func isOpenAIReasoningModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4") || strings.Contains(lower, "gpt-4.5")
}

// oaiStreamOptions asks OpenAI-compatible providers to include a final usage
// chunk in streamed responses (include_usage), so token consumption is
// visible without a second request.
type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaiUsage is the Chat Completions token-usage block.
type oaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// oaiMessage is one Chat Completions message: a plain system/user/assistant
// turn, an assistant turn carrying tool_calls, or a tool result.
// ReasoningContent is the DeepSeek thinking-passback field: set on outgoing
// assistant messages only once the provider has demanded it (see
// injectReasoningPassback), empty otherwise.
type oaiMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
}

type oaiTool struct {
	Type     string      `json:"type"` // "function"
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaiToolCall struct {
	Index    int             `json:"index"`
	ID       string          `json:"id"`
	Type     string          `json:"type"` // "function"
	Function oaiFunctionCall `json:"function"`
	// Arguments is a JSON-encoded string in both directions.
	Arguments string `json:"arguments"`
}

type oaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

// oaiResponse is the non-streaming response body. The message's
// reasoning_content / reasoning / thought / thinking fields (across DeepSeek-R1,
// Qwen, Ollama, SiliconFlow, Gemini proxies, Groq, and relays) are captured onto
// Response.Reasoning for display only — surfaced to a reasoning sink, never
// merged into the answer or session history (D14).
type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"`
			Reasoning        string        `json:"reasoning"`
			Thought          string        `json:"thought"`
			Thinking         string        `json:"thinking"`
			ToolCalls        []oaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage,omitempty"`
	Error *oaiError `json:"error,omitempty"`
}

type oaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// oaiChunk is one streamed chunk: choices[0].delta carries either text or
// incremental tool_call fragments addressed by index. Reasoning-bearing
// providers stream chain-of-thought on delta.reasoning_content / delta.reasoning
// / delta.thought / delta.thinking — surfaced live to the reasoning sink and
// kept off the answer (D14). The final chunk (with stream_options.include_usage)
// carries the usage block and empty choices.
type oaiChunk struct {
	Choices []struct {
		Delta struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"`
			Reasoning        string        `json:"reasoning"`
			Thought          string        `json:"thought"`
			Thinking         string        `json:"thinking"`
			ToolCalls        []oaiToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage,omitempty"`
	Error *oaiError `json:"error,omitempty"`
}

// turnsToOpenAI converts the internal Turn history into Chat Completions
// messages: the system prompt becomes the leading system message; a Turn with
// Blocks becomes an assistant tool_calls message followed by tool result
// messages (one per tool_result block); plain turns pass through.
func turnsToOpenAI(system string, turns []Turn) []oaiMessage {
	msgs := []oaiMessage{{Role: "system", Content: system}}
	for _, t := range normalizeTurns(turns) {
		if len(t.Blocks) == 0 {
			msgs = append(msgs, oaiMessage{Role: t.Role, Content: t.Content})
			continue
		}
		var assistant oaiMessage
		assistant.Role = "assistant"
		var results []oaiMessage
		for _, b := range t.Blocks {
			switch b.Type {
			case "text":
				assistant.Content += b.Text
			case "tool_use":
				args, _ := json.Marshal(b.Input)
				assistant.ToolCalls = append(assistant.ToolCalls, oaiToolCall{
					ID:       b.ID,
					Type:     "function",
					Function: oaiFunctionCall{Name: b.Name, Arguments: string(args)},
				})
			case "tool_result":
				results = append(results, oaiMessage{
					Role:       "tool",
					ToolCallID: b.ToolUseID,
					Content:    b.Content,
				})
			}
		}
		if assistant.ToolCalls != nil || assistant.Content != "" {
			msgs = append(msgs, assistant)
		}
		msgs = append(msgs, results...)
	}
	return msgs
}

// injectReasoningPassback is the Chat Completions counterpart of
// injectThinkingPassback: every assistant message gains a placeholder
// reasoning_content, the passback shape DeepSeek's Chat Completions route
// demands in thinking mode. Other roles pass through untouched.
func injectReasoningPassback(msgs []oaiMessage) []oaiMessage {
	out := make([]oaiMessage, len(msgs))
	for i, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent == "" {
			m.ReasoningContent = "."
		}
		out[i] = m
	}
	return out
}

// specsToOpenAI converts internal tool specs into Chat Completions tools.
func specsToOpenAI(specs []ToolSpec) []oaiTool {
	out := make([]oaiTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  s.InputSchema,
			},
		})
	}
	return out
}

// parseOpenAIResponse reduces a non-streaming response to the internal shape.
// The content passes stripThinkingBlock: a reasoner or relay that inlines
// chain-of-thought into content as  tags must not leak it into the
// answer, the session history, or a task result (D14).
func parseOpenAIResponse(r *oaiResponse) Response {
	var out Response
	if len(r.Choices) == 0 {
		return out
	}
	c := r.Choices[0]
	out.Text = stripThinkingBlock(c.Message.Content)
	// Chain-of-thought from the separate reasoning field is display-only (D14):
	// captured on Response.Reasoning, never merged into Text.
	switch {
	case c.Message.ReasoningContent != "":
		out.Reasoning = c.Message.ReasoningContent
	case c.Message.Reasoning != "":
		out.Reasoning = c.Message.Reasoning
	case c.Message.Thought != "":
		out.Reasoning = c.Message.Thought
	case c.Message.Thinking != "":
		out.Reasoning = c.Message.Thinking
	}
	for _, tc := range c.Message.ToolCalls {
		var input map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				input = map[string]any{"_raw": tc.Function.Arguments}
			}
		}
		id := tc.ID
		if id == "" {
			id = "call_" + tc.Function.Name
		}
		out.ToolUses = append(out.ToolUses, ToolUse{ID: id, Name: tc.Function.Name, Input: input})
	}
	out.Truncated = c.FinishReason == "length"
	return out
}

// oaiAccumulator assembles a Response from streamed chunks. Tool-call
// fragments arrive split across chunks and addressed by index; Arguments
// strings are concatenated until finish. Usage arrives once, in the final
// chunk, and is accumulated per attempt (the caller bills it only when the
// stream completes, so a retried attempt is never double-counted).
type oaiAccumulator struct {
	texts     []string
	reasoning []string
	calls     map[int]oaiToolCall
	order     []int
	truncated bool
	usageIn   int64
	usageOut  int64
}

func (a *oaiAccumulator) feed(chunk *oaiChunk, onDelta func(string), onReasoning func(string)) {
	if chunk.Usage != nil {
		a.usageIn = chunk.Usage.PromptTokens
		a.usageOut = chunk.Usage.CompletionTokens
	}
	if len(chunk.Choices) == 0 {
		return
	}
	c := chunk.Choices[0]
	// Chain-of-thought arrives on separate reasoning fields:
	// reasoning_content, reasoning, thought, or thinking.
	r := c.Delta.ReasoningContent
	if r == "" {
		r = c.Delta.Reasoning
	}
	if r == "" {
		r = c.Delta.Thought
	}
	if r == "" {
		r = c.Delta.Thinking
	}
	if r != "" {
		a.reasoning = append(a.reasoning, r)
		if onReasoning != nil {
			onReasoning(r)
		}
	}
	if c.Delta.Content != "" {
		a.texts = append(a.texts, c.Delta.Content)
		if onDelta != nil {
			onDelta(c.Delta.Content)
		}
	}
	for _, tc := range c.Delta.ToolCalls {
		cur, ok := a.calls[tc.Index]
		if !ok {
			cur = oaiToolCall{Index: tc.Index, Type: "function"}
			a.calls[tc.Index] = cur
			a.order = append(a.order, tc.Index)
		}
		if tc.ID != "" {
			cur.ID = tc.ID
		}
		if tc.Function.Name != "" {
			cur.Function.Name += tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			cur.Function.Arguments += tc.Function.Arguments
		}
		a.calls[tc.Index] = cur
	}
	if c.FinishReason != nil && *c.FinishReason == "length" {
		a.truncated = true
	}
}

func (a *oaiAccumulator) result() Response {
	var out Response
	out.Text = strings.Join(a.texts, "")
	out.Reasoning = strings.Join(a.reasoning, "")
	out.Truncated = a.truncated
	for _, idx := range a.order {
		tc := a.calls[idx]
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d_%s", idx, tc.Function.Name)
		}
		var input map[string]any
		if s := tc.Function.Arguments; s != "" {
			if err := json.Unmarshal([]byte(s), &input); err != nil {
				input = map[string]any{"_raw": s}
			}
		}
		out.ToolUses = append(out.ToolUses, ToolUse{ID: id, Name: tc.Function.Name, Input: input})
	}
	return out
}
