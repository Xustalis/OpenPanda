// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

// Google Generative AI (Gemini) wire support.
//
// The model and method are part of the URL —
// {root}/v1beta/models/{model}:generateContent, with :streamGenerateContent
// and ?alt=sse for streaming — so the request body carries no model field.
// The system prompt lives in systemInstruction, the history in contents with
// roles user/model, and tools as one functionDeclarations array. Function
// calls come back on parts[].functionCall and are answered by a user turn
// whose parts carry functionResponse — the call has no id, so results are
// linked by tool name.
//
// Thinking is a generationConfig.thinkingConfig object; models that do not
// support it reject the field with a 400, which the shared retry ladder drops
// once (thinkingDenied is sticky).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/security"
)

// googleRequest is the generateContent request body.
type googleRequest struct {
	Contents          []googleContent         `json:"contents"`
	SystemInstruction *googleContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *googleGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []googleTool            `json:"tools,omitempty"`
}

// googleContent is one conversation turn: role "user" or "model" (an empty
// role on systemInstruction is what the API expects).
type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts"`
}

// googlePart is one content part in either direction. Thought marks a
// reasoning part; FunctionCall/FunctionResponse carry tool traffic.
type googlePart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResponse `json:"functionResponse,omitempty"`
}

type googleFunctionCall struct {
	Name string `json:"name"`
	// Args is always an object on the wire: a no-argument tool sends {} rather
	// than null, which strict Gemini endpoints reject.
	Args map[string]any `json:"args"`
}

// googleFunctionResponse links the result to its call by name — Gemini has no
// call ids in this wire shape.
type googleFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type googleGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *googleThinkingConfig `json:"thinkingConfig,omitempty"`
}

type googleThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDecl `json:"functionDeclarations"`
}

type googleFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// googleResponse is one non-streaming body or streamed chunk. Function calls
// and usage metadata arrive on the same shape in both modes.
type googleResponse struct {
	Candidates     []googleCandidate `json:"candidates"`
	UsageMetadata  *googleUsage      `json:"usageMetadata"`
	PromptFeedback *googleFeedback   `json:"promptFeedback"`
	Error          *googleError      `json:"error"`
}

type googleCandidate struct {
	Content      googleContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type googleUsage struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	TotalTokenCount      int64 `json:"totalTokenCount"`
	// ThoughtsTokenCount is thinking spend, which candidatesTokenCount does
	// not include (only totalTokenCount does). applyGoogleThinking asks for a
	// 4096-token budget, so ignoring it under-reported output and cost on
	// exactly the models configured to think.
	ThoughtsTokenCount int64 `json:"thoughtsTokenCount"`
}

// googleFeedback reports a prompt blocked before any candidate was produced.
type googleFeedback struct {
	BlockReason string `json:"blockReason"`
}

type googleError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// buildGoogleRequest assembles one request from the internal turns.
func (c *Client) buildGoogleRequest(system string, turns []Turn, tools []ToolSpec) googleRequest {
	req := googleRequest{Contents: turnsToGoogle(turns)}
	if system != "" {
		req.SystemInstruction = &googleContent{Parts: []googlePart{{Text: system}}}
	}
	if c.maxTokens > 0 {
		req.GenerationConfig = &googleGenerationConfig{MaxOutputTokens: c.maxTokens}
	}
	if len(tools) > 0 {
		req.Tools = []googleTool{{FunctionDeclarations: specsToGoogle(tools)}}
	}
	c.applyGoogleThinking(&req)
	return req
}

