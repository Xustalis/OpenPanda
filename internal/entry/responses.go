// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

// OpenAI Responses API (/responses).
//
// The Responses API is OpenAI's newer wire surface: the system prompt travels
// as `instructions`, the history as typed input items (messages, function_call
// and function_call_output), and tools are flat function declarations rather
// than Chat Completions' nested {"type":"function","function":{…}} objects.
// The GPT-5/6 family and the Grok/Muse models on OpenCode's gateway are served
// there only — a Chat Completions request against those ids is rejected.
//
// Streaming is SSE with semantic event types: text arrives as
// response.output_text.delta, reasoning summaries as
// response.reasoning_summary_text.delta, tool arguments as
// response.function_call_arguments.delta, and usage on the final
// response.completed envelope. Unlike Chat Completions there is no [DONE]
// terminator promised by every compatible server, so the scanner treats it as
// optional.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// responsesRequest is the Responses API request body. Store is pinned false:
// OpenAI otherwise retains the response server-side, and panda's history is
// the node's own business.
type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []any               `json:"input"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Stream          bool                `json:"stream,omitempty"`
	Tools           []responsesTool     `json:"tools,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	PromptCacheKey  string              `json:"prompt_cache_key,omitempty"`
	Store           bool                `json:"store"`
}

// responsesTool is one Responses function declaration — flat, unlike the Chat
// Completions wrapper.
type responsesTool struct {
	Type        string         `json:"type"` // "function"
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// responsesReasoning carries the reasoning-effort knob; the Responses API has
// no thinking/enable_thinking flag.
type responsesReasoning struct {
	Effort string `json:"effort,omitempty"` // low | medium | high
}

// responsesMessage is one role-tagged input item. Assistant history uses
// output_text parts, every other role input_text.
type responsesMessage struct {
	Type    string             `json:"type"` // "message"
	Role    string             `json:"role"`
	Content []responsesContent `json:"content"`
}

// responsesContent is one content part in a request message or a response
// item. output_text carries the answer; refusal carries a safety refusal.
type responsesContent struct {
	Type    string `json:"type"` // input_text | output_text | refusal
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

// responsesFunctionCall is an assistant tool call in the input history.
type responsesFunctionCall struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

// responsesFunctionOutput is one tool result in the input history, linked to
// its call by call_id.
type responsesFunctionOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// responsesResponse is the non-streaming body (and the payload embedded in a
// response.completed stream event).
type responsesResponse struct {
	Status            string               `json:"status"` // completed | incomplete | failed | in_progress
	Output            []responsesItem      `json:"output"`
	Usage             *responsesUsage      `json:"usage"`
	IncompleteDetails *responsesIncomplete `json:"incomplete_details"`
	Error             *responsesError      `json:"error"`
}

type responsesItem struct {
	Type      string             `json:"type"` // message | reasoning | function_call
	ID        string             `json:"id"`
	Role      string             `json:"role"`
	Content   []responsesContent `json:"content"`
	Summary   []responsesSummary `json:"summary"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
}

type responsesSummary struct {
	Type string `json:"type"` // summary_text
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"` // max_output_tokens | content_filter
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// responsesStreamEvent is one SSE data payload. Only the fields panda acts on
// are modeled; every event carries its own type, so the `event:` line is not
// needed.
type responsesStreamEvent struct {
	Type        string             `json:"type"`
	Delta       string             `json:"delta"`
	ItemID      string             `json:"item_id"`
	OutputIndex int                `json:"output_index"`
	Item        *responsesItem     `json:"item"`
	Response    *responsesResponse `json:"response"`
	Message     string             `json:"message"`
	Error       *responsesError    `json:"error"`
	// Refusal rides on response.refusal.done; the .delta events carry it in
	// Delta. Non-streaming puts the same text in message content of type
	// "refusal".
	Refusal string `json:"refusal"`
}

// buildResponsesRequest assembles one request from the internal turns.
func (c *Client) buildResponsesRequest(system string, turns []Turn, tools []ToolSpec, stream bool) responsesRequest {
	req := responsesRequest{
		Model:           c.model,
		Instructions:    system,
		Input:           turnsToResponses(turns),
		MaxOutputTokens: c.maxTokens,
		Stream:          stream,
		Store:           false,
	}
	if len(tools) > 0 {
		req.Tools = specsToResponses(tools)
	}
	if c.promptCache.Load() {
		req.PromptCacheKey = c.oaiPromptCacheKey(system)
	}
	c.applyResponsesThinking(&req)
	return req
}

// applyResponsesThinking maps the configured mode onto reasoning effort. An
// unset mode sends nothing (the provider default applies); a provider that
// rejected the field once gets nothing either (thinkingDenied is sticky, set
// by the shared retry ladder).
func (c *Client) applyResponsesThinking(req *responsesRequest) {
	if c.thinkingDenied.Load() || c.thinking == "" {
		return
	}
	effort := "medium"
	switch c.thinking {
	case "on":
		effort = "high"
	case "off":
		effort = "low"
	}
	req.Reasoning = &responsesReasoning{Effort: effort}
}

// turnsToResponses converts the internal history into Responses input items:
// a plain turn becomes one message, a block-bearing turn becomes an optional
// assistant message followed by its function_call items and then the
// function_call_output items that answer them.
func turnsToResponses(turns []Turn) []any {
	turns = normalizeTurns(turns)
	items := make([]any, 0, len(turns))
	for _, t := range turns {
		if len(t.Blocks) == 0 {
			if t.Content == "" {
				continue
			}
			items = append(items, responsesMessage{
				Type:    "message",
				Role:    t.Role,
				Content: []responsesContent{{Type: responsesTextType(t.Role), Text: t.Content}},
			})
			continue
		}
		var msg responsesMessage
		msg.Type = "message"
		msg.Role = t.Role
		var calls []responsesFunctionCall
		var outputs []responsesFunctionOutput
		for _, b := range t.Blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					msg.Content = append(msg.Content, responsesContent{Type: responsesTextType(t.Role), Text: b.Text})
				}
			case "tool_use":
				input := b.Input
				if input == nil {
					input = map[string]any{}
				}
				args, _ := json.Marshal(input)
				calls = append(calls, responsesFunctionCall{
					Type:      "function_call",
					CallID:    b.ID,
					Name:      b.Name,
					Arguments: string(args),
				})
			case "tool_result":
				outputs = append(outputs, responsesFunctionOutput{
					Type:   "function_call_output",
					CallID: b.ToolUseID,
					Output: b.Content,
				})
			}
		}
		if len(msg.Content) > 0 {
			items = append(items, msg)
		}
		for _, call := range calls {
			items = append(items, call)
		}
		for _, out := range outputs {
			items = append(items, out)
		}
	}
	return items
}

