// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeJWT builds a token whose payload carries the exp claim. The signature is
// never checked — only the deadline is read — so three base64url parts suffice.
func fakeJWT(exp time.Time) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return head + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func writeCodexAuth(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", dir)
	return dir
}

func TestCodexLoginTokenReadsTheCLIsFile(t *testing.T) {
	exp := time.Now().Add(4 * 24 * time.Hour).Truncate(time.Second)
	writeCodexAuth(t, `{"auth_mode":"chatgpt","tokens":{"access_token":"`+fakeJWT(exp)+`","refresh_token":"r","account_id":"acct-1"}}`)
	tok, err := codexLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "" {
		t.Fatal("empty token")
	}
	if !tok.ExpiresAt.Equal(exp) {
		t.Errorf("expiry = %v, want %v", tok.ExpiresAt, exp)
	}
	// The ChatGPT backend routes by account; the id travels with the token.
	if tok.AccountID != "acct-1" {
		t.Errorf("account id = %q, want acct-1", tok.AccountID)
	}
}

func TestCodexLoginTokenAcceptsAnOmittedAuthMode(t *testing.T) {
	// Older CLI releases wrote no auth_mode; the access token is what opens
	// the endpoint, and only an explicit api-key login has nothing to offer.
	writeCodexAuth(t, `{"tokens":{"access_token":"`+fakeJWT(time.Now().Add(time.Hour))+`"}}`)
	if _, err := codexLoginToken(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexLoginTokenRefusesAnExpiredLogin(t *testing.T) {
	// An expired token is not silently refreshed here: the refresh chain
	// belongs to the CLI, and rotating it from this side would invalidate the
	// CLI's copy. The error has to name the one command that fixes it.
	writeCodexAuth(t, `{"auth_mode":"chatgpt","tokens":{"access_token":"`+
		fakeJWT(time.Now().Add(-time.Hour))+`"}}`)
	_, err := codexLoginToken()
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexLoginTokenRefusesAnAPIKeyMode(t *testing.T) {
	writeCodexAuth(t, `{"auth_mode":"apikey","tokens":{"access_token":""}}`)
	if _, err := codexLoginToken(); err == nil {
		t.Fatal("expected an error for a file with no ChatGPT login")
	}
}

func TestCodexLoginTokenWithoutAFile(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := codexLoginToken(); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v", err)
	}
}

// The store must route an external provider to its owner's file instead of
// looking in its own token map, which never holds a Codex token.
func TestStoreDelegatesExternalProviders(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	writeCodexAuth(t, `{"auth_mode":"chatgpt","tokens":{"access_token":"`+fakeJWT(exp)+`"}}`)
	s := OpenStore(t.TempDir())
	got, err := s.AccessToken(t.Context(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" || got != fakeJWT(exp) {
		t.Fatalf("token = %q", got)
	}
}
