// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Token is one stored OAuth credential set for a provider.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}

// expirySkew refreshes early so an in-flight request never races the
// provider's cutoff.
const expirySkew = 60 * time.Second

// Expired reports whether the token is at or past its usable life. A token
// without an expiry never reports expired (some providers issue non-expiring
// access tokens).
func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && now.Add(expirySkew).After(t.ExpiresAt)
}

// Store persists OAuth tokens at <state dir>/auth.json, mode 0600 — the
// same on-disk trust level as the config's api_key.
type Store struct {
	path string
	mu   sync.Mutex
}

// ErrNoToken is returned when the store holds no credential for a provider.
var ErrNoToken = errors.New("auth: no token — run `panda auth login <provider>`")

// OpenStore returns the token store rooted at stateDir.
func OpenStore(stateDir string) *Store {
	return &Store{path: filepath.Join(stateDir, "auth.json")}
}

// Path exposes the backing file path for diagnostics.
func (s *Store) Path() string { return s.path }

// DefaultStateDir mirrors the CLI's state-dir convention
// (XDG_STATE_HOME/openpanda, %LOCALAPPDATA%\openpanda on Windows, else
// ~/.local/state/openpanda) so a token written by `panda auth login` is the
// same one the entry client finds later.
func DefaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "openpanda")
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "openpanda")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "openpanda")
}

func (s *Store) load() (map[string]Token, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Token{}, nil
		}
		return nil, err
	}
	var tokens map[string]Token
	if err := json.Unmarshal(data, &tokens); err != nil {
		return map[string]Token{}, nil // a corrupt file degrades to empty
	}
	if tokens == nil {
		tokens = map[string]Token{}
	}
	return tokens, nil
}

func (s *Store) save(tokens map[string]Token) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Put stores a token under its provider id.
func (s *Store) Put(provider string, tok Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return err
	}
	tokens[provider] = tok
	return s.save(tokens)
}

// Delete removes a provider's token (logout).
func (s *Store) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return err
	}
	delete(tokens, provider)
	return s.save(tokens)
}

// Get returns a provider's stored token, if any.
func (s *Store) Get(provider string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return Token{}, false
	}
	tok, ok := tokens[provider]
	return tok, ok
}

// Tokens returns a snapshot of all stored tokens, for `auth status`.
func (s *Store) Tokens() (map[string]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// AccessToken returns a live access token for the provider, transparently
// refreshing an expired one when a refresh token exists. ErrNoToken means
// the user has not logged in.
func (s *Store) AccessToken(ctx context.Context, provider string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return "", err
	}
	tok, ok := tokens[provider]
	if !ok || strings.TrimSpace(tok.AccessToken) == "" {
		return "", ErrNoToken
	}
	if !tok.Expired(time.Now()) {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		return "", errors.New("auth: token expired and no refresh token — run `panda auth login " + provider + "`")
	}
	p, err := Lookup(provider)
	if err != nil {
		return "", err
	}
	fresh, err := p.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		return "", err
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = tok.RefreshToken // providers may omit rotation
	}
	tokens[provider] = *fresh
	if err := s.save(tokens); err != nil {
		return "", err
	}
	return fresh.AccessToken, nil
}
