package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// anthropicVersion is the header required by Anthropic-compatible endpoints.
const anthropicVersion = "2023-06-01"

// defaultMaxTokens is the completion cap when the config does not specify one.
// It is high enough that a normal answer is not silently truncated (the previous
// hardcoded 1024 was), while still bounding a runaway generation.
const defaultMaxTokens = 4096

// Client talks to an Anthropic-compatible Messages API or an OpenAI-compatible
// Chat Completions API, selected by the config's api_type. It is small and
// dependency-free so the core daemon does not pull in an SDK.
type Client struct {
	apiType   string
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	hc        *http.Client
	// hcStream serves streaming requests: no total timeout (a long stream is
	// legitimate); liveness comes from the caller's context.
	hcStream  *http.Client
	maxRetry  int
	retryBase time.Duration
}

// NewClient builds a client from the model config. A zero baseURL/model falls
// back to config defaults, so callers can pass config.Default().Model. The
// endpoint must be HTTPS so the API key never travels cleartext (M2); loopback
// http stays allowed for a local dev model, matching the guard the commander
// applies to adapter endpoints (D7).
func NewClient(model config.ModelConfig) (*Client, error) {
	base := model.BaseURL
	if base == "" {
		base = "https://api.deepseek.com/anthropic"
	}
	if err := security.NewNetworkGuard(security.EndpointHost(base)).CheckURL(base); err != nil {
		return nil, err
	}
	name := model.Model
	if name == "" {
		name = "deepseek-chat"
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	return &Client{
		apiType:   model.NormalizedAPIType(),
		baseURL:   strings.TrimRight(base, "/"),
		apiKey:    model.APIKey,
		model:     name,
		maxTokens: maxTokens,
		hc:        &http.Client{Timeout: 30 * time.Second},
		hcStream:  &http.Client{},
		maxRetry:  2,
		retryBase: 500 * time.Millisecond,
	}, nil
}

// messagesRequest is the Anthropic Messages API request body.
type messagesRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	Stream    bool       `json:"stream,omitempty"`
	System    string     `json:"system,omitempty"`
	Messages  []message  `json:"messages"`
	Tools     []ToolSpec `json:"tools,omitempty"`
}

// message is one conversation message. Content is either a plain string (the
// common case) or a []ContentBlock when the turn carries tool_use/tool_result.
type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ContentBlock is a structured content block in a message or response. It
// covers the Messages API vocabulary PANDA uses: text/thinking (responses) and
// tool_use/tool_result (both directions).
type ContentBlock struct {
	Type      string         `json:"type"`                  // text | thinking | tool_use | tool_result
	Text      string         `json:"text,omitempty"`        // text/thinking
	ID        string         `json:"id,omitempty"`          // tool_use id
	Name      string         `json:"name,omitempty"`        // tool_use name
	Input     map[string]any `json:"input,omitempty"`       // tool_use input
	ToolUseID string         `json:"tool_use_id,omitempty"` // tool_result linkage
	Content   string         `json:"content,omitempty"`     // tool_result content
}

// ToolSpec describes one tool the model may call (Anthropic Messages API shape).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolUse is one tool invocation the model emitted in a response.
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]any
}

// Response is the parsed result of one completion call: the textual answer plus
// any tool_use blocks the model emitted (a model may return text alongside a
// tool call, or only a tool call). Truncated reports that the provider stopped
// at max_tokens, so the text is incomplete.
type Response struct {
	Text      string
	ToolUses  []ToolUse
	Truncated bool
}