// applyGoogleThinking maps the configured mode onto a thinkingConfig. An unset
// mode sends nothing; "on" asks for thoughts with a budget (lifting the
// output cap above it, since thinking tokens count as output), "off" sends a
// zero budget, which the models that can disable reasoning accept.
func (c *Client) applyGoogleThinking(req *googleRequest) {
	if c.thinkingDenied.Load() || c.thinking == "" {
		return
	}
	if req.GenerationConfig == nil {
		req.GenerationConfig = &googleGenerationConfig{}
	}
	tc := &googleThinkingConfig{}
	switch c.thinking {
	case "on", "auto":
		tc.IncludeThoughts = true
		budget := c.thinkingBudget
		if budget <= 0 {
			budget = 4096
		}
		tc.ThinkingBudget = &budget
		if req.GenerationConfig.MaxOutputTokens > 0 && req.GenerationConfig.MaxOutputTokens <= budget {
			req.GenerationConfig.MaxOutputTokens = budget + 4096
		}
		if c.maxTokensCap > 0 && req.GenerationConfig.MaxOutputTokens > c.maxTokensCap {
			req.GenerationConfig.MaxOutputTokens = c.maxTokensCap
		}
	case "off":
		zero := 0
		tc.ThinkingBudget = &zero
	default:
		return
	}
	req.GenerationConfig.ThinkingConfig = tc
}

// turnsToGoogle converts the internal history into Gemini contents. A plain
// turn keeps its role (assistant → model); a block-bearing turn splits by
// block kind, because Gemini scopes functionCall to a model turn and
// functionResponse to a user turn even when the engine packed both into one
// Turn.
func turnsToGoogle(turns []Turn) []googleContent {
	turns = normalizeTurns(turns)
	// Gemini links a result to its call by name; the ids live in the
	// tool_use blocks, which precede the results in the history.
	names := map[string]string{}
	for _, t := range turns {
		for _, b := range t.Blocks {
			if b.Type == "tool_use" {
				names[b.ID] = b.Name
			}
		}
	}
	out := make([]googleContent, 0, len(turns))
	for _, t := range turns {
		if len(t.Blocks) == 0 {
			if t.Content == "" {
				continue
			}
			out = append(out, googleContent{Role: googleRole(t.Role), Parts: []googlePart{{Text: t.Content}}})
			continue
		}
		var modelParts, userParts []googlePart
		for _, b := range t.Blocks {
			switch b.Type {
			case "text":
				if b.Text == "" {
					continue
				}
				if t.Role == "assistant" {
					modelParts = append(modelParts, googlePart{Text: b.Text})
				} else {
					userParts = append(userParts, googlePart{Text: b.Text})
				}
			case "tool_use":
				args := b.Input
				if args == nil {
					args = map[string]any{}
				}
				modelParts = append(modelParts, googlePart{FunctionCall: &googleFunctionCall{Name: b.Name, Args: args}})
			case "tool_result":
				name := names[b.ToolUseID]
				if name == "" {
					// functionResponse.name must be one the request declared;
					// the API rejects anything else with 400 INVALID_ARGUMENT,
					// and a call id is not a name. A history whose tool_use was
					// trimmed (or built by hand) cannot be expressed as a
					// function response, so the output rides as text: the model
					// still sees it and every later turn stays valid.
					text := b.Content
					if text == "" {
						text = "(no output)"
					}
					userParts = append(userParts, googlePart{Text: text})
					continue
				}
				userParts = append(userParts, googlePart{FunctionResponse: &googleFunctionResponse{
					Name:     name,
					Response: map[string]any{"result": b.Content},
				}})
			}
		}
		if len(modelParts) > 0 {
			out = append(out, googleContent{Role: "model", Parts: modelParts})
		}
		if len(userParts) > 0 {
			out = append(out, googleContent{Role: "user", Parts: userParts})
		}
	}
	return out
}

func googleRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return role
}

// specsToGoogle converts internal tool specs into one functionDeclarations
// array.
func specsToGoogle(specs []ToolSpec) []googleFunctionDecl {
	out := make([]googleFunctionDecl, 0, len(specs))
	for _, s := range specs {
		out = append(out, googleFunctionDecl{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.InputSchema,
		})
	}
	return out
}

