// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := OpenStore(dir)

	if _, err := s.AccessToken(context.Background(), "anthropic"); err != ErrNoToken {
		t.Fatalf("AccessToken before login: %v, want ErrNoToken", err)
	}

	tok := Token{AccessToken: "sk-ant-oat-x", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.Put("anthropic", tok); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("anthropic")
	if !ok || got.AccessToken != tok.AccessToken {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}
	at, err := s.AccessToken(context.Background(), "anthropic")
	if err != nil || at != tok.AccessToken {
		t.Fatalf("AccessToken = %q, %v", at, err)
	}

	// File is private to the owner. Skipped on Windows: the platform maps
	// only the read-only bit, so POSIX group/other bits never apply — ACLs
	// on the user profile carry the same protection.
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(s.path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("auth.json mode = %v / %v, want 0600", fi.Mode().Perm(), err)
		}
	}

	if err := s.Delete("anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("anthropic"); ok {
		t.Error("token still present after Delete")
	}
}

func TestAccessTokenRefreshesExpired(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	}))
	defer ts.Close()

	// Point the provider's token endpoint at the fake server.
	p, _ := Lookup("anthropic")
	orig := p.TokenURL
	p.TokenURL = ts.URL
	providers["anthropic"] = p
	defer func() { p.TokenURL = orig; providers["anthropic"] = p }()

	dir := t.TempDir()
	s := OpenStore(dir)
	expired := Token{AccessToken: "old", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := s.Put("anthropic", expired); err != nil {
		t.Fatalf("Put: %v", err)
	}
	at, err := s.AccessToken(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if at != "fresh-token" {
		t.Errorf("AccessToken = %q, want fresh-token", at)
	}
	if gotBody["refresh_token"] != "rt-1" {
		t.Errorf("refresh body = %v", gotBody)
	}
	// The rotated refresh token replaced the stored one.
	got, _ := s.Get("anthropic")
	if got.RefreshToken != "rt-2" {
		t.Errorf("stored refresh = %q, want rt-2", got.RefreshToken)
	}
}

func TestExchangeSendsPKCEParams(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "expires_in": 60})
	}))
	defer ts.Close()
	p, _ := Lookup("anthropic")
	p.TokenURL = ts.URL
	tok, err := p.Exchange(context.Background(), "the-code", "the-verifier", "the-state")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "at-1" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code",
		"code_verifier": "the-verifier", "state": "the-state",
		"client_id": p.ClientID, "redirect_uri": p.RedirectURI,
	} {
		if gotBody[k] != want {
			t.Errorf("body[%s] = %v, want %v", k, gotBody[k], want)
		}
	}
}

func TestAuthorizeURLCarriesPKCE(t *testing.T) {
	p, _ := Lookup("anthropic")
	u := p.AuthorizeURLFor("chall", "st")
	for _, want := range []string{"client_id=" + p.ClientID, "code_challenge=chall",
		"code_challenge_method=S256", "state=st", "response_type=code"} {
		if !strings.Contains(u, want) {
			t.Errorf("authorize url missing %q: %s", want, u)
		}
	}
}
