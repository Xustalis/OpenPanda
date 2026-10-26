// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// responsesClient points a Responses client at a fake server that streams body.
func responsesClient(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeResponses, BaseURL: srv.URL + "/v1",
		APIKey: "sk-test", Model: "gpt-6.1-sol", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

// TestResponsesStreamRefusalIsAnswer pins the streaming counterpart of the
// non-streaming refusal handling: the model's own explanation is the answer.
// Dropping it left an empty completion, which classify treats as "the model
// said nothing" and pays a full replay to hear again.
func TestResponsesStreamRefusalIsAnswer(t *testing.T) {
	c := responsesClient(t, strings.Join([]string{
		`data: {"type":"response.refusal.delta","delta":"no"}`, "",
		`data: {"type":"response.refusal.delta","delta":"pe"}`, "",
		`data: {"type":"response.completed","response":{"status":"completed"}}`, "",
	}, "\n"))

	var deltas []string
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { deltas = append(deltas, s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "nope" {
		t.Fatalf("text = %q, want the refusal text", resp.Text)
	}
	if got := strings.Join(deltas, ""); got != "nope" {
		t.Fatalf("streamed deltas = %q, want the refusal shown live", got)
	}
}

// TestResponsesStreamEnvelopeSuppliesToolCall pins the per-field fallback: a
// proxy that streams text but not the function-call events used to lose the
// call entirely, because the envelope was only consulted when nothing at all
// had arrived.
func TestResponsesStreamEnvelopeSuppliesToolCall(t *testing.T) {
	c := responsesClient(t, strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"thinking"}`, "",
		`data: {"type":"response.completed","response":{"status":"completed","output":[` +
			`{"type":"message","content":[{"type":"output_text","text":"thinking"}]},` +
			`{"type":"function_call","call_id":"call_7","name":"web_search","arguments":"{\"q\":\"x\"}"}]}}`, "",
	}, "\n"))

	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "thinking" {
		t.Fatalf("text = %q", resp.Text)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].Name != "web_search" ||
		resp.ToolUses[0].ID != "call_7" || resp.ToolUses[0].Input["q"] != "x" {
		t.Fatalf("tool uses = %+v, want the envelope's function call", resp.ToolUses)
	}
}

// TestResponsesStreamUnnamedCallFails pins the loud failure: arguments for a
// call whose output_item.added never arrived cannot be dispatched, and silently
// dropping them turned a tool round into a plausible "the model said nothing".
func TestResponsesStreamUnnamedCallFails(t *testing.T) {
	c := responsesClient(t, strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"x\"}"}`, "",
		`data: {"type":"response.completed","response":{"status":"completed"}}`, "",
	}, "\n"))

	_, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Fatalf("err = %v, want a diagnosis about the nameless call", err)
	}
}

// TestStreamRetryResetsShapeGuard pins that each attempt decides the shape of
// its own response. Sharing one guard across attempts meant a first response
// that opened on '{' — judged structured, then dropped mid-stream — suppressed
// every delta of the retry too, so the answer arrived in one silent burst.
func TestStreamRetryResetsShapeGuard(t *testing.T) {
	c := &Client{maxRetry: 1, retryBase: time.Millisecond}
	var got []string
	attempt := 0
	resp, err := c.streamWithRetry(context.Background(),
		func(onDelta, onReasoning func(string)) (Response, error) {
			attempt++
			if attempt == 1 {
				onDelta(`{"status":"answer","text":"hi"}`)
				return Response{}, &transientError{err: errors.New("connection reset")}
			}
			onDelta("hello")
			return Response{Text: "hello"}, nil
		},
		func(s string) { got = append(got, s) }, nil)
	if err != nil {
		t.Fatalf("streamWithRetry: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("response text = %q", resp.Text)
	}
	if joined := strings.Join(got, ""); joined != "hello" {
		t.Fatalf("delivered = %q, want the retry's prose streamed live", joined)
	}
}

// TestNewClientKeepsDialectOnForeignRelay pins that rebasing an endpoint and
// a dialect move together. The catalogue surface describes the provider's
// own wire; on a relay the user pointed at, applying the catalogue's dialect
// sent the wrong body shape, and the 400 that followed named nothing in the
// config.
func TestNewClientKeepsDialectOnForeignRelay(t *testing.T) {
	c, err := NewClient(config.ModelConfig{
		Provider: "opencode-go", Model: "claude-opus-4.6",
		BaseURL: "https://relay.example.com/v1", APIType: config.APITypeAnthropic,
		APIKey: "sk-test", MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if c.baseURL != "https://relay.example.com/v1" {
		t.Fatalf("base = %q, want the relay left alone", c.baseURL)
	}
	if c.apiType != config.APITypeAnthropic {
		t.Fatalf("apiType = %q, want the declared anthropic kept for a relay", c.apiType)
	}

	// The catalogue base takes the row's surface: Go serves every model on
	// its OpenAI-compatible endpoint, so a stale /inference URL is migrated
	// there along with the dialect.
	c, err = NewClient(config.ModelConfig{
		Provider: "opencode-go", Model: "claude-opus-4.6",
		BaseURL: "https://opencode.ai/inference/anthropic", APIType: config.APITypeAnthropic,
		APIKey: "sk-test", MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("catalogue client: %v", err)
	}
	if c.apiType != config.APITypeOpenAI {
		t.Fatalf("apiType = %q, want the row's openai on the catalogue base", c.apiType)
	}
	if c.baseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("base = %q, want the current Go endpoint", c.baseURL)
	}
}