// responsesTextType picks the content-part type a role's history uses.
func responsesTextType(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}

// specsToResponses converts internal tool specs into flat Responses tools.
func specsToResponses(specs []ToolSpec) []responsesTool {
	out := make([]responsesTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, responsesTool{
			Type:        "function",
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.InputSchema,
		})
	}
	return out
}

// responsesURL normalizes a base URL into the full responses endpoint, the
// same way openAIURL does for chat completions.
func responsesURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "https://api.openai.com/v1/responses"
	}
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	base = strings.TrimSuffix(base, "/chat/completions")
	if strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/v2") ||
		strings.HasSuffix(base, "/v3") || strings.HasSuffix(base, "/v4") {
		return base + "/responses"
	}
	return base + "/v1/responses"
}

// completeResponses runs one non-streaming Responses call with retry. The
// Responses API has no thinking passback contract, so only the shared thinking
// probe applies.
func (c *Client) completeResponses(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	return c.completeWithRetryFn(ctx, func() (Response, error) {
		return c.completeOnceResponses(ctx, system, turns, tools)
	}, nil)
}

func (c *Client) completeOnceResponses(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	payload, err := c.marshalWithParams(c.buildResponsesRequest(system, turns, tools, false))
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if err := c.applyAuth(ctx, httpReq); err != nil {
		return Response{}, err
	}
	c.applyExtraHeaders(ctx, httpReq)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes))
	if err != nil {
		return Response{}, &transientError{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &statusError{status: resp.StatusCode, body: string(body)}
	}

	var rr responsesResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if rr.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", rr.Error.Message)
	}
	if rr.Usage != nil {
		c.addUsage(rr.Usage.InputTokens, rr.Usage.OutputTokens)
	}
	return parseResponsesResponse(&rr), nil
}