// googleURL builds the model-and-method endpoint from an API root. A base the
// user copied from the docs (already carrying /models/<id>:generateContent)
// is reduced to its root, so the configured model wins.
func googleURL(base, model string, stream bool) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	for _, leaf := range []string{":generateContent", ":streamGenerateContent"} {
		base = strings.TrimSuffix(base, leaf)
	}
	if i := strings.Index(base, "/models/"); i >= 0 {
		base = base[:i+len("/models")]
	}
	if !strings.HasSuffix(base, "/models") {
		base += "/models"
	}
	name := strings.TrimPrefix(strings.TrimSpace(model), "models/")
	out := base + "/" + url.PathEscape(name) + ":" + method
	if stream {
		out += "?alt=sse"
	}
	return out
}

// isGoogleHost reports whether base points at Google's own API, where the key
// belongs in x-goog-api-key alone. A gateway such as OpenCode's Console also
// accepts a bearer token, so it gets both headers.
func isGoogleHost(base string) bool {
	// Host equality, not substring: "googleapis.com.example.net" and
	// "evil-googleapis.com" are somebody else's server, and sending them only
	// the Google header earned a 401 that looked like a bad key.
	host := security.EndpointHost(base)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "googleapis.com" || strings.HasSuffix(host, ".googleapis.com")
}

// completeGoogle runs one non-streaming generateContent call with retry.
func (c *Client) completeGoogle(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	return c.completeWithRetryFn(ctx, func() (Response, error) {
		return c.completeOnceGoogle(ctx, system, turns, tools)
	}, nil)
}

func (c *Client) completeOnceGoogle(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	payload, err := c.marshalWithParams(c.buildGoogleRequest(system, turns, tools))
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, googleURL(c.baseURL, c.model, false), bytes.NewReader(payload))
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

	var gr googleResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if gr.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", gr.Error.Message)
	}
	if gr.PromptFeedback != nil && gr.PromptFeedback.BlockReason != "" && len(gr.Candidates) == 0 {
		return Response{}, fmt.Errorf("entry: api error: prompt blocked (%s)", gr.PromptFeedback.BlockReason)
	}
	if gr.UsageMetadata != nil {
		c.addUsage(gr.UsageMetadata.PromptTokenCount,
			gr.UsageMetadata.CandidatesTokenCount+gr.UsageMetadata.ThoughtsTokenCount)
	}
	return parseGoogleResponse(&gr), nil
}

// parseGoogleResponse reduces candidates to text, reasoning and tool calls.
// A part flagged thought is chain-of-thought scratch (D14): it is surfaced on
// Response.Reasoning, never folded into the answer.
func parseGoogleResponse(r *googleResponse) Response {
	var out Response
	var texts, reasoning []string
	calls := 0
	for _, cand := range r.Candidates {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				out.ToolUses = append(out.ToolUses, googleToolUse(calls, p.FunctionCall))
				calls++
			case p.Thought:
				reasoning = append(reasoning, p.Text)
			case p.Text != "":
				texts = append(texts, p.Text)
			}
		}
		if cand.FinishReason == "MAX_TOKENS" {
			out.Truncated = true
		}
	}
	out.Text = stripThinkingBlock(strings.Join(texts, ""))
	out.Reasoning = strings.Join(reasoning, "")
	return out
}

// googleToolUse builds one internal tool use from a Gemini function call.
func googleToolUse(index int, call *googleFunctionCall) ToolUse {
	return ToolUse{
		ID:    fmt.Sprintf("call_%d_%s", index, call.Name),
		Name:  call.Name,
		Input: call.Args,
	}
}

// googleAccumulator assembles a Response from streamed chunks. Gemini streams
// text as incremental parts and sends a function call whole, so calls are
// collected as they arrive.
type googleAccumulator struct {
	texts     []string
	reasoning []string
	calls     []ToolUse
	truncated bool
	usageIn   int64
	usageOut  int64
}

