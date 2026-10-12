// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestStoreSnapshots covers the diagnostic surface: Path, Tokens, corrupt-file
// degradation, and the load() contract that a malformed auth.json degrades to
// empty rather than locking the user out of every provider.
func TestStoreSnapshots(t *testing.T) {
	dir := t.TempDir()
	s := OpenStore(dir)

	if got := s.Path(); got != filepath.Join(dir, "auth.json") {
		t.Errorf("Path = %q", got)
	}
	if toks, err := s.Tokens(); err != nil || len(toks) != 0 {
		t.Errorf("Tokens empty-store = %v, %v", toks, err)
	}

	s.Put("anthropic", Token{AccessToken: "a"})
	s.Put("google", Token{AccessToken: "g"})
	toks, err := s.Tokens()
	if err != nil || len(toks) != 2 || toks["anthropic"].AccessToken != "a" || toks["google"].AccessToken != "g" {
		t.Errorf("Tokens = %v, %v", toks, err)
	}
	// Snapshot must not alias store internals — mutating it must not leak
	// into subsequent loads.
	toks["evil"] = Token{AccessToken: "x"}
	if toks2, _ := s.Tokens(); len(toks2) != 2 {
		t.Errorf("mutated snapshot leaked: %v", toks2)
	}

	// Corrupt file degrades to empty (documented behavior), not an error.
	if err := os.WriteFile(s.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if toks, err := s.Tokens(); err != nil || len(toks) != 0 {
		t.Errorf("Tokens corrupt-file = %v, %v", toks, err)
	}
	if _, ok := s.Get("anthropic"); ok {
		t.Error("Get on corrupt file found a token")
	}
}

// TestProviderIDsRegistry pins the catalog contract: non-empty, each id
// resolves through Lookup() with a complete descriptor, and Anthropic's
// public CLI client stays registered (its id is contractual with the docs).
func TestLookupRegistry(t *testing.T) {
	ids := ProviderIDs()
	if len(ids) == 0 {
		t.Fatal("ProviderIDs empty")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate provider id %q", id)
		}
		seen[id] = true
		p, err := Lookup(id)
		if err != nil {
			t.Errorf("Lookup(%q) from ProviderIDs: %v", id, err)
			continue
		}
		if p.AuthorizeURL == "" || p.TokenURL == "" || p.ClientID == "" {
			t.Errorf("provider %q descriptor incomplete: %+v", id, p)
		}
	}
	if !seen["anthropic"] {
		t.Error("anthropic missing from registry")
	}
	if _, err := Lookup(" nope "); err == nil {
		t.Error("Lookup(unknown) should error")
	}
	// Whitespace tolerance is contractual — config values arrive padded.
	if _, err := Lookup(" anthropic "); err != nil {
		t.Errorf("Provider with padded id: %v", err)
	}
}

// TestHeadTruncates pins the audit-log helper: long values get a visible
// ellipsis so logs can't silently look complete while truncated.
func TestHeadTruncates(t *testing.T) {
	if got := head("short", 10); got != "short" {
		t.Errorf("head(short) = %q", got)
	}
	if got := head("12345678901234567890", 8); got != "12345678…" {
		t.Errorf("head(long) = %q, want 8 chars + ellipsis", got)
	}
	if got := head("", 4); got != "" {
		t.Errorf("head(empty) = %q", got)
	}
}

// TestDefaultStateDir pins the XDG convention the CLI and entry client must
// agree on: XDG_STATE_HOME wins, else ~/.local/state/openpanda.
func TestDefaultStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg"))
	got := DefaultStateDir()
	if !strings.HasSuffix(got, filepath.Join("xdg", "openpanda")) {
		t.Errorf("DefaultStateDir with XDG = %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	if runtime.GOOS != "windows" {
		got = DefaultStateDir()
		if !strings.HasSuffix(got, filepath.Join(".local", "state", "openpanda")) {
			t.Errorf("DefaultStateDir fallback = %q", got)
		}
	}
}