// parseResponsesResponse reduces the output items to text, reasoning and tool
// calls. A refusal part is surfaced as the answer: it is the model's own
// explanation of why it declined, and hiding it leaves the caller with an
// empty result.
func parseResponsesResponse(r *responsesResponse) Response {
	var out Response
	var texts, reasoning []string
	for i, item := range r.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text", "text":
					texts = append(texts, c.Text)
				case "refusal":
					if c.Refusal != "" {
						texts = append(texts, c.Refusal)
					} else if c.Text != "" {
						texts = append(texts, c.Text)
					}
				}
			}
		case "reasoning":
			for _, s := range item.Summary {
				if s.Text != "" {
					reasoning = append(reasoning, s.Text)
				}
			}
		case "function_call":
			out.ToolUses = append(out.ToolUses, responsesToolUse(item.CallID, item.Name, item.Arguments, i))
		}
	}
	out.Text = stripThinkingBlock(strings.Join(texts, ""))
	out.Reasoning = strings.Join(reasoning, "")
	out.Truncated = r.Status == "incomplete" ||
		(r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens")
	return out
}

// responsesToolUse builds one internal tool use from a Responses function call.
// seq disambiguates two parallel calls of the same name when the API omitted
// call ids: "call_<name>_<seq>" stays unique, where "call_<name>" gave the
// second call a duplicate id and the next request's input a 400.
func responsesToolUse(callID, name, arguments string, seq int) ToolUse {
	if callID == "" {
		callID = fmt.Sprintf("call_%s_%d", name, seq)
	}
	var input map[string]any
	if s := strings.TrimSpace(arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &input); err != nil {
			input = map[string]any{"_raw": s}
		}
	}
	return ToolUse{ID: callID, Name: name, Input: input}
}

// responsesCall is one tool call under construction in a stream.
type responsesCall struct {
	id   string
	name string
	args strings.Builder
}

// responsesAccumulator assembles a Response from streamed events. Text and
// reasoning arrive as deltas; a function call's arguments may arrive as deltas
// and are finalized by its output_item.done envelope.
type responsesAccumulator struct {
	texts     []string
	reasoning []string
	// refusal is the model's own statement of why it declined; it is folded
	// into the answer, because an empty answer reads as a failed generation.
	refusal         []string
	refusalStreamed bool
	calls           map[string]*responsesCall
	order           []string
	truncated       bool
	usageIn         int64
	usageOut        int64
	// fallback holds the completion envelope's own output items. Deltas and
	// the envelope describe the same output, but a proxy may forward only one
	// of the two, so result() fills each field the deltas left empty.
	fallback *Response
}

// call returns (creating on first sight) the call a fragment belongs to.
func (a *responsesAccumulator) call(key string) *responsesCall {
	if c, ok := a.calls[key]; ok {
		return c
	}
	c := &responsesCall{}
	a.calls[key] = c
	a.order = append(a.order, key)
	return c
}

