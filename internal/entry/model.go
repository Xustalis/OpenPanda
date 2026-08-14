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

	"github.com/xenith/panda/internal/config"
)

// anthropicVersion is the header required by Anthropic-compatible endpoints.
const anthropicVersion = "2023-06-01"

// Client talks to an Anthropic-compatible Messages API (DeepSeek's
// /anthropic endpoint). It is small and dependency-free so the core daemon
// does not pull in an SDK.
type Client struct {
	baseURL   string
	apiKey    string
	model     string
	hc        *http.Client
	maxRetry  int
	retryBase time.Duration
}

// NewClient builds a client from the model config. A zero baseURL/model falls
// back to config defaults, so callers can pass config.Default().Model.
func NewClient(model config.ModelConfig) *Client {
	base := model.BaseURL
	if base == "" {
		base = "https://api.deepseek.com/anthropic"
	}
	name := model.Model
	if name == "" {
		name = "deepseek-chat"
	}
	return &Client{
		baseURL:   strings.TrimRight(base, "/"),
		apiKey:    model.APIKey,
		model:     name,
		hc:        &http.Client{Timeout: 30 * time.Second},
		maxRetry:  2,
		retryBase: 500 * time.Millisecond,
	}
}

// messagesRequest is the Anthropic Messages API request body (subset).
type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// contentBlock is an assistant content block in a response.
type contentBlock struct {
	Type string `json:"type"` // text | thinking
	Text string `json:"text"`
}

type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Error   *apiError      `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrNoKey is returned when no API key is configured.
var ErrNoKey = errors.New("entry: no model api_key configured")

// Turn is one conversation turn for multi-turn classification.
type Turn struct {
	Role    string // "user" | "assistant"
	Content string
}

// Complete runs one non-streaming call and returns the model's text. Text and
// thinking blocks are concatenated; thinking is dropped only when a text block
// is present (DeepSeek reasoner emits a thinking block alongside the answer).
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	return c.CompleteTurns(ctx, system, []Turn{{Role: "user", Content: user}})
}

// CompleteTurns runs one call with a conversation history, so a tool result can
// be fed back for another round (the memory-merge loop). It mirrors Complete but
// passes the full turn list as messages.
func (c *Client) CompleteTurns(ctx context.Context, system string, turns []Turn) (string, error) {
	if c.apiKey == "" {
		return "", ErrNoKey
	}
	msgs := make([]message, len(turns))
	for i, t := range turns {
		msgs[i] = message{Role: t.Role, Content: t.Content}
	}
	req := messagesRequest{Model: c.model, MaxTokens: 1024, System: system, Messages: msgs}
	return c.completeWithRetry(ctx, req)
}

// completeWithRetry runs req with the configured retry/backoff and returns the
// extracted text.
func (c *Client) completeWithRetry(ctx context.Context, req messagesRequest) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return "", err
			}
		}
		text, err := c.completeOnce(ctx, req)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}
	return "", lastErr
}

// completeOnce performs a single request and extracts the text.
func (c *Client) completeOnce(ctx context.Context, req messagesRequest) (string, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("entry: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("entry: read response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("entry: api status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var mr messagesResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return "", fmt.Errorf("entry: parse response: %w", err)
	}
	if mr.Error != nil {
		return "", fmt.Errorf("entry: api error: %s", mr.Error.Message)
	}
	return extractText(&mr), nil
}

// extractText concatenates text blocks; thinking blocks are ignored only when
// text is present, so a pure-reasoning response still surfaces something.
func extractText(mr *messagesResponse) string {
	var texts, thinkings []string
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "thinking":
			thinkings = append(thinkings, b.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "")
	}
	return strings.Join(thinkings, "")
}

type retryableError struct {
	status int
	body   string
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("entry: retryable status %d: %s", e.status, truncate(e.body, 200))
}

func retryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
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
