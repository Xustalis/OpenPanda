// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// anthropicUsageStream is one streamed reply whose usage arrives the way an
// Anthropic-compatible endpoint reports it: input on message_start, the running
// output count on message_delta.
const anthropicUsageStream = `data: {"type":"message_start","message":{"usage":{"input_tokens":120,"output_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

data: {"type":"message_delta","usage":{"output_tokens":5},"delta":{"stop_reason":"end_turn"}}
`

// usageOnSSE serves body as one SSE response and returns a client pointed at it.
func usageOnSSE(t *testing.T, body string) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(body))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeAnthropic, BaseURL: srv.URL,
		APIKey: "sk-test", Model: "claude-test", MaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c, srv.Close
}

// TestStreamReportsLiveUsage pins the status-line contract: the observer sees
// the provider's counts before the stream ends, and the last report equals the
// billed totals rather than a double count.
func TestStreamReportsLiveUsage(t *testing.T) {
	c, closeSrv := usageOnSSE(t, anthropicUsageStream)
	defer closeSrv()

	var seen []Usage
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil,
		WithUsageObserver(func(u Usage) { seen = append(seen, u) }))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "hi" {
		t.Fatalf("text = %q", resp.Text)
	}
	if len(seen) == 0 {
		t.Fatal("observer never fired")
	}
	last := seen[len(seen)-1]
	if last.InputTokens != 120 || last.OutputTokens != 5 {
		t.Fatalf("last usage = %+v, want 120/5", last)
	}
	if billed := c.Usage(); billed.InputTokens != 120 || billed.OutputTokens != 5 {
		t.Fatalf("billed usage = %+v, want 120/5", billed)
	}
}

// TestUsageObserverSeesEarlierCalls: the observer reports the client's
// cumulative total, so a second call's stream starts reporting from the first
// call's counts instead of restarting at zero.
func TestUsageObserverSeesEarlierCalls(t *testing.T) {
	c, closeSrv := usageOnSSE(t, anthropicUsageStream)
	defer closeSrv()

	if _, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "one"}}, nil, nil, nil); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	var seen []Usage
	if _, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "two"}}, nil, nil, nil,
		WithUsageObserver(func(u Usage) { seen = append(seen, u) })); err != nil {
		t.Fatalf("second stream: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("observer never fired")
	}
	last := seen[len(seen)-1]
	if last.InputTokens != 240 || last.OutputTokens != 10 {
		t.Fatalf("last usage = %+v, want 240/10 (both calls)", last)
	}
}

// TestStreamWithoutObserverIsUnaffected: passing no option keeps the old call
// shape working, including for callers that only want text.
func TestStreamWithoutObserverIsUnaffected(t *testing.T) {
	c, closeSrv := usageOnSSE(t, anthropicUsageStream)
	defer closeSrv()
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "hi" {
		t.Fatalf("text = %q", resp.Text)
	}
}