// feed folds one stream event in. It returns an error for the explicit
// failure envelopes so the caller stops the stream and surfaces the reason.
func (a *responsesAccumulator) feed(ev *responsesStreamEvent, onDelta, onReasoning func(string)) error {
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta != "" {
			a.texts = append(a.texts, ev.Delta)
			if onDelta != nil {
				onDelta(ev.Delta)
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if ev.Delta != "" {
			a.reasoning = append(a.reasoning, ev.Delta)
			if onReasoning != nil {
				onReasoning(ev.Delta)
			}
		}
	case "response.refusal.delta":
		// A refusal is the model's own explanation of why it declined, and
		// the non-streaming path surfaces it as the answer. Dropping it here
		// left the streamed result empty, which the caller reads as "the model
		// said nothing" — and pays for a full replay to hear it again.
		if ev.Delta != "" {
			a.refusal = append(a.refusal, ev.Delta)
			a.refusalStreamed = true
			if onDelta != nil {
				onDelta(ev.Delta)
			}
		}
	case "response.refusal.done":
		// The done event repeats the whole refusal; take it only when no
		// delta arrived, so the text is never doubled.
		if !a.refusalStreamed && ev.Refusal != "" {
			a.refusal = append(a.refusal, ev.Refusal)
			if onDelta != nil {
				onDelta(ev.Refusal)
			}
		}
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			c := a.call(responsesEventKey(ev))
			if ev.Item.CallID != "" {
				c.id = ev.Item.CallID
			}
			if ev.Item.Name != "" {
				c.name = ev.Item.Name
			}
		}
	case "response.function_call_arguments.delta":
		a.call(responsesEventKey(ev)).args.WriteString(ev.Delta)
	case "response.output_item.done":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			c := a.call(responsesEventKey(ev))
			if ev.Item.CallID != "" {
				c.id = ev.Item.CallID
			}
			if ev.Item.Name != "" {
				c.name = ev.Item.Name
			}
			if ev.Item.Arguments != "" {
				// The done envelope is authoritative; the accumulated deltas
				// are the same string and must not be appended twice.
				c.args.Reset()
				c.args.WriteString(ev.Item.Arguments)
			}
		}
	case "response.completed", "response.incomplete":
		if ev.Response != nil {
			a.noteResponse(ev.Response)
		}
		if ev.Type == "response.incomplete" {
			a.truncated = true
		}
	case "response.failed":
		msg := ev.Message
		if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
			msg = ev.Response.Error.Message
		}
		if msg == "" {
			msg = "response failed"
		}
		return fmt.Errorf("entry: api error: %s", msg)
	case "error":
		msg := ev.Message
		if msg == "" && ev.Error != nil {
			msg = ev.Error.Message
		}
		if msg == "" {
			msg = "stream error"
		}
		return fmt.Errorf("entry: api error: %s", msg)
	}
	return nil
}

// noteResponse folds a completion envelope's usage and truncation in, and
// keeps its output items as a fallback for a stream that carried no deltas.
func (a *responsesAccumulator) noteResponse(r *responsesResponse) {
	if r.Usage != nil {
		if r.Usage.InputTokens > a.usageIn {
			a.usageIn = r.Usage.InputTokens
		}
		if r.Usage.OutputTokens > a.usageOut {
			a.usageOut = r.Usage.OutputTokens
		}
	}
	if r.Status == "incomplete" ||
		(r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens") {
		a.truncated = true
	}
	if len(r.Output) > 0 {
		parsed := parseResponsesResponse(r)
		a.fallback = &parsed
	}
}

// unnamedCall reports the first function call whose name never arrived. A
// gateway that forwards argument fragments but omits output_item.added leaves
// nothing to dispatch; dropping the call silently turned a tool round into a
// plausible "the model said nothing".
func (a *responsesAccumulator) unnamedCall() string {
	for _, key := range a.order {
		if c := a.calls[key]; c != nil && c.name == "" {
			return key
		}
	}
	return ""
}

func (a *responsesAccumulator) result() Response {
	var out Response
	texts := append(append([]string(nil), a.texts...), a.refusal...)
	out.Text = strings.Join(texts, "")
	out.Reasoning = strings.Join(a.reasoning, "")
	out.Truncated = a.truncated
	for _, key := range a.order {
		c := a.calls[key]
		if c == nil || c.name == "" {
			continue
		}
		out.ToolUses = append(out.ToolUses,
			responsesToolUse(c.id, c.name, c.args.String(), len(out.ToolUses)))
	}
	// Fill per field, not all-or-nothing: text deltas can arrive while the
	// function-call events do not, and in that shape the old guard skipped the
	// envelope too, losing the tool call on a round that otherwise looked
	// successful. Deltas never carry tool calls, so the two cannot duplicate.
	if a.fallback != nil {
		if out.Text == "" {
			out.Text = a.fallback.Text
		}
		if out.Reasoning == "" {
			out.Reasoning = a.fallback.Reasoning
		}
		if len(out.ToolUses) == 0 {
			out.ToolUses = a.fallback.ToolUses
		}
	}
	return out
}

