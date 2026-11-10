// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestIsContextOverflowError pins the narrow verdict: only a 400/413/414 whose
// text actually names an oversized input qualifies. A generic 400 (bad key,
// retired model, malformed tool schema) must keep its own error path, and a
// rate limit must never be mistaken for an overflow and re-sent with a trimmed
// transcript.
func TestIsContextOverflowError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("context length exceeded"), false},
		{"400 openai wording", &statusError{status: 400, body: `{"error":{"message":"context_length_exceeded"}}`}, true},
		{"400 prompt too long", &statusError{status: 400, body: "prompt is too long"}, true},
		{"400 maximum context", &statusError{status: 400, body: "maximum context length is 8192 tokens"}, true},
		{"413 payload too large", &statusError{status: 413, body: "payload too large"}, true},
		{"414 request URI too long", &statusError{status: 414, body: "request uri too long"}, true},
		{"chinese gateway", &statusError{status: 400, body: "上下文长度超出限制"}, true},
		{"chinese prompt", &statusError{status: 400, body: "提示词过长，请减少输入"}, true},
		{"400 bad model", &statusError{status: 400, body: "invalid model name"}, false},
		{"400 too long unrelated", &statusError{status: 400, body: "model name too long"}, false},
		{"401 with overflow words", &statusError{status: 401, body: "context length exceeded"}, false},
		{"429 rate limit", &statusError{status: 429, body: "token limit reached, slow down"}, false},
		{"500 with overflow words", &statusError{status: 500, body: "context window exceeded"}, false},
		{"wrapped status", fmt.Errorf("classify: %w", &statusError{status: 400, body: "input too long"}), true},
		// WrapAPIError's shape: the raw body is short but Detail carries the
		// provider's reason. Both links on the chain must be consulted.
		{"wrapped classify detail", &ClassifyError{
			Status: 400, Detail: "maximum context length exceeded",
			UserMsg: "bad request", Err: &statusError{status: 400, body: "bad request"},
		}, true},
		{"wrapped classify usermsg", &ClassifyError{
			Status: 400, Detail: "", UserMsg: "请求过长：prompt too long",
		}, true},
		{"wrapped generic 400", &ClassifyError{
			Status: 400, Detail: "malformed tool schema", UserMsg: "bad request",
			Err: &statusError{status: 400, body: "malformed tool schema"},
		}, false},
		{"wrapped 429", &ClassifyError{
			Status: 429, UserMsg: "rate limited",
			Err: &statusError{status: 429, body: "token limit reached"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContextOverflowError(tc.err); got != tc.want {
				t.Fatalf("IsContextOverflowError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestUnknownProviderMaxTokensIsBounded pins the other half of the overflow
// defence: a self-hosted/aggregator route with no tuning entry used to pass a
// config-declared 125000-token completion cap straight to the provider, which
// rejects the request before generating anything. Only the unknown-provider
// case is bounded; a provider the tuning table knows keeps its own ceiling.
func TestUnknownProviderMaxTokensIsBounded(t *testing.T) {
	base := config.ModelConfig{
		APIType: "openai", BaseURL: "https://example.invalid/v1",
		APIKey: "sk-test", Model: "unlisted-local-model", ContextWindow: 1000000,
	}
	optimistic := base
	optimistic.MaxTokens = 125000
	c, err := NewClient(optimistic)
	if err != nil {
		t.Fatal(err)
	}
	if c.maxTokens != absoluteMaxTokens {
		t.Fatalf("maxTokens = %d, want the defensive ceiling %d", c.maxTokens, absoluteMaxTokens)
	}
	modest := base
	modest.MaxTokens = 8192
	c2, err := NewClient(modest)
	if err != nil {
		t.Fatal(err)
	}
	if c2.maxTokens != 8192 {
		t.Fatalf("a sane configured cap was rewritten: %d", c2.maxTokens)
	}
}