// messagesResponse is the Messages API response body.
type messagesResponse struct {
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Error      *apiError      `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrNoKey is returned when no API key is configured.
var ErrNoKey = errors.New("entry: no model api_key configured")

// Turn is one conversation turn for multi-turn classification.
type Turn struct {
	Role    string         // "user" | "assistant"
	Content string         // plain text (used when Blocks is empty)
	Blocks  []ContentBlock // structured blocks for tool_use/tool_result
}

// Complete runs one non-streaming call and returns the model's text.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	return c.CompleteTurns(ctx, system, []Turn{{Role: "user", Content: user}})
}

// CompleteTurns runs one call with a conversation history and no tools,
// returning the model's text. It is the answer/fallback path; the tool path is
// CompleteTurnsWithTools.
func (c *Client) CompleteTurns(ctx context.Context, system string, turns []Turn) (string, error) {
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, nil)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// CompleteTurnsWithTools runs one call with a conversation history and an
// optional tool set, returning both text and any tool_use blocks. It dispatches
// on the configured api type; the Anthropic path leaves tool_choice unset: the
// Messages API defaults to "auto" when tools are present, and DeepSeek's
// Anthropic endpoint rejects the string "auto" (it wants the internally-tagged
// object form). Omitting it is simpler and correct.
func (c *Client) CompleteTurnsWithTools(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.apiType == config.APITypeOpenAI {
		return c.completeOpenAI(ctx, system, turns, tools)
	}
	req := messagesRequest{Model: c.model, MaxTokens: c.maxTokens, System: system, Messages: turnsToMessages(turns)}
	if len(tools) > 0 {
		req.Tools = tools
	}
	return c.completeWithRetry(ctx, req)
}

// turnsToMessages converts the internal Turn history into Messages API
// messages: a Turn with Blocks carries structured content, a plain Turn a
// string content.
func turnsToMessages(turns []Turn) []message {
	msgs := make([]message, len(turns))
	for i, t := range turns {
		if len(t.Blocks) > 0 {
			msgs[i] = message{Role: t.Role, Content: t.Blocks}
		} else {
			msgs[i] = message{Role: t.Role, Content: t.Content}
		}
	}
	return msgs
}

// completeOpenAI runs one non-streaming Chat Completions call with retry.
func (c *Client) completeOpenAI(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.apiKey == "" {
		return Response{}, ErrNoKey
	}
	req := oaiRequest{Model: c.model, MaxTokens: c.maxTokens, Messages: turnsToOpenAI(system, turns)}
	if len(tools) > 0 {
		req.Tools = specsToOpenAI(tools)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		resp, err := c.completeOnceOpenAI(ctx, payload)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}
	return Response{}, lastErr
}

func (c *Client) completeOnceOpenAI(ctx context.Context, payload []byte) (Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+c.apiKey)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, &transientError{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("entry: api status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var or oaiResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if or.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", or.Error.Message)
	}
	return parseOpenAIResponse(&or), nil
}

// completeWithRetry runs req with the configured retry/backoff and returns the
// parsed response.
func (c *Client) completeWithRetry(ctx context.Context, req messagesRequest) (Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		resp, err := c.completeOnce(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}
	return Response{}, lastErr
}

// completeOnce performs a single request and parses the response.
func (c *Client) completeOnce(ctx context.Context, req messagesRequest) (Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		// A caller-cancelled context is not a transient network failure and must
		// not be retried. Anything else (DNS, connection reset, EOF) is.
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// A mid-body truncation (unexpected EOF / connection reset) is a
		// transient transport failure like a failed Do: no complete response was
		// received, so it is safe to retry.
		return Response{}, &transientError{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("entry: api status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var mr messagesResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if mr.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", mr.Error.Message)
	}
	return parseResponse(&mr), nil
}

// parseResponse reduces a response to its text (text blocks only) and its
// tool_use blocks. It records whether the provider stopped at max_tokens, so
// callers can surface a truncation rather than pass a silently cut answer
// through. Thinking blocks are deliberately not used as the answer: a
// thinking-only response carries no reply, and surfacing the model's private
// reasoning would leak it to the user (D14).
func parseResponse(mr *messagesResponse) Response {
	var out Response
	var texts []string
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			out.ToolUses = append(out.ToolUses, ToolUse{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	out.Text = strings.Join(texts, "")
	out.Truncated = mr.StopReason == "max_tokens"
	return out
}

type retryableError struct {
	status int
	body   string
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("entry: retryable status %d: %s", e.status, truncate(e.body, 200))
}

// transientError marks a transport-level failure (connection reset, DNS, EOF,
// timeout) that is safe to retry because no definitive response was received.
type transientError struct{ err error }

func (e *transientError) Error() string { return fmt.Sprintf("entry: transient: %v", e.err) }
func (e *transientError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var te *transientError
	return errors.As(err, &te)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