// responsesEventKey identifies the tool call a fragment belongs to: the
// item id when the event carries one, else the output index.
func responsesEventKey(ev *responsesStreamEvent) string {
	if ev.ItemID != "" {
		return ev.ItemID
	}
	if ev.Item != nil && ev.Item.ID != "" {
		return ev.Item.ID
	}
	return fmt.Sprintf("#%d", ev.OutputIndex)
}

// sendResponsesStream sends one streaming request and returns the open
// response on 200, splitting non-200 shapes into the retryable/status errors
// the shared ladder understands.
func (c *Client) sendResponsesStream(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (*http.Response, error) {
	payload, err := c.marshalWithParams(c.buildResponsesRequest(system, turns, tools, true))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	if err := c.applyAuth(ctx, httpReq); err != nil {
		return nil, err
	}
	c.applyExtraHeaders(ctx, httpReq)

	resp, err := c.hcStream.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("entry: request: %w", err)
		}
		return nil, &transientError{err: err}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &statusError{status: resp.StatusCode, body: string(body)}
	}
	return resp, nil
}

// streamResponses runs one streaming Responses call, replaying once without
// the reasoning field when the provider rejects it before any event.
func (c *Client) streamResponses(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string), onReasoning func(string)) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	body, err := func() (io.ReadCloser, error) {
		resp, err := c.sendResponsesStream(ctx, system, turns, tools)
		if err == nil {
			return resp.Body, nil
		}
		if thinkingRejected(err) && !c.thinkingDenied.Load() {
			c.thinkingDenied.Store(true)
			resp, err = c.sendResponsesStream(ctx, system, turns, tools)
			if err == nil {
				return resp.Body, nil
			}
		}
		return nil, err
	}()
	if err != nil {
		return Response{}, err
	}
	defer body.Close()

	acc := &responsesAccumulator{calls: map[string]*responsesCall{}}
	var raw strings.Builder
	sawData := false
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxEventBytes)
	for scanner.Scan() {
		line := scanner.Text()
		after, ok := strings.CutPrefix(line, "data:")
		if !ok {
			// A gateway that ignored "stream" answers with one JSON body;
			// keep it for the fallback below.
			if raw.Len() < maxModelBodyBytes {
				raw.WriteString(line)
				raw.WriteByte('\n')
			}
			continue
		}
		sawData = true
		data := strings.TrimSpace(after)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // unknown event shape; skip like the other dialects
		}
		if err := acc.feed(&ev, onDelta, onReasoning); err != nil {
			return Response{}, err
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return Response{}, sseReadError(err)
	}
	if !sawData {
		var one responsesResponse
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw.String())), &one); err == nil {
			if one.Error != nil {
				return Response{}, fmt.Errorf("entry: api error: %s", one.Error.Message)
			}
			if one.Usage != nil {
				c.addUsage(one.Usage.InputTokens, one.Usage.OutputTokens)
			}
			return parseResponsesResponse(&one), nil
		}
		// Neither SSE nor a JSON body (an HTML error page, a proxy notice):
		// returning a zero Response with a nil error made the caller read it as
		// "the model said nothing" and pay for a whole replay to find out.
		return Response{}, fmt.Errorf("entry: unexpected response body: %.200s",
			strings.TrimSpace(raw.String()))
	}
	if key := acc.unnamedCall(); key != "" && (acc.fallback == nil || len(acc.fallback.ToolUses) == 0) {
		return Response{}, fmt.Errorf("entry: stream: function call %q carried arguments but no name "+
			"(upstream sent no output_item.added)", key)
	}
	c.addUsage(acc.usageIn, acc.usageOut)
	return acc.result(), nil
}