func (a *googleAccumulator) feed(r *googleResponse, onDelta, onReasoning func(string)) error {
	if r.Error != nil {
		return fmt.Errorf("entry: api error: %s", r.Error.Message)
	}
	if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" && len(r.Candidates) == 0 {
		return fmt.Errorf("entry: api error: prompt blocked (%s)", r.PromptFeedback.BlockReason)
	}
	if u := r.UsageMetadata; u != nil {
		if u.PromptTokenCount > a.usageIn {
			a.usageIn = u.PromptTokenCount
		}
		// Chunks carry cumulative counters; thinking is billed as output but
		// sits outside candidatesTokenCount.
		if out := u.CandidatesTokenCount + u.ThoughtsTokenCount; out > a.usageOut {
			a.usageOut = out
		}
	}
	for _, cand := range r.Candidates {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				a.calls = append(a.calls, googleToolUse(len(a.calls), p.FunctionCall))
			case p.Thought:
				if p.Text != "" {
					a.reasoning = append(a.reasoning, p.Text)
					if onReasoning != nil {
						onReasoning(p.Text)
					}
				}
			case p.Text != "":
				a.texts = append(a.texts, p.Text)
				if onDelta != nil {
					onDelta(p.Text)
				}
			}
		}
		if cand.FinishReason == "MAX_TOKENS" {
			a.truncated = true
		}
	}
	return nil
}

func (a *googleAccumulator) result() Response {
	return Response{
		Text:      strings.Join(a.texts, ""),
		Reasoning: strings.Join(a.reasoning, ""),
		ToolUses:  a.calls,
		Truncated: a.truncated,
	}
}

// sendGoogleStream sends one streaming generateContent request and returns the
// open response on 200.
func (c *Client) sendGoogleStream(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (*http.Response, error) {
	payload, err := c.marshalWithParams(c.buildGoogleRequest(system, turns, tools))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, googleURL(c.baseURL, c.model, true), bytes.NewReader(payload))
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

// streamGoogle runs one streaming Gemini call, replaying once without the
// thinkingConfig when the model rejects it before any chunk.
func (c *Client) streamGoogle(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string), onReasoning func(string)) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	body, err := func() (io.ReadCloser, error) {
		resp, err := c.sendGoogleStream(ctx, system, turns, tools)
		if err == nil {
			return resp.Body, nil
		}
		if thinkingRejected(err) && !c.thinkingDenied.Load() {
			c.thinkingDenied.Store(true)
			resp, err = c.sendGoogleStream(ctx, system, turns, tools)
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

	acc := &googleAccumulator{}
	var raw strings.Builder
	sawData := false
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxEventBytes)
	for scanner.Scan() {
		line := scanner.Text()
		after, ok := strings.CutPrefix(line, "data:")
		if !ok {
			// A gateway that ignores alt=sse answers streamGenerateContent
			// with a JSON array instead of SSE; keep the body so the
			// fallback below can still deliver it.
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
		var chunk googleResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // unknown chunk shape; skip like the other dialects
		}
		if err := acc.feed(&chunk, onDelta, onReasoning); err != nil {
			return Response{}, err
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return Response{}, sseReadError(err)
	}
	if !sawData {
		if err := feedGoogleJSON(strings.TrimSpace(raw.String()), acc, onDelta, onReasoning); err != nil {
			return Response{}, err
		}
	}
	c.addUsage(acc.usageIn, acc.usageOut)
	return acc.result(), nil
}

// feedGoogleJSON folds a non-SSE generateContent body into the accumulator: an
// array of chunks (the native streaming shape without alt=sse) or one object. A
// body that is neither is reported instead of ignored — an ignored one left an
// empty result, which the caller reads as "the model said nothing" and pays a
// whole replay to discover otherwise.
func feedGoogleJSON(raw string, acc *googleAccumulator, onDelta, onReasoning func(string)) error {
	if raw == "" {
		return nil
	}
	var chunks []googleResponse
	if err := json.Unmarshal([]byte(raw), &chunks); err == nil {
		for i := range chunks {
			if err := acc.feed(&chunks[i], onDelta, onReasoning); err != nil {
				return err
			}
		}
		return nil
	}
	var one googleResponse
	if err := json.Unmarshal([]byte(raw), &one); err != nil {
		return fmt.Errorf("entry: unexpected response body: %.200s", raw)
	}
	return acc.feed(&one, onDelta, onReasoning)
}
