// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"net/http"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/auth"
	"github.com/Xustalis/OpenPanda/internal/config"
)

// A Codex plan token only opens the CLI's backend when the request names the
// account: the backend routes by chatgpt-account-id and the CLI sends the same
// protocol headers alongside it.
func TestApplyAuthCodexAccountHeaders(t *testing.T) {
	c := &Client{
		apiType: config.APITypeResponses,
		oauth:   true,
		tokenFn: func(context.Context) (auth.Token, error) {
			return auth.Token{AccessToken: "tok", AccountID: "acct-1"}, nil
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for header, want := range map[string]string{
		"authorization":      "Bearer tok",
		"chatgpt-account-id": "acct-1",
		"OpenAI-Beta":        "responses=experimental",
		"originator":         "codex_cli_rs",
	} {
		if got := req.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// A stored subscription token (Anthropic) carries no account id, so none of
// the Codex headers may be attached to someone else's endpoint.
func TestApplyAuthWithoutAccountSendsNoCodexHeaders(t *testing.T) {
	c := &Client{
		apiType: config.APITypeAnthropic,
		oauth:   true,
		tokenFn: func(context.Context) (auth.Token, error) {
			return auth.Token{AccessToken: "tok"}, nil
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"chatgpt-account-id", "OpenAI-Beta", "originator"} {
		if got := req.Header.Get(header); got != "" {
			t.Errorf("%s = %q, want it absent", header, got)
		}
	}
}
