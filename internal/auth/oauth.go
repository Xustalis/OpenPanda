// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth holds subscription-OAuth plumbing: PKCE flows against
// providers whose consumer subscriptions also grant API access, plus the
// on-disk token store the entry client reads for a Bearer credential.
//
// The first supported provider is Anthropic (a Claude Pro/Max subscription
// can answer Messages API calls with an OAuth Bearer where an API key
// would go). Other providers with different token shapes — e.g. OpenAI's
// subscription tokens only work against a separate backend surface, not
// /v1/chat/completions — slot into the Providers table once the entry
// client speaks their wire protocol.
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider describes one OAuth integration point.
type Provider struct {
	ID    string // provider key used in config and the token store
	Label string // human label for status output
	// AuthorizeURL is the browser-facing consent page; TokenURL the
	// code/refresh exchange endpoint. RedirectURI must be a URI the
	// provider has registered for this client — Claude's flow lands on a
	// hosted page that shows the user a code to paste back.
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	RedirectURI  string
	Scope        string
}

// providers is the registry. Anthropic's public client id is the one the
// vendor publishes for CLI integrations (Claude Code and compatible
// tools); it is not a secret.
var providers = map[string]Provider{
	"anthropic": {
		ID:           "anthropic",
		Label:        "Claude subscription (Pro/Max)",
		AuthorizeURL: "https://claude.ai/oauth/authorize",
		TokenURL:     "https://console.anthropic.com/v1/oauth/token",
		ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		RedirectURI:  "https://console.anthropic.com/oauth/code/callback",
		Scope:        "org:create_api_key user:profile user:inference",
	},
}

// ErrUnknownProvider is returned for a provider id not in the registry.
var ErrUnknownProvider = errors.New("auth: unknown oauth provider")

// Lookup resolves a provider id.
func Lookup(id string) (Provider, error) {
	p, ok := providers[strings.TrimSpace(id)]
	if !ok {
		return Provider{}, ErrUnknownProvider
	}
	return p, nil
}

// ProviderIDs lists registered provider ids, for help text.
func ProviderIDs() []string {
	out := make([]string, 0, len(providers))
	for id := range providers {
		out = append(out, id)
	}
	return out
}

// NewPKCE generates a code verifier, its S256 challenge, and a random
// state parameter.
func NewPKCE() (verifier, challenge, state string, err error) {
	v := make([]byte, 32)
	st := make([]byte, 16)
	if _, err = rand.Read(v); err != nil {
		return "", "", "", err
	}
	if _, err = rand.Read(st); err != nil {
		return "", "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(v)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	state = base64.RawURLEncoding.EncodeToString(st)
	return verifier, challenge, state, nil
}

// AuthorizeURL builds the consent URL the user opens in a browser.
func (p Provider) AuthorizeURLFor(challenge, state string) string {
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", p.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", p.Scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return p.AuthorizeURL + "?" + q.Encode()
}

// tokenResponse mirrors the provider's token endpoint JSON.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// Exchange trades an authorization code (+ PKCE verifier + state, as the
// user pastes it back) for tokens.
func (p Provider) Exchange(ctx context.Context, code, verifier, state string) (*Token, error) {
	body := map[string]any{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  p.RedirectURI,
		"client_id":     p.ClientID,
		"code_verifier": verifier,
		"state":         state,
	}
	return p.tokenCall(ctx, body)
}

// Refresh trades a stored refresh token for a fresh access token. The
// provider may rotate the refresh token; the new pair is returned whole.
func (p Provider) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	body := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     p.ClientID,
	}
	return p.tokenCall(ctx, body)
}

func (p Provider) tokenCall(ctx context.Context, body map[string]any) (*Token, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("auth: token response: %w", err)
	}
	if tr.Error != "" {
		desc := tr.ErrorDesc
		if desc == "" {
			desc = tr.Error
		}
		return nil, fmt.Errorf("auth: token endpoint: %s", desc)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("auth: token endpoint %d: %s", resp.StatusCode, head(string(raw), 200))
	}
	tok := &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		Scope:        tr.Scope,
	}
	if tr.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func head(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
