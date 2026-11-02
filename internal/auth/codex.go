// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

// The Codex login is the one credential this package does not own.
//
// A ChatGPT plan answers the Responses API at Codex's own backend
// (https://chatgpt.com/backend-api/codex) — the same surface the CLI dials —
// but the token that opens it lives in ~/.codex/auth.json and is rotated by the
// CLI on its own schedule. Copying it into this package's store would fork the
// refresh chain: the first refresh here rotates the refresh token and silently
// invalidates the CLI's copy. So the file is read on every call and never
// written. The CLI keeps ownership, and a stale token fails with the one
// instruction that fixes it.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexAuthPath resolves CODEX_HOME the way the CLI does: an absolute
// override, else ~/.codex. A relative CODEX_HOME is refused for the same
// reason the collector refuses it — it would name a different file every
// launch.
func codexAuthPath() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		if filepath.IsAbs(home) {
			return filepath.Join(home, "auth.json")
		}
		return ""
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, ".codex", "auth.json")
}

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// codexLoginToken reads the CLI's current ChatGPT credential.
func codexLoginToken() (Token, error) {
	path := codexAuthPath()
	if path == "" {
		return Token{}, errors.New("auth: CODEX_HOME is not an absolute path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Token{}, fmt.Errorf("auth: no Codex login at %s — run `codex login` first", path)
	}
	var f codexAuthFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Token{}, fmt.Errorf("auth: %s is not valid JSON: %w", path, err)
	}
	// auth_mode has been spelled "chatgpt", "chatgptAuthTokens" and omitted
	// across CLI releases. The access token is what opens the endpoint; only
	// an explicit api-key login has nothing to offer here.
	mode := strings.ToLower(strings.TrimSpace(f.AuthMode))
	if mode == "apikey" || strings.TrimSpace(f.Tokens.AccessToken) == "" {
		return Token{}, fmt.Errorf("auth: %s holds no ChatGPT login — run `codex login`", path)
	}
	exp := jwtExpiry(f.Tokens.AccessToken)
	if !exp.IsZero() && time.Now().Add(expirySkew).After(exp) {
		return Token{}, fmt.Errorf("auth: the Codex login expired at %s — run `codex` once to refresh it",
			exp.Local().Format("2006-01-02 15:04"))
	}
	return Token{
		AccessToken:  f.Tokens.AccessToken,
		RefreshToken: f.Tokens.RefreshToken,
		ExpiresAt:    exp,
		AccountID:    f.Tokens.AccountID,
	}, nil
}

// jwtExpiry reads the exp claim. The signature is not verified: this is the
// token's own copy of its deadline, not a trust decision — the endpoint is the
// one that decides whether the token is good.
func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}
