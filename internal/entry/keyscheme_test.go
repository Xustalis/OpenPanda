// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"net/http"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// A row whose credential is an OAuth-style token (Claude Code's
// ANTHROPIC_AUTH_TOKEN) says so with key_scheme: bearer; the Anthropic
// dialect must then use Authorization instead of x-api-key.
func TestApplyAuthKeySchemeBearer(t *testing.T) {
	c, err := NewClient(config.ModelConfig{
		APIType:   config.APITypeAnthropic,
		BaseURL:   "https://relay.example.com",
		APIKey:    "tok",
		KeyScheme: "bearer",
		Model:     "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://relay.example.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("authorization"); got != "Bearer tok" {
		t.Errorf("authorization = %q, want Bearer tok", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q, want it absent", got)
	}
}

// Without the scheme, the Anthropic dialect keeps x-api-key.
func TestApplyAuthAnthropicDefaultStaysAPIKey(t *testing.T) {
	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeAnthropic,
		BaseURL: "https://relay.example.com",
		APIKey:  "sk",
		Model:   "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://relay.example.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-api-key"); got != "sk" {
		t.Errorf("x-api-key = %q, want sk", got)
	}
	if got := req.Header.Get("authorization"); got != "" {
		t.Errorf("authorization = %q, want it absent", got)
	}
}
